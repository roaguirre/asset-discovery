# Asset Discovery

A robust, highly-concurrent Golang application for enterprise asset discovery.

## Overview

This project implements a Directed Acyclic Graph (DAG) for processing asset discovery. It takes "seeds" (e.g., Company Name, Domain, Address, Industry) and processes them through decoupled stages:
1. **Collection**: Gathering raw data from OSINT sources and APIs.
2. **Enrichment**: Augmenting the collected domains with DNS, IP, and provider data.
3. **Filtering**: Removing false positives, dead domains, or out-of-scope assets.
4. **Exporting**: Formatting the final dataset for consumption (JSON, CSV, DB).

If enrichment discovers new seeds, the engine can schedule a later follow-up collection wave using only that new frontier. This preserves acyclic stage execution inside each wave while still expanding coverage across public asset pivots.

## Usage

Currently, the entrypoint is a local CLI.

```bash
# Build the project
go build -o discover cmd/discover/main.go

# Run the discovery pipeline with archived default outputs
./discover --seeds path/to/seeds.json

# Or choose explicit output files
./discover --seeds path/to/seeds.json --outputs results.json,results.csv,visualizer.html
```

When `--outputs` is omitted, each run is archived under `exports/runs/<run-id>/` and [`exports/visualizer.html`](exports/visualizer.html) is refreshed to show the latest run by default while keeping older runs selectable.

Exports separate registrable domains from discovered subdomains. JSON stays as a flat asset array and adds per-row `domain_kind` and `registrable_domain` metadata, CSV includes `Domain Kind` and `Registrable Domain` columns, XLSX uses dedicated `Registrable Domains` and `Subdomains` sheets, and the visualizer exposes the same split as sortable/filterable columns.

### Optional LLM Judging For Ownership Decisions

The ownership-style pivots can use LLM judges instead of hardcoded brand and evidence-weight heuristics:

- `web_hint_collector` for external anchor-link roots found on the homepage
- `reverse_registration_collector` for candidate domains discovered through CT and RDAP overlap
- `asn_cidr_collector` and `ip_enricher` for PTR-derived registrable domains found from network pivots

Deterministic parsing still extracts candidates from canonical tags, redirects, `security.txt`, and external anchors. Cross-root ownership collection now stays judge-gated instead of promoting those roots directly.

Set these environment variables to enable the web-hint judge:

```bash
export ASSET_DISCOVERY_WEB_HINT_LLM_MODEL="your-model"
export ASSET_DISCOVERY_WEB_HINT_LLM_API_KEY="your-api-key"

# Optional when using an OpenAI-compatible endpoint other than the default.
export ASSET_DISCOVERY_WEB_HINT_LLM_BASE_URL="https://your-provider.example/v1"
# Or provide the full chat-completions endpoint directly.
export ASSET_DISCOVERY_WEB_HINT_LLM_ENDPOINT="https://your-provider.example/v1/chat/completions"
```

Set these environment variables to enable the broader ownership judge used by registration and PTR pivots:

```bash
export ASSET_DISCOVERY_OWNERSHIP_LLM_MODEL="your-model"
export ASSET_DISCOVERY_OWNERSHIP_LLM_API_KEY="your-api-key"

# Optional when using an OpenAI-compatible endpoint other than the default.
export ASSET_DISCOVERY_OWNERSHIP_LLM_BASE_URL="https://your-provider.example/v1"
# Or provide the full chat-completions endpoint directly.
export ASSET_DISCOVERY_OWNERSHIP_LLM_ENDPOINT="https://your-provider.example/v1/chat/completions"
```

If either judge is not configured, its ownership decisions are skipped rather than falling back to hardcoded heuristics.

## Docker Environment

For reproducibility, a Dockerfile is provided:

```bash
docker build -t asset-discovery .
docker run -v $(pwd):/data asset-discovery --seeds /data/seeds.json --outputs /data/results.json,/data/results.csv
```

## Developing

See [ARCHITECTURE.md](ARCHITECTURE.md) for design principles and data models.
See [AGENTS.md](AGENTS.md) for how AI coding assistants should interact with this repository.
See [docs/dag_visualization.html](docs/dag_visualization.html) for an interactive view of the scheduler-managed pipeline and topological sort behavior.
