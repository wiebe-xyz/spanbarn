#!/bin/sh
set -eu

# reader and ingest modes open the DB read-only — Litestream must not run.
# writer mode (default when Litestream creds are present) replicates the WAL.
if [ -z "${LITESTREAM_ACCESS_KEY_ID:-}" ] || \
   [ "${SPANBARN_MODE:-}" = "ingest" ] || \
   [ "${SPANBARN_MODE:-}" = "reader" ]; then
  exec spanbarn
fi

case "$LITESTREAM_ACCESS_KEY_ID" in
  REPLACE*|CHANGEME*|TODO*|PLACEHOLDER*)
    echo "WARNING: LITESTREAM_ACCESS_KEY_ID looks like a placeholder ('$LITESTREAM_ACCESS_KEY_ID'), skipping Litestream."
    exec spanbarn
    ;;
esac

if [ ! -f "$SPANBARN_DB_PATH" ]; then
  echo "Restoring database from Litestream replica (${LITESTREAM_REPLICA_PATH})..."
  litestream restore \
    -config /etc/litestream.yml \
    -if-replica-exists \
    "$SPANBARN_DB_PATH" || echo "No replica found, starting fresh."
fi

exec litestream replicate \
  -config /etc/litestream.yml \
  -exec "spanbarn"
