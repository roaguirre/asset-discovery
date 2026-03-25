# Asset Discovery

A robust, highly-concurrent Golang application for enterprise asset discovery.

## Overview

This project implements a Directed Acyclic Graph (DAG) for processing asset discovery. It takes "seeds" (e.g., Company Name, Domain, Address, Industry) and processes them through decoupled stages:
1. **Collection**: Gathering raw data from OSINT sources and APIs and emitting raw observations plus discovery relations.
2. **Enrichment**: Augmenting canonical domain and IP assets with DNS, RDAP, PTR, ASN, organization, and provider data.
3. **Filtering / Validation**: Validating the canonical runtime graph and applying downstream scope policy.
4. **Exporting**: Formatting the canonical asset set plus its provenance graph for JSON, CSV, XLSX, and the visualizer.

Runtime state now uses a hybrid model instead of one overloaded asset stream:

- `PipelineContext.Assets` holds canonical assets keyed by `(type, identifier)`.
- `PipelineContext.Observations` preserves raw per-stage emissions for provenance.
- `PipelineContext.Relations` stores discovery edges such as `dns_a`, `dns_aaaa`, `dns_ns`, `ptr`, `web_hint`, `crawl_link`, and judge promotions.

That split lets repeated sightings across collection waves add provenance without forcing repeat enrichment lookups or producing duplicate final rows.

If enrichment discovers new seeds, the engine can schedule a later follow-up collection wave using only that new frontier. This preserves acyclic stage execution inside each wave while still expanding coverage across public asset pivots.

Once the normal collection/enrichment frontier is exhausted, the engine now also performs one automatic post-run reconsideration pass over discarded judge candidates. If that reconsideration promotes any roots, the scheduler opens exactly one extra frontier for those promoted seeds and then continues to filtering/exporting. If the aggregated reconsideration prompt exceeds the internal size budget, the pass is skipped as a non-fatal step.

## Usage

This repository now supports two entrypoints:

- the local CLI for deterministic regression testing and archived exports
- an HTTP server for Firebase-backed live runs, Google sign-in, and human-in-the-loop pivot review

```bash
# Build the project
go build -o discover cmd/discover/main.go

# Run the discovery pipeline with archived default outputs
./discover --seeds path/to/seeds.json

# Or choose explicit output files plus a visualizer archive directory
./discover --seeds path/to/seeds.json --outputs results.json,results.csv,visualizer:exports/visualizer
```

### Live Server

```bash
export ASSET_DISCOVERY_FIREBASE_PROJECT_ID="your-project-id"
export ASSET_DISCOVERY_SERVER_ADDR=":8080"

# Optional when running with a service-account file locally.
export GOOGLE_APPLICATION_CREDENTIALS="/path/to/service-account.json"

# Optional: use GCS for resumable run checkpoints instead of the local checkpoints/ directory.
export ASSET_DISCOVERY_CHECKPOINT_GCS_BUCKET="your-checkpoint-bucket"

go run ./cmd/server
```

For local development, you can also place those variables in `.env.local` and run:

```bash
make server
```

### Live Run Model

The live server keeps the browser-facing read model in Firestore and the resumable runtime checkpoint outside Firestore:

- Firestore stores runs, assets, traces, pivots, seeds, and activity events for the web client.
- checkpoints are stored either under the local `checkpoints/` directory or in GCS when `ASSET_DISCOVERY_CHECKPOINT_GCS_BUCKET` is configured.
- the worker is queue-ready but currently runs in-process inside the Go server.

Each run is created in one of two modes:

- `autonomous`: AI-judged pivots are applied automatically and the run keeps executing.
- `manual`: AI-judged pivots are written as pending review items and the run pauses in `awaiting_review` until the creator accepts or rejects them.

Creator ownership is enforced on pivot decisions. The same authenticated user who created the run must approve or reject manual pivots.

The live server exposes:

- `POST /api/runs`
- `POST /api/runs/{runId}/pivots/{pivotId}/decision`

