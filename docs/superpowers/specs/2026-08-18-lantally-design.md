# LanTally v0.1 design

Date: 2026-08-18  
Status: approved; v0.1 implementation plan accepted in `docs/superpowers/plans/2026-08-18-v0.1-implementation.md`

## 1. Product statement

LanTally is a privacy-first, self-hosted, open-source traffic ledger for home and small networks. Lightweight reporting nodes account for traffic visible at gateways and hosts, while a central service produces per-device, per-node, direct, proxied, and multiplier-adjusted histories.

The first release solves trustworthy observation and reconciliation. It does not control production traffic.

## 2. v0.1 goals

- Accept reports from multiple heterogeneous nodes.
- Identify traffic by device when the reporting node can observe the original source.
- Distinguish direct traffic from traffic that used a proxy node.
- Record raw proxy bytes and multiplier-adjusted estimated billing bytes.
- Preserve daily and monthly histories across agent, proxy-core, and server restarts.
- Compare local estimates with a manually entered or provider-reported usage counter.
- Alert on unexpected growth, node silence, reset anomalies, and reconciliation drift.
- Provide a simple all-in-one deployment with one public endpoint and one persistent volume.
- Keep agents read-only, outbound-only, and light enough for common OpenWrt devices.

## 3. Non-goals

- Traffic shaping, blocking, forced direct routing, firewall changes, or proxy-policy changes.
- Remote shell or general remote command execution.
- Full packet capture, payload inspection, or browsing-history collection.
- Exact reconstruction of traffic that occurred before LanTally was installed.
- Guaranteed provider billing equivalence when the provider does not disclose overhead, multipliers, or accounting rules.
- ISP-scale inline shaping or replacement of ntopng, OpenWISP, or LibreQoS.

## 4. Users and deployment sizes

The primary user runs one self-hosted central service and one to ten reporting nodes. Typical nodes include OpenWrt gateways, Linux routers, NAS hosts, and proxy gateways. Larger installations may split storage and metadata services, but use the same protocol and APIs.

## 5. Architecture

### 5.1 Reporting agent

`lantally-agent` is a statically compiled Go program. It discovers available collectors, samples local counters, converts mutable connection state into monotonic interval deltas, aggregates privacy-sensitive detail locally, and pushes compressed batches to the server.

Initial collectors:

- Interface counters for node-level totals and rates.
- Conntrack for generic flow/accounting context where supported.
- nlbwmon for efficient IP/MAC monthly host accounting.
- Mihomo's connection API for source device, route policy, proxy chain, upload, and download counters.

Collectors fail independently. A missing collector reduces capability but does not prevent the node from reporting other metrics.

The agent does not listen on a management port. Proxy API secrets and subscription URLs remain local. The default OpenWrt buffer is memory-only and bounded to avoid flash wear.

### 5.2 Ingestion server

`lantally-server` handles enrollment, node credentials, batch validation, deduplication, identity resolution, accounting, alerts, queries, and the embedded web UI.

Each batch carries:

- Protocol version.
- Site and node identifiers.
- Boot identifier.
- Monotonically increasing sequence number.
- Sample interval and timestamps.
- Collector capability set.
- Aggregated counter deltas.

The server treats `node_id + boot_id + sequence` as the idempotency key. Retries acknowledge the existing batch without applying it twice. A new boot identifier creates an explicit reset boundary rather than a negative counter delta.

### 5.3 Storage

SQLite stores low-rate transactional state:

- Sites and nodes.
- Hashed node credentials and enrollment state.
- Device identities and user-assigned names.
- Proxy multipliers and provider-accounting configuration.
- Alert configuration and alert state.
- Ingestion checkpoints and schema migrations.

VictoriaMetrics stores bounded-cardinality time series:

- Node and device upload/download counters.
- Direct and proxy byte counters.
- Multiplier-adjusted billing estimates.
- Current rates and node health.
- Reconciliation differences.

Domains, URLs, complete destination addresses, and unbounded connection IDs are not metric labels.

### 5.4 Web application

The TypeScript/React UI is compiled into static assets and embedded in `lantally-server`. The initial interface includes:

- Overview: today, current billing cycle, raw proxy use, adjusted estimate, and reconciliation drift.
- Devices: name, IP/MAC observations, current rate, daily/monthly totals, and direct/proxy split.
- Nodes: capability set, last report, resets, collector health, and version.
- Proxy accounting: policy and node summaries, multipliers, and provider checkpoints.
- Alerts: unexpected growth, missing node, counter reset, and reconciliation drift.

Grafana remains an optional expert tool and is not required for normal operation.

## 6. Device identity

LanTally identifies a device within a site using the strongest available evidence:

1. User-pinned identity.
2. Stable MAC observation.
3. DHCP or neighbor-table association.
4. Source IP scoped to a node and time interval.

Identity merges are conservative and reversible. Randomized MAC addresses, NAT boundaries, and devices visible only after aggregation are surfaced as limitations rather than silently merged.

