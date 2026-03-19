package nodes

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/html"

	"asset-discovery/internal/discovery"
	"asset-discovery/internal/fetchutil"
	"asset-discovery/internal/models"
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

// WebHintCollector mines conservative ownership hints from the primary site.
type WebHintCollector struct {
	client       *http.Client
	buildTargets func(domain string) []webFetchTarget
	judge        webhint.Judge
}

func NewWebHintCollector() *WebHintCollector {
	return &WebHintCollector{
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
}

func (c *WebHintCollector) Process(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	log.Println("[Web Hint Collector] Processing seeds...")

	var newEnums []models.Enumeration
	var newErrors []error
	var newAssets []models.Asset

	for _, seed := range pCtx.CollectionSeeds() {
		enum := models.Enumeration{
			ID:        newNodeID("enum-web-hint"),
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
		addCandidate := func(raw, text string) {
			collectWebHintCandidates(candidateByRoot, seenSamples, knownRoots, raw, text)
		}

		for _, baseDomain := range seed.Domains {
			if judgeBaseDomain == "" {
				judgeBaseDomain = baseDomain
			}
			for _, target := range c.buildTargets(baseDomain) {
				resp, err := fetchutil.DoRequest(ctx, c.client, func(ctx context.Context) (*http.Request, error) {
					retryReq, err := http.NewRequestWithContext(ctx, http.MethodGet, target.URL, nil)
					if err != nil {
						return nil, err
					}
					retryReq.Header.Set("User-Agent", "Asset-Discovery-Bot/1.0")
					return retryReq, nil
				})
				if err != nil {
					continue
				}
				if resp.StatusCode >= 400 {
					resp.Body.Close()
					continue
				}

				finalRoot := discovery.RegistrableDomain(resp.Request.URL.Hostname())
				if target.Kind == "homepage" && finalRoot != "" && finalRoot != discovery.RegistrableDomain(baseDomain) {
					addCandidate(resp.Request.URL.String(), "redirect")
				}

				body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
				resp.Body.Close()
				if err != nil {
					newErrors = append(newErrors, err)
					continue
				}

				switch target.Kind {
				case "homepage":
					err := extractHTMLCandidates(body, addCandidate)
					if err != nil {
						newErrors = append(newErrors, err)
					}
				case "securitytxt":
					extractSecurityTXTCandidates(body, addCandidate)
				}
			}
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

			decisions, err := c.judge.EvaluateAnchorRoots(ctx, seed, judgeBaseDomain, candidates)
			if err != nil {
				newErrors = append(newErrors, err)
			} else {
				for _, decision := range decisions {
					signalsByRoot[decision.Root] = append(signalsByRoot[decision.Root], models.SeedEvidence{
						Source:     "web_hint_collector",
						Kind:       decision.Kind,
						Value:      decision.Root,
						Confidence: decision.Confidence,
						Reasoned:   true,
					})
				}
			}
		}

		for root, evidences := range signalsByRoot {
			newAssets = append(newAssets, models.Asset{
				ID:            newNodeID("dom-web-hint"),
				EnumerationID: enum.ID,
				Type:          models.AssetTypeDomain,
				Identifier:    root,
				Source:        "web_hint_collector",
				DiscoveryDate: time.Now(),
				DomainDetails: &models.DomainDetails{},
			})

			promoted := false
			candidate := discovery.BuildDiscoveredSeed(seed, root, "web-hint-pivot")
			for _, evidence := range evidences {
				if pCtx.EnqueueSeedCandidate(candidate, evidence) {
					promoted = true
				}
			}

			if promoted {
				log.Printf("[Web Hint Collector] Promoted %s from web ownership hints.", root)
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

func extractHTMLCandidates(body []byte, addCandidate func(raw, text string)) error {
	if len(body) == 0 {
		return nil
	}

	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil
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

func extractSecurityTXTCandidates(body []byte, addCandidate func(raw, text string)) {
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
