package runservice

import (
	"asset-discovery/internal/models"
	"asset-discovery/internal/tracing/lineage"
)

func buildProjectedJudgeSummary(pCtx *models.PipelineContext) lineage.JudgeSummary {
	if pCtx == nil {
		return lineage.JudgeSummary{}
	}

	summary := lineage.BuildJudgeSummary(pCtx.JudgeEvaluations)
	if summary == nil {
		return lineage.JudgeSummary{}
	}
	return *summary
}

func applyProjectedRunMetrics(
	run *RunRecord,
	pCtx *models.PipelineContext,
	pendingPivotCount int,
	judgeSummary lineage.JudgeSummary,
) {
	if run == nil {
		return
	}

	if pCtx != nil {
		run.AssetCount = len(pCtx.Assets)
		run.EnumerationCount = len(pCtx.Enumerations)
		run.SeedCount = len(pCtx.Seeds)
	}
	run.PendingPivotCount = pendingPivotCount
	run.JudgeEvaluationCount = judgeSummary.EvaluationCount
	run.JudgeAcceptedCount = judgeSummary.AcceptedCount
	run.JudgeDiscardedCount = judgeSummary.DiscardedCount
}
