package collect

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"

	"asset-discovery/internal/models"
	"asset-discovery/internal/ownership"
	"asset-discovery/internal/registration"
)

func TestDNSCollector_ExactDomainRecordsAndSameRootHosts(t *testing.T) {
	collector := NewDNSCollector()
	resolver := newDNSCollectorTestResolver()
	resolver.ips["example.com"] = []string{"203.0.113.10"}
	resolver.mx["example.com"] = []string{"mail.example.com"}
	resolver.ns["example.com"] = []string{"ns1.example.com"}
	resolver.txt["example.com"] = []string{
		`v=spf1 include:_spf.example.com include:example-help.com`,
	}
	resolver.cname["example.com"] = "edge.example.com."

	collector.lookupIPs = resolver.lookupIPs
	collector.lookupMX = resolver.lookupMX
	collector.lookupNS = resolver.lookupNS
	collector.lookupTXT = resolver.lookupTXT
	collector.lookupCNAME = resolver.lookupCNAME
	collector.lookupRDAP = resolver.lookupRDAP
	collector.judge = nil
	collector.variantSweepMode = DNSVariantSweepModePrioritized
	collector.maxVariantProbesPerRoot = 0

	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{
			{ID: "seed-1", CompanyName: "Example Corp", Domains: []string{"example.com"}},
		},
	}
	pCtx.InitializeSeedFrontier(1)

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected collector to succeed, got %v", err)
	}

	rootAsset := findAssetByIdentifier(pCtx.Assets, "example.com")
	if rootAsset == nil || rootAsset.DomainDetails == nil {
		t.Fatalf("expected root domain asset, got %+v", pCtx.Assets)
	}

	for _, expectedType := range []string{"A", "MX", "NS", "TXT", "CNAME"} {
		if !recordTypeExists(rootAsset.DomainDetails.Records, expectedType) {
			t.Fatalf("expected %s record on root asset, got %+v", expectedType, rootAsset.DomainDetails.Records)
		}
	}

	for _, host := range []string{"mail.example.com", "ns1.example.com", "_spf.example.com", "edge.example.com"} {
		if !assetExists(pCtx.Assets, host) {
			t.Fatalf("expected same-root host asset %s, got %+v", host, pCtx.Assets)
		}
	}

	if assetExists(pCtx.Assets, "example-help.com") {
		t.Fatalf("expected cross-root TXT candidate to stay judge-gated, got %+v", pCtx.Assets)
	}
}

func TestDNSCollector_ExhaustiveVariantSweepProcessesAllBatches(t *testing.T) {
	collector := NewDNSCollector()
	resolver := newDNSCollectorTestResolver()
	resolver.ips["example.com"] = []string{"203.0.113.10"}
	resolver.ns["example.com"] = []string{"ns1.example.com"}

	for _, root := range []string{"example.net", "example.org", "example.dev", "example.app"} {
		resolver.ips[root] = []string{"203.0.113.10"}
		resolver.ns[root] = []string{"ns1.example.com"}
	}

	collector.lookupIPs = resolver.lookupIPs
	collector.lookupMX = resolver.lookupMX
	collector.lookupNS = resolver.lookupNS
	collector.lookupTXT = resolver.lookupTXT
	collector.lookupCNAME = resolver.lookupCNAME
	collector.lookupRDAP = resolver.lookupRDAP
	collector.judge = nil
	collector.variantSuffixes = []string{"com", "net", "org", "dev", "app"}
	collector.variantProbeBatchSize = 2

	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{
			{ID: "seed-1", CompanyName: "Example Corp", Domains: []string{"example.com"}},
		},
	}
	pCtx.InitializeSeedFrontier(1)

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected collector to succeed, got %v", err)
	}

	for _, root := range []string{"example.net", "example.org", "example.dev", "example.app"} {
		if resolver.lookupCount(root) == 0 {
			t.Fatalf("expected exhaustive sweep to probe %s across batches, got %d lookups", root, resolver.lookupCount(root))
		}
	}
}

