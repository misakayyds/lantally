# LanTally

LanTally is a privacy-first, self-hosted traffic ledger for home and small networks. Lightweight agents report per-device usage to a central server, which separates direct and proxied traffic, accounts for proxy-node multipliers, and presents a unified history across multiple gateways.

> Status: v0.1 design and implementation plan are approved. Milestone 1 is on `main`. There is no working release yet; follow the plan. Source: https://github.com/misakayyds/lantally

## v0.1 scope

- Multiple read-only reporting nodes.
- Per-device upload, download, and current rate.
- Direct versus proxied traffic accounting.
- Raw and multiplier-adjusted proxy usage.
- Daily and monthly history.
- Provider-usage reconciliation and anomaly alerts.
- A single-container default deployment.
- Linux and OpenWrt agents, with tiered architecture support.

## Explicit non-goals for v0.1

- Remote command execution.
- Bandwidth limiting or traffic blocking.
- Firewall, routing, DHCP, DNS, or proxy-policy changes.
- Default collection of domains, URLs, or full destination addresses.
- Replacing packet-analysis or ISP-grade shaping platforms.

## Planned components

- `lantally-agent`: outbound-only collector for interface counters, conntrack, nlbwmon, and Mihomo.
- `lantally-server`: enrollment, ingestion, identity, accounting, alerting, API, and embedded web UI.
- SQLite: metadata, identities, configuration, and alert state.
- VictoriaMetrics: traffic time series and retention.
- All-in-one image: server, UI, and VictoriaMetrics behind one port and one data volume.

## Design and plan

- Approved design: [`docs/superpowers/specs/2026-08-18-lantally-design.md`](docs/superpowers/specs/2026-08-18-lantally-design.md)
- v0.1 implementation plan: [`docs/superpowers/plans/2026-08-18-v0.1-implementation.md`](docs/superpowers/plans/2026-08-18-v0.1-implementation.md)
- Delivery sequence: [`docs/roadmap.md`](docs/roadmap.md)
- Implementation ADRs: [`docs/adr/`](docs/adr/)

## License

Apache License 2.0. See [`LICENSE`](LICENSE).

