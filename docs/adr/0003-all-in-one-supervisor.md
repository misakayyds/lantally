# ADR 0003: All-in-one process supervisor

Date: 2026-08-18  
Status: Accepted

## Context

The default image must run `lantally-server` (with the embedded UI) and VictoriaMetrics behind one published HTTP port and one data volume. VictoriaMetrics must not be reachable from the network. Advanced users may still run the two processes separately.

## Decision

- Ship `cmd/lantally-allinone` as a small Go supervisor: start VictoriaMetrics on `127.0.0.1:8428`, start `lantally-server` on `0.0.0.0:8080`, combine `/healthz`, and forward `SIGTERM`/`SIGINT`.
- The published port is **8080**. The volume is `/var/lib/lantally`.
- Pin VictoriaMetrics to **v1.150.0** in the image build.
- Do not put s6-overlay, systemd, or a shell-only entrypoint in the default image.
- Split mode is a configuration change (`metrics_url`) with the same APIs and batch format.

## Consequences

- Operators run one container without learning VictoriaMetrics ports.
- Health is honest only when both processes answer.
- Image builds must vendor or fetch the pinned VictoriaMetrics binary with checksum verification.
- First-run admin password printing happens in the server process logs, which the supervisor must not swallow.

## Alternatives rejected

- **Two-container compose as the only default:** extra moving parts for the primary audience.
- **s6-overlay:** extra distro surface for a two-process tree.
- **Embedding VictoriaMetrics as a library:** unsupported and couples upgrade cycles.
- **Exposing `8428` on the default image:** bypasses LanTally auth and the label whitelist.