func TestDNSCollector_PrioritizedVariantSweepHonorsCapAndOrdering(t *testing.T) {
	collector := NewDNSCollector()
	resolver := newDNSCollectorTestResolver()
	resolver.ips["example.com"] = []string{"203.0.113.10"}
	resolver.ns["example.com"] = []string{"ns1.example.com"}

	resolver.ips["example.net"] = []string{"203.0.113.10"}
	resolver.ns["example.net"] = []string{"ns1.example.com"}
	resolver.ips["example.org"] = []string{"203.0.113.11"}
	resolver.ns["example.org"] = []string{"ns1.example.com"}
	resolver.ips["example.dev"] = []string{"203.0.113.12"}
	resolver.ns["example.dev"] = []string{"ns1.example.com"}

	collector.lookupIPs = resolver.lookupIPs
	collector.lookupMX = resolver.lookupMX
	collector.lookupNS = resolver.lookupNS
	collector.lookupTXT = resolver.lookupTXT
	collector.lookupCNAME = resolver.lookupCNAME
	collector.lookupRDAP = resolver.lookupRDAP
	collector.variantSweepMode = DNSVariantSweepModePrioritized
	collector.variantSuffixes = []string{"com", "net", "org", "dev"}
	collector.maxVariantProbesPerRoot = 2
	collector.judge = &stubOwnershipJudge{}

	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{
			{ID: "seed-1", CompanyName: "Example Corp", Domains: []string{"example.com"}},
		},
	}
	pCtx.InitializeSeedFrontier(1)

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected collector to succeed, got %v", err)
	}

	if resolver.lookupCount("example.dev") != 0 {
		t.Fatalf("expected example.dev to stay outside the probe cap, got %d lookups", resolver.lookupCount("example.dev"))
	}

	judge, _ := collector.judge.(*stubOwnershipJudge)
	if judge == nil || len(judge.seen) != 1 {
		t.Fatalf("expected one ownership-judge call, got %+v", judge)
	}
	if len(judge.seen[0].Candidates) != 2 {
		t.Fatalf("expected prioritized sweep to keep only two live variants, got %+v", judge.seen[0].Candidates)
	}
	if judge.seen[0].Candidates[0].Root != "example.net" || judge.seen[0].Candidates[1].Root != "example.org" {
		t.Fatalf("expected prioritized sweep ordering to be preserved, got %+v", judge.seen[0].Candidates)
	}
}

func TestDNSCollector_PromotesJudgeApprovedExactDNSPivot(t *testing.T) {
	collector := NewDNSCollector()
	resolver := newDNSCollectorTestResolver()
	resolver.ips["example.com"] = []string{"203.0.113.10"}
	resolver.ns["example.com"] = []string{"ns1.example.com"}
	resolver.txt["example.com"] = []string{`v=spf1 include:spf.example-ops.com`}

	resolver.ips["example-ops.com"] = []string{"203.0.113.50"}
	resolver.ns["example-ops.com"] = []string{"ns1.example.com"}
	resolver.rdap["example.com"] = &models.RDAPData{RegistrantOrg: "Example Corp"}
	resolver.rdap["example-ops.com"] = &models.RDAPData{RegistrantOrg: "Example Corp"}

	collector.lookupIPs = resolver.lookupIPs
	collector.lookupMX = resolver.lookupMX
	collector.lookupNS = resolver.lookupNS
	collector.lookupTXT = resolver.lookupTXT
	collector.lookupCNAME = resolver.lookupCNAME
	collector.lookupRDAP = resolver.lookupRDAP
	collector.variantSweepMode = DNSVariantSweepModePrioritized
	collector.maxVariantProbesPerRoot = 0
	collector.judge = &stubOwnershipJudge{
		decisions: []ownership.Decision{
			{
				Root:       "example-ops.com",
				Kind:       "ownership_judged",
				Confidence: 0.96,
			},
		},
	}

	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{
			{ID: "seed-1", CompanyName: "Example Corp", Domains: []string{"example.com"}},
		},
	}
	pCtx.InitializeSeedFrontier(1)

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected collector to succeed, got %v", err)
	}

	if !seedExists(pCtx.Seeds, "example-ops.com") {
		t.Fatalf("expected exact DNS pivot root to be promoted, got %+v", pCtx.Seeds)
	}
	if !assetWithSourceExists(pCtx.Assets, "example-ops.com", dnsAssetSourcePivot) {
		t.Fatalf("expected exact DNS pivot asset with %s source, got %+v", dnsAssetSourcePivot, pCtx.Assets)
	}
}