Both endpoints require a Firebase ID token from a verified Google account in `@zerofox.com` or `roaguirred@gmail.com`.

Cross-origin browser writes are supported only from the current demo web app origins and the local Vite dev server:

- `http://localhost:5173`
- `http://127.0.0.1:5173`
- `https://asset-discovery-0325-f111.web.app`
- `https://asset-discovery-0325-f111.firebaseapp.com`

Requests without an `Origin` header continue to work normally, so same-origin and server-to-server callers do not depend on CORS.

### Local End-to-End Development

The intended local setup is the backend plus the sibling `asset-discovery-web` dev server:

```bash
# terminal 1
make server

# terminal 2
cd ../asset-discovery-web
npm run dev
```

Leave `VITE_ASSET_DISCOVERY_API_BASE_URL` empty in the web app for local development. Vite proxies `/api/*` and `/healthz` to `http://127.0.0.1:8080`, which avoids cross-origin browser calls during local work.

For Firestore-backed integration coverage against the real emulator:

```bash
make test-firebase
```

That target starts the Firestore emulator using the sibling `asset-discovery-web/firebase.json` config, runs the `internal/runservice` suite against the emulator-backed projection store, and auto-selects an installed JDK 21+ if the default Java runtime is older.

`make test-firebase` uses the Firestore emulator port from the sibling Firebase config. If you are already running another service on that port, stop it first or run the emulator test with a temporary alternate config.

When `--outputs` is omitted, each run is archived under `exports/runs/<run-id>/` and a visualizer data archive is written under [`exports/visualizer/`](exports/visualizer). That archive contains `manifest.json` plus per-run snapshots under `runs/<run-id>.json`.

Exports separate registrable domains from discovered subdomains. JSON stays as a flat asset array and adds per-row `domain_kind` and `registrable_domain` metadata, CSV includes `Domain Kind` and `Registrable Domain` columns, XLSX uses dedicated `Registrable Domains` and `Subdomains` sheets, and the visualizer exposes the same split as sortable/filterable columns.

Canonical assets also carry:

- `ownership_state`: `owned`, `associated_infrastructure`, or `uncertain`
- `inclusion_reason`: a short explanation of why the asset is present in the final dataset

The visualizer uses those fields directly in the browse tables and trace view so questionable infrastructure can be shown and explained instead of silently merged into "owned" assets.

## Visualizer

The visualizer is now split into two pieces:

- Go exports data only: `manifest.json` plus archived run JSON under `exports/visualizer/` or any `visualizer:<dir>` target.
- The browser client lives in the separate `asset-discovery-web` repository and loads that data in the browser.

This repository now owns the visualizer data contract instead of the browser implementation. The checked-in contract artifacts live under `contracts/visualizer/`:

- `manifest.v1.schema.json`
- `run.v1.schema.json`
- `manifest.v1.fixture.json`
- `run.v1.fixture.json`

The external client keeps the existing runtime behavior:

- default manifest URL `/exports/visualizer/manifest.json`
- `?manifest=<url-to-manifest.json>` override
- hash routes such as `#trace/<run-id>/<asset-id>`

Breaking payload changes should introduce a new contract major version rather than silently replacing the v1 schema files.

The client has two main modes:

- **Browse views** for domains and IPs with compact inline summaries and an `Open Trace` action.
- **Trace workspace** at `#trace/<run-id>/<asset-id>` with a left trace tree and a right detail panel.

Each trace is rooted at the canonical asset and can include:

- contributing observations
- seed and enumeration context
- discovery relations between assets
- enrichment-state snapshots
- related assets in the same run

This is meant to answer "why is this asset here?" without dumping every merged detail inline in the main results table.

### Optional LLM Judging For Ownership Decisions

The ownership-style pivots can use LLM judges instead of hardcoded brand and evidence-weight heuristics:

