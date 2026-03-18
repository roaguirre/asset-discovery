package nodes

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"asset-discovery/internal/models"
)

func TestJSONExporter_WritesFlatAssetsWithApexDomainMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.json")
	exporter := NewJSONExporter(path)

	if _, err := exporter.Process(context.Background(), sampleExportContext()); err != nil {
		t.Fatalf("expected JSON export to succeed, got %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected JSON output to exist, got %v", err)
	}

	var payload []models.ExportAsset
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("expected flat JSON export to parse, got %v", err)
	}

	if len(payload) != 3 {
		t.Fatalf("expected 3 exported assets, got %d", len(payload))
	}

	if payload[0].Identifier != "example.com" || payload[0].DomainKind != models.DomainKindApex || payload[0].ApexDomain != "example.com" {
		t.Fatalf("expected first JSON row to be apex domain metadata, got %+v", payload[0])
	}

	if payload[1].Identifier != "api.example.com" || payload[1].DomainKind != models.DomainKindSubdomain || payload[1].ApexDomain != "example.com" {
		t.Fatalf("expected second JSON row to be subdomain metadata, got %+v", payload[1])
	}

	if payload[2].Identifier != "203.0.113.10" || payload[2].DomainKind != "" || payload[2].ApexDomain != "" {
		t.Fatalf("expected third JSON row to be IP metadata, got %+v", payload[2])
	}
}

func TestCSVExporter_WritesDomainKindAndApexDomainColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.csv")
	exporter := NewCSVExporter(path)

	if _, err := exporter.Process(context.Background(), sampleExportContext()); err != nil {
		t.Fatalf("expected CSV export to succeed, got %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("expected CSV output to exist, got %v", err)
	}
	defer f.Close()

	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatalf("expected CSV output to parse, got %v", err)
	}

	expectedHeader := []string{"Asset ID", "Domain Kind", "Apex Domain", "Type", "Identifier", "Source", "Date"}
	if len(rows) != 4 {
		t.Fatalf("expected 4 CSV rows including header, got %d", len(rows))
	}

	for i, value := range expectedHeader {
		if rows[0][i] != value {
			t.Fatalf("expected CSV header %q at column %d, got %q", value, i, rows[0][i])
		}
	}

	if rows[1][1] != string(models.DomainKindApex) || rows[1][2] != "example.com" || rows[1][4] != "example.com" {
		t.Fatalf("expected first data row to be grouped apex domain, got %+v", rows[1])
	}

	if rows[2][1] != string(models.DomainKindSubdomain) || rows[2][2] != "example.com" || rows[2][4] != "api.example.com" {
		t.Fatalf("expected second data row to be grouped subdomain, got %+v", rows[2])
	}

	if rows[3][1] != "" || rows[3][2] != "" || rows[3][4] != "203.0.113.10" {
		t.Fatalf("expected third data row to be grouped IP, got %+v", rows[3])
	}
}

func TestXLSXExporter_SeparatesApexDomainsAndSubdomains(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.xlsx")
	exporter := NewXLSXExporter(path)

	if _, err := exporter.Process(context.Background(), sampleExportContext()); err != nil {
		t.Fatalf("expected XLSX export to succeed, got %v", err)
	}

	f, err := excelize.OpenFile(path)
	if err != nil {
		t.Fatalf("expected XLSX output to open, got %v", err)
	}
	defer f.Close()

	if rows, err := f.GetRows("Apex Domains"); err != nil {
		t.Fatalf("expected Apex Domains sheet to exist, got %v", err)
	} else if len(rows) < 2 || len(rows[1]) < 2 || rows[1][1] != "example.com" {
		t.Fatalf("expected example.com in Apex Domains sheet, got %+v", rows)
	}

	if rows, err := f.GetRows("Subdomains"); err != nil {
		t.Fatalf("expected Subdomains sheet to exist, got %v", err)
	} else if len(rows) < 2 || len(rows[1]) < 3 || rows[1][1] != "example.com" || rows[1][2] != "api.example.com" {
		t.Fatalf("expected api.example.com mapped to example.com in Subdomains sheet, got %+v", rows)
	}

	if rows, err := f.GetRows("IPs"); err != nil {
		t.Fatalf("expected IPs sheet to exist, got %v", err)
	} else if len(rows) < 2 || len(rows[1]) < 2 || rows[1][1] != "203.0.113.10" {
		t.Fatalf("expected 203.0.113.10 in IPs sheet, got %+v", rows)
	}
}

func sampleExportContext() *models.PipelineContext {
	ts := time.Date(2026, time.March, 17, 23, 0, 0, 0, time.FixedZone("-0300", -3*60*60))

	return &models.PipelineContext{
		Seeds: []models.Seed{
			{ID: "seed-1", CompanyName: "Example Corp", Domains: []string{"example.com"}},
		},
		Enumerations: []models.Enumeration{
			{
				ID:        "enum-1",
				SeedID:    "seed-1",
				Status:    "running",
				CreatedAt: ts.Add(-5 * time.Minute),
				UpdatedAt: ts.Add(-1 * time.Minute),
			},
		},
		Assets: []models.Asset{
			{
				ID:            "asset-root",
				EnumerationID: "enum-1",
				Type:          models.AssetTypeDomain,
				Identifier:    "example.com",
				Source:        "rdap",
				DiscoveryDate: ts.Add(-2 * time.Minute),
				DomainDetails: &models.DomainDetails{
					RDAP: &models.RDAPData{
						RegistrarName: "Example Registrar",
						RegistrantOrg: "Example Corp",
						NameServers:   []string{"ns1.example.com", "ns2.example.com"},
					},
				},
			},
			{
				ID:            "asset-sub",
				EnumerationID: "enum-1",
				Type:          models.AssetTypeDomain,
				Identifier:    "api.example.com",
				Source:        "crt.sh",
				DiscoveryDate: ts.Add(-1 * time.Minute),
				DomainDetails: &models.DomainDetails{
					Records: []models.DNSRecord{
						{Type: "A", Value: "203.0.113.10"},
						{Type: "CNAME", Value: "edge.example.net"},
					},
				},
			},
			{
				ID:            "asset-ip",
				EnumerationID: "enum-1",
				Type:          models.AssetTypeIP,
				Identifier:    "203.0.113.10",
				Source:        "dns_collector",
				DiscoveryDate: ts,
				IPDetails: &models.IPDetails{
					ASN:          64500,
					Organization: "Example Networks",
					PTR:          "api.example.com",
				},
				EnrichmentData: map[string]interface{}{
					"cidr": "203.0.113.0/24",
				},
			},
		},
	}
}
