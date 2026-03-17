# Asset Discovery

A robust, highly-concurrent Golang application for enterprise asset discovery.

## Overview

This project implements a Directed Acyclic Graph (DAG) for processing asset discovery. It takes "seeds" (e.g., Company Name, Domain, Address, Industry) and processes them through decoupled stages:
1. **Collection**: Gathering raw data from OSINT sources and APIs.
2. **Enrichment**: Augmenting the collected domains with DNS, IP, and provider data.
3. **Filtering**: Removing false positives, dead domains, or out-of-scope assets.
4. **Exporting**: Formatting the final dataset for consumption (JSON, CSV, DB).

## Usage

Currently, the entrypoint is a local CLI.

```bash
# Build the project
go build -o discover cmd/discover/main.go

# Run the discovery pipeline
./discover --seeds path/to/seeds.json --output results.json
```

## Docker Environment

For reproducibility, a Dockerfile is provided:

```bash
docker build -t asset-discovery .
docker run -v $(pwd):/data asset-discovery --seeds /data/seeds.json --output /data/results.json
```

## Developing

See [ARCHITECTURE.md](ARCHITECTURE.md) for design principles and data models.
See [AGENTS.md](AGENTS.md) for how AI coding assistants should interact with this repository.
