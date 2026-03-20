package export

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"

	"asset-discovery/internal/models"
	"asset-discovery/internal/tracing/telemetry"
)

// JSONExporter writes the final asset list to a JSON file.
type JSONExporter struct {
	filepath string
}

func NewJSONExporter(filepath string) *JSONExporter {
	return &JSONExporter{filepath: filepath}
}

func (e *JSONExporter) Process(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	telemetry.Infof(ctx, "[JSON Exporter] Writing %d assets to %s...", len(pCtx.Assets), e.filepath)
	markEnumerationsCompleted(pCtx, time.Now())

	if err := os.MkdirAll(filepath.Dir(e.filepath), 0755); err != nil {
		return pCtx, fmt.Errorf("failed to create directory for JSON output: %w", err)
	}

	// Open file to create/overwrite
	f, err := os.Create(e.filepath)
	if err != nil {
		return pCtx, fmt.Errorf("failed to create JSON file: %w", err)
	}
	defer f.Close()

	// Encode JSON securely
	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(BuildJSONExportAssets(pCtx.Assets)); err != nil {
		return pCtx, fmt.Errorf("failed to encode JSON output: %w", err)
	}

	return pCtx, nil
}

// CSVExporter writes the final asset list to a CSV file.
type CSVExporter struct {
	filepath string
}

func NewCSVExporter(filepath string) *CSVExporter {
	return &CSVExporter{filepath: filepath}
}

func (e *CSVExporter) Process(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	telemetry.Infof(ctx, "[CSV Exporter] Writing %d assets to %s...", len(pCtx.Assets), e.filepath)
	markEnumerationsCompleted(pCtx, time.Now())

	if err := os.MkdirAll(filepath.Dir(e.filepath), 0755); err != nil {
		return pCtx, fmt.Errorf("failed to create directory for CSV output: %w", err)
	}

	f, err := os.Create(e.filepath)
	if err != nil {
		return pCtx, fmt.Errorf("failed to create CSV file: %w", err)
	}
	defer f.Close()

	writer := csv.NewWriter(f)
	defer writer.Flush()

	// Write header
	header := []string{"Asset ID", "Domain Kind", "Registrable Domain", "Type", "Identifier", "Source", "Date"}
	if err := writer.Write(header); err != nil {
		return pCtx, fmt.Errorf("failed to write CSV header: %w", err)
	}

	// Write rows
	for _, asset := range SortedAssetsForExport(pCtx.Assets) {
		classified := ClassifyAsset(asset)
		row := []string{
			asset.ID,
			string(classified.DomainKind),
			classified.RegistrableDomain,
			string(asset.Type),
			asset.Identifier,
			asset.Source,
			FormatDateTime(asset.DiscoveryDate),
		}
		if err := writer.Write(row); err != nil {
			return pCtx, fmt.Errorf("failed to write CSV row: %w", err)
		}
	}

	return pCtx, nil
}

// XLSXExporter writes the final asset list to an Excel file.
type XLSXExporter struct {
	filepath string
}

func NewXLSXExporter(filepath string) *XLSXExporter {
	return &XLSXExporter{filepath: filepath}
}

