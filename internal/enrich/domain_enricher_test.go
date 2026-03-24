package enrich

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"asset-discovery/internal/models"
	"asset-discovery/internal/registration"
)

func TestDomainEnricher_BackfillsDNSRecordsAndCreatesIPAssets(t *testing.T) {
	enricher := NewDomainEnricher()
	enricher.lookupIP = func(ctx context.Context, network string, host string) ([]net.IP, error) {
		switch network {
		case "ip4":
			return []net.IP{net.ParseIP("203.0.113.10")}, nil
		case "ip6":
			return []net.IP{net.ParseIP("2001:db8::10")}, nil
		default:
			return nil, nil
		}
	}
	enricher.lookupCNAME = func(ctx context.Context, host string) (string, error) {
		return "edge.example.com.", nil
	}
	enricher.lookupMX = func(ctx context.Context, host string) ([]*net.MX, error) {
		return []*net.MX{{Host: "mail.example.com."}}, nil
	}
	enricher.lookupTXT = func(ctx context.Context, host string) ([]string, error) {
		return []string{`v=spf1 include:_spf.example.com`}, nil
	}
	enricher.lookupRDAP = func(ctx context.Context, domain string) (*models.RDAPData, error) {
		return nil, nil
	}

	pCtx := &models.PipelineContext{
		Assets: []models.Asset{
			{
				ID:            "dom-1",
				EnumerationID: "enum-1",
				Type:          models.AssetTypeDomain,
				Identifier:    "example.com",
				Source:        "alienvault_collector",
			},
		},
	}

	if _, err := enricher.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected enricher to succeed, got %v", err)
	}

	asset := &pCtx.Assets[0]
	if asset.DomainDetails == nil {
		t.Fatalf("expected domain details to be initialized")
	}

	for _, recordType := range []string{"A", "AAAA", "CNAME", "MX", "TXT"} {
		if !domainRecordTypeExists(asset.DomainDetails.Records, recordType) {
			t.Fatalf("expected %s record, got %+v", recordType, asset.DomainDetails.Records)
		}
	}

	if !enrichAssetExists(pCtx.Assets, "203.0.113.10") {
		t.Fatalf("expected IPv4 asset to be created, got %+v", pCtx.Assets)
	}
	if !enrichAssetExists(pCtx.Assets, "2001:db8::10") {
		t.Fatalf("expected IPv6 asset to be created, got %+v", pCtx.Assets)
	}
	if !hasEnrichmentObservation(pCtx.Observations, "example.com", "domain_enricher") {
		t.Fatalf("expected domain enrichment observation to be recorded, got %+v", pCtx.Observations)
	}
}

func TestDomainEnricher_BackfillsRDAPOnlyForRegistrableDomains(t *testing.T) {
	enricher := NewDomainEnricher()
	rdapLookups := 0
	enricher.lookupIP = func(ctx context.Context, network string, host string) ([]net.IP, error) { return nil, nil }
	enricher.lookupCNAME = func(ctx context.Context, host string) (string, error) { return "", nil }
	enricher.lookupMX = func(ctx context.Context, host string) ([]*net.MX, error) { return nil, nil }
	enricher.lookupTXT = func(ctx context.Context, host string) ([]string, error) { return nil, nil }
	enricher.lookupRDAP = func(ctx context.Context, domain string) (*models.RDAPData, error) {
		rdapLookups++
		if domain != "example.com" {
			t.Fatalf("expected RDAP lookup only for registrable root, got %s", domain)
		}
		return &models.RDAPData{RegistrantOrg: "Example Corp"}, nil
	}

	pCtx := &models.PipelineContext{
		Assets: []models.Asset{
			{
				ID:            "dom-1",
				EnumerationID: "enum-1",
				Type:          models.AssetTypeDomain,
				Identifier:    "example.com",
				Source:        "crt.sh",
			},
			{
				ID:            "dom-2",
				EnumerationID: "enum-1",
				Type:          models.AssetTypeDomain,
				Identifier:    "www.example.com",
				Source:        "wayback_collector",
			},
		},
	}

	if _, err := enricher.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected enricher to succeed, got %v", err)
	}

	if rdapLookups != 1 {
		t.Fatalf("expected one RDAP lookup, got %d", rdapLookups)
	}
	if pCtx.Assets[0].DomainDetails == nil || pCtx.Assets[0].DomainDetails.RDAP == nil {
		t.Fatalf("expected RDAP to be backfilled on registrable domain, got %+v", pCtx.Assets[0])
	}
	if pCtx.Assets[1].DomainDetails != nil && pCtx.Assets[1].DomainDetails.RDAP != nil {
		t.Fatalf("expected subdomain RDAP to remain empty, got %+v", pCtx.Assets[1])
	}
}

