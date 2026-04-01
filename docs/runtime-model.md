# Runtime Model

This document describes the runtime data model used while a discovery run is executing. The source of truth lives in [`internal/models/`](../internal/models/), but the goal here is to describe the semantics rather than mirror every field literally.

## Overview

`models.PipelineContext` is the live runtime object passed between DAG stages. It carries canonical assets, raw provenance, scheduler state, judge output, and mutation hooks needed by exporters and live projections.

Treat `PipelineContext` as live mutable state:

- use it by pointer
- do not copy it by value
- use snapshot helpers when another subsystem needs a stable read view

## Canonical Graph Versus Raw Evidence

The runtime graph is intentionally split into distinct layers.

| Runtime state | Purpose | Notes |
| --- | --- | --- |
| `Assets` | Canonical runtime asset set | Keyed by `(type, identifier)` and updated through canonical upsert helpers |
| `Observations` | Raw per-stage emissions | Preserves repeated sightings and `discovery` versus `enrichment` context |
| `Relations` | Discovery and promotion edges | Resolves to canonical asset IDs when possible |
| `JudgeEvaluations` | Explain ambiguous decisions | Records judge requests and candidate outcomes for later projection |
| `Errors` | Runtime failures | Captures collector errors and panic recovery without pretending they are canonical data |
| `DNSVariantSweepLabels` | Per-run probe cache markers | Prevents duplicate variant sweep work within a run |
| `AISearchExecutedRoots` | Per-run AI-search cache markers | Prevents duplicate AI-search passes for the same registrable root within a run |

The important consequence is that repeated sightings do not create duplicate canonical rows. They add observations, provenance, and relations instead.

## Canonical Upsert Rules

Canonical asset maintenance belongs to `PipelineContext` helper methods:

- `AppendAssets`
- `AppendAssetObservations`
- `AppendAssetRelations`

Those helpers normalize identifiers, assign IDs, merge provenance, preserve stronger ownership states, resolve relation endpoints, and update enrichment state consistently.

Stages should not bypass those helpers with direct append patterns unless they are deliberately operating on a lock-safe snapshot or DTO outside the live runtime context.

## Scheduler State And Frontier Expansion

`PipelineContext` contains both public runtime slices and private scheduler bookkeeping. While a run is active, the engine uses helper methods such as:

- `InitializeSeedFrontier`
- `CollectionSeeds`
- `EnqueueSeed`
- `EnqueueSeedCandidate`
- `AdvanceSeedFrontier`
- `ReserveExtraCollectionWave`

The semantics are:

- submitted seeds initialize the first frontier
- a collection wave processes only the active frontier
- enrichers, expanders, and judges can enqueue discovered seeds for a later wave
- the normal expansion depth is bounded to the initial frontier plus three discovered frontiers
- after the normal frontier is exhausted, reconsideration may reserve one final extra frontier
- seeds discovered during that final frontier are still recorded, but they do not open another follow-up wave

The engine persists the scheduler-specific pieces separately as `models.SchedulerState` so checkpoints can restore the frontier accurately without serializing internal mutex state.

## Judge Output And Live Activity

Two related concepts support explainability:

### Judge Evaluations

`JudgeEvaluations` lives on `PipelineContext` and records:

- which collector or scenario asked for judgment
- what seed context was involved
- which candidates were accepted, rejected, or left uncollected
- why those outcomes happened

This data later feeds the live judge summary and pivot review surfaces.

### Execution Events

Execution events are runtime activity signals emitted through `PipelineContext.EmitExecutionEvent` and forwarded to the installed `MutationListener`. They are projected to the live `events` read model, but they are not stored as a slice on `PipelineContext`.

That distinction matters:

- canonical graph state belongs in `PipelineContext`
- live activity is emitted alongside the graph, not merged into it

## Checkpoints, Snapshots, And Resume

Live runs do not persist only the graph. A resumable checkpoint combines several pieces:

- `runservice.RunRecord`: external run metadata and status
- `*models.PipelineContext`: canonical graph plus judge data
- `models.SchedulerState`: frontier bookkeeping needed to continue correctly
- `dag.RunProgress`: current engine phase and wave number
- pending pivot state for manual review

`PipelineContext.SnapshotReadModel()` produces a deep-cloned view for exporters and live projections. That snapshot intentionally excludes the live mutex and other runtime-only internals.

## Runtime State Versus Exported Or Projected State

Different packages own different representations for a reason:

- `internal/models/` owns pipeline-core runtime state
- `internal/export/` owns export-facing DTOs and file formats
- `internal/runservice/` owns Firestore-facing run, pivot, event, and asset projection records
- `internal/tracing/lineage/` owns exported provenance and judge lineage views

Do not collapse those concerns back into one shared struct. The runtime graph exists to execute the pipeline safely; projections and exports exist to explain results outside the live engine.

## Source Of Truth

When documentation and code disagree, use the code:

- [`internal/models/models.go`](../internal/models/models.go)
- [`internal/models/assets.go`](../internal/models/assets.go)
- [`internal/models/snapshot.go`](../internal/models/snapshot.go)
- [`internal/dag/engine.go`](../internal/dag/engine.go)
- [`internal/runservice/types.go`](../internal/runservice/types.go)
