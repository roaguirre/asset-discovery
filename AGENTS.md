# AGENTS.md

## Information for AI Agents

This file describes how AI coding assistants should interact with this repository.

### Core Architecture
-   **Language**: Golang 1.21+ (or latest).
-   **Paradigm**: Directed Acyclic Graph (DAG). Stages are intentionally decoupled.
-   **Data Transfer**: Uses `models.PipelineContext` for exchanging state.
-   **Canonical Runtime State**: `PipelineContext.Assets` is the canonical runtime asset set. Raw stage emissions belong in `PipelineContext.Observations`, and discovery / promotion edges belong in `PipelineContext.Relations`.
-   **Extensibility**: Interfaces MUST be used for processing nodes (`Collector`, `Enricher`, `Filter`, `Exporter`) to allow easy replacement with a PubSub/message broker strategy in the future.
-   **Runtime Assembly**: `internal/app/` owns pipeline wiring, shared HTTP clients, judges, exporters, run IDs, and output policy. CLI code should delegate there instead of assembling the DAG directly.
-   **Stage Packaging**: Concrete stage implementations live under `internal/collect/`, `internal/enrich/`, `internal/filter/`, and `internal/export/`.
-   **Tracing Split**: Runtime observability belongs in `internal/tracing/telemetry/`. Exported provenance, judge traces, and result lineage belong in `internal/tracing/lineage/`.
-   **Model Boundary**: `internal/models/` is for pipeline-core state only. Export/view DTOs belong with `internal/export/` or `internal/tracing/lineage/`, not `internal/models/`.

### Workflow Rules
1.  **Do not break the DAG**: Never make the `Collector` call the `Enricher` directly. Nodes must be scheduled by a central DAG engine or an event system.
2.  **Engine-owned scheduling only**: If an `Enricher` discovers new seeds, it must hand them back to the engine or scheduler layer. Do not let processing nodes control recursion, collection loops, or stage-to-stage orchestration themselves.
3.  **Frontier-based collection**: Follow-up collection waves must process only the active seed frontier, not the full historical seed set, to avoid duplicate collection work.
4.  **Strict Typing**: Ensure all JSON tags are strictly defined and follow idiomatic Go (e.g. `json:"company_name,omitempty"`).
5.  **Local Testing First**: The app must remain operable from a simple local CLI (`cmd/discover/main.go`).
6.  **No Global State**: Avoid `init()` functions that mutate global variables, pass dependencies explicitely.
7.  **Deterministic Parsing vs Ownership Reasoning**: Use deterministic code for protocol parsing, normalization, extraction, deduplication, and other stable mechanical transforms. When the task is to judge ownership, first-party scope, or whether a discovered candidate should be collected or promoted despite ambiguous evidence, prefer an LLM judge over hardcoded heuristics. Heuristics may generate candidates or evidence, but they must not silently make final ownership decisions.
8.  **Runtime-Owned Dependencies**: Shared judges, HTTP clients, telemetry providers, and other high-level dependencies should be assembled in `internal/app/` and injected into stages. Avoid adding new ad hoc construction logic inside stage implementations unless it is strictly local and low-level.
9.  **Telemetry API Only**: Stage packages should emit runtime logging/tracing through `internal/tracing/telemetry/` rather than calling the global `log` package directly.
10. **Canonical Upserts Only**: When emitting discovered assets or runtime relations, prefer the canonical helper paths on `PipelineContext` instead of direct append patterns. The runtime model relies on those helpers to maintain canonical assets, observations, provenance, and relations consistently.
11. **Enrich Canonical Assets**: Enrichers should iterate canonical assets and use per-stage enrichment state for cache / retry decisions. A new observation or contributor should add provenance, not force duplicate network work by default.

### Adding New Stages
To add a new stage to the DAG:
1.  Define its structs in `internal/models/`.
2.  Implement the `Node` interface in the stage package that matches the concern:
    `internal/collect/`, `internal/enrich/`, `internal/filter/`, or `internal/export/`.
3.  Register it in `internal/app/` so runtime assembly stays centralized.
