# LanTally roadmap

v0.1 execution details are in [`docs/superpowers/plans/2026-08-18-v0.1-implementation.md`](superpowers/plans/2026-08-18-v0.1-implementation.md).

The path from the current `main` to the first installable release (v0.1.0) is planned in [`docs/superpowers/plans/2026-08-19-road-to-first-release.md`](superpowers/plans/2026-08-19-road-to-first-release.md) (R1-R7: outbound/device ledgers, time and device filters, per-outbound proxy view, multipliers, alerts, zero-CLI deployment, release engineering).

## v0.1: trustworthy read-only accounting

1. Repository foundation, protocol schema, and simulated agents.
2. Node enrollment, scoped credentials, ingestion, deduplication, and retry handling.
3. Generic Linux and OpenWrt interface collectors.
4. Device identity from IP, MAC, neighbor, and DHCP observations.
5. nlbwmon per-device accounting.
6. Mihomo direct/proxy accounting and multiplier-adjusted estimates.
7. Daily and monthly device views, node health, and anomaly alerts.
8. All-in-one image, backup, restore, migration, and retention controls.
9. Twenty-four-hour and seven-day reconciliation on a real gateway using sanitized results.
10. Signed multi-architecture pre-release with checksums and SBOM.

## v0.2: broader collection

- Agentless SNMP polling.
- NetFlow/IPFIX/sFlow ingestion.
- Additional proxy adapters when their accounting semantics can be verified.
- Optional destination summaries with explicit privacy controls and bounded cardinality.
- PostgreSQL metadata backend for larger installations.

## v0.3: policy preview

- Read-only simulations of quotas and rate-limit policies.
- Explainable impact previews and rollback plans.
- No automatic enforcement until the preview and accounting models have independent acceptance evidence.

## Later: opt-in control plane

- Separate, capability-scoped control agents.
- Per-device rate limits, proxy budgets, direct fallback, and deny policies.
- Signed commands, explicit local opt-in, audit logs, safety timeouts, and tested rollback.

Control-plane work remains outside v0.1 and requires a new approved design.

