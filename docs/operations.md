# LanTally operations

## All-in-one container

- Published port: `8080`
- Persistent volume: `/var/lib/lantally`
- VictoriaMetrics listens on `127.0.0.1:8428` inside the container
- SQLite metadata lives at `/var/lib/lantally/meta/lantally.db`

Start:

```bash
docker run -d --name lantally -p 8080:8080 -v lantally:/var/lib/lantally ghcr.io/misakayyds/lantally:latest
```

The GHCR tag is published with v0.1.0. Until then, build locally:

```bash
docker compose -f deploy/docker/compose.yaml up --build
```

The image serves agent binaries from `LANTALLY_AGENT_DIR` (`/usr/share/lantally/agents`). Open the UI, set the admin password, then add a node with a one-time claim code. Agent install scripts are `GET /install.sh` and `GET /install.ps1`.

If you expose LanTally beyond the LAN, put it behind an HTTPS reverse proxy. The session cookie sets `Secure` when the request is TLS or `X-Forwarded-Proto: https`. Do not publish port 8080 directly to the internet.

Ingest `protocol_version` is frozen at `1` for v0.1. Older agents keep working; unknown versions are rejected with an explicit error body.

Health:

```bash
curl -fsS http://127.0.0.1:8080/healthz
```

## Backup

Opening a database whose schema is behind the current version copies SQLite beside the live file before migrating:

```text
/var/lib/lantally/meta/lantally.db.bak-<unix>
```

Download a copy while logged in: **Settings → 下载 lantally.db**, or `GET /v1/backup`.

Manual backup while the server is stopped:

```bash
cp /var/lib/lantally/meta/lantally.db /var/lib/lantally/meta/lantally.db.manual-$(date -u +%s)
```

## Restore

1. Stop the server or container.
2. Copy the desired backup over `lantally.db`.
3. Start the server again.
4. Verify `GET /healthz` returns 200 and charts still show history.

## Upgrade

Replace the image (or rebuild compose) and restart with the same volume. Schema migrations run on start. If the new process exits during migration, follow Rollback.

## Rollback

1. Stop the container.
2. Restore the latest `lantally.db.bak-<unix>` over `/var/lib/lantally/meta/lantally.db`.
3. Start the previous image tag.
4. Verify `GET /healthz` returns 200.

VictoriaMetrics metrics are independent; restoring SQLite does not roll back time-series history.

## Publishing v0.1.0

Do not invent a tag from a dirty tree. From a reviewed revision:

```bash
git tag -a v0.1.0 -m "LanTally v0.1.0"
git push origin v0.1.0
```

The `Release` workflow runs tests, builds agent/server binaries (linux amd64/arm64/armv7, mips/mipsle softfloat, darwin, windows), writes `SHA256SUMS` and an SPDX SBOM, pushes `ghcr.io/misakayyds/lantally:v0.1.0` (and `:latest`) for linux/amd64+arm64, and creates the GitHub Release.

Make the GHCR package public in the GitHub Packages UI after the first push so `docker run` works without a token.

## Split VictoriaMetrics endpoint

Point the metrics writer at another endpoint with server configuration. The ingest protocol and SQLite schema do not change when VictoriaMetrics runs outside the all-in-one container.
