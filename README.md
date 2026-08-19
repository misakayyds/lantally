# LanTally

[English](README.md) | [简体中文](README.zh-CN.md)

LanTally is a privacy-first, self-hosted traffic ledger for home and small networks. Lightweight agents report per-device usage to a central server, which separates direct and proxied traffic, accounts for proxy-node multipliers, and presents a unified history across multiple gateways.

> Source: https://github.com/misakayyds/lantally · First installable image lands with v0.1.0 (`ghcr.io/misakayyds/lantally`). Until that tag is published, build the compose file locally.

## Quick start

1. Run the server (one container, one port, one volume):

```bash
docker run -d --name lantally -p 8080:8080 -v lantally:/var/lib/lantally ghcr.io/misakayyds/lantally:latest
```

Or from this repo:

```bash
docker compose -f deploy/docker/compose.yaml up --build
```

2. Open `http://<host>:8080` and set the admin password in the first-run wizard.
3. Click **Add node**, copy the one-line install command, and run it on the gateway or on the same machine as the server. The page waits until the first batch arrives.

The installer reads a local Mihomo/OpenClash config if present and never uploads the secret. A laptop-only setup works too: check **This machine is the node** and point Mihomo at `127.0.0.1:9090`.

Put a public deployment behind HTTPS. Claim codes are short-lived (10 minutes, one use) and travel in the install command.

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

