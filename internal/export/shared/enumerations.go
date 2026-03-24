package shared

import (
	"time"

	"asset-discovery/internal/models"
)

func MarkEnumerationsCompleted(pCtx *models.PipelineContext, completedAt time.Time) {
	for i := range pCtx.Enumerations {
		if pCtx.Enumerations[i].StartedAt.IsZero() {
			pCtx.Enumerations[i].StartedAt = pCtx.Enumerations[i].CreatedAt
		}
		pCtx.Enumerations[i].Status = "completed"
		pCtx.Enumerations[i].UpdatedAt = completedAt
		if pCtx.Enumerations[i].EndedAt.IsZero() {
			pCtx.Enumerations[i].EndedAt = completedAt
		}
	}
}
