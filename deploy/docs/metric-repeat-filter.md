# Metric repeat filter

The writer stores a cumulative sum or histogram point that repeats the
previous point of its stream (project, name, attributes, start time) once per
`SPANBARN_METRIC_REPEAT_HEARTBEAT_SECONDS` (default 300). The last repeat is
stored just before the stream changes, and a restarted process's last point is
stored when its new stream appears, so rates and window quantiles are the same
as without the filter. The rollups see every point. See
`internal/aggregation/metric_repeats.go`.

The writer reads the setting once at start. The pipeline applies the configmap
but only rolls pods when the image changes, so every change of the value needs
a writer restart.

## Rollout

Staging runs the filter first. Production keeps it off through
`deploy/k8s/production/configmap-metric-repeat-patch.yaml` ("0") until the
staging check below matches; a PR that deletes the patch turns it on. Every
merge cuts a release whose version is in the pod spec, so the pods roll with
it and read the new value.

## Rollback

Documented path:

1. Set `SPANBARN_METRIC_REPEAT_HEARTBEAT_SECONDS: "0"` in
   `deploy/k8s/base/configmap.yaml` and ship it through the pipeline.
2. `kubectl -n spanbarn-production rollout restart deployment/spanbarn`
   (writer only; ingest and web do not read it).

Emergency path, for a wedge or wrong rates in production:

1. `kubectl -n spanbarn-production patch configmap spanbarn-config --type=merge -p '{"data":{"SPANBARN_METRIC_REPEAT_HEARTBEAT_SECONDS":"0"}}'`
2. `kubectl -n spanbarn-production rollout restart deployment/spanbarn`
3. Within the hour, commit the same `"0"` to `deploy/k8s/base/configmap.yaml`,
   so the next deploy does not set it back to 300.

## Checking it

Staging compare: per single-stream cumulative counter, the 30-minute increase
from raw points must equal the increase from the 5-minute rollups.