func TestDNSCollector_DoesNotJudgeRecordReferenceWithoutOverlap(t *testing.T) {
	collector := NewDNSCollector()
	resolver := newDNSCollectorTestResolver()
	resolver.ips["example.com"] = []string{"203.0.113.10"}
	resolver.txt["example.com"] = []string{`v=spf1 include:spf.vendor.net`}

	resolver.ips["vendor.net"] = []string{"198.51.100.50"}
	resolver.ns["vendor.net"] = []string{"ns1.vendor.net"}
	resolver.rdap["example.com"] = &models.RDAPData{RegistrantOrg: "Example Corp"}
	resolver.rdap["vendor.net"] = &models.RDAPData{RegistrantOrg: "Vendor Corp"}

	collector.lookupIPs = resolver.lookupIPs
	collector.lookupMX = resolver.lookupMX
	collector.lookupNS = resolver.lookupNS
	collector.lookupTXT = resolver.lookupTXT
	collector.lookupCNAME = resolver.lookupCNAME
	collector.lookupRDAP = resolver.lookupRDAP
	collector.variantSweepMode = DNSVariantSweepModePrioritized
	collector.maxVariantProbesPerRoot = 0
	collector.judge = &stubOwnershipJudge{}

	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{
			{ID: "seed-1", CompanyName: "Example Corp", Domains: []string{"example.com"}},
		},
	}
	pCtx.InitializeSeedFrontier(1)

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected collector to succeed, got %v", err)
	}

	judge, _ := collector.judge.(*stubOwnershipJudge)
	if judge == nil {
		t.Fatalf("expected stub judge")
	}
	if len(judge.seen) != 0 {
		t.Fatalf("expected uncorroborated external reference to stay below judge threshold, got %+v", judge.seen)
	}
	if seedExists(pCtx.Seeds, "vendor.net") {
		t.Fatalf("expected vendor.net to stay out of the collection frontier, got %+v", pCtx.Seeds)
	}
}

func TestDNSCollector_DoesNotTreatCollapsedOrganizationNamesAsCorroboration(t *testing.T) {
	collector := NewDNSCollector()
	resolver := newDNSCollectorTestResolver()
	resolver.ips["example.com"] = []string{"203.0.113.10"}
	resolver.txt["example.com"] = []string{`v=spf1 include:spf.example-holdings.com`}

	resolver.ips["example-holdings.com"] = []string{"198.51.100.50"}
	resolver.ns["example-holdings.com"] = []string{"ns1.shared-hosting.net"}
	resolver.rdap["example.com"] = &models.RDAPData{RegistrantOrg: "Example Group"}
	resolver.rdap["example-holdings.com"] = &models.RDAPData{RegistrantOrg: "Example Holdings"}

	collector.lookupIPs = resolver.lookupIPs
	collector.lookupMX = resolver.lookupMX
	collector.lookupNS = resolver.lookupNS
	collector.lookupTXT = resolver.lookupTXT
	collector.lookupCNAME = resolver.lookupCNAME
	collector.lookupRDAP = resolver.lookupRDAP
	collector.variantSweepMode = DNSVariantSweepModePrioritized
	collector.maxVariantProbesPerRoot = 0
	collector.judge = &stubOwnershipJudge{}

	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{
			{ID: "seed-1", CompanyName: "Example Group", Domains: []string{"example.com"}},
		},
	}
	pCtx.InitializeSeedFrontier(1)

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected collector to succeed, got %v", err)
	}

	judge, _ := collector.judge.(*stubOwnershipJudge)
	if judge == nil {
		t.Fatalf("expected stub judge")
	}
	if len(judge.seen) != 0 {
		t.Fatalf("expected distinct legal names to stay below judge threshold, got %+v", judge.seen)
	}
}

