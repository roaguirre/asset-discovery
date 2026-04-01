# AGENTS.md

This file is the operational guide for AI coding assistants working in this repository. Use [SOUL.md](SOUL.md) for project posture and decision-making bias. Use this file for concrete rules, source-of-truth locations, and workflow expectations.

## Read These Docs First

- [README.md](README.md): landing page, entrypoints, and local workflow
- [ARCHITECTURE.md](ARCHITECTURE.md): stable system boundaries
- [docs/runtime-model.md](docs/runtime-model.md): canonical runtime state, scheduler semantics, and checkpoints
- [docs/pipeline-state-machine.md](docs/pipeline-state-machine.md): engine phases, wave ordering, and Mermaid conventions
- [SOUL.md](SOUL.md): values and architectural posture

## Working Model

- Language: Golang 1.25
- Paradigm: a scheduler-owned DAG with resumable waves
- Runtime assembly: [`internal/app/`](internal/app/) owns pipeline wiring, judges, shared clients, exporters, run IDs, and output policy
- Stage packaging: concrete stage families live under [`internal/collect/`](internal/collect/), [`internal/enrich/`](internal/enrich/), [`internal/filter/`](internal/filter/), and [`internal/export/`](internal/export/). The engine also supports dedicated expander stages between enrichment and filtering.
- Canonical runtime graph:
  - `PipelineContext.Assets` is the canonical runtime asset set
  - `PipelineContext.Observations` stores raw per-stage emissions
  - `PipelineContext.Relations` stores discovery and promotion edges
- Explainability state:
  - `PipelineContext.JudgeEvaluations` records judge outcomes for later projection
  - execution events are emitted through `EmitExecutionEvent` and projected by listeners; they are not stored as a slice on `PipelineContext`
- Tracing split:
  - runtime observability belongs in [`internal/tracing/telemetry/`](internal/tracing/telemetry/)
  - exported provenance and judge lineage belong in [`internal/tracing/lineage/`](internal/tracing/lineage/)
- Model boundary: [`internal/models/`](internal/models/) is for pipeline-core runtime state only. Export or view DTOs belong with the packages that own those outputs.

## Non-Negotiable Rules

1. Do not let stages orchestrate other stages. Collectors, enrichers, expanders, filters, reconsiderers, and exporters are scheduled by the engine.
2. Keep frontier expansion scheduler-owned. If a stage discovers seeds, hand them back through `PipelineContext` helper paths instead of recursing directly.
3. Follow-up collection waves must consume only the active frontier. Do not re-run the full historical seed set by default.
4. Use canonical upsert helpers on `PipelineContext` for assets, observations, and relations. Do not hand-roll append logic that bypasses canonical identity, provenance, or relation resolution.
5. Enrich canonical assets. New evidence should usually add provenance or enrichment state, not trigger duplicate network work automatically.
6. Treat `PipelineContext` as live mutex-bearing state. Use pointers for live access and lock-safe snapshot or DTO shapes for checkpoints, projections, and exports.
7. Keep runtime-owned dependencies in [`internal/app/`](internal/app/). Avoid ad hoc construction of judges, shared HTTP clients, or telemetry providers inside stage packages.
8. Use deterministic code for protocol parsing, normalization, extraction, and deduplication. Use judges for ambiguous ownership or promotion decisions instead of silently widening scope with heuristics.
9. Emit runtime observability through [`internal/tracing/telemetry/`](internal/tracing/telemetry/), not through new direct logging patterns inside stage packages.
10. When optional configuration causes a stage to no-op, emit an execution event so the live Activity view explains the silent state.
11. Keep the CLI path viable. Structural changes should still work from [`cmd/discover/main.go`](cmd/discover/main.go), not only through the live server.
12. Before handoff, run `make validate`. That is the repository's CI-parity verification path.

## Local Workflow

- Preferred local server command: `make server`
- `make server` loads `.env.local` before running `go run ./cmd/server`
- Primary verification command: `make validate`
- Faster iteration command: `go test ./...`
- Firestore emulator coverage: `make test-firebase`
- `make validate` may rewrite tracked Go files through `go fmt ./...`; inspect the working tree after it completes

## Stage And Model Changes

When adding or changing pipeline behavior:

1. Put pipeline-core runtime state in [`internal/models/`](internal/models/) only if it is truly part of the live runtime graph or scheduler state.
2. Implement the appropriate interface in the stage package that owns the concern.
3. Register stage wiring in [`internal/app/`](internal/app/) so runtime assembly stays centralized.
4. Put export-facing, projection-facing, or lineage-facing DTOs in the package that owns those outputs instead of inflating `internal/models/`.

## Design Bias

- Prefer deep modules with narrow interfaces.
- Reduce complexity before adding flexibility.
- Write comments for non-obvious intent, not for obvious mechanics.
- Keep provenance, ownership reasoning, and runtime state boundaries explainable.
