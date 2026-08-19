# ADR 0001: thin platform with an all-in-one default

Date: 2026-08-18  
Status: accepted for v0.1 design

## Context

LanTally must be easy enough for a household to deploy while remaining extensible to multiple reporting nodes and larger self-hosted installations. Existing projects cover parts of the problem, but none combines device identity, proxy-aware accounting, multiplier estimates, provider reconciliation, and a read-only multi-node model.

## Decision

- Build a small LanTally-specific agent, server, accounting model, and web UI.
- Reuse existing operating-system counters, nlbwmon, Mihomo APIs, SQLite, and VictoriaMetrics rather than implementing packet accounting or a time-series database from scratch.
- Ship a single all-in-one container by default with one endpoint and one persistent volume.
- Preserve an advanced split mode in which `lantally-server` uses an external VictoriaMetrics instance.
- Keep v0.1 agents outbound-only and read-only.
- Use Apache License 2.0 and maintain LanTally as an independent Git repository.

## Consequences

- Normal users get a one-container setup and an integrated product UI.
- Internal service and storage interfaces must stay explicit even when packaged together.
- The all-in-one image needs process supervision, combined health checks, coordinated backup, and migration tests.
- The project owns identity, reliable ingestion, proxy accounting, reconciliation, and product UX.
- Traffic control remains a separately designed future subsystem.