func TestDNSCollector_RecordsExactLookupErrors(t *testing.T) {
	collector := NewDNSCollector()
	resolver := newDNSCollectorTestResolver()

	collector.lookupIPs = resolver.lookupIPs
	collector.lookupMX = resolver.lookupMX
	collector.lookupNS = resolver.lookupNS
	collector.lookupTXT = resolver.lookupTXT
	collector.lookupCNAME = resolver.lookupCNAME
	collector.lookupRDAP = resolver.lookupRDAP
	collector.judge = nil
	collector.variantSweepMode = DNSVariantSweepModePrioritized
	collector.maxVariantProbesPerRoot = 0

	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{
			{ID: "seed-1", CompanyName: "Example Corp", Domains: []string{"example.com"}},
		},
	}
	pCtx.InitializeSeedFrontier(1)

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected collector to complete with recorded errors, got %v", err)
	}

	if !containsErrorSubstring(pCtx.Errors, "lookup A/AAAA example.com") {
		t.Fatalf("expected exact lookup errors to be recorded, got %+v", pCtx.Errors)
	}
}

func TestDNSCollector_VariantPreflightNXDOMAINSkipsOtherLookups(t *testing.T) {
	collector := NewDNSCollector()
	resolver := newDNSCollectorTestResolver()
	resolver.ips["example.com"] = []string{"203.0.113.10"}
	resolver.ns["example.com"] = []string{"ns1.example.com"}
	resolver.setLookupError("NS", "example.net", &net.DNSError{
		Err:        "no such host",
		Name:       "example.net",
		Server:     "resolver",
		IsNotFound: true,
	})

	collector.lookupIPs = resolver.lookupIPs
	collector.lookupMX = resolver.lookupMX
	collector.lookupNS = resolver.lookupNS
	collector.lookupTXT = resolver.lookupTXT
	collector.lookupCNAME = resolver.lookupCNAME
	collector.lookupRDAP = resolver.lookupRDAP
	collector.judge = nil
	collector.variantSuffixes = []string{"com", "net"}
	collector.variantProbeBatchSize = 1

	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{
			{ID: "seed-1", CompanyName: "Example Corp", Domains: []string{"example.com"}},
		},
	}
	pCtx.InitializeSeedFrontier(1)

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected collector to complete with NXDOMAIN preflight skips, got %v", err)
	}

	if resolver.lookupCountByKind("NS", "example.net") != 1 {
		t.Fatalf("expected one NS preflight lookup for example.net, got %d", resolver.lookupCountByKind("NS", "example.net"))
	}
	for _, kind := range []string{"A/AAAA", "MX", "TXT", "CNAME"} {
		if resolver.lookupCountByKind(kind, "example.net") != 0 {
			t.Fatalf("expected %s lookup to be skipped after NXDOMAIN preflight, got %d", kind, resolver.lookupCountByKind(kind, "example.net"))
		}
	}
}

