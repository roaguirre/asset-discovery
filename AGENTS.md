# AGENTS.md

## Information for AI Agents

This file describes how AI coding assistants should interact with this repository.

### Core Architecture
-   **Language**: Golang 1.21+ (or latest).
-   **Paradigm**: Directed Acyclic Graph (DAG). Stages are intentionally decoupled.
-   **Data Transfer**: Uses `models.PipelineContext` for exchanging state.
-   **Extensibility**: Interfaces MUST be used for processing nodes (`Collector`, `Enricher`, `Filter`, `Exporter`) to allow easy replacement with a PubSub/message broker strategy in the future.

### Workflow Rules
1.  **Do not break the DAG**: Never make the `Collector` call the `Enricher` directly. Nodes must be scheduled by a central DAG engine or an event system.
2.  **Engine-owned scheduling only**: If an `Enricher` discovers new seeds, it must hand them back to the engine or scheduler layer. Do not let processing nodes control recursion, collection loops, or stage-to-stage orchestration themselves.
3.  **Frontier-based collection**: Follow-up collection waves must process only the active seed frontier, not the full historical seed set, to avoid duplicate collection work.
4.  **Strict Typing**: Ensure all JSON tags are strictly defined and follow idiomatic Go (e.g. `json:"company_name,omitempty"`).
5.  **Local Testing First**: The app must remain operable from a simple local CLI (`cmd/discover/main.go`).
6.  **No Global State**: Avoid `init()` functions that mutate global variables, pass dependencies explicitely.

### Adding New Stages
To add a new stage to the DAG:
1.  Define its structs in `internal/models/`.
2.  Implement the `Node` interface in `internal/nodes/`.
3.  Register it in `internal/dag/engine.go` (or via config).