func TestDomainEnricher_DedupesLookupsAndRecordsErrors(t *testing.T) {
	enricher := NewDomainEnricher()
	ip4Lookups := 0
	mxLookups := 0
	enricher.lookupIP = func(ctx context.Context, network string, host string) ([]net.IP, error) {
		if network == "ip4" {
			ip4Lookups++
			return nil, errors.New("lookup failed")
		}
		return nil, nil
	}
	enricher.lookupCNAME = func(ctx context.Context, host string) (string, error) { return "", nil }
	enricher.lookupMX = func(ctx context.Context, host string) ([]*net.MX, error) {
		mxLookups++
		return nil, errors.New("mx failed")
	}
	enricher.lookupTXT = func(ctx context.Context, host string) ([]string, error) { return nil, nil }
	enricher.lookupRDAP = func(ctx context.Context, domain string) (*models.RDAPData, error) { return nil, nil }

	pCtx := &models.PipelineContext{
		Assets: []models.Asset{
			{
				ID:            "dom-1",
				EnumerationID: "enum-1",
				Type:          models.AssetTypeDomain,
				Identifier:    "example.com",
				Source:        "crt.sh",
			},
			{
				ID:            "dom-2",
				EnumerationID: "enum-2",
				Type:          models.AssetTypeDomain,
				Identifier:    "example.com",
				Source:        "wayback_collector",
			},
		},
	}

	if _, err := enricher.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected enricher to record errors without failing, got %v", err)
	}

	if ip4Lookups != 1 {
		t.Fatalf("expected deduped IPv4 lookup, got %d", ip4Lookups)
	}
	if mxLookups != 1 {
		t.Fatalf("expected deduped MX lookup, got %d", mxLookups)
	}
	if !containsEnrichErrorSubstring(pCtx.Errors, "domain_enricher lookup A example.com") {
		t.Fatalf("expected A lookup error to be recorded, got %+v", pCtx.Errors)
	}
	if !containsEnrichErrorSubstring(pCtx.Errors, "domain_enricher lookup MX example.com") {
		t.Fatalf("expected MX lookup error to be recorded, got %+v", pCtx.Errors)
	}
	if got := pCtx.Assets[0].EnrichmentStates["domain_enricher"].Status; got != "retryable" {
		t.Fatalf("expected retryable enrichment state after transient errors, got %+v", pCtx.Assets[0].EnrichmentStates)
	}
}

