# Compacting main after the shard cut-over

With `SPANBARN_SHARDS` on, new logs, metrics and prompts go to shard files in
`spanbarn.db.d/`. The rows written before that stay in main and age out under
row retention. Once one of those tables is empty, the writer lists it in
`retired_tables`, readers stop reading it within 30 seconds, and the writer
drops it 5 minutes later (`internal/repository/main_retire.go`).

Dropping a table does not shrink the file: main has no incremental vacuum, so
the freed pages stay on its freelist. Once all three tables are gone (prompts
take the longest, 30 days by default), one `VACUUM INTO` and a swap give the
space back to the volume. This runs once per environment.

## When

All three heavy tables are gone from main:

```
kubectl -n spanbarn-<env> logs deployment/spanbarn | grep '"main table dropped"'
```

lists `logs`, `metrics` and `prompt_records`. Or, from a pod with the volume
mounted:

```
sqlite3 -readonly /var/lib/spanbarn/spanbarn.db \
  "SELECT name FROM sqlite_master WHERE name IN ('logs','metrics','prompt_records')"
```

prints nothing. Do not run `dbstat` against production; it reads every page.

## Space

`VACUUM INTO` writes a new file the size of main's live pages. Check that the
volume has at least that much free, plus margin for ingest during the swap:

```
sqlite3 -readonly /var/lib/spanbarn/spanbarn.db \
  "SELECT (page_count - freelist_count) * page_size / 1048576 AS live_mib FROM pragma_page_count, pragma_freelist_count, pragma_page_size"
df -h /var/lib/spanbarn
```

## Procedure

This follows the snapshot restore in `disaster-recovery.md`: the writer is
stopped, the file is replaced from a pod on the volume, and the writer starts
on the new file. Ingest waits in the Redis write queue meanwhile.

1. **Scale the writer to 0** so nothing writes main during the copy:
   ```
   kubectl -n spanbarn-<env> scale deployment/spanbarn --replicas=0
   ```

2. **Start a pod on the volume.** `spanbarn-data` is ReadWriteOnce, so the pod
   must run on the node that holds it (the same technique as the 2026-07-11
   storage migration). Any image with `sqlite3` works:
   ```
   kubectl -n spanbarn-<env> run compact-main --rm -it --restart=Never \
     --image=alpine:3.20 \
     --overrides='{"spec":{"volumes":[{"name":"data","persistentVolumeClaim":{"claimName":"spanbarn-data"}}],
       "containers":[{"name":"compact-main","image":"alpine:3.20","stdin":true,"tty":true,
       "command":["sh"],"volumeMounts":[{"name":"data","mountPath":"/var/lib/spanbarn"}]}]}}'
   apk add --no-cache sqlite
   ```

3. **Write the compacted copy** next to main and check it:
   ```
   cd /var/lib/spanbarn
   sqlite3 spanbarn.db "PRAGMA wal_checkpoint(TRUNCATE)"
   sqlite3 spanbarn.db "VACUUM INTO 'spanbarn.db.compact'"
   sqlite3 spanbarn.db.compact "PRAGMA integrity_check"
   ```
   `integrity_check` must print `ok`. Compare row counts of a few settings
   tables (`projects`, `users`, `api_keys`, `shards`) between the two files.

4. **Swap the files.** Keep the old main until the writer is healthy:
   ```
   mv spanbarn.db spanbarn.db.pre-compact
   rm -f spanbarn.db-wal spanbarn.db-shm
   mv spanbarn.db.compact spanbarn.db
   ```
   Leave `spanbarn.db.spans` and `spanbarn.db.d/` alone. They are separate
   files and the compacted main lists the same shards.

5. **Start the writer, then restart the readers.** Readers hold the old main
   inode open until they reopen it:
   ```
   kubectl -n spanbarn-<env> scale deployment/spanbarn --replicas=1
   kubectl -n spanbarn-<env> rollout status deployment/spanbarn --timeout=10m
   kubectl -n spanbarn-<env> rollout restart deployment/spanbarn-ingest
   ```

6. **Verify** that the dashboard shows logs, metrics and prompts from the
   shards, that `bb issues --project spanbarn` shows nothing new, and that ingest
   is current (`max(ingested_at)` of the newest logs shard keeps moving). Then
   delete the old file to free its space:
   ```
   rm /var/lib/spanbarn/spanbarn.db.pre-compact
   ```

## Rollback

Until step 6 deletes it, `spanbarn.db.pre-compact` is a complete main. Scale
the writer to 0, move it back to `spanbarn.db` (removing the `-wal` and `-shm`
of the compacted file), scale up and restart the readers.

## Turning shards off afterwards

With `SPANBARN_SHARDS` off, the writer recreates main's logs, metrics and
prompts tables, empty, on startup, so ingest keeps working. Readers keep
reading the shard files listed in `shards`, and nothing expires those files
while shards are off.