- `web_hint_collector` for external anchor-link roots found on the homepage
- `sitemap_collector` for cross-root hosts surfaced by robots.txt and sitemap documents
- `crawler_collector` for outbound roots found while recursively crawling in-scope site pages
- `reverse_registration_collector` for candidate domains discovered through CT and RDAP overlap
- `asn_cidr_collector` and `ip_enricher` for PTR-derived registrable domains found from network pivots

Deterministic parsing still extracts candidates from canonical tags, redirects, `security.txt`, sitemap documents, internal page crawls, and external anchors. Cross-root ownership collection now stays judge-gated instead of promoting those roots directly.

The domain enricher now backfills `A`, `AAAA`, `CNAME`, `MX`, `TXT`, and missing RDAP metadata across discovered domain assets. When it observes fresh `A` or `AAAA` answers, it also materializes IP assets so the IP enricher can run against them in the same collection wave.

The IP enricher performs PTR lookups and ASN / organization enrichment for both IPv4 and IPv6 through Team Cymru DNS pivots. It caches results per canonical IP for the current run, so a later collector wave can reuse the cached enrichment while still attaching new contributor provenance.

When the ownership judge is enabled, the same configuration is also reused for the automatic post-run reconsideration pass. There is no separate CLI flag for this in v1.

Set these environment variables to enable the web-hint judge:

```bash
export ASSET_DISCOVERY_WEB_HINT_LLM_MODEL="your-model"
export ASSET_DISCOVERY_WEB_HINT_LLM_API_KEY="your-api-key"

# Optional when using an OpenAI-compatible endpoint other than the default.
export ASSET_DISCOVERY_WEB_HINT_LLM_BASE_URL="https://your-provider.example/v1"
# Or provide the full chat-completions endpoint directly.
export ASSET_DISCOVERY_WEB_HINT_LLM_ENDPOINT="https://your-provider.example/v1/chat/completions"
```

If `OPENAI_API_KEY` is set, the web-hint judge now enables itself by default using OpenAI's default Chat Completions endpoint and the `gpt-5.4-nano` model. You only need the explicit `ASSET_DISCOVERY_WEB_HINT_*` variables when you want to override the model, key, or endpoint.

Set these environment variables to enable the broader ownership judge used by registration and PTR pivots:

```bash
export ASSET_DISCOVERY_OWNERSHIP_LLM_MODEL="your-model"
export ASSET_DISCOVERY_OWNERSHIP_LLM_API_KEY="your-api-key"

# Optional when using an OpenAI-compatible endpoint other than the default.
export ASSET_DISCOVERY_OWNERSHIP_LLM_BASE_URL="https://your-provider.example/v1"
# Or provide the full chat-completions endpoint directly.
export ASSET_DISCOVERY_OWNERSHIP_LLM_ENDPOINT="https://your-provider.example/v1/chat/completions"
```

Likewise, if `OPENAI_API_KEY` is set, the ownership judge now defaults to OpenAI's `gpt-5.4-nano` model and endpoint automatically. That enables the LLM-gated ownership pivots used by `crawler_collector`, `reverse_registration_collector`, `asn_cidr_collector`, and `ip_enricher` without extra configuration.

If either judge is not configured, its ownership decisions are skipped rather than falling back to hardcoded heuristics.

## Docker Environment

For reproducibility, a Dockerfile is provided:

```bash
docker build -t asset-discovery .
docker run -v $(pwd):/data asset-discovery --seeds /data/seeds.json --outputs /data/results.json,/data/results.csv
```

## Developing

### Generated DNS Suffix Data

The DNS variant sweep uses a generated ICANN public suffix list derived from the pinned `golang.org/x/net/publicsuffix` module version in `go.mod`.

Normal installs and builds do not require `make generate`; the generated file is checked in. Run `make generate` only when you update `golang.org/x/net` and want to refresh the generated suffix data before testing or committing.

See [ARCHITECTURE.md](ARCHITECTURE.md) for design principles and data models.
See [AGENTS.md](AGENTS.md) for how AI coding assistants should interact with this repository.
See [docs/dag_visualization.html](docs/dag_visualization.html) for an interactive view of the scheduler-managed pipeline and topological sort behavior.
