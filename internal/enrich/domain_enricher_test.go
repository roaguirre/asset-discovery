package enrich

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"asset-discovery/internal/models"
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

func containsEnrichErrorSubstring(errs []error, want string) bool {
	for _, err := range errs {
		if err != nil && strings.Contains(err.Error(), want) {
			return true
		}
	}
	return false
}
