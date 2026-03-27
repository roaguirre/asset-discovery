package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"asset-discovery/internal/discovery"
	"asset-discovery/internal/fetchutil"
	"asset-discovery/internal/models"
	"asset-discovery/internal/ownership"
	"asset-discovery/internal/tracing/lineage"
	"asset-discovery/internal/tracing/telemetry"
)

type announcedPrefixesResponse struct {
	Data struct {
		Prefixes []struct {
			Prefix string `json:"prefix"`
		} `json:"prefixes"`
	} `json:"data"`
}

// ASNCIDRCollector pivots from known network ownership into PTR-derived domains.
type ASNCIDRCollector struct {
	client          *http.Client
	prefixesLookup  func(ctx context.Context, asn int) ([]string, error)
	ptrLookup       func(ctx context.Context, ip string) ([]string, error)
	maxHostsPerCIDR int
	maxHostsPerASN  int
	judge           ownership.Judge
}

type ASNCIDRCollectorOption func(*ASNCIDRCollector)

func WithASNCIDRClient(client *http.Client) ASNCIDRCollectorOption {
	return func(c *ASNCIDRCollector) {
		if client != nil {
			c.client = client
		}
	}
}

func WithASNCIDRJudge(judge ownership.Judge) ASNCIDRCollectorOption {
	return func(c *ASNCIDRCollector) {
		c.judge = judge
	}
}

func NewASNCIDRCollector(options ...ASNCIDRCollectorOption) *ASNCIDRCollector {
	collector := &ASNCIDRCollector{
		client:          &http.Client{Timeout: 30 * time.Second},
		maxHostsPerCIDR: 256,
		maxHostsPerASN:  512,
		judge:           ownership.NewDefaultJudge(),
	}
	for _, option := range options {
		if option != nil {
			option(collector)
		}
	}
	collector.prefixesLookup = collector.lookupAnnouncedPrefixes
	collector.ptrLookup = collector.lookupPTR
	return collector
}