func TestDomainEnricher_CachesAcrossWavesAndReusesForNewAssets(t *testing.T) {
	enricher := NewDomainEnricher()
	ip4Lookups := 0
	rdapLookups := 0
	enricher.lookupIP = func(ctx context.Context, network string, host string) ([]net.IP, error) {
		if network == "ip4" {
			ip4Lookups++
			return []net.IP{net.ParseIP("203.0.113.10")}, nil
		}
		return nil, nil
	}
	enricher.lookupCNAME = func(ctx context.Context, host string) (string, error) { return "", nil }
	enricher.lookupMX = func(ctx context.Context, host string) ([]*net.MX, error) { return nil, nil }
	enricher.lookupTXT = func(ctx context.Context, host string) ([]string, error) { return nil, nil }
	enricher.lookupRDAP = func(ctx context.Context, domain string) (*models.RDAPData, error) {
		rdapLookups++
		return &models.RDAPData{RegistrantOrg: "Example Corp"}, nil
	}

	pCtx := &models.PipelineContext{
		Assets: []models.Asset{
			{
				ID:            "dom-1",
				EnumerationID: "enum-1",
				Type:          models.AssetTypeDomain,
				Identifier:    "example.com",
				Source:        "crt.sh",
			},
		},
	}

	if _, err := enricher.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected first enrich pass to succeed, got %v", err)
	}
	if ip4Lookups != 1 || rdapLookups != 1 {
		t.Fatalf("expected one network pass, got ip4=%d rdap=%d", ip4Lookups, rdapLookups)
	}
	if !assetHasContributorEnumeration(pCtx.Assets, models.AssetTypeIP, "203.0.113.10", "enum-1") {
		t.Fatalf("expected one emitted IP asset for enum-1, got %+v", pCtx.Assets)
	}

	if _, err := enricher.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected second enrich pass to succeed, got %v", err)
	}
	if ip4Lookups != 1 || rdapLookups != 1 {
		t.Fatalf("expected no repeated lookup for already enriched assets, got ip4=%d rdap=%d", ip4Lookups, rdapLookups)
	}
	if countEnrichmentObservations(pCtx.Observations, "example.com", "domain_enricher") != 1 {
		t.Fatalf("expected one canonical domain enrichment observation after cache reuse, got %+v", pCtx.Observations)
	}
	if countCanonicalAssets(pCtx.Assets, models.AssetTypeIP, "203.0.113.10") != 1 {
		t.Fatalf("expected no duplicate IP asset replay for enum-1, got %+v", pCtx.Assets)
	}

	pCtx.Assets = append(pCtx.Assets, models.Asset{
		ID:            "dom-2",
		EnumerationID: "enum-2",
		Type:          models.AssetTypeDomain,
		Identifier:    "example.com",
		Source:        "wayback_collector",
	})

	if _, err := enricher.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected cached enrich pass for duplicate asset to succeed, got %v", err)
	}
	if ip4Lookups != 1 || rdapLookups != 1 {
		t.Fatalf("expected cached result reuse for new duplicate asset, got ip4=%d rdap=%d", ip4Lookups, rdapLookups)
	}
	if pCtx.Assets[len(pCtx.Assets)-2].DomainDetails == nil || pCtx.Assets[len(pCtx.Assets)-2].DomainDetails.RDAP == nil {
		t.Fatalf("expected cached RDAP to be applied to new duplicate asset, got %+v", pCtx.Assets[len(pCtx.Assets)-2])
	}
	if countCanonicalAssets(pCtx.Assets, models.AssetTypeIP, "203.0.113.10") != 1 {
		t.Fatalf("expected canonical IP asset dedupe to retain one IP asset, got %+v", pCtx.Assets)
	}
	if !assetHasContributorEnumeration(pCtx.Assets, models.AssetTypeIP, "203.0.113.10", "enum-2") {
		t.Fatalf("expected cached replay to add enum-2 as IP contributor provenance, got %+v", pCtx.Assets)
	}
}

