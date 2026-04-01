# Pipeline State Machine

## Engine State Machine

The DAG engine (`internal/dag/engine.go`) is a resumable state machine driven by `RunPhase`.
Checkpoints allow the server to pause execution (e.g. for human-in-the-loop pivot review) and resume later.

```mermaid
stateDiagram-v2
    [*] --> WaveGroup : InitializeSeedFrontier

    state "Wave N" as WaveGroup {
        [*] --> CollectionWave
        CollectionWave --> AdvanceFrontier : checkpoint
        AdvanceFrontier --> CollectionWave : frontier has new seeds\n(wave++)
        AdvanceFrontier --> [*] : frontier exhausted
    }

    WaveGroup --> Reconsideration

    Reconsideration --> AdvanceExtraWave : checkpoint
    AdvanceExtraWave --> ExtraWave : frontier has seeds\n(wave++)
    AdvanceExtraWave --> Filtering : frontier empty

    ExtraWave --> Filtering : checkpoint

    Filtering --> Exporting
    Exporting --> Completed
    Completed --> [*]
```

## What Happens Inside Each Wave (`runWave`)

Each collection wave runs collectors in parallel, then enrichers sequentially.

```mermaid
stateDiagram-v2
    [*] --> Collectors

    state "Collectors (parallel)" as Collectors {
        state fork_collectors <<fork>>
        state join_collectors <<join>>

        [*] --> fork_collectors

        fork_collectors --> DNS
        fork_collectors --> CrtSh
        fork_collectors --> RDAP
        fork_collectors --> ReverseRegistration
        fork_collectors --> HackerTarget
        fork_collectors --> AlienVault
        fork_collectors --> Wayback
        fork_collectors --> ASNCIDR
        fork_collectors --> Sitemap
        fork_collectors --> Crawler
        fork_collectors --> WebHint

        DNS --> join_collectors
        CrtSh --> join_collectors
        RDAP --> join_collectors
        ReverseRegistration --> join_collectors
        HackerTarget --> join_collectors
        AlienVault --> join_collectors
        Wayback --> join_collectors
        ASNCIDR --> join_collectors
        Sitemap --> join_collectors
        Crawler --> join_collectors
        WebHint --> join_collectors

        join_collectors --> [*]
    }

    Collectors --> Enrichers

    state "Enrichers (sequential)" as Enrichers {
        [*] --> DomainEnricher
        DomainEnricher --> IPEnricher : creates IP assets
        IPEnricher --> [*]
    }

    Enrichers --> [*]

    note right of Enrichers
        DomainEnricher resolves domains (DNS + RDAP)
        and creates new IP assets.
        IPEnricher must see those IPs, so it runs second.
    end note
```

## Enricher Dependency Detail

```mermaid
stateDiagram-v2
    state "DomainEnricher" as DE {
        state "Read domain assets" as DE_Read
        state "DNS lookups (32 workers)" as DE_DNS
        state "RDAP lookups" as DE_RDAP
        state "Create IP assets" as DE_CreateIP
        state "Mutate domain assets\n(Records, RDAP, EnrichmentState)" as DE_Mutate

        [*] --> DE_Read
        DE_Read --> DE_DNS
        DE_RDAP --> DE_Mutate
        DE_DNS --> DE_CreateIP
        DE_CreateIP --> DE_RDAP
        DE_Mutate --> [*]
    }

    state "IPEnricher" as IE {
        state "Read IP assets\n(including DomainEnricher-created)" as IE_Read
        state "PTR lookups (50 workers)" as IE_PTR
        state "ASN lookups (Cymru DNS)" as IE_ASN
        state "Ownership classification" as IE_Own
        state "Mutate IP assets\n(PTR, ASN, Org, OwnershipState)" as IE_Mutate

        state fork_ip <<fork>>
        state join_ip <<join>>

        [*] --> IE_Read
        IE_Read --> fork_ip
        fork_ip --> IE_PTR
        fork_ip --> IE_ASN
        IE_PTR --> join_ip
        IE_ASN --> join_ip
        join_ip --> IE_Own
        IE_Own --> IE_Mutate
        IE_Mutate --> [*]
    }

    DE --> IE : IP assets must be\nvisible before IPEnricher reads
```

## DNS Collector Internal Concurrency (after optimization)

```mermaid
flowchart TD
    Start([Start]) --> BaseDomains[Base Domain Resolution - parallel 32 workers]
    BaseDomains --> DomainFanout{Per domain parallel fan-out}
    DomainFanout --> ObserveDomain[observeDomain]
    DomainFanout --> LookupRDAP[lookupRDAP]
    ObserveDomain --> DNSFanout[A/AAAA, MX, TXT, NS, and CNAME lookups in parallel]
    DNSFanout --> JoinDomain[Merge per-domain results]
    LookupRDAP --> JoinDomain
    JoinDomain --> MergeResults[Merge Results]
    MergeResults --> VariantSweep[Variant Sweep - parallel 32 workers]
    VariantSweep --> JudgeEvaluation[Judge Evaluation]
    JudgeEvaluation --> End([End])
```
