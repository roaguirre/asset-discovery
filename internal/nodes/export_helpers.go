package nodes

import (
	"sort"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"

	"asset-discovery/internal/models"
)

type classifiedAsset struct {
	exportGroup int
	domainKind  models.DomainKind
	apexDomain  string
}

const (
	exportGroupApexDomain = iota
	exportGroupSubdomain
	exportGroupIP
)

func classifyAsset(asset models.Asset) classifiedAsset {
	classified := classifiedAsset{}

	switch asset.Type {
	case models.AssetTypeDomain:
		normalized := normalizeDomainIdentifier(asset.Identifier)
		if normalized == "" {
			classified.exportGroup = exportGroupApexDomain
			classified.domainKind = models.DomainKindApex
			return classified
		}

		apexDomain, err := publicsuffix.EffectiveTLDPlusOne(normalized)
		if err != nil {
			classified.exportGroup = exportGroupApexDomain
			classified.domainKind = models.DomainKindApex
			classified.apexDomain = normalized
			return classified
		}

		classified.apexDomain = apexDomain
		if apexDomain == normalized {
			classified.exportGroup = exportGroupApexDomain
			classified.domainKind = models.DomainKindApex
			return classified
		}

		classified.exportGroup = exportGroupSubdomain
		classified.domainKind = models.DomainKindSubdomain
		return classified
	case models.AssetTypeIP:
		classified.exportGroup = exportGroupIP
		return classified
	default:
		classified.exportGroup = exportGroupIP
		return classified
	}
}

func buildJSONExportAssets(assets []models.Asset) []models.ExportAsset {
	exported := make([]models.ExportAsset, 0, len(assets))

	for _, asset := range sortedAssetsForExport(assets) {
		classified := classifyAsset(asset)
		exported = append(exported, models.ExportAsset{
			Asset:      asset,
			DomainKind: classified.domainKind,
			ApexDomain: classified.apexDomain,
		})
	}

	return exported
}

func sortedAssetsForExport(assets []models.Asset) []models.Asset {
	sorted := append([]models.Asset(nil), assets...)

	sort.SliceStable(sorted, func(i, j int) bool {
		left := classifyAsset(sorted[i])
		right := classifyAsset(sorted[j])

		if left.exportGroup != right.exportGroup {
			return left.exportGroup < right.exportGroup
		}

		if left.apexDomain != right.apexDomain {
			return left.apexDomain < right.apexDomain
		}

		leftID := normalizeDomainIdentifier(sorted[i].Identifier)
		rightID := normalizeDomainIdentifier(sorted[j].Identifier)
		if leftID != rightID {
			return leftID < rightID
		}

		if !sorted[i].DiscoveryDate.Equal(sorted[j].DiscoveryDate) {
			return sorted[i].DiscoveryDate.Before(sorted[j].DiscoveryDate)
		}

		return sorted[i].ID < sorted[j].ID
	})

	return sorted
}

func normalizeDomainIdentifier(identifier string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(identifier), "."))
}

func formatDateTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}

	return value.Format("2006-01-02 15:04:05")
}