func TestDomainEnricher_TreatsNotFoundLookupsAsCompletedUnresolved(t *testing.T) {
	enricher := NewDomainEnricher()
	notFound := &net.DNSError{Err: "no such host", Name: "missing.example.com", IsNotFound: true}
	enricher.lookupIP = func(ctx context.Context, network string, host string) ([]net.IP, error) { return nil, notFound }
	enricher.lookupCNAME = func(ctx context.Context, host string) (string, error) { return "", notFound }
	enricher.lookupMX = func(ctx context.Context, host string) ([]*net.MX, error) { return nil, notFound }
	enricher.lookupTXT = func(ctx context.Context, host string) ([]string, error) { return nil, notFound }
	enricher.lookupRDAP = func(ctx context.Context, domain string) (*models.RDAPData, error) { return nil, nil }

	pCtx := &models.PipelineContext{
		Assets: []models.Asset{{
			ID:            "dom-1",
			EnumerationID: "enum-1",
			Type:          models.AssetTypeDomain,
			Identifier:    "missing.example.com",
			Source:        "crt.sh",
		}},
	}

	if _, err := enricher.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected enricher to succeed, got %v", err)
	}

	state := pCtx.Assets[0].EnrichmentStates["domain_enricher"]
	if state.Status != "completed" {
		t.Fatalf("expected not-found lookups to settle as completed, got %+v", state)
	}
	if got := models.DomainResolutionStatusForAsset(pCtx.Assets[0]); got != models.DomainResolutionStatusUnresolved {
		t.Fatalf("expected unresolved domain status, got %q", got)
	}
	if len(pCtx.Errors) != 0 {
		t.Fatalf("expected terminal not-found lookups to stay out of errors, got %+v", pCtx.Errors)
	}
}

func TestDomainEnricher_RetriesRetryableLookupsOnLaterPass(t *testing.T) {
	enricher := NewDomainEnricher()
	ip4Lookups := 0
	enricher.lookupIP = func(ctx context.Context, network string, host string) ([]net.IP, error) {
		if network != "ip4" {
			return nil, nil
		}
		ip4Lookups++
		if ip4Lookups == 1 {
			return nil, errors.New("i/o timeout")
		}
		return []net.IP{net.ParseIP("203.0.113.10")}, nil
	}
	enricher.lookupCNAME = func(ctx context.Context, host string) (string, error) { return "", nil }
	enricher.lookupMX = func(ctx context.Context, host string) ([]*net.MX, error) { return nil, nil }
	enricher.lookupTXT = func(ctx context.Context, host string) ([]string, error) { return nil, nil }
	enricher.lookupRDAP = func(ctx context.Context, domain string) (*models.RDAPData, error) { return nil, nil }

	pCtx := &models.PipelineContext{
		Assets: []models.Asset{{
			ID:            "dom-1",
			EnumerationID: "enum-1",
			Type:          models.AssetTypeDomain,
			Identifier:    "example.com",
			Source:        "crt.sh",
		}},
	}

	if _, err := enricher.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected first enrich pass to succeed, got %v", err)
	}
	if ip4Lookups != 1 {
		t.Fatalf("expected one initial lookup, got %d", ip4Lookups)
	}
	if got := pCtx.Assets[0].EnrichmentStates["domain_enricher"].Status; got != "retryable" {
		t.Fatalf("expected retryable state after transient failure, got %+v", pCtx.Assets[0].EnrichmentStates)
	}
	if domainRecordTypeExists(pCtx.Assets[0].DomainDetails.Records, "A") {
		t.Fatalf("expected no A record before retry succeeds, got %+v", pCtx.Assets[0].DomainDetails.Records)
	}

	if _, err := enricher.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected second enrich pass to succeed, got %v", err)
	}
	if ip4Lookups != 2 {
		t.Fatalf("expected retryable lookup to run again, got %d", ip4Lookups)
	}
	if got := pCtx.Assets[0].EnrichmentStates["domain_enricher"].Status; got != "completed" {
		t.Fatalf("expected completed state after retry succeeds, got %+v", pCtx.Assets[0].EnrichmentStates)
	}
	if !domainRecordTypeExists(pCtx.Assets[0].DomainDetails.Records, "A") {
		t.Fatalf("expected A record after retry succeeds, got %+v", pCtx.Assets[0].DomainDetails.Records)
	}
}

