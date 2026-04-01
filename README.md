# Asset Discovery

Asset Discovery is a Go-based system for expanding a seed set into an explainable asset graph. It combines deterministic OSINT collection, judge-gated ownership decisions, canonical runtime state, and resumable live runs for the sibling web client.

## What It Does

- Collects candidate domains and IPs from multiple OSINT and network-oriented sources.
- Enriches canonical assets with DNS, RDAP, PTR, ASN, organization, and ownership context.
- Runs dedicated expanders after enrichment to propose new seeds from the current wave context.
- Preserves canonical assets separately from raw observations and discovery relations.
- Lets the scheduler, not individual stages, decide when another collection frontier exists.
- Supports both local CLI runs and Firebase-backed live runs with optional human review.

## Quickstart

```bash
# Build the CLI
go build -o discover ./cmd/discover

# Run a local discovery pass
./discover --seeds test.json

# Validate the repository the same way CI does
make validate
```

Enable the tracked Git hooks once per clone:

```bash
git config --local core.hooksPath .githooks
```

## Choose Your Entrypoint

### CLI (`cmd/discover`)

Use the CLI for deterministic local runs, regression checks, and file exports.

```bash
./discover --seeds path/to/seeds.json
./discover --seeds path/to/seeds.json --outputs results.json,results.csv,results.xlsx
```

If `--outputs` is omitted, the CLI writes `results.json`, `results.csv`, and `results.xlsx` under `exports/runs/<run-id>/`.

### Server (`cmd/server`)

Use the HTTP server for Firebase-backed live runs, pivot review, Firestore projections, and artifact publishing for the sibling `asset-discovery-web` app.

```bash
cp .env.example .env.local
make server
```

`make server` loads `.env.local` automatically. At minimum, set:

- `ASSET_DISCOVERY_FIREBASE_PROJECT_ID`
- `ASSET_DISCOVERY_EXPORT_GCS_BUCKET`

For local end-to-end development, run the backend here and the web client in the sibling repository:

```bash
# terminal 1
make server

# terminal 2
cd ../asset-discovery-web
npm run dev
```

### Worker (`cmd/worker`)

Use the worker when live runs should execute outside the HTTP server process, typically through a Cloud Run job or another external dispatcher.

By default, the server runs jobs in-process. If `ASSET_DISCOVERY_WORKER_JOB_NAME` and `ASSET_DISCOVERY_WORKER_JOB_REGION` are configured, the server dispatches runs to the worker entrypoint instead.

```bash
export ASSET_DISCOVERY_RUN_ID="<run-id>"
go run ./cmd/worker
```

Optional worker tuning:

- `ASSET_DISCOVERY_WORKER_LEASE_TTL`
- `ASSET_DISCOVERY_WORKER_HEARTBEAT_INTERVAL`

## Runtime At A Glance

- `PipelineContext.Assets` is the canonical runtime asset graph.
- `PipelineContext.Observations` stores raw per-stage emissions.
- `PipelineContext.Relations` stores discovery and promotion edges between assets.
- Ambiguous ownership and promotion decisions stay judge-gated instead of being silently hardcoded.

## Documentation Map

- [ARCHITECTURE.md](ARCHITECTURE.md): stable system boundaries and operational shape
- [docs/runtime-model.md](docs/runtime-model.md): canonical runtime state, scheduler semantics, checkpoints, and read models
- [docs/pipeline-state-machine.md](docs/pipeline-state-machine.md): engine phases, wave execution order, and Mermaid diagrams
- [AGENTS.md](AGENTS.md): operational guidance for AI coding assistants
- [SOUL.md](SOUL.md): project posture, values, and decision-making bias

## Development Notes

- Preferred local server command: `make server`
- Primary verification command: `make validate`
- Faster iteration command: `go test ./...`
- Firestore emulator coverage: `make test-firebase`
- `make validate` may rewrite tracked Go files through `go fmt ./...`; inspect the working tree after it completes

See [ARCHITECTURE.md](ARCHITECTURE.md) and [docs/runtime-model.md](docs/runtime-model.md) before making structural changes.
