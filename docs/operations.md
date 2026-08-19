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

Health:

```bash
curl -fsS http://127.0.0.1:8080/healthz
```

## Backup

Before migrations, LanTally copies the SQLite database beside the live file:

```text
/var/lib/lantally/meta/lantally.db.bak-<unix>
```

Manual backup while the server is stopped:

```bash
cp /var/lib/lantally/meta/lantally.db /var/lib/lantally/meta/lantally.db.manual-$(date -u +%s)
```

## Restore

1. Stop the server or container.
2. Copy the desired backup over `lantally.db`.
3. Start the server again.
4. Verify `GET /healthz` returns 200.

## Rollback

If a migration fails, restore the latest `lantally.db.bak-<unix>` file and restart. Metrics in VictoriaMetrics are independent; restoring SQLite does not roll back time-series history.

## Split VictoriaMetrics endpoint

Point the metrics writer at another endpoint with server configuration. The ingest protocol and SQLite schema do not change when VictoriaMetrics runs outside the all-in-one container.
