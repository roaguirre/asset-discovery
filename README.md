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
