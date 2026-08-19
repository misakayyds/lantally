# v0.1 field reconciliation (sanitized)

Do not record private hostnames, LAN addresses, tokens, or subscription URLs.

## Threshold

The product flags drift when local `proxy_adjusted` and the provider checkpoint differ by more than **10%** in the current billing window (R4). That is the v0.1 acceptance target for a 7-day publisher self-test.

## Status

A 7-day run against a real gateway is **not in this repository yet**. It must be filled by the publisher after `v0.1.0` has been tagged and one site has run for a week. Until then, use the UI billing form and [`reconciliation-template.md`](reconciliation-template.md).

## How to fill this later

1. Run LanTally on the gateway for at least 7 days without changing multipliers mid-window.
2. On the Proxy page, enter the provider's used bytes and reset day.
3. Copy only the counts below. Replace any site/node IDs with documentation labels (`site-a`, `node-1`).

## Measured drift (pending)

- Date range (UTC):
- Site / node labels:
- Provider checkpoint bytes:
- Local `proxy_adjusted` bytes:
- Drift ratio:
- Within 10% (yes/no):
- Notes (no secrets):

## Synthetic example (not a field run)

For README screenshots and CI, `-demo` / `LANTALLY_DEMO=1` seeds `home` / `demo-gateway` with TEST-NET device `203.0.113.1`. That path is not a provider reconciliation.
