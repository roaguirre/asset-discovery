# Pipeline State Machine

The source of truth for execution order is [`internal/dag/engine.go`](../internal/dag/engine.go). These diagrams intentionally use a conservative Mermaid subset so they render reliably in GitHub-flavored Markdown.

## Engine State Machine

The engine is resumable. Checkpoints let the live server pause after major boundaries such as wave completion or reconsideration.

```mermaid
stateDiagram-v2
    [*] --> CollectionWave : initialize frontier
    CollectionWave --> AdvanceFrontier : checkpoint
    AdvanceFrontier --> CollectionWave : next wave
    AdvanceFrontier --> Reconsideration : frontier exhausted
    Reconsideration --> AdvanceExtraWave : checkpoint
    AdvanceExtraWave --> ExtraWave : final frontier ready
    AdvanceExtraWave --> Filtering : no final frontier
    ExtraWave --> Filtering : checkpoint
    Filtering --> Exporting
    Exporting --> Completed
    Completed --> [*]
```

Notes:

- `AdvanceFrontier` increments the wave number before the next collection pass.
- `AdvanceExtraWave` can open at most one bounded extra frontier after reconsideration.
- If the extra frontier is empty, the engine proceeds directly to filtering and export.

## What Happens Inside Each Wave

Collectors run in parallel. Enrichers and expanders then run in order so later stages can see the canonical state created earlier in the wave.

```mermaid
flowchart TD
    Start([Wave starts]) --> Collectors

    subgraph Collectors["Collectors run in parallel"]
        DNS["DNS"]
        CrtSh["crt.sh"]
        RDAP["RDAP"]
        ReverseRegistration["Reverse registration"]
        HackerTarget["HackerTarget"]
        AlienVault["AlienVault"]
        Wayback["Wayback"]
        ASNCIDR["ASN and CIDR"]
        Sitemap["Sitemap"]
        Crawler["Crawler"]
        WebHint["Web hint"]
    end

    Collectors --> DomainEnricher["Domain enricher"]
    DomainEnricher --> IPEnricher["IP enricher"]

    subgraph Expanders["Expanders run in order"]
        AISearch["AI search expander"]
    end

    IPEnricher --> AISearch
    AISearch --> End([Wave completes])
```

Notes:

- The stage set is assembled in [`internal/app/pipeline.go`](../internal/app/pipeline.go).
- Collector failures are recorded on the runtime context so one collector panic does not automatically abort the whole wave.
- Enrichers and expanders are intentionally ordered, not parallelized together.

## Enricher Dependency Detail

The important dependency is that `IPEnricher` must read canonical IP assets after `DomainEnricher` has had a chance to create them.

```mermaid
flowchart LR
    subgraph DomainEnricher["DomainEnricher"]
        DERead["Read canonical domains"] --> DEDNS["DNS lookups"]
        DEDNS --> DECreateIP["Create IP assets"]
        DECreateIP --> DERDAP["RDAP lookups"]
        DERDAP --> DEMutate["Update canonical domain assets"]
    end

    subgraph IPEnricher["IPEnricher"]
        IERead["Read canonical IPs"] --> IEFork{"Parallel lookups"}
        IEFork --> IEPTR["PTR lookups"]
        IEFork --> IEASN["ASN lookups"]
        IEPTR --> IEJoin["Join results"]
        IEASN --> IEJoin
        IEJoin --> IEOwn["Ownership classification"]
        IEOwn --> IEMutate["Update canonical IP assets"]
    end

    DEMutate --> IERead
```

Notes:

- `DomainEnricher` mutates canonical domain assets and may create new canonical IP assets in the same wave.
- `IPEnricher` performs PTR and ASN work in parallel internally, then joins those results before mutating canonical IP state.
- The dependency is on canonical visibility, not on direct stage-to-stage calls.

## DNS Collector Internal Concurrency

The DNS collector uses nested concurrency: a base worker pool across domains, plus parallel lookups inside each domain task.

```mermaid
flowchart TD
    Start([Start]) --> Base["Base domain resolution"]
    Base --> Fanout{"Per-domain fan-out"}
    Fanout --> Observe["observeDomain"]
    Fanout --> LookupRDAP["lookupRDAP"]
    Observe --> DNS["Parallel DNS record lookups"]
    DNS --> Join["Merge per-domain result"]
    LookupRDAP --> Join
    Join --> Merge["Merge collector result"]
    Merge --> Sweep["Variant sweep"]
    Sweep --> Judge["Judge evaluation"]
    Judge --> End([End])
```

Notes:

- Base domain resolution and variant sweep use the collector worker pool.
- Per-domain DNS record lookups run in parallel for `A`, `AAAA`, `MX`, `TXT`, `NS`, and `CNAME`.
- Judge evaluation happens after the collector has merged the domain-level evidence it gathered.

## Mermaid Authoring Note

GitHub Markdown is the compatibility baseline for diagrams in this repository.

- Do not use raw `\n` inside node labels or edge labels.
- Prefer short labels and move detail into the surrounding prose.
- Use quoted or bracketed labels when punctuation makes Mermaid parsing fragile.
- If a diagram becomes text-heavy, simplify the diagram and explain the nuance below it.
