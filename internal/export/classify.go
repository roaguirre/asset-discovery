package export

import (
	"sort"
	"time"

	"asset-discovery/internal/discovery"
	"asset-discovery/internal/models"
)

const (
	exportGroupRegistrableDomain = iota
	exportGroupSubdomain
	exportGroupIP
)

func ClassifyAsset(asset models.Asset) ClassifiedAsset {
	classified := ClassifiedAsset{}

	switch asset.Type {
	case models.AssetTypeDomain:
		normalized := discovery.NormalizeDomainIdentifier(asset.Identifier)
		if normalized == "" {
			classified.DomainKind = DomainKindRegistrable
			return classified
		}

		registrableDomain := discovery.RegistrableDomain(normalized)
		classified.RegistrableDomain = registrableDomain
		if registrableDomain == normalized {
			classified.DomainKind = DomainKindRegistrable
			return classified
		}

		classified.DomainKind = DomainKindSubdomain
		return classified
	default:
		return classified
	}
}

func BuildJSONExportAssets(assets []models.Asset) []ExportAsset {
	exported := make([]ExportAsset, 0, len(assets))

	for _, asset := range SortedAssetsForExport(assets) {
		classified := ClassifyAsset(asset)
		exported = append(exported, ExportAsset{
			Asset:             asset,
			DomainKind:        classified.DomainKind,
			RegistrableDomain: classified.RegistrableDomain,
		})
	}

	return exported
}

func SortedAssetsForExport(assets []models.Asset) []models.Asset {
	sorted := append([]models.Asset(nil), assets...)

	sort.SliceStable(sorted, func(i, j int) bool {
		left := ClassifyAsset(sorted[i])
		right := ClassifyAsset(sorted[j])

		if exportGroupForAsset(sorted[i], left) != exportGroupForAsset(sorted[j], right) {
			return exportGroupForAsset(sorted[i], left) < exportGroupForAsset(sorted[j], right)
		}

		if left.RegistrableDomain != right.RegistrableDomain {
			return left.RegistrableDomain < right.RegistrableDomain
		}

		leftID := discovery.NormalizeDomainIdentifier(sorted[i].Identifier)
		rightID := discovery.NormalizeDomainIdentifier(sorted[j].Identifier)
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

func FormatDateTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}

	return value.Format("2006-01-02 15:04:05")
}

func exportGroupForAsset(asset models.Asset, classified ClassifiedAsset) int {
	switch asset.Type {
	case models.AssetTypeDomain:
		if classified.DomainKind == DomainKindSubdomain {
			return exportGroupSubdomain
		}
		return exportGroupRegistrableDomain
	default:
		return exportGroupIP
	}
}
