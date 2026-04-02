package collect

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/html"

	"asset-discovery/internal/discovery"
	"asset-discovery/internal/fetchutil"
	"asset-discovery/internal/models"
	"asset-discovery/internal/ownership"
	"asset-discovery/internal/tracing/lineage"
	"asset-discovery/internal/tracing/telemetry"
	"asset-discovery/internal/webhint"
)

type webFetchTarget struct {
	URL  string
	Kind string
}

type webHint struct {
	Root     string
	Evidence models.SeedEvidence
}

type webHintSeedStats struct {
	targetCount          int
	successfulFetchCount int
	skippedStatusCount   int
	fetchErrorCount      int
	readErrorCount       int
	parseErrorCount      int
}

// WebHintCollector mines conservative ownership hints from the primary site.
type WebHintCollector struct {
	client       *http.Client
	buildTargets func(domain string) []webFetchTarget
	judge        webhint.Judge
}

type WebHintCollectorOption func(*WebHintCollector)

func WithWebHintClient(client *http.Client) WebHintCollectorOption {
	return func(c *WebHintCollector) {
		if client != nil {
			c.client = client
		}
	}
}

func WithWebHintJudge(judge webhint.Judge) WebHintCollectorOption {
	return func(c *WebHintCollector) {
		c.judge = judge
	}
}

func NewWebHintCollector(options ...WebHintCollectorOption) *WebHintCollector {
	collector := &WebHintCollector{
		client: &http.Client{Timeout: 20 * time.Second},
		buildTargets: func(domain string) []webFetchTarget {
			domain = discovery.NormalizeDomainIdentifier(domain)
			return []webFetchTarget{
				{URL: "https://" + domain + "/", Kind: "homepage"},
				{URL: "http://" + domain + "/", Kind: "homepage"},
				{URL: "https://" + domain + "/.well-known/security.txt", Kind: "securitytxt"},
				{URL: "http://" + domain + "/.well-known/security.txt", Kind: "securitytxt"},
			}
		},
		judge: webhint.NewDefaultJudge(),
	}

	for _, option := range options {
		if option != nil {
			option(collector)
		}
	}

	return collector
}