func TestDomainEnricher_ResetsCacheForNewPipelineContext(t *testing.T) {
	enricher := NewDomainEnricher()
	ip4Lookups := 0
	rdapLookups := 0
	enricher.lookupIP = func(ctx context.Context, network string, host string) ([]net.IP, error) {
		if network == "ip4" {
			ip4Lookups++
			return []net.IP{net.ParseIP("203.0.113.10")}, nil
		}
		return nil, nil
	}
	enricher.lookupCNAME = func(ctx context.Context, host string) (string, error) { return "", nil }
	enricher.lookupMX = func(ctx context.Context, host string) ([]*net.MX, error) { return nil, nil }
	enricher.lookupTXT = func(ctx context.Context, host string) ([]string, error) { return nil, nil }
	enricher.lookupRDAP = func(ctx context.Context, domain string) (*models.RDAPData, error) {
		rdapLookups++
		return &models.RDAPData{RegistrantOrg: "Example Corp"}, nil
	}

	first := &models.PipelineContext{
		Assets: []models.Asset{{
			ID:            "dom-1",
			EnumerationID: "enum-1",
			Type:          models.AssetTypeDomain,
			Identifier:    "example.com",
			Source:        "crt.sh",
		}},
	}
	second := &models.PipelineContext{
		Assets: []models.Asset{{
			ID:            "dom-2",
			EnumerationID: "enum-2",
			Type:          models.AssetTypeDomain,
			Identifier:    "example.com",
			Source:        "crt.sh",
		}},
	}

	if _, err := enricher.Process(context.Background(), first); err != nil {
		t.Fatalf("expected first enrich pass to succeed, got %v", err)
	}
	if _, err := enricher.Process(context.Background(), second); err != nil {
		t.Fatalf("expected second enrich pass to succeed, got %v", err)
	}

	if ip4Lookups != 2 || rdapLookups != 2 {
		t.Fatalf("expected fresh lookups for a new pipeline context, got ip4=%d rdap=%d", ip4Lookups, rdapLookups)
	}
}

func TestDomainEnricher_RetriesRDAPFailureAcrossPasses(t *testing.T) {
	enricher := NewDomainEnricher()
	rdapLookups := 0
	enricher.lookupIP = func(ctx context.Context, network string, host string) ([]net.IP, error) { return nil, nil }
	enricher.lookupCNAME = func(ctx context.Context, host string) (string, error) { return "", nil }
	enricher.lookupMX = func(ctx context.Context, host string) ([]*net.MX, error) { return nil, nil }
	enricher.lookupTXT = func(ctx context.Context, host string) ([]string, error) { return nil, nil }
	enricher.lookupRDAP = func(ctx context.Context, domain string) (*models.RDAPData, error) {
		rdapLookups++
		return nil, errors.New("whois refused")
	}

	pCtx := &models.PipelineContext{
		Assets: []models.Asset{
			{
				ID:            "dom-1",
				EnumerationID: "enum-1",
				Type:          models.AssetTypeDomain,
				Identifier:    "example.com",
				Source:        "crt.sh",
			},
		},
	}

	if _, err := enricher.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected first enrich pass to succeed, got %v", err)
	}
	if rdapLookups != 1 {
		t.Fatalf("expected first RDAP lookup, got %d", rdapLookups)
	}
	if len(pCtx.Errors) != 1 {
		t.Fatalf("expected one recorded RDAP error, got %+v", pCtx.Errors)
	}
	if got := pCtx.Assets[0].EnrichmentStates["domain_enricher"].Status; got != "retryable" {
		t.Fatalf("expected retryable RDAP state after failure, got %+v", pCtx.Assets[0].EnrichmentStates)
	}

	pCtx.Assets = append(pCtx.Assets, models.Asset{
		ID:            "dom-2",
		EnumerationID: "enum-2",
		Type:          models.AssetTypeDomain,
		Identifier:    "example.com",
		Source:        "wayback_collector",
	})

	if _, err := enricher.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected second enrich pass to succeed, got %v", err)
	}
	if rdapLookups != 2 {
		t.Fatalf("expected retryable RDAP failure to be retried, got %d lookups", rdapLookups)
	}
	if len(pCtx.Errors) != 2 {
		t.Fatalf("expected each retryable RDAP failure to be recorded, got %+v", pCtx.Errors)
	}
}