func (c *ASNCIDRCollector) Process(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	telemetry.Info(ctx, "[ASN/CIDR Collector] Processing seeds...")

	var newEnums []models.Enumeration
	var newErrors []error
	var newAssets []models.Asset

	for _, seed := range pCtx.CollectionSeeds() {
		enum := models.Enumeration{
			ID:        models.NewID("enum-asn-cidr"),
			SeedID:    seed.ID,
			Status:    "running",
			CreatedAt: time.Now(),
			StartedAt: time.Now(),
		}
		newEnums = append(newEnums, enum)

		ptrRoots := make(map[string]int)
		ptrHosts := make(map[string]struct{})
		ptrSamples := make(map[string][]string)

		cidrs := append([]string{}, seed.CIDR...)
		remainingASNHosts := c.maxHostsPerASN
		for _, asn := range seed.ASN {
			prefixes, err := c.prefixesLookup(ctx, asn)
			if err != nil {
				newErrors = append(newErrors, fmt.Errorf("lookup ASN %d prefixes: %w", asn, err))
				continue
			}
			cidrs = append(cidrs, prefixes...)
		}

		for _, cidr := range discovery.UniqueLowerStrings(cidrs) {
			prefix, err := netip.ParsePrefix(cidr)
			if err != nil || !prefix.Addr().Is4() {
				continue
			}

			limit := c.maxHostsPerCIDR
			if remainingASNHosts > 0 && len(seed.ASN) > 0 {
				if remainingASNHosts < limit {
					limit = remainingASNHosts
				}
				if limit <= 0 {
					break
				}
			}

			for _, ip := range enumeratePrefixIPs(prefix, limit) {
				names, err := c.ptrLookup(ctx, ip)
				if err != nil {
					continue
				}

				for _, name := range names {
					host := discovery.NormalizeDomainIdentifier(name)
					if len(discovery.ExtractDomainCandidates(host)) == 0 {
						continue
					}

					ptrHosts[host] = struct{}{}
					root := discovery.RegistrableDomain(host)
					if root == "" {
						continue
					}
					ptrRoots[root]++
					if len(ptrSamples[root]) < 3 {
						ptrSamples[root] = append(ptrSamples[root], host)
					}
				}
			}

			if len(seed.ASN) > 0 {
				remainingASNHosts -= limit
			}
		}

		for host := range ptrHosts {
			newAssets = append(newAssets, models.Asset{
				ID:            models.NewID("dom-asn-cidr-host"),
				EnumerationID: enum.ID,
				Type:          models.AssetTypeDomain,
				Identifier:    host,
				Source:        "asn_cidr_collector",
				DiscoveryDate: time.Now(),
				DomainDetails: &models.DomainDetails{},
			})
		}

		if c.judge == nil || len(ptrRoots) == 0 {
			continue
		}

		judgeCandidates := make([]ownership.Candidate, 0, len(ptrRoots))
		for root, hits := range ptrRoots {
			evidence := []ownership.EvidenceItem{
				{
					Kind:    "ptr_root",
					Summary: fmt.Sprintf("Observed %d PTR hostnames under this registrable domain from the seed's network scope", hits),
				},
			}
			if len(ptrSamples[root]) > 0 {
				evidence = append(evidence, ownership.EvidenceItem{
					Kind:    "ptr_host_samples",
					Summary: fmt.Sprintf("Sample PTR hostnames: %s", strings.Join(ptrSamples[root], ", ")),
				})
			}

			judgeCandidates = append(judgeCandidates, ownership.Candidate{
				Root:     root,
				Evidence: evidence,
			})
		}

		request := ownership.Request{
			Scenario:   "network ptr pivot",
			Seed:       seed,
			Candidates: judgeCandidates,
		}
		decisions, err := c.judge.EvaluateCandidates(ctx, request)
		if err != nil {
			newErrors = append(newErrors, err)
			continue
		}
		lineage.RecordOwnershipJudgeEvaluation(pCtx, "asn_cidr_collector", request, decisions)

		for _, decision := range decisions {
			if !decision.Collect {
				continue
			}
			if !ownership.IsConfidenceAtLeast(
				decision.Confidence,
				pCtx.CandidatePromotionConfidenceThreshold(),
			) {
				telemetry.Infof(ctx, "[ASN/CIDR Collector] Skipping %s due to low-confidence judge decision %.2f.", decision.Root, decision.Confidence)
				continue
			}

			newAssets = append(newAssets, models.Asset{
				ID:            models.NewID("dom-asn-cidr-root"),
				EnumerationID: enum.ID,
				Type:          models.AssetTypeDomain,
				Identifier:    decision.Root,
				Source:        "asn_cidr_collector",
				DiscoveryDate: time.Now(),
				DomainDetails: &models.DomainDetails{},
			})

			candidate := discovery.BuildDiscoveredSeed(seed, decision.Root, "asn-cidr-pivot")
			if pCtx.EnqueueSeedCandidate(candidate, models.SeedEvidence{
				Source:     "ownership_judge",
				Kind:       decision.Kind,
				Value:      decision.Root,
				Confidence: decision.Confidence,
				Reasoned:   true,
			}) {
				telemetry.Infof(ctx, "[ASN/CIDR Collector] Promoted %s from judged network pivots.", decision.Root)
			}
		}
	}

	pCtx.Lock()
	pCtx.Enumerations = append(pCtx.Enumerations, newEnums...)
	pCtx.Errors = append(pCtx.Errors, newErrors...)
	pCtx.Unlock()
	pCtx.AppendAssets(newAssets...)

	return pCtx, nil
}

func (c *ASNCIDRCollector) lookupAnnouncedPrefixes(ctx context.Context, asn int) ([]string, error) {
	url := fmt.Sprintf("https://stat.ripe.net/data/announced-prefixes/data.json?resource=AS%d", asn)

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

	var payload announcedPrefixesResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}

	prefixes := make([]string, 0, len(payload.Data.Prefixes))
	for _, prefix := range payload.Data.Prefixes {
		if prefix.Prefix != "" {
			prefixes = append(prefixes, prefix.Prefix)
		}
	}
	return discovery.UniqueLowerStrings(prefixes), nil
}

func (c *ASNCIDRCollector) lookupPTR(ctx context.Context, ip string) ([]string, error) {
	lookupCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	names, err := net.DefaultResolver.LookupAddr(lookupCtx, ip)
	if err != nil {
		return nil, err
	}

	for i := range names {
		names[i] = discovery.NormalizeDomainIdentifier(names[i])
	}

	return names, nil
}

func enumeratePrefixIPs(prefix netip.Prefix, limit int) []string {
	if limit <= 0 {
		return nil
	}

	prefix = prefix.Masked()
	ips := make([]string, 0, limit)
	for addr := prefix.Addr(); prefix.Contains(addr) && len(ips) < limit; addr = addr.Next() {
		ips = append(ips, addr.String())
	}
	return ips
}