func (c *WebHintCollector) Process(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	telemetry.Info(ctx, "[Web Hint Collector] Processing seeds...")
	if c.judge == nil {
		pCtx.EmitExecutionEvent(models.ExecutionEvent{
			Kind:    "judge_disabled",
			Message: "Web hint judge is disabled; external ownership hints will be observed but not promoted.",
			Metadata: map[string]interface{}{
				"collector": "web_hint_collector",
				"judge":     "web_hint",
			},
		})
	}

	var newEnums []models.Enumeration
	var newErrors []error
	var newAssets []models.Asset

	for _, seed := range pCtx.CollectionSeeds() {
		seedLabel := webHintSeedLabel(seed)
		enum := models.Enumeration{
			ID:        models.NewID("enum-web-hint"),
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

		judgeBaseDomain := ""
		candidateByRoot := make(map[string]*webhint.Candidate)
		seenSamples := make(map[string]map[string]struct{})
		stats := webHintSeedStats{}
		addCandidate := func(raw, text string) {
			collectWebHintCandidates(candidateByRoot, seenSamples, knownRoots, raw, text)
		}

		for _, baseDomain := range seed.Domains {
			if judgeBaseDomain == "" {
				judgeBaseDomain = baseDomain
			}
			for _, target := range c.buildTargets(baseDomain) {
				stats.targetCount++
				resp, err := fetchutil.DoRequest(ctx, c.client, func(ctx context.Context) (*http.Request, error) {
					retryReq, err := http.NewRequestWithContext(ctx, http.MethodGet, target.URL, nil)
					if err != nil {
						return nil, err
					}
					retryReq.Header.Set("User-Agent", "Asset-Discovery-Bot/1.0")
					return retryReq, nil
				})
				if err != nil {
					stats.fetchErrorCount++
					telemetry.Errorf(
						ctx,
						"[Web Hint Collector] Fetch failed for %s (%s) while evaluating %s: %v",
						target.URL,
						target.Kind,
						seedLabel,
						err,
					)
					emitWebHintEvent(
						pCtx,
						seed,
						judgeBaseDomain,
						"web_hint_fetch_failed",
						fmt.Sprintf("Web hint collector failed to fetch %s for %s.", target.Kind, seedLabel),
						map[string]interface{}{
							"error":       err.Error(),
							"target_kind": target.Kind,
							"target_url":  target.URL,
						},
					)
					continue
				}
				if resp.StatusCode >= 400 {
					stats.skippedStatusCount++
					telemetry.Infof(
						ctx,
						"[Web Hint Collector] Skipping %s (%s) for %s due to HTTP %d.",
						target.URL,
						target.Kind,
						seedLabel,
						resp.StatusCode,
					)
					emitWebHintEvent(
						pCtx,
						seed,
						judgeBaseDomain,
						"web_hint_target_skipped",
						fmt.Sprintf(
							"Web hint collector skipped %s for %s due to HTTP %d.",
							target.Kind,
							seedLabel,
							resp.StatusCode,
						),
						map[string]interface{}{
							"status_code": resp.StatusCode,
							"target_kind": target.Kind,
							"target_url":  target.URL,
						},
					)
					resp.Body.Close()
					continue
				}
				stats.successfulFetchCount++

				finalRoot := discovery.RegistrableDomain(resp.Request.URL.Hostname())
				if target.Kind == "homepage" && finalRoot != "" && finalRoot != discovery.RegistrableDomain(baseDomain) {
					addCandidate(resp.Request.URL.String(), "redirect")
				}

				body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
				resp.Body.Close()
				if err != nil {
					stats.readErrorCount++
					newErrors = append(newErrors, err)
					telemetry.Errorf(
						ctx,
						"[Web Hint Collector] Failed to read %s (%s) for %s: %v",
						target.URL,
						target.Kind,
						seedLabel,
						err,
					)
					emitWebHintEvent(
						pCtx,
						seed,
						judgeBaseDomain,
						"web_hint_read_failed",
						fmt.Sprintf("Web hint collector failed to read %s for %s.", target.Kind, seedLabel),
						map[string]interface{}{
							"error":       err.Error(),
							"target_kind": target.Kind,
							"target_url":  target.URL,
						},
					)
					continue
				}

				switch target.Kind {
				case "homepage":
					err := extractHTMLCandidates(body, addCandidate)
					if err != nil {
						stats.parseErrorCount++
						newErrors = append(newErrors, err)
						telemetry.Errorf(
							ctx,
							"[Web Hint Collector] Failed to parse homepage hints from %s for %s: %v",
							target.URL,
							seedLabel,
							err,
						)
						emitWebHintEvent(
							pCtx,
							seed,
							judgeBaseDomain,
							"web_hint_parse_failed",
							fmt.Sprintf("Web hint collector failed to parse homepage hints for %s.", seedLabel),
							map[string]interface{}{
								"error":       err.Error(),
								"target_kind": target.Kind,
								"target_url":  target.URL,
							},
						)
					}
				case "securitytxt":
					err := extractSecurityTXTCandidates(body, addCandidate)
					if err != nil {
						stats.parseErrorCount++
						newErrors = append(newErrors, err)
						telemetry.Errorf(
							ctx,
							"[Web Hint Collector] Failed to parse security.txt hints from %s for %s: %v",
							target.URL,
							seedLabel,
							err,
						)
						emitWebHintEvent(
							pCtx,
							seed,
							judgeBaseDomain,
							"web_hint_parse_failed",
							fmt.Sprintf("Web hint collector failed to parse security.txt hints for %s.", seedLabel),
							map[string]interface{}{
								"error":       err.Error(),
								"target_kind": target.Kind,
								"target_url":  target.URL,
							},
						)
					}
				}
			}
		}

		candidateRoots := sortedWebHintCandidateRoots(candidateByRoot)
		switch len(candidateRoots) {
		case 0:
			telemetry.Infof(
				ctx,
				"[Web Hint Collector] No cross-root candidates discovered for %s after %d targets (successful=%d skipped=%d errors=%d).",
				seedLabel,
				stats.targetCount,
				stats.successfulFetchCount,
				stats.skippedStatusCount,
				stats.errorCount(),
			)
			emitWebHintEvent(
				pCtx,
				seed,
				judgeBaseDomain,
				"web_hint_no_candidates",
				fmt.Sprintf("Web hint collector found no cross-root candidates for %s.", seedLabel),
				map[string]interface{}{
					"candidate_count":        0,
					"error_count":            stats.errorCount(),
					"skipped_status_count":   stats.skippedStatusCount,
					"successful_fetch_count": stats.successfulFetchCount,
					"target_count":           stats.targetCount,
				},
			)
		default:
			telemetry.Infof(
				ctx,
				"[Web Hint Collector] Discovered %d candidate root(s) for %s: %s",
				len(candidateRoots),
				seedLabel,
				strings.Join(candidateRoots, ", "),
			)
			emitWebHintEvent(
				pCtx,
				seed,
				judgeBaseDomain,
				"web_hint_candidates_discovered",
				fmt.Sprintf("Web hint collector found %d candidate root(s) for %s.", len(candidateRoots), seedLabel),
				map[string]interface{}{
					"candidate_count":        len(candidateRoots),
					"candidate_roots":        candidateRoots,
					"error_count":            stats.errorCount(),
					"skipped_status_count":   stats.skippedStatusCount,
					"successful_fetch_count": stats.successfulFetchCount,
					"target_count":           stats.targetCount,
				},
			)
		}

		signalsByRoot := make(map[string][]models.SeedEvidence)
		if c.judge != nil && len(candidateByRoot) > 0 {
			candidates := make([]webhint.Candidate, 0, len(candidateByRoot))
			for _, candidate := range candidateByRoot {
				candidates = append(candidates, *candidate)
			}
			sort.Slice(candidates, func(i, j int) bool {
				return candidates[i].Root < candidates[j].Root
			})

			telemetry.Infof(
				ctx,
				"[Web Hint Collector] Sending %d candidate root(s) to judge for %s.",
				len(candidates),
				seedLabel,
			)
			decisions, err := c.judge.EvaluateAnchorRoots(ctx, seed, judgeBaseDomain, candidates)
			if err != nil {
				newErrors = append(newErrors, err)
				telemetry.Errorf(
					ctx,
					"[Web Hint Collector] Judge failed for %s with %d candidate root(s): %v",
					seedLabel,
					len(candidates),
					err,
				)
				emitWebHintEvent(
					pCtx,
					seed,
					judgeBaseDomain,
					"web_hint_judge_failed",
					fmt.Sprintf("Web hint judge failed for %s.", seedLabel),
					map[string]interface{}{
						"candidate_count": len(candidates),
						"candidate_roots": candidateRoots,
						"error":           err.Error(),
					},
				)
			} else {
				lineage.RecordWebHintJudgeEvaluation(pCtx, "web_hint_collector", seed, judgeBaseDomain, candidates, decisions)
				acceptedCount := 0
				for _, decision := range decisions {
					if !decision.Collect {
						continue
					}
					acceptedCount++
					signalsByRoot[decision.Root] = append(signalsByRoot[decision.Root], models.SeedEvidence{
						Source:     "web_hint_collector",
						Kind:       decision.Kind,
						Value:      decision.Root,
						Confidence: decision.Confidence,
						Reasoned:   true,
					})
				}
				telemetry.Infof(
					ctx,
					"[Web Hint Collector] Judge evaluated %d candidate root(s) for %s: accepted=%d discarded=%d.",
					len(candidates),
					seedLabel,
					acceptedCount,
					len(candidates)-acceptedCount,
				)
				emitWebHintEvent(
					pCtx,
					seed,
					judgeBaseDomain,
					"web_hint_judge_completed",
					fmt.Sprintf("Web hint judge evaluated %d candidate root(s) for %s.", len(candidates), seedLabel),
					map[string]interface{}{
						"accepted_count":  acceptedCount,
						"candidate_count": len(candidates),
						"candidate_roots": candidateRoots,
						"discarded_count": len(candidates) - acceptedCount,
					},
				)
			}
		} else if c.judge == nil && len(candidateRoots) > 0 {
			telemetry.Infof(
				ctx,
				"[Web Hint Collector] Judge disabled; %d candidate root(s) for %s will remain unpromoted.",
				len(candidateRoots),
				seedLabel,
			)
			emitWebHintEvent(
				pCtx,
				seed,
				judgeBaseDomain,
				"web_hint_candidates_unjudged",
				fmt.Sprintf("Web hint collector found %d candidate root(s) for %s, but the judge is disabled.", len(candidateRoots), seedLabel),
				map[string]interface{}{
					"candidate_count": len(candidateRoots),
					"candidate_roots": candidateRoots,
				},
			)
		}

		acceptedRoots := make([]string, 0, len(signalsByRoot))
		lowConfidenceRoots := make([]string, 0, len(signalsByRoot))
		promotedRoots := make([]string, 0, len(signalsByRoot))
		for root, evidences := range signalsByRoot {
			promoted := false
			accepted := false
			materialized := false
			candidate := discovery.BuildDiscoveredSeed(seed, root, "web-hint-pivot")
			for _, evidence := range evidences {
				if !ownership.IsConfidenceAtLeast(
					evidence.Confidence,
					pCtx.CandidatePromotionConfidenceThreshold(),
				) {
					telemetry.Infof(ctx, "[Web Hint Collector] Skipping %s due to low-confidence judge decision %.2f.", root, evidence.Confidence)
					lowConfidenceRoots = append(lowConfidenceRoots, root)
					continue
				}
				accepted = true
				promotion := pCtx.PromoteSeedCandidate(candidate, evidence)
				if promotion.Decision == models.CandidatePromotionAccepted {
					materialized = true
				}
				if promotion.Scheduled {
					promoted = true
				}
			}
			if !accepted {
				continue
			}
			acceptedRoots = append(acceptedRoots, root)

			if materialized {
				newAssets = append(newAssets, models.Asset{
					ID:            models.NewID("dom-web-hint"),
					EnumerationID: enum.ID,
					Type:          models.AssetTypeDomain,
					Identifier:    root,
					Source:        "web_hint_collector",
					DiscoveryDate: time.Now(),
					DomainDetails: &models.DomainDetails{},
				})
			}

			if promoted {
				promotedRoots = append(promotedRoots, root)
				telemetry.Infof(ctx, "[Web Hint Collector] Promoted %s from web ownership hints.", root)
			}
		}

		sort.Strings(acceptedRoots)
		sort.Strings(lowConfidenceRoots)
		sort.Strings(promotedRoots)

		switch {
		case len(candidateRoots) > 0 && len(acceptedRoots) == 0 && c.judge != nil:
			emitWebHintEvent(
				pCtx,
				seed,
				judgeBaseDomain,
				"web_hint_no_accepted_candidates",
				fmt.Sprintf("Web hint collector accepted no candidate roots for %s.", seedLabel),
				map[string]interface{}{
					"candidate_count": len(candidateRoots),
					"candidate_roots": candidateRoots,
				},
			)
		case len(acceptedRoots) > 0:
			emitWebHintEvent(
				pCtx,
				seed,
				judgeBaseDomain,
				"web_hint_candidates_accepted",
				fmt.Sprintf("Web hint collector accepted %d candidate root(s) for %s.", len(acceptedRoots), seedLabel),
				map[string]interface{}{
					"accepted_count": len(acceptedRoots),
					"accepted_roots": acceptedRoots,
					"promoted_count": len(promotedRoots),
					"promoted_roots": promotedRoots,
				},
			)
		}

		if len(lowConfidenceRoots) > 0 {
			emitWebHintEvent(
				pCtx,
				seed,
				judgeBaseDomain,
				"web_hint_low_confidence_skipped",
				fmt.Sprintf(
					"Web hint collector skipped %d accepted candidate root(s) for %s due to confidence threshold %.2f.",
					len(lowConfidenceRoots),
					seedLabel,
					pCtx.CandidatePromotionConfidenceThreshold(),
				),
				map[string]interface{}{
					"confidence_threshold": pCtx.CandidatePromotionConfidenceThreshold(),
					"skipped_roots":        lowConfidenceRoots,
				},
			)
		}
	}

	pCtx.Lock()
	pCtx.Enumerations = append(pCtx.Enumerations, newEnums...)
	pCtx.Errors = append(pCtx.Errors, newErrors...)
	pCtx.Unlock()
	pCtx.AppendAssets(newAssets...)

	return pCtx, nil
}

func extractHTMLCandidates(body []byte, addCandidate func(raw, text string)) error {
	if len(body) == 0 {
		return nil
	}

	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return err
	}

	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode {
			switch node.Data {
			case "link":
				rel := attr(node, "rel")
				href := attr(node, "href")
				if strings.Contains(rel, "canonical") {
					addCandidate(href, "canonical")
				}
				if strings.Contains(rel, "alternate") && attr(node, "hreflang") != "" {
					addCandidate(href, "alternate")
				}
			case "meta":
				property := strings.ToLower(attr(node, "property"))
				name := strings.ToLower(attr(node, "name"))
				content := attr(node, "content")
				if property == "og:url" || name == "og:url" {
					addCandidate(content, "canonical")
				}
			case "a":
				href := attr(node, "href")
				if href == "" {
					break
				}

				text := strings.TrimSpace(nodeText(node))
				addCandidate(href, text)
			}
		}

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)

	return nil
}

