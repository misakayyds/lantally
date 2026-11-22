# ADR 0006: Dynamic multipliers and orthogonal dimensions

Date: 2026-11-21  
Status: Accepted

## Context

ADR `0004` stored `proxy_adjusted` and `proxy_unadjusted` forward-only and prohibited a `1.0` default multiplier, while v0.1 only tracked outbound-level proxy bytes without orthogonal strategy-group, process, domain, or interface breakdowns. Operators routinely change node billing multipliers, want historical charts updated when a rule changes, need custom multi-dimensional watchlists, and reconcile multiple proxy providers with varying subscription formats.

This ADR amends ADR `0004` (specifically the forward-only multiplier rule and no-`1.0`-default constraint).

## Decision

- `proxy_raw` (with separate `rx` and `tx` counters per `outbound`) is the single source of truth for proxy traffic. Query paths (`LedgerBreakdown`, `ListDeviceLedgersInRange`) evaluate compiled rules against `proxy_raw` via `accounting.ScaleBytes` so rule edits and JSON imports apply retroactively across all retained history (`<= 7d` from `ledger_samples` + `ledger_daily`, `> 7d` from `ledger_daily`, unbounded from `ledger_totals`).
- Migration `0012_dimensions_rules.sql` introduces `multiplier_rules` (`exact`, `keyword`, `regex` with capture groups, and `auto_extract` supporting `2x`/`0.5X`/`×`/`倍率` within `0..100`), compiled deterministically in `priority DESC, id ASC, name ASC` order, alongside `proxy_node_catalog` for live rule preview.
- Unmatched outbounds now evaluate to a visible `1.0x` default (`MultiplierMatch{Factor: 1.0, MatchSource: "default"}`) while preserving `proxy_unadjusted` totals so operators can distinguish explicit rules (`match_source: "rule"`) from unconfigured nodes (`match_source: "default"`).
- Orthogonal dimension ledgers (`dimension_samples` and `dimension_daily` for `dim_type` in `group`, `process`, `domain`, `iface`) record separate `rx`/`tx` counters independently of device/outbound tables to avoid Cartesian row explosion, exposing `{rx, tx, total}` across APIs and supporting `today`, `24h`, `72h`, `7d`, `30d`, `billing`, and custom `from`/`to` time ranges.
- Migration `0013_watch_providers.sql` adds `watch_items` (multi-dimensional matcher rules with optional `override_factor`, MB/GB/TB quotas, reset day, and Overview pinning) and `provider_subscriptions` (auto-synced Mihomo `HTTP` provider `subscriptionInfo` handling `Compatible` filtering and shared subscriptions, plus manual MB/GB/TB subscriptions and >10% drift alerts).
- Ingest stays on `protocol_version = 1` with strict `DisallowUnknownFields()` and additive `omitempty` fields (`Proxy.ByOutbound[].Group`, `Dimensions`, `ProxyNodes`, `Providers`).

## Consequences

- Editing, reordering, or importing `multiplier_rules` (`replace` or `merge` mode) updates historical weighted estimates immediately without rewriting stored `proxy_raw` rows.
- Every interface (`iface`) is visible individually in dimensions while virtual/loopback interfaces remain excluded from node `total`.
- Dimension sums within one `dim_type` can be inspected independently without double-counting across `total`, `direct`, or `proxy_raw`.

## Alternatives rejected

- **Materializing `proxy_adjusted` at ingest time:** makes rule changes non-retroactive and complicates rule import/preview workflows.
- **Storing a single wide fact table `(device, outbound, group, process, domain, iface)`:** creates unbounded row cardinality and query slowdown in SQLite.
- **Bumping `protocol_version` to `2`:** breaks existing v0.1 agents unnecessarily when `omitempty` fields preserve full wire compatibility.
