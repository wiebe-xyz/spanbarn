# Disaster recovery

SpanBarn does not run continuous WAL replication (Litestream was removed —
it repeatedly caused its own outages: WAL bloat, a 117 GB local-cache runaway
that filled a node's disk, a dual-checkpoint outage from TRUNCATE vs. PASSIVE
checkpoints fighting over WAL generations). Spans/traces/logs/metrics are
short-retention by design and are not considered worth protecting against
disaster — losing them is acceptable.

What's worth protecting is configuration: projects, users, API keys, alert
rules, org settings, saved queries, and trace exclusions. An hourly CronJob
(`deploy/k8s/base/cronjob-settings-snapshot`, applied in production and
testing) builds a settings-only SQLite snapshot via `spanbarn db
snapshot-settings` and uploads it to
`s3://barn-backups/spanbarn/settings-snapshots/<env>/<timestamp>.db` — the
shared "barn-backups" Cloudflare R2 bucket (EU jurisdiction endpoint,
`https://354b481466ca4280c50f2fb8cf9e3d45.eu.r2.cloudflarestorage.com`) also
used by other barn services. The `spanbarn/` prefix is load-bearing: the
CronJob's upload and its retention pruning only ever touch keys under that
prefix, so it can never overwrite or delete another service's backups in the
same bucket. The newest 14 snapshots are kept per environment.

The snapshot file is a **complete, ready-to-serve `spanbarn.db`** — every
telemetry table exists (via the app's normal migration) but is empty. There
is no separate restore step or format conversion: the object you download
*is* the database you deploy.

## Recovery procedure

Use this when the live DB is wedged, corrupted, or you've decided a fresh
start is faster than fighting whatever's currently wrong with it.

1. **Scale the writer to 0** so nothing is holding the DB file open:
   ```
   kubectl -n spanbarn-<env> scale deployment/spanbarn --replicas=0
   ```

2. **Fetch the newest settings snapshot** for the environment:
   ```
   R2_ENDPOINT=https://354b481466ca4280c50f2fb8cf9e3d45.eu.r2.cloudflarestorage.com
   aws --endpoint-url "$R2_ENDPOINT" s3 ls \
     s3://barn-backups/spanbarn/settings-snapshots/<env>/ | sort | tail -1
   aws --endpoint-url "$R2_ENDPOINT" s3 cp \
     s3://barn-backups/spanbarn/settings-snapshots/<env>/<latest>.db /tmp/settings.db
   ```
   (Needs `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY` for the R2 access key
   below, and `AWS_DEFAULT_REGION=auto`.)

3. **Replace the live DB.** From a shell with access to the PVC (e.g. a
   temporary debug pod mounting `spanbarn-data`, same technique used for the
   2026-07-11 storage migration — see project memory), remove the existing
   `spanbarn.db` / `spanbarn.db-wal` / `spanbarn.db-shm` and copy the
   downloaded snapshot in as `spanbarn.db`:
   ```
   rm -f /var/lib/spanbarn/spanbarn.db /var/lib/spanbarn/spanbarn.db-wal /var/lib/spanbarn/spanbarn.db-shm
   cp /tmp/settings.db /var/lib/spanbarn/spanbarn.db
   ```

4. **Scale the writer back up:**
   ```
   kubectl -n spanbarn-<env> scale deployment/spanbarn --replicas=1
   ```
   Startup runs the normal migration path (a no-op — the snapshot was
   already built against the current schema) and the app comes up
   immediately: logins, projects, and API keys work exactly as before;
   dashboards show zero spans/traces/logs/metrics until new data arrives.

## Prerequisites

The CronJob authenticates to R2 via `SPANBARN_SNAPSHOT_ACCESS_KEY_ID` /
`SPANBARN_SNAPSHOT_SECRET_ACCESS_KEY`, set in each environment's
`deploy/k8s/<env>/secret.yaml` (SOPS-encrypted) to the "barn-backups" R2
access key (write access to the whole bucket — scoping to our own writes is
enforced entirely by the `spanbarn/` prefix convention above, not by the
credential itself). To rotate the key:
```
sops set deploy/k8s/<env>/secret.yaml '["stringData"]["SPANBARN_SNAPSHOT_ACCESS_KEY_ID"]' '"<value>"'
sops set deploy/k8s/<env>/secret.yaml '["stringData"]["SPANBARN_SNAPSHOT_SECRET_ACCESS_KEY"]' '"<value>"'
```