func TestDomainEnricher_CachesUnsupportedRDAPForRun(t *testing.T) {
	enricher := NewDomainEnricher()
	rdapLookups := 0
	enricher.lookupIP = func(ctx context.Context, network string, host string) ([]net.IP, error) { return nil, nil }
	enricher.lookupCNAME = func(ctx context.Context, host string) (string, error) { return "", nil }
	enricher.lookupMX = func(ctx context.Context, host string) ([]*net.MX, error) { return nil, nil }
	enricher.lookupTXT = func(ctx context.Context, host string) ([]string, error) { return nil, nil }
	enricher.lookupRDAP = func(ctx context.Context, domain string) (*models.RDAPData, error) {
		rdapLookups++
		return nil, registration.ErrUnsupportedRegistrationData
	}

	pCtx := &models.PipelineContext{
		Assets: []models.Asset{
			{
				ID:            "dom-1",
				EnumerationID: "enum-1",
				Type:          models.AssetTypeDomain,
				Identifier:    "example.com",
				Source:        "crt.sh",
			},
		},
	}

	if _, err := enricher.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected first enrich pass to succeed, got %v", err)
	}

	pCtx.Assets = append(pCtx.Assets, models.Asset{
		ID:            "dom-2",
		EnumerationID: "enum-2",
		Type:          models.AssetTypeDomain,
		Identifier:    "example.com",
		Source:        "wayback_collector",
	})

	if _, err := enricher.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected second enrich pass to succeed, got %v", err)
	}
	if rdapLookups != 1 {
		t.Fatalf("expected unsupported RDAP state to be cached, got %d lookups", rdapLookups)
	}
	if len(pCtx.Errors) != 0 {
		t.Fatalf("expected unsupported RDAP to remain non-fatal and non-duplicated, got %+v", pCtx.Errors)
	}
}

func domainRecordTypeExists(records []models.DNSRecord, recordType string) bool {
	for _, record := range records {
		if record.Type == recordType {
			return true
		}
	}
	return false
}

func enrichAssetExists(assets []models.Asset, identifier string) bool {
	for _, asset := range assets {
		if asset.Identifier == identifier {
			return true
		}
	}
	return false
}

func countCanonicalAssets(assets []models.Asset, assetType models.AssetType, identifier string) int {
	count := 0
	for _, asset := range assets {
		if asset.Type == assetType && asset.Identifier == identifier {
			count++
		}
	}
	return count
}

func assetHasContributorEnumeration(assets []models.Asset, assetType models.AssetType, identifier string, enumerationID string) bool {
	for _, asset := range assets {
		if asset.Type != assetType || asset.Identifier != identifier {
			continue
		}
		if asset.EnumerationID == enumerationID {
			return true
		}
		for _, item := range asset.Provenance {
			if item.EnumerationID == enumerationID {
				return true
			}
		}
	}
	return false
}

func containsEnrichErrorSubstring(errs []error, want string) bool {
	for _, err := range errs {
		if err != nil && strings.Contains(err.Error(), want) {
			return true
		}
	}
	return false
}

func hasEnrichmentObservation(observations []models.AssetObservation, identifier, source string) bool {
	return countEnrichmentObservations(observations, identifier, source) > 0
}

func countEnrichmentObservations(observations []models.AssetObservation, identifier, source string) int {
	count := 0
	for _, observation := range observations {
		if observation.Kind != models.ObservationKindEnrichment {
			continue
		}
		if observation.Identifier != identifier || observation.Source != source {
			continue
		}
		count++
	}
	return count
}