func TestDNSCollector_VariantPreflightTransientNSErrorFallsBackToFullObservation(t *testing.T) {
	collector := NewDNSCollector()
	resolver := newDNSCollectorTestResolver()
	resolver.ips["example.com"] = []string{"203.0.113.10"}
	resolver.ns["example.com"] = []string{"ns1.example.com"}

	resolver.ips["example.net"] = []string{"203.0.113.10"}
	resolver.setLookupError("NS", "example.net", &net.DNSError{
		Err:         "temporary failure",
		Name:        "example.net",
		Server:      "resolver",
		IsTemporary: true,
	})

	collector.lookupIPs = resolver.lookupIPs
	collector.lookupMX = resolver.lookupMX
	collector.lookupNS = resolver.lookupNS
	collector.lookupTXT = resolver.lookupTXT
	collector.lookupCNAME = resolver.lookupCNAME
	collector.lookupRDAP = resolver.lookupRDAP
	collector.judge = nil
	collector.variantSuffixes = []string{"com", "net"}
	collector.variantProbeBatchSize = 1

	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{
			{ID: "seed-1", CompanyName: "Example Corp", Domains: []string{"example.com"}},
		},
	}
	pCtx.InitializeSeedFrontier(1)

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected collector to complete after transient NS fallback, got %v", err)
	}

	for _, kind := range []string{"A/AAAA", "MX", "TXT", "CNAME"} {
		if resolver.lookupCountByKind(kind, "example.net") == 0 {
			t.Fatalf("expected %s lookup to run after transient NS preflight failure", kind)
		}
	}
	if resolver.lookupCountByKind("NS", "example.net") < 2 {
		t.Fatalf("expected transient NS preflight to fall back to the full observation path, got %d NS lookups", resolver.lookupCountByKind("NS", "example.net"))
	}
}

func TestDNSCollector_ExhaustiveVariantSweepDoesNotTruncateLiveCandidates(t *testing.T) {
	collector := NewDNSCollector()
	resolver := newDNSCollectorTestResolver()
	resolver.ips["example.com"] = []string{"203.0.113.10"}
	resolver.ns["example.com"] = []string{"ns1.example.com"}

	variantSuffixes := []string{
		"com", "net", "org", "dev", "app", "ai", "io", "cloud", "tech", "ac",
		"ad", "ae", "aero", "af", "ag", "am", "as", "at", "au", "be",
		"biz", "br", "ca", "cc", "ch", "cl", "cn", "co", "de", "es",
		"eu", "fi", "fr",
	}
	for _, suffix := range variantSuffixes[1:] {
		root := "example." + suffix
		resolver.ips[root] = []string{"203.0.113.10"}
		resolver.ns[root] = []string{"ns1.example.com"}
	}

	collector.lookupIPs = resolver.lookupIPs
	collector.lookupMX = resolver.lookupMX
	collector.lookupNS = resolver.lookupNS
	collector.lookupTXT = resolver.lookupTXT
	collector.lookupCNAME = resolver.lookupCNAME
	collector.lookupRDAP = resolver.lookupRDAP
	collector.variantSuffixes = variantSuffixes
	collector.variantProbeBatchSize = 8
	collector.judge = &stubOwnershipJudge{}

	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{
			{ID: "seed-1", CompanyName: "Example Corp", Domains: []string{"example.com"}},
		},
	}
	pCtx.InitializeSeedFrontier(1)

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected collector to succeed with many live variants, got %v", err)
	}

	judge, _ := collector.judge.(*stubOwnershipJudge)
	if judge == nil || len(judge.seen) != 1 {
		t.Fatalf("expected one ownership-judge call, got %+v", judge)
	}
	if len(judge.seen[0].Candidates) != len(variantSuffixes)-1 {
		t.Fatalf("expected all live variants to remain eligible for judge submission, got %d of %d", len(judge.seen[0].Candidates), len(variantSuffixes)-1)
	}
}

type dnsCollectorTestResolver struct {
	mu         sync.Mutex
	ips        map[string][]string
	mx         map[string][]string
	ns         map[string][]string
	txt        map[string][]string
	cname      map[string]string
	rdap       map[string]*models.RDAPData
	errors     map[string]error
	callCounts map[string]int
	lookupHits map[string]int
}

func newDNSCollectorTestResolver() *dnsCollectorTestResolver {
	return &dnsCollectorTestResolver{
		ips:        make(map[string][]string),
		mx:         make(map[string][]string),
		ns:         make(map[string][]string),
		txt:        make(map[string][]string),
		cname:      make(map[string]string),
		rdap:       make(map[string]*models.RDAPData),
		errors:     make(map[string]error),
		callCounts: make(map[string]int),
		lookupHits: make(map[string]int),
	}
}

