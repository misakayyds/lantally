# ADR 0005: SQLite-primary storage and server entrypoint

Date: 2026-11-21  
Status: Accepted

## Context

ADRs `0002` and `0003` paired `lantally-server` with an in-container VictoriaMetrics (`127.0.0.1:8428`) supervised by `cmd/lantally-allinone`. In v0.2, SQLite already stores all transactional state and 14-day/daily traffic ledgers, while bundling VictoriaMetrics inside the default container added image weight and operational complexity for single-host deployments. The Web UI also moved to a Vite + React + TypeScript SPA compiled into `web/dist` with zero external font or CDN dependencies.

This ADR amends ADRs `0002` and `0003`.

## Decision

- `deploy/docker/Dockerfile` sets `ENTRYPOINT ["/usr/local/bin/lantally-server"]` on `debian:bookworm-slim` without bundling `victoria-metrics`; `cmd/lantally-allinone` remains available for explicit external/custom supervision setups.
- SQLite (`modernc.org/sqlite`, `CGO_ENABLED=0`, `schemaVersion = 14`, single-writer pool `SetMaxOpenConns(1)`) is the primary store at `/var/lib/lantally/meta/lantally.db`, retaining 14 days of raw samples (`ledger_samples`, `dimension_samples`, `SampleRetentionDays = 14`) and permanent daily rollups (`ledger_daily`, `dimension_daily`, `ledger_totals`).
- Before applying forward migrations on an existing file-backed database (`sqliteUserVersion < schemaVersion`), `Store.Open` automatically copies `lantally.db` to `<db>.bak-<unix>` (`0600`).
- `cmd/lantally-server` accepts `-listen` (default `0.0.0.0:8080`), `-db` (default `/var/lib/lantally/meta/lantally.db`), `-demo` (or `LANTALLY_DEMO=1`), and `-version`, and honors `LANTALLY_AGENT_DIR` (default `/usr/share/lantally/agents`), `LANTALLY_BOOTSTRAP_ADMIN_PASSWORD`, `LANTALLY_METRICS_URL`, and `LANTALLY_SECRET_KEY`.
- The Vite + React + TypeScript frontend builds offline-ready assets into `web/dist/` embedded via `web/embed.go` (`//go:embed dist/*`) and served by `staticSPAHandler` with zero external CDN or Google Fonts requests.
- `LANTALLY_METRICS_URL` remains an optional external Prometheus remote-write target (`<endpoint>/api/v1/import/prometheus`) enforcing the strict 5-label allowlist (`site_id`, `node_id`, `device_id`, `class`, `direction`).

## Consequences

- Default single-container deployments run a single Go process with one volume (`/var/lib/lantally`) and one HTTP port (`8080`), reducing image size and startup failure modes.
- SQLite holds both metadata and time-series history, so restoring `lantally.db` rolls back time-series charts as well as configuration.
- External VictoriaMetrics remains supported without changing the ingest protocol or UI APIs, and `lantally-allinone` is preserved for split/supervised deployments.

## Alternatives rejected

- **Keeping VictoriaMetrics mandatory in the default image:** duplicates time-series persistence already handled by SQLite rollups and bloats the default container.
- **Serving external font/CDN assets from the SPA:** breaks air-gapped or LAN-only deployments and leaks client requests to third-party origins.
- **Deleting `cmd/lantally-allinone`:** would break explicit multi-process supervisor workflows for operators who still pair it with an external `victoria-metrics` binary.
