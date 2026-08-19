# LanTally agent guidance

## Project purpose

LanTally is a privacy-first, self-hosted, multi-node network traffic accounting project. Its first release is read-only: it collects per-device traffic, separates direct and proxied traffic, applies proxy multipliers, and reconciles estimates with provider usage.

## Safety boundaries

- v0.1 must not implement remote command execution, traffic blocking, rate limiting, routing changes, firewall changes, or automatic proxy-policy changes.
- Never commit subscription URLs, API secrets, node tokens, IP inventories, browsing destinations, packet captures, or real household traffic fixtures.
- Tests must use synthetic addresses, synthetic device identifiers, and generated traffic fixtures.
- Domain and destination collection is opt-in and disabled by default.
- OpenWrt collectors must avoid persistent flash writes by default.

## Repository workflow

- Read `README.md`, the latest design in `docs/superpowers/specs/`, `docs/roadmap.md`, and `docs/superpowers/plans/2026-08-18-v0.1-implementation.md` before implementation.
- Keep the agent, server, protocol, web UI, and packaging boundaries explicit.
- Prefer small, reviewable changes and add focused tests with behavior changes.
- Do not commit generated binaries, runtime databases, local configuration, or credentials.
- Do not commit or push unless the user explicitly requests it.
- Record architecture-level changes in `docs/adr/` once implementation begins.

## Product constraints

- The default deployment is a single all-in-one container with one persistent volume and one public HTTP endpoint.
- Advanced deployments may split the server and VictoriaMetrics without changing APIs or data formats.
- The agent is outbound-only, read-only, statically compiled, and capability-driven.
- The server must deduplicate retries using node identity, boot identity, and monotonically increasing sequence numbers.
- The user-facing UI is built into the server; Grafana is optional and not the primary product interface.

## Verification expectations

- Unit-test accounting, reset handling, deduplication, identity merge rules, and privacy defaults.
- Use integration tests for agent-to-server retry and restart behavior.
- Verify multi-architecture builds before release.
- Validate resource targets on representative OpenWrt and Linux devices.
- Compare weighted proxy estimates against an external provider counter before claiming accounting accuracy.

