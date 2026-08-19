# LanTally

[English](README.md) | [简体中文](README.zh-CN.md)

LanTally is a privacy-first, self-hosted traffic ledger for home and small networks. Lightweight agents report usage to a local server, which splits direct vs proxied traffic, applies outbound multipliers, and keeps a history you can reconcile against a provider bill.

It only observes. It does not change routing, firewall, DHCP, or proxy policy.

> v0.1.0 · https://github.com/misakayyds/lantally · image `ghcr.io/misakayyds/lantally`

## Three steps

**1. Run one container**

```bash
docker run -d --name lantally -p 8080:8080 -v lantally:/var/lib/lantally ghcr.io/misakayyds/lantally:v0.1.0
```

From this repo: `docker compose -f deploy/docker/compose.yaml up --build`

Preview with synthetic charts (TEST-NET data only):

```bash
docker run -d --name lantally -p 8080:8080 -e LANTALLY_DEMO=1 -v lantally:/var/lib/lantally ghcr.io/misakayyds/lantally:v0.1.0
```

**2. Set the admin password** in the first-run wizard at `http://<host>:8080`.

**3. Add a node.** Copy the one-line install command (Linux / macOS / Windows) and run it on the gateway, or on the same machine if that laptop *is* the node. The page waits until the first batch arrives.

![Synthetic 72-hour traffic chart](docs/screenshots/overview-synthetic.svg)

The installer may read a local Mihomo/OpenClash config. The secret stays on that machine. Public deployments must sit behind HTTPS.

If the GHCR tag is not visible yet, build locally with compose and use `:latest` after the first tagged release pipeline has run (`git tag v0.1.0 && git push origin v0.1.0`).

## What you get

- Per-device and per-node totals, direct vs proxy, multiplier-adjusted proxy usage
- 24h / 72h / 7d / 30d charts (samples kept 14 days; daily totals kept permanently)
- Alerts for silence, resets, growth, and billing drift over 10%
- One-time claim codes instead of copying long tokens

## What v0.1 will not do

- Remote commands, rate limits, or firewall/proxy writes
- Collect domains, URLs, or full destinations
- Match an ISP bill to the last byte when the provider does not publish rules

## Install agents from a Release

GitHub Releases attach checksums (`SHA256SUMS`), an SPDX SBOM, and binaries for linux/amd64+arm64+armv7+mips/mipsle (softfloat), darwin/amd64+arm64, and windows/amd64. The container already hosts those agents at `/agents/{os}/{arch}`.

## Docs

- Operations (upgrade, backup, HTTPS): [`docs/operations.md`](docs/operations.md)
- Field reconciliation log: [`docs/reconciliation-v0.1.md`](docs/reconciliation-v0.1.md)
- Design and ADRs: [`docs/superpowers/specs/2026-08-18-lantally-design.md`](docs/superpowers/specs/2026-08-18-lantally-design.md), [`docs/adr/`](docs/adr/)

## License

Apache License 2.0. See [`LICENSE`](LICENSE).
