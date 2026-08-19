# Field reconciliation template

Use this template after an authorized read-only or controlled ingest run. Do not record private hostnames, LAN addresses, tokens, or subscription URLs here.

## Run metadata

- Date (UTC):
- Site ID:
- Node IDs under test:
- Agent versions:
- Server version:

## Scenarios

| Scenario | Expected | Observed | Pass |
| --- | --- | --- | --- |
| Two agents report concurrently | No identity or sequence collisions | | |
| Duplicate batch retry | Totals unchanged | | |
| Agent restart | Zero-width reboot gap, no negative delta | | |
| Missing optional collector | Other collectors still report | | |
| Device ledgers | total/direct/proxy_raw/proxy_adjusted shown separately | | |
| Privacy labels | destination/domain/url/conn_id absent | | |
| Provider checkpoint | 24h and 7d drift recorded | | |
| Fixed multiplier target | Drift <= 10% or visibly flagged | | |
| All-in-one health | `/healthz` 200 with VM + server healthy | | |
| Control plane | No routing/firewall/proxy writes exposed | | |

## Measured drift

- Provider checkpoint bytes:
- Local adjusted bytes:
- Drift ratio:
- Multiplier under test:
- Within 10% target (yes/no):

## Sanitized notes

- 

## Residual risks

- 