## 7. Accounting model

LanTally maintains distinct ledgers:

- Total observed traffic.
- Direct traffic.
- Raw proxied traffic.
- Multiplier-adjusted estimated billing traffic.
- Provider-reported usage checkpoints.

Mihomo connection counters are sampled locally. The agent tracks connection identity and last-seen values, emits only positive deltas, and records reset/disappearance boundaries. Short connections may be missed when the upstream API does not expose them between samples; provider reconciliation measures the resulting drift.

A multiplier applies only when its matching proxy node or policy is known. Unknown multipliers remain unadjusted and are visibly marked instead of guessed.

## 8. Privacy and security

- No telemetry leaves the user's installation by default.
- Domains and full destinations are disabled by default.
- Real traffic fixtures are prohibited from the repository.
- Each reporting node receives a unique, revocable credential.
- Server-side credentials are stored as hashes.
- Internet exposure requires trusted HTTPS termination.
- The all-in-one image exposes only the LanTally endpoint; VictoriaMetrics remains internal.
- v0.1 has no remote execution or traffic-control channel.
- Logs redact authorization headers, subscription URLs, proxy credentials, and enrollment secrets.

## 9. Packaging

### 9.1 Default all-in-one image

The default image contains:

- `lantally-server` and embedded UI.
- A VictoriaMetrics process bound to the container's internal interface.
- Lightweight process supervision and combined health reporting.

It exposes one HTTP endpoint and uses one persistent mount rooted at `/var/lib/lantally`. Metadata and metrics use separate subdirectories so they can later move to external services.

### 9.2 Advanced split deployment

Advanced users may run `lantally-server` and VictoriaMetrics separately. Configuration changes the storage endpoint but not ingestion, query, or agent protocols. PostgreSQL metadata support is deferred until a demonstrated scale requirement.

### 9.3 Agent distribution

Tier 1 tested targets for v0.1:

- Linux amd64 and arm64.
- OpenWrt/ImmortalWrt x86_64 and aarch64.

Experimental artifacts:

- armv7 and mipsle.

Releases include checksums, an SBOM, signatures, and explicit stable or pre-release channels. Agents do not auto-update in v0.1.

## 10. Reliability and failure handling

- The agent uses bounded batches and exponential retry with jitter.
- Server acknowledgements advance the local sequence checkpoint.
- Memory-only OpenWrt buffers may lose unsent data on reboot; this is reported as an explicit gap.
- Linux hosts may opt into a bounded disk spool in a later implementation step.
- Storage unavailability causes ingestion backpressure rather than silent acceptance.
- Schema migrations are versioned and backed up before applying.
- All-in-one upgrades document backup, restore, and rollback procedures.

## 11. Resource targets

- Agent resident memory: at most 25 MB on supported gateways.
- Agent idle CPU: near zero; average collection CPU below 1% under representative home traffic.
- No continuous OpenWrt flash writes by default.
- Default all-in-one memory target: at most 512 MB for a small installation.
- Default cardinality excludes destinations and per-connection labels.

## 12. Acceptance criteria

- Two or more agents can report concurrently without identity or sequence collisions.
- Duplicate batches do not change totals.
- Agent, proxy-core, and server restarts do not create negative deltas or double accounting.
- A missing optional collector leaves other collectors operational.
- Device pages show total, direct, raw proxy, and adjusted proxy values separately.
- Privacy-sensitive dimensions are absent unless explicitly enabled.
- A 24-hour and seven-day comparison against a provider checkpoint reports measured drift.
- The target reconciliation error for a disclosed fixed multiplier is at most 10%; larger differences remain visible and are not normalized away.
- The all-in-one deployment starts with one container, one volume, and one exposed endpoint.
- No v0.1 API can modify routing, firewall, proxy policy, or traffic state.

## 13. Testing strategy

- Unit tests: counter deltas, resets, deduplication, multiplier rules, identity evidence, retention labels, and redaction.
- Property tests: counters never decrease and duplicate/reordered batches do not inflate totals.
- Integration tests: simulated agents, retries, server restart, storage outage, and protocol-version rejection.
- Fixture tests: synthetic nlbwmon and Mihomo responses with no real destinations or credentials.
- End-to-end tests: all-in-one startup, enrollment, sample ingestion, device view, backup, restore, and upgrade rollback.
- Resource tests: supported OpenWrt and Linux architectures.
- Field validation: sanitized 24-hour and seven-day comparisons on an authorized gateway.

## 14. Project governance

- Apache License 2.0.
- Semantic versioning.
- Developer Certificate of Origin rather than a CLA for initial contributions.
- Architecture decisions recorded under `docs/adr/`.
- Signed releases with checksums and SBOM.
- No commits or publication containing real credentials, private topology, or traffic history.

## 15. Delivery sequence

Implementation follows the milestones in `docs/roadmap.md` and the accepted plan in `docs/superpowers/plans/2026-08-18-v0.1-implementation.md`. Traffic-control work requires a separate future design and approval.

