# ADR 0004: Accounting overlap and ledger precedence

Date: 2026-08-18  
Status: Accepted

## Context

v0.1 maintains distinct ledgers: total observed, direct, raw proxied, multiplier-adjusted estimate, and provider checkpoints. Several collectors can observe the same packets:

- Interface counters see node-level totals, including traffic that is not per-device.
- nlbwmon sees IP/MAC host totals on OpenWrt.
- Mihomo sees connections that traversed the proxy core, including DIRECT and proxy outbounds.

Adding these sources together would double-count. Guessing an unknown node multiplier would hide reconciliation error.

## Decision

- Never add nlbwmon bytes to Mihomo bytes, and never add either to iface bytes as a single combined total.
- Ledger `total` uses this precedence: nlbwmon if the node reported it, else iface, else Mihomo.
- Ledgers `direct`, `proxy_raw`, `proxy_adjusted`, and `proxy_unadjusted` come only from a proxy adapter (Mihomo in v0.1).
- A multiplier applies only when the outbound or policy name matches configuration. Otherwise the series is `proxy_unadjusted` and the UI marks it. Do not default to `1.0`.
- Provider checkpoints are manually entered or imported counters, not scraped billing portals.
- Drift is `|adjusted - provider| / provider` when provider > 0. The 10% target for a disclosed fixed multiplier is a reporting goal, not a reason to rewrite stored series.

## Consequences

- Overview and device pages must show the four traffic ledgers separately.
- A node without Mihomo can still show totals and cannot claim a direct/proxy split.
- Short-lived Mihomo connections missed between samples appear as reconciliation drift, not as interpolated bytes.
- Collectors remain independently optional.

## Alternatives rejected

- **Summing all collectors “to be safe”:** systematic double counting.
- **Always preferring Mihomo for totals:** undercounts traffic that never hits the proxy core.
- **Silent multiplier default of 1.0:** hides unknown billing rules.
- **Normalizing drift down to 10%:** conceals real provider disagreement.