func extractSecurityTXTCandidates(body []byte, addCandidate func(raw, text string)) error {
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.ToLower(strings.TrimSpace(parts[0]))
		value := strings.TrimSpace(parts[1])
		switch key {
		case "canonical":
			addCandidate(value, "securitytxt")
		case "contact", "policy", "hiring":
			addCandidate(value, "securitytxt")
		}
	}

	return scanner.Err()
}

func collectWebHintCandidates(candidateByRoot map[string]*webhint.Candidate, seenSamples map[string]map[string]struct{}, knownRoots map[string]struct{}, raw, text string) {
	for _, candidate := range discovery.ExtractDomainCandidates(raw) {
		root := discovery.RegistrableDomain(candidate)
		if root == "" {
			continue
		}
		if _, exists := knownRoots[root]; exists {
			continue
		}

		if candidateByRoot[root] == nil {
			candidateByRoot[root] = &webhint.Candidate{Root: root}
			seenSamples[root] = make(map[string]struct{})
		}

		sampleKey := strings.ToLower(strings.TrimSpace(raw)) + "|" + strings.ToLower(strings.TrimSpace(text))
		if _, exists := seenSamples[root][sampleKey]; exists {
			continue
		}
		seenSamples[root][sampleKey] = struct{}{}

		if len(candidateByRoot[root].Samples) >= 3 {
			continue
		}
		candidateByRoot[root].Samples = append(candidateByRoot[root].Samples, webhint.LinkSample{
			Href: raw,
			Text: text,
		})
	}
}

