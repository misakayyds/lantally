# ADR 0002: SQLite metadata and VictoriaMetrics series

Date: 2026-08-18  
Status: Accepted

## Context

The design stores low-rate transactional state separately from bounded-cardinality time series. The default deployment is a single container and a single volume. PostgreSQL is deferred until a demonstrated scale need.

The server must remain statically compilable. Metric labels must not include domains, URLs, destinations, or connection IDs.

## Decision

- SQLite holds sites, nodes, hashed credentials, device identities, multiplier configuration, provider checkpoints, alert state, ingest checkpoints, and schema migrations.
- Use `modernc.org/sqlite` so the server does not require CGO.
- VictoriaMetrics single-node **v1.150.0** stores traffic series. The server writes Prometheus remote-write and queries PromQL over HTTP.
- Volume layout is `/var/lib/lantally/meta/` for SQLite and `/var/lib/lantally/metrics/` for VictoriaMetrics, so either directory can later move to an external service without changing the ingest protocol.
- Series names are `lantally_bytes_total`, `lantally_rate_bps`, and `lantally_node_last_report_timestamp_seconds`.
- Allowed labels: `site_id`, `node_id`, `device_id`, `class`, `direction`.

## Consequences

- Home and small-network installs need no external database.
- Split deployments change only the metrics endpoint configuration.
- Cardinality stays bounded by devices and nodes, not by flows.
- Migrations must back up the SQLite file before applying.

## Alternatives rejected

- **Grafana as the product UI:** optional for experts, not the default operator path.
- **A self-written time-series store:** duplicates VictoriaMetrics without benefit.
- **mattn/go-sqlite3:** CGO complicates static and cross builds.
- **PostgreSQL in v0.1:** extra operational cost before any scale evidence.
- **Logging every connection as a metric:** unbounded labels and a privacy leak.
