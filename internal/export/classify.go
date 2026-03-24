package export

import (
	"time"

	exportshared "asset-discovery/internal/export/shared"
	"asset-discovery/internal/models"
)

func ClassifyAsset(asset models.Asset) ClassifiedAsset {
	return exportshared.ClassifyAsset(asset)
}

func BuildJSONExportAssets(assets []models.Asset) []ExportAsset {
	exported := make([]ExportAsset, 0, len(assets))

	for _, asset := range exportshared.SortedAssetsForExport(assets) {
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
	return exportshared.SortedAssetsForExport(assets)
}

func FormatDateTime(value time.Time) string {
	return exportshared.FormatDateTime(value)
}