func attr(node *html.Node, key string) string {
	key = strings.ToLower(key)
	for _, attribute := range node.Attr {
		if strings.ToLower(attribute.Key) == key {
			return strings.TrimSpace(attribute.Val)
		}
	}
	return ""
}

func nodeText(node *html.Node) string {
	var builder strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			builder.WriteString(n.Data)
			builder.WriteByte(' ')
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return builder.String()
}

func (s webHintSeedStats) errorCount() int {
	return s.fetchErrorCount + s.readErrorCount + s.parseErrorCount
}

// emitWebHintEvent enriches collector diagnostics with stable seed metadata so
// live activity records can explain why a web-hint stage produced no assets.
func emitWebHintEvent(
	pCtx *models.PipelineContext,
	seed models.Seed,
	baseDomain string,
	kind string,
	message string,
	extra map[string]interface{},
) {
	metadata := map[string]interface{}{
		"collector":  "web_hint_collector",
		"seed_id":    strings.TrimSpace(seed.ID),
		"seed_label": webHintSeedLabel(seed),
	}
	if normalizedBase := discovery.NormalizeDomainIdentifier(baseDomain); normalizedBase != "" {
		metadata["base_domain"] = normalizedBase
	}
	for key, value := range extra {
		metadata[key] = value
	}

	pCtx.EmitExecutionEvent(models.ExecutionEvent{
		Kind:     kind,
		Message:  message,
		Metadata: metadata,
	})
}

func sortedWebHintCandidateRoots(candidateByRoot map[string]*webhint.Candidate) []string {
	if len(candidateByRoot) == 0 {
		return nil
	}

	roots := make([]string, 0, len(candidateByRoot))
	for root := range candidateByRoot {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	return roots
}

func webHintSeedLabel(seed models.Seed) string {
	if companyName := strings.TrimSpace(seed.CompanyName); companyName != "" {
		return companyName
	}

	for _, domain := range seed.Domains {
		if normalized := discovery.NormalizeDomainIdentifier(domain); normalized != "" {
			return normalized
		}
	}

	return strings.TrimSpace(seed.ID)
}
