package nodes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"asset-discovery/internal/discovery"
	"asset-discovery/internal/fetchutil"
	"asset-discovery/internal/models"
	"asset-discovery/internal/ownership"
	"asset-discovery/internal/registration"
)

const maxReverseRegistrationCandidates = 25

// ReverseRegistrationCollector finds sibling registrable domains that share
// registration characteristics with the current seed set.
type ReverseRegistrationCollector struct {
	client       *http.Client
	searchCT     func(ctx context.Context, term string) ([]string, error)
	lookupDomain func(ctx context.Context, domain string) (*models.RDAPData, error)
	judge        ownership.Judge
}

func NewReverseRegistrationCollector() *ReverseRegistrationCollector {
	collector := &ReverseRegistrationCollector{
		client: &http.Client{Timeout: 60 * time.Second},
	}
	collector.searchCT = collector.searchCertificateTransparency
	collector.lookupDomain = func(ctx context.Context, domain string) (*models.RDAPData, error) {
		return registration.LookupDomain(ctx, collector.client, domain)
	}
	collector.judge = ownership.NewDefaultJudge()
	return collector
}

type reverseRegistrationCandidate struct {
	root string
	rdap *models.RDAPData
	seed models.Seed
}

func (c *ReverseRegistrationCollector) Process(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	log.Println("[Reverse Registration Collector] Processing seeds...")

	var newEnums []models.Enumeration
	var newErrors []error
	var newAssets []models.Asset

	for _, seed := range pCtx.CollectionSeeds() {
		enum := models.Enumeration{
			ID:        newNodeID("enum-revreg"),
			SeedID:    seed.ID,
			Status:    "running",
			CreatedAt: time.Now(),
			StartedAt: time.Now(),
		}
		newEnums = append(newEnums, enum)

		knownRoots := make(map[string]struct{}, len(seed.Domains))
		for _, domain := range seed.Domains {
			if root := discovery.RegistrableDomain(domain); root != "" {
				knownRoots[root] = struct{}{}
			}
		}

		baselineOrgs := make(map[string]struct{})
		baselineEmailRoots := make(map[string]struct{})
		baselineNameservers := make([]string, 0)
		if normalized := normalizeOrganization(seed.CompanyName); normalized != "" {
			baselineOrgs[normalized] = struct{}{}
		}

		cache := make(map[string]*models.RDAPData)
		for _, domain := range seed.Domains {
			root := discovery.RegistrableDomain(domain)
			if root == "" {
				continue
			}

			data, err := c.lookupDomain(ctx, root)
			if err != nil && err != registration.ErrUnsupportedRegistrationData {
				newErrors = append(newErrors, err)
				continue
			}
			if data == nil {
				continue
			}
			cache[root] = data

			if normalized := normalizeOrganization(data.RegistrantOrg); normalized != "" {
				baselineOrgs[normalized] = struct{}{}
			}
			if emailRoot := emailRoot(data.RegistrantEmail); emailRoot != "" {
				baselineEmailRoots[emailRoot] = struct{}{}
			}
			baselineNameservers = append(baselineNameservers, data.NameServers...)
		}
		baselineNameservers = discovery.UniqueLowerStrings(baselineNameservers)

		candidateSearchTerms := make(map[string][]string)
		for _, term := range reverseRegistrationSearchTerms(seed) {
			domains, err := c.searchCT(ctx, term)
			if err != nil {
				newErrors = append(newErrors, fmt.Errorf("reverse registration search %q: %w", term, err))
				continue
			}

			for _, domain := range domains {
				root := discovery.RegistrableDomain(domain)
				if root == "" {
					continue
				}
				if _, exists := knownRoots[root]; exists {
					continue
				}
				candidateSearchTerms[root] = append(candidateSearchTerms[root], term)
				if len(candidateSearchTerms) >= maxReverseRegistrationCandidates {
					break
				}
			}

			if len(candidateSearchTerms) >= maxReverseRegistrationCandidates {
				break
			}
		}

		roots := make([]string, 0, len(candidateSearchTerms))
		for root := range candidateSearchTerms {
			roots = append(roots, root)
		}
		sort.Strings(roots)

		judgeCandidates := make([]ownership.Candidate, 0, len(roots))
		candidateByRoot := make(map[string]reverseRegistrationCandidate, len(roots))
		for _, root := range roots {
			data, ok := cache[root]
			if !ok {
				var err error
				data, err = c.lookupDomain(ctx, root)
				if err != nil && err != registration.ErrUnsupportedRegistrationData {
					newErrors = append(newErrors, err)
					continue
				}
				cache[root] = data
			}

			evidenceItems := []ownership.EvidenceItem{
				{
					Kind:    "ct_query",
					Summary: fmt.Sprintf("Found in certificate-transparency results for search terms: %s", strings.Join(uniquePreservingCase(candidateSearchTerms[root]), ", ")),
				},
			}

			if data != nil {
				if normalized := normalizeOrganization(data.RegistrantOrg); normalized != "" {
					if _, exists := baselineOrgs[normalized]; exists {
						evidenceItems = append(evidenceItems, ownership.EvidenceItem{
							Kind:    "registrant_org_match",
							Summary: fmt.Sprintf("Registrant organization %q matches the seed baseline", data.RegistrantOrg),
						})
					}
				}

				if emailDomain := emailRoot(data.RegistrantEmail); emailDomain != "" {
					if _, exists := baselineEmailRoots[emailDomain]; exists {
						evidenceItems = append(evidenceItems, ownership.EvidenceItem{
							Kind:    "registrant_email_root_match",
							Summary: fmt.Sprintf("Registrant email root %q matches the seed baseline", emailDomain),
						})
					}
				}

				if overlap := overlappingRoots(data.NameServers, baselineNameservers); len(overlap) > 0 {
					evidenceItems = append(evidenceItems, ownership.EvidenceItem{
						Kind:    "nameserver_overlap",
						Summary: fmt.Sprintf("Shares registrable nameserver roots with the seed baseline: %s", strings.Join(overlap, ", ")),
					})
				}
			}

			if len(evidenceItems) == 1 {
				continue
			}

			candidateByRoot[root] = reverseRegistrationCandidate{
				root: root,
				rdap: data,
				seed: discovery.BuildDiscoveredSeed(seed, root, "reverse-registration"),
			}
			judgeCandidates = append(judgeCandidates, ownership.Candidate{
				Root:     root,
				Evidence: evidenceItems,
			})
		}

		if c.judge == nil || len(judgeCandidates) == 0 {
			continue
		}

		request := ownership.Request{
			Scenario:   "registration pivot",
			Seed:       seed,
			Candidates: judgeCandidates,
		}
		decisions, err := c.judge.EvaluateCandidates(ctx, request)
		if err != nil {
			newErrors = append(newErrors, err)
			continue
		}
		recordOwnershipJudgeEvaluation(pCtx, "reverse_registration_collector", request, decisions)

		for _, decision := range decisions {
			if !decision.Collect {
				continue
			}
			if !hasHighConfidenceOwnership(decision.Confidence) {
				log.Printf("[Reverse Registration Collector] Skipping %s due to low-confidence judge decision %.2f.", decision.Root, decision.Confidence)
				continue
			}

			candidate, exists := candidateByRoot[decision.Root]
			if !exists {
				continue
			}

			newAssets = append(newAssets, models.Asset{
				ID:            newNodeID("dom-revreg"),
				EnumerationID: enum.ID,
				Type:          models.AssetTypeDomain,
				Identifier:    decision.Root,
				Source:        "reverse_registration_collector",
				DiscoveryDate: time.Now(),
				DomainDetails: &models.DomainDetails{
					RDAP: candidate.rdap,
				},
			})

			if pCtx.EnqueueSeedCandidate(candidate.seed, models.SeedEvidence{
				Source:     "ownership_judge",
				Kind:       decision.Kind,
				Value:      decision.Root,
				Confidence: decision.Confidence,
				Reasoned:   true,
			}) {
				log.Printf("[Reverse Registration Collector] Promoted %s from judged registration pivots.", decision.Root)
			}
		}
	}

	pCtx.Lock()
	pCtx.Enumerations = append(pCtx.Enumerations, newEnums...)
	pCtx.Errors = append(pCtx.Errors, newErrors...)
	pCtx.Assets = append(pCtx.Assets, newAssets...)
	pCtx.Unlock()

	return pCtx, nil
}

