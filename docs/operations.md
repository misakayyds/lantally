# LanTally operations

## Container deployment

The default container image runs `lantally-server` directly (`ENTRYPOINT ["/usr/local/bin/lantally-server"]`) with SQLite as the primary metadata and time-series store and an embedded offline-ready Vite + React + TypeScript Web UI (no external fonts or CDN assets). VictoriaMetrics is not bundled inside the default image.

- Published port: `8080`
- Persistent volume: `/var/lib/lantally`
- SQLite database: `/var/lib/lantally/meta/lantally.db`
- Secret encryption key (when `LANTALLY_SECRET_KEY` is unset): `/var/lib/lantally/meta/lantally.db.key` (`0600`)
- Sample retention: 14 days for raw samples (`ledger_samples`, `dimension_samples`); daily rollups (`ledger_daily`, `dimension_daily`, `ledger_totals`) are retained permanently.

Start:

```bash
docker run -d --name lantally -p 8080:8080 -v lantally:/var/lib/lantally ghcr.io/misakayyds/lantally:latest
```

Build locally with Docker Compose:

```bash
docker compose -f deploy/docker/compose.yaml up --build
```

The image serves agent binaries from `LANTALLY_AGENT_DIR` (`/usr/share/lantally/agents`). Open the UI, set the admin password (or supply `LANTALLY_BOOTSTRAP_ADMIN_PASSWORD`), then collect traffic either by:

- **Direct Collectors (`direct_collectors`)**: Configure agentless read-only polling of local or LAN Clash/Mihomo APIs in the **Nodes** page (`GET` + WebSocket `/connections`, `/proxies`, `/providers/proxies`). Collector secrets are encrypted at rest with AES-256-GCM.
- **Remote Agents**: Add a node with a one-time claim code (`GET /install.sh` or `GET /install.ps1`).

If you expose LanTally beyond the LAN, put it behind an HTTPS reverse proxy. The session cookie sets `Secure` when the request is TLS or `X-Forwarded-Proto: https`. Do not publish port 8080 directly to the internet.

Ingest `protocol_version` remains `1` with `DisallowUnknownFields()` and additive `omitempty` fields; v0.1 agents keep working and unknown versions are rejected with an explicit error body.

Health check:

```bash
curl -fsS http://127.0.0.1:8080/healthz
```

Note: The runtime container (`debian:bookworm-slim`) includes `ca-certificates` without `curl` or `wget`; run health probes from the host or reverse proxy.

## CLI flags & environment variables

`lantally-server` CLI flags:

- `-listen` (default `0.0.0.0:8080`): HTTP listen address
- `-db` (default `/var/lib/lantally/meta/lantally.db`): SQLite database path
- `-demo`: seed synthetic TEST-NET traffic for local preview
- `-version`: print version and exit

Supported environment variables:

- `LANTALLY_AGENT_DIR` (default `/usr/share/lantally/agents`): directory holding downloadable agent binaries served at `/agents/{os}/{arch}`
- `LANTALLY_DEMO=1`: seed synthetic demo traffic on startup (equivalent to `-demo`)
- `LANTALLY_BOOTSTRAP_ADMIN_PASSWORD`: optional non-interactive initial admin password used when no admin credential exists yet
- `LANTALLY_SECRET_KEY`: optional passphrase/key material used to derive the 32-byte AES-256-GCM key via HKDF-SHA256 for encrypting `direct_collectors` secrets (overrides `<db>.key`)
- `LANTALLY_METRICS_URL`: optional external VictoriaMetrics base URL (for example `http://victoriametrics.example.invalid:8428`); when set, the server pushes bounded-cardinality Prometheus remote-write metrics (`site_id`, `node_id`, `device_id`, `class`, `direction`) to `<url>/api/v1/import/prometheus`

## Backup

Opening an existing database whose schema version is behind the current version (`schemaVersion = 14`) copies SQLite beside the live file before running migrations:

```text
/var/lib/lantally/meta/lantally.db.bak-<unix>
```

Download a database copy while logged in: **Settings → Download lantally.db**, or `GET /v1/backup`.

**Critical — back up the encryption key alongside `lantally.db`:** `GET /v1/backup` downloads only `lantally.db`, not `lantally.db.key`. If you use Direct Collectors (`direct_collectors`), you must **also** back up `/var/lib/lantally/meta/lantally.db.key` (or preserve the exact `LANTALLY_SECRET_KEY` value). Restoring `lantally.db` onto a volume with a missing or different key makes encrypted `direct_collectors` credentials undecryptable.

Manual backup while the server is stopped:

```bash
ts=$(date -u +%s)
cp /var/lib/lantally/meta/lantally.db "/var/lib/lantally/meta/lantally.db.manual-${ts}"
cp /var/lib/lantally/meta/lantally.db.key "/var/lib/lantally/meta/lantally.db.key.manual-${ts}"
```

## Restore

1. Stop the server or container.
2. Restore the desired `lantally.db` backup over `/var/lib/lantally/meta/lantally.db`.
3. Ensure `/var/lib/lantally/meta/lantally.db.key` (with `0600` permissions) or the matching `LANTALLY_SECRET_KEY` environment variable is in place if `direct_collectors` were configured.
4. Start the server again.
5. Verify `GET /healthz` returns `200` and charts/nodes load as expected.

## Upgrade

Replace the image (or rebuild compose) and restart with the same `/var/lib/lantally` volume. Forward schema migrations (`0001`–`0014`) run automatically on startup after creating `<db>.bak-<unix>`. If the new process exits during migration, follow Rollback.

## Rollback

1. Stop the container.
2. Restore the latest `lantally.db.bak-<unix>` over `/var/lib/lantally/meta/lantally.db` (keep the matching `lantally.db.key` / `LANTALLY_SECRET_KEY`).
3. Start the previous image tag.
4. Verify `GET /healthz` returns `200`.

Because SQLite is the primary store for both metadata and traffic ledgers (`ledger_samples`, `ledger_daily`, `dimension_samples`, `dimension_daily`, `ledger_totals`), restoring `lantally.db` rolls back SQLite time-series history to the backup point. Only an external VictoriaMetrics instance configured via `LANTALLY_METRICS_URL` retains independent external series.

## Publishing a release

Do not invent a tag from a dirty tree. From a reviewed revision:

```bash
git tag -a v0.2.0 -m "LanTally v0.2.0"
git push origin v0.2.0
```

The `Release` workflow runs tests, builds binaries (`lantally-server` for `linux/{amd64,arm64}`, and `lantally-agent` for `linux/{amd64,arm64,armv7,mips,mipsle}` with softfloat MIPS, `darwin/{amd64,arm64}`, and `windows/{amd64,arm64}`), writes `SHA256SUMS` and an SPDX SBOM, pushes `ghcr.io/misakayyds/lantally:<tag>` (and `:latest`) for `linux/amd64` + `linux/arm64`, and creates the GitHub Release.

Make the GHCR package public in the GitHub Packages UI after the first push so `docker run` works without a token.

## Split VictoriaMetrics endpoint

Set `LANTALLY_METRICS_URL` (base URL, e.g., `http://victoriametrics.example.invalid:8428`) on `lantally-server` to forward Prometheus remote-write series externally while keeping SQLite as the primary store. The ingest protocol and SQLite schema do not change when VictoriaMetrics runs outside the container. `cmd/lantally-allinone` remains available in the image for operators who explicitly supervise a custom `/usr/local/bin/victoria-metrics` process alongside `lantally-server`.
