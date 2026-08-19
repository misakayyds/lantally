# Contributing to LanTally

LanTally has an approved v0.1 design and implementation plan. Contributions must preserve the privacy-first and read-only v0.1 boundary.

## Before contributing

1. Read `AGENTS.md`, the design in `docs/superpowers/specs/`, and [`docs/superpowers/plans/2026-08-18-v0.1-implementation.md`](docs/superpowers/plans/2026-08-18-v0.1-implementation.md).
2. Follow the current milestone in that plan. Do not skip ahead of a failing earlier milestone.
3. Open or reference an issue before starting a large feature.
4. Keep one pull request focused on one behavior or one architectural concern.
5. Never include real credentials, subscription links, device inventories, browsing history, or packet captures.

## Locked toolchain (v0.1)

- Go 1.26 (patch `go1.26.6` at plan time).
- Module path: `github.com/misakayyds/lantally`.
- SQLite driver: `modernc.org/sqlite` (no CGO).
- VictoriaMetrics single-node **v1.150.0** in the all-in-one image.
- UI (from milestone 7): React 19, TypeScript, Vite.
- Ingest protocol: gzip JSON, `protocol_version=1`. No gRPC or protobuf.

## Commands

These targets exist once milestone 1 lands `go.mod` and the `Makefile`. Until then, do not invent a local module.

```sh
make test    # go test ./...
make vet     # go vet ./...
make lint    # golangci-lint run ./...
```

After milestone 7, UI checks are:

```sh
cd web && npm test && npm run build
```

After milestone 8, image checks are documented in `docs/operations.md`.

Release, SBOM, and signing commands belong to milestone 10.

## Developer Certificate of Origin

Contributions use the Developer Certificate of Origin. Sign commits with `git commit -s` to certify that you have the right to submit the contribution under the project's license.

## Quality expectations

- Add or update tests for behavior changes.
- Keep collectors capability-driven and safe when a dependency is absent.
- Preserve compatibility with the versioned ingestion protocol.
- Keep destination-level collection disabled by default.
- Document user-visible configuration and migration behavior.
- Do not add remote-control capabilities to v0.1.
- Record architecture-level changes in `docs/adr/`.
