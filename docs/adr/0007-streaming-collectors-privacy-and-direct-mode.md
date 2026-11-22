# ADR 0007: Streaming collectors, opt-in domain privacy, and direct mode

Date: 2026-11-21  
Status: Accepted

## Context

v0.1 polled Mihomo `/connections` every 10 seconds with baseline skipping that could drop short-lived connections or emit false collector-reset gaps when connections closed cleanly, supported `iface` only on Linux, omitted domain/process insights, and required installing an agent even when `lantally-server` could reach a local or LAN Clash/Mihomo API directly. Enabling agentless direct collectors also introduced persistent API secrets in SQLite.

## Decision

- **Mihomo extraction and streaming (`internal/collector/mihomo`):**
  - `extractChain` maps `chains[0]` to the exit `outbound` (`"DIRECT"` if empty or `"DIRECT"`) and `chains[len-1]` (when `len > 1`) to the top-level strategy `group`.
  - Connections prefer WebSocket `ws(s)://.../connections` (`DialTimeout = 10s`, `WSIdleTimeout = 15s`, jittered exponential backoff `1s..60s`) with automatic 1-second HTTP `GET /connections` fallback on `200`, `400`, `404`, `405`, or `501`.
  - Startup frames (`prev == nil`) initialize the baseline with zero deltas; unseen connection IDs on subsequent frames start from `0`; counter decreases emit `GapCollectorReset`; normal connection closures clean up without emitting resets. Read-only `GET /version`, `GET /proxies`, and `GET /providers/proxies` populate node catalogs and provider quotas.
- **Cross-platform pure-Go `iface` (`internal/collector/iface`, `internal/accounting`):**
  - Pure-Go (`CGO_ENABLED=0`) collectors cover Linux (`/proc/net/dev`), Windows (`GetIfTable2Ex`), and macOS (`sysctl NET_RT_IFLIST2`).
  - All interfaces are stored individually under `dim_type = 'iface'`, while node-level `total` excludes prefixes `lo`, `tun`, `utun`, `wg`, `tailscale`, `docker`, `veth`, and `br-` by default (configurable per node via `ExcludeIfacePrefixes` / `IncludeIfaces`).
- **Privacy-bounded process & opt-in domain collection:**
  - `extractProcess` keeps only `metadata.process` or the basename of `metadata.processPath`.
  - Domain collection (`nodes.collect_domains`) defaults to `0` (`false`). When enabled per node (synced via optional `collect_domains` and `watch_domains` fields in the `POST /v1/ingest` response without editing agent configs) or matched by an active domain watch rule, IP literals and ports are stripped, regular hosts fold to eTLD+1 via `publicsuffix`, watched rules stay exact, and only the top `defaultDomainTopN = 50` domains are retained individually (`"other"` for the tail). Full URLs are never collected.
- **Agentless `direct_collectors` (`0014_direct_collectors.sql`, `internal/collector/direct`, `internal/store/sqlite/secrets.go`):**
  - `lantally-server` manages read-only `direct_collectors` (`nodes.mode = 'direct'`, rejecting active `mode = 'agent'` `node_id` collisions) polling local/LAN Mihomo APIs over the unified accounting pipeline.
  - Stored secrets use AES-256-GCM (`0x01 || 12B nonce || ciphertext+tag`). The 32-byte key comes from `LANTALLY_SECRET_KEY` (via HKDF-SHA256 with salt `lantally-secret-enc-v1` and info `aes-256-gcm`), an ephemeral CSPRNG key for `:memory:`, or `<db>.key` (`0600`, auto-generated with `O_CREATE|O_EXCL`). API responses mask secrets as `"********"` and `internal/redact` scrubs URLs and tokens from logs/errors.

## Consequences

- Short-lived Mihomo connections are counted accurately without spurious reset alerts, and Windows/macOS/Linux agents work without CGO.
- Domain collection stays strictly opt-in and cardinality-bounded while still allowing UI-driven toggling and watchlist matching.
- Operators using `direct_collectors` must retain `<db>.key` or `LANTALLY_SECRET_KEY` alongside `lantally.db` backups; otherwise encrypted collector secrets cannot be decrypted on restore.

## Alternatives rejected

- **Polling `/connections` with baseline-skipping every 10s:** drops short flows and misclassifies normal connection expiry as resets.
- **Enabling full domain/URL capture by default:** violates LanTally's privacy-first boundary and bloats SQLite cardinality.
- **Storing direct collector secrets in plaintext or deriving keys from DB bytes:** exposes credentials on database backup or breaks decryption when DB bytes change.