func (r *dnsCollectorTestResolver) lookupIPs(ctx context.Context, host string) ([]net.IP, error) {
	r.recordLookup("A/AAAA", host)
	if err, exists := r.errors[lookupKey("A/AAAA", host)]; exists {
		return nil, err
	}
	values, exists := r.ips[host]
	if !exists {
		return nil, errors.New("not found")
	}

	ips := make([]net.IP, 0, len(values))
	for _, value := range values {
		parsed := net.ParseIP(value)
		if parsed != nil {
			ips = append(ips, parsed)
		}
	}
	return ips, nil
}

func (r *dnsCollectorTestResolver) lookupMX(ctx context.Context, host string) ([]*net.MX, error) {
	r.recordLookup("MX", host)
	if err, exists := r.errors[lookupKey("MX", host)]; exists {
		return nil, err
	}
	values, exists := r.mx[host]
	if !exists {
		return nil, errors.New("not found")
	}

	out := make([]*net.MX, 0, len(values))
	for _, value := range values {
		out = append(out, &net.MX{Host: value})
	}
	return out, nil
}

func (r *dnsCollectorTestResolver) lookupNS(ctx context.Context, host string) ([]*net.NS, error) {
	r.recordLookup("NS", host)
	if err, exists := r.errors[lookupKey("NS", host)]; exists {
		return nil, err
	}
	values, exists := r.ns[host]
	if !exists {
		return nil, errors.New("not found")
	}

	out := make([]*net.NS, 0, len(values))
	for _, value := range values {
		out = append(out, &net.NS{Host: value})
	}
	return out, nil
}

func (r *dnsCollectorTestResolver) lookupTXT(ctx context.Context, host string) ([]string, error) {
	r.recordLookup("TXT", host)
	if err, exists := r.errors[lookupKey("TXT", host)]; exists {
		return nil, err
	}
	values, exists := r.txt[host]
	if !exists {
		return nil, errors.New("not found")
	}

	return append([]string(nil), values...), nil
}

func (r *dnsCollectorTestResolver) lookupCNAME(ctx context.Context, host string) (string, error) {
	r.recordLookup("CNAME", host)
	if err, exists := r.errors[lookupKey("CNAME", host)]; exists {
		return "", err
	}
	value, exists := r.cname[host]
	if !exists {
		return "", errors.New("not found")
	}

	return value, nil
}

func (r *dnsCollectorTestResolver) lookupRDAP(ctx context.Context, domain string) (*models.RDAPData, error) {
	if value, exists := r.rdap[domain]; exists {
		return value, nil
	}
	return nil, registration.ErrUnsupportedRegistrationData
}

func (r *dnsCollectorTestResolver) lookupCount(host string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.callCounts[host]
}

func (r *dnsCollectorTestResolver) lookupCountByKind(kind string, host string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lookupHits[lookupKey(kind, host)]
}

func (r *dnsCollectorTestResolver) setLookupError(kind string, host string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errors[lookupKey(kind, host)] = err
}

func (r *dnsCollectorTestResolver) recordLookup(kind string, host string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.callCounts[host]++
	r.lookupHits[lookupKey(kind, host)]++
}

func lookupKey(kind string, host string) string {
	return kind + "|" + host
}

func findAssetByIdentifier(assets []models.Asset, identifier string) *models.Asset {
	for i := range assets {
		if assets[i].Identifier == identifier {
			return &assets[i]
		}
	}
	return nil
}

func recordTypeExists(records []models.DNSRecord, recordType string) bool {
	for _, record := range records {
		if record.Type == recordType {
			return true
		}
	}
	return false
}

func assetWithSourceExists(assets []models.Asset, identifier string, source string) bool {
	for _, asset := range assets {
		if asset.Identifier == identifier && asset.Source == source {
			return true
		}
	}
	return false
}

func containsErrorSubstring(errs []error, want string) bool {
	for _, err := range errs {
		if err != nil && strings.Contains(err.Error(), want) {
			return true
		}
	}
	return false
}
