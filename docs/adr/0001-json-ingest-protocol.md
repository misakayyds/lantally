# ADR 0001: Versioned JSON ingest protocol

Date: 2026-08-18  
Status: Accepted

## Context

The approved design requires a versioned ingest protocol with site and node identifiers, a boot identifier, a monotonic sequence, sample timestamps, a capability set, and aggregated counter deltas. It does not specify the encoding.

Agents run on OpenWrt and Linux. Operators need to inspect a failed batch without a special decoder. The first public remote and Go module path are not final.

## Decision

- Agents POST gzip-compressed JSON to `POST /v1/ingest` with `Authorization: Bearer <node-token>`.
- `protocol_version` is an integer. v0.1 accepts only `1`. Unknown versions are rejected.
- JSON Schema lives in `proto/v1/batch.schema.json`. Shared Go types live in `internal/protocol`.
- Idempotency key is `node_id + boot_id + sequence`.
- Go module path is `github.com/lantally/lantally` until a public remote exists. Renaming changes imports only, never the wire format.
- Language floor is Go 1.26.

## Consequences

- Batches are greppable and easy to fixture with synthetic JSON.
- Schema changes require a new `protocol_version` or a documented additive field policy. Counter fields must not be dropped silently.
- Gzip is required on the wire in production; tests may decode raw JSON for convenience if `Decode` accepts both.

## Alternatives rejected

- **gRPC / protobuf:** smaller on the wire and stronger typing, but harder to inspect on a router, heavier for a first release, and unnecessary at home-network batch sizes.
- **YAML or Clash-compatible documents:** ambiguous types and no natural idempotency key.
- **One Go module per binary:** extra versioning cost before there is a public API surface.