func (c *ReverseRegistrationCollector) searchCertificateTransparency(ctx context.Context, term string) ([]string, error) {
	url := "https://crt.sh/?q=" + url.QueryEscape(term) + "&output=json"

	resp, err := fetchutil.DoRequest(ctx, c.client, func(ctx context.Context) (*http.Request, error) {
		retryReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		retryReq.Header.Set("User-Agent", "Asset-Discovery-Bot/1.0")
		return retryReq, nil
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}

	var records []crtshResponse
	if err := json.Unmarshal(body, &records); err != nil {
		return nil, err
	}

	var domains []string
	for _, record := range records {
		domains = append(domains, discovery.ExtractDomainCandidates(record.CommonName)...)
		for _, value := range strings.Split(record.NameValue, "\n") {
			domains = append(domains, discovery.ExtractDomainCandidates(value)...)
		}
	}

	return discovery.UniqueLowerStrings(domains), nil
}

func reverseRegistrationSearchTerms(seed models.Seed) []string {
	terms := make([]string, 0, 2)
	if company := strings.TrimSpace(seed.CompanyName); company != "" {
		terms = append(terms, company)
	}

	if len(terms) == 0 {
		for _, domain := range seed.Domains {
			root := discovery.RegistrableDomain(domain)
			if root == "" {
				continue
			}
			label := strings.Split(root, ".")[0]
			if len(label) >= 4 {
				terms = append(terms, label)
				break
			}
		}
	}

	return uniquePreservingCase(terms)
}

func normalizeOrganization(raw string) string {
	return discovery.NormalizeOrganization(raw)
}

func emailRoot(email string) string {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" {
		return ""
	}
	parts := strings.Split(email, "@")
	if len(parts) != 2 {
		return ""
	}
	return discovery.RegistrableDomain(parts[1])
}

func overlappingRoots(left, right []string) []string {
	if len(left) == 0 || len(right) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(left))
	for _, candidate := range left {
		root := discovery.RegistrableDomain(candidate)
		if root == "" {
			continue
		}
		seen[root] = struct{}{}
	}

	var overlap []string
	for _, candidate := range right {
		root := discovery.RegistrableDomain(candidate)
		if root == "" {
			continue
		}
		if _, exists := seen[root]; exists {
			overlap = append(overlap, root)
		}
	}

	return uniquePreservingCase(overlap)
}

func uniquePreservingCase(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, strings.TrimSpace(value))
	}
	return out
}