func (e *XLSXExporter) Process(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	telemetry.Infof(ctx, "[XLSX Exporter] Writing %d assets to %s...", len(pCtx.Assets), e.filepath)
	markEnumerationsCompleted(pCtx, time.Now())

	if err := os.MkdirAll(filepath.Dir(e.filepath), 0755); err != nil {
		return pCtx, fmt.Errorf("failed to create directory for XLSX output: %w", err)
	}

	f := excelize.NewFile()
	defer f.Close()

	// Setup Sheets
	sheetDomains := "Registrable Domains"
	sheetSubdomains := "Subdomains"
	sheetIPs := "IPs"
	f.NewSheet(sheetDomains)
	f.NewSheet(sheetSubdomains)
	f.NewSheet(sheetIPs)
	f.DeleteSheet("Sheet1")

	// Write Headers
	domHeaders := []interface{}{"Asset ID", "Registrable Domain", "Source", "Date", "Registrar", "Created", "Updated", "Expires", "Registrant Org", "Nameservers"}
	if err := f.SetSheetRow(sheetDomains, "A1", &domHeaders); err != nil {
		return pCtx, fmt.Errorf("failed to write XLSX domain header: %w", err)
	}

	subHeaders := []interface{}{"Asset ID", "Registrable Domain", "Hostname", "Source", "Date", "A Records", "AAAA Records", "CNAME", "MX", "TXT"}
	if err := f.SetSheetRow(sheetSubdomains, "A1", &subHeaders); err != nil {
		return pCtx, fmt.Errorf("failed to write XLSX subdomain header: %w", err)
	}

	ipHeaders := []interface{}{"Asset ID", "IP", "Source", "Date", "ASN", "Organization", "PTR", "CIDR"}
	if err := f.SetSheetRow(sheetIPs, "A1", &ipHeaders); err != nil {
		return pCtx, fmt.Errorf("failed to write XLSX IP header: %w", err)
	}

	domRowIdx := 2
	subRowIdx := 2
	ipRowIdx := 2

	// Write rows
	for _, asset := range SortedAssetsForExport(pCtx.Assets) {
		classified := ClassifyAsset(asset)

		if asset.Type == models.AssetTypeDomain {
			if classified.DomainKind == DomainKindRegistrable {
				var registrar, created, updated, expires, registrantOrg, nsStr string
				if asset.DomainDetails != nil && asset.DomainDetails.RDAP != nil {
					rdap := asset.DomainDetails.RDAP
					registrar = rdap.RegistrarName
					if !rdap.CreationDate.IsZero() {
						created = rdap.CreationDate.Format("2006-01-02")
					}
					if !rdap.UpdatedDate.IsZero() {
						updated = rdap.UpdatedDate.Format("2006-01-02")
					}
					if !rdap.ExpirationDate.IsZero() {
						expires = rdap.ExpirationDate.Format("2006-01-02")
					}
					registrantOrg = rdap.RegistrantOrg
					nsStr = strings.Join(rdap.NameServers, ", ")
				}

				row := []interface{}{
					asset.ID,
					classified.RegistrableDomain,
					asset.Source,
					FormatDateTime(asset.DiscoveryDate),
					registrar,
					created,
					updated,
					expires,
					registrantOrg,
					nsStr,
				}
				axis := fmt.Sprintf("A%d", domRowIdx)
				if err := f.SetSheetRow(sheetDomains, axis, &row); err != nil {
					return pCtx, fmt.Errorf("failed to write XLSX domain row %d: %w", domRowIdx, err)
				}
				domRowIdx++
			} else {
				var aRecs, aaaaRecs, cnameRecs, mxRecs, txtRecs []string
				if asset.DomainDetails != nil {
					for _, rec := range asset.DomainDetails.Records {
						switch rec.Type {
						case "A":
							aRecs = append(aRecs, rec.Value)
						case "AAAA":
							aaaaRecs = append(aaaaRecs, rec.Value)
						case "CNAME":
							cnameRecs = append(cnameRecs, rec.Value)
						case "MX":
							mxRecs = append(mxRecs, rec.Value)
						case "TXT":
							txtRecs = append(txtRecs, rec.Value)
						}
					}
				}

				row := []interface{}{
					asset.ID,
					classified.RegistrableDomain,
					asset.Identifier,
					asset.Source,
					FormatDateTime(asset.DiscoveryDate),
					strings.Join(aRecs, ", "),
					strings.Join(aaaaRecs, ", "),
					strings.Join(cnameRecs, ", "),
					strings.Join(mxRecs, ", "),
					strings.Join(txtRecs, ", "),
				}
				axis := fmt.Sprintf("A%d", subRowIdx)
				if err := f.SetSheetRow(sheetSubdomains, axis, &row); err != nil {
					return pCtx, fmt.Errorf("failed to write XLSX subdomain row %d: %w", subRowIdx, err)
				}
				subRowIdx++
			}
		} else if asset.Type == models.AssetTypeIP {
			var asn interface{}
			var org, ptr, cidr string

			if asset.IPDetails != nil {
				if asset.IPDetails.ASN != 0 {
					asn = asset.IPDetails.ASN
				}
				org = asset.IPDetails.Organization
				ptr = asset.IPDetails.PTR
			}

			if asset.EnrichmentData != nil {
				if val, ok := asset.EnrichmentData["cidr"].(string); ok {
					cidr = val
				}
			}

			row := []interface{}{
				asset.ID,
				asset.Identifier,
				asset.Source,
				FormatDateTime(asset.DiscoveryDate),
				asn,
				org,
				ptr,
				cidr,
			}
			axis := fmt.Sprintf("A%d", ipRowIdx)
			if err := f.SetSheetRow(sheetIPs, axis, &row); err != nil {
				return pCtx, fmt.Errorf("failed to write XLSX IP row %d: %w", ipRowIdx, err)
			}
			ipRowIdx++
		}
	}

	// Save to filepath
	if err := f.SaveAs(e.filepath); err != nil {
		return pCtx, fmt.Errorf("failed to save XLSX file: %w", err)
	}

	return pCtx, nil
}
