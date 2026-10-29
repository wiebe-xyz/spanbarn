#!/bin/sh
# Deploy smoke: the running pods report the version and commit that were deployed.
#
#   assert-version.sh NAMESPACE DEPLOYMENT CONTAINER WANT_VERSION WANT_COMMIT
#
# Reads GET /api/v1/version from inside each pod (no ingress or DNS in the way).
# `version` is what the deploy claims (SPANBARN_VERSION, set from the release
# tag); `commit` is baked into the binary at build time. Checking both is what
# proves the claimed version belongs to the code that is running: a deploy that
# sets the tag of one release on the image of another fails here.
#
# `kubectl rollout status` returns while the old pod of a rolling update is
# still terminating, and `exec deploy/X` can pick that pod ("container not
# found", or the previous version). So the check asks every Running pod of the
# deployment's current ReplicaSet that is not being deleted, and retries until
# ASSERT_VERSION_WAIT_SECONDS (default 150, above the longest
# terminationGracePeriodSeconds plus a pod start) has passed. A real mismatch
# outlives the wait and fails with the last reply in the message.
set -eu

if [ "$#" -ne 5 ]; then
	echo "usage: $0 NAMESPACE DEPLOYMENT CONTAINER WANT_VERSION WANT_COMMIT" >&2
	exit 2
fi
ns="$1"
dep="$2"
ctr="$3"
want_version="$4"
want_commit="$5"
wait_seconds="${ASSERT_VERSION_WAIT_SECONDS:-150}"
interval="${ASSERT_VERSION_INTERVAL_SECONDS:-5}"

# current_pods prints the Running, non-terminating pods of the ReplicaSet that
# carries the deployment's current revision.
current_pods() {
	# Split on purpose into the revision and the selector. SC2016: the $ are
	# go-template variables.
	# shellcheck disable=SC2016,SC2046
	set -- $(kubectl -n "$ns" get deploy "$dep" -o go-template='{{index .metadata.annotations "deployment.kubernetes.io/revision"}} {{range $k, $v := .spec.selector.matchLabels}}{{$k}}={{$v}},{{end}}')
	[ "$#" -eq 2 ] || return 0
	rev="$1"
	sel="${2%,}"
	hash="$(kubectl -n "$ns" get rs -l "$sel" -o go-template='{{range .items}}{{if eq (index .metadata.annotations "deployment.kubernetes.io/revision") "'"$rev"'"}}{{index .metadata.labels "pod-template-hash"}}{{end}}{{end}}')"
	[ -n "$hash" ] || return 0
	kubectl -n "$ns" get pods -l "$sel,pod-template-hash=$hash" -o go-template='{{range .items}}{{if and (not .metadata.deletionTimestamp) (eq .status.phase "Running")}}{{.metadata.name}} {{end}}{{end}}'
}

# check sets $problem and returns 1 unless every current pod reports the release.
check() {
	if ! pods="$(current_pods)"; then
		problem="listing the current pods failed"
		return 1
	fi
	if [ -z "$pods" ]; then
		problem="no running pod of the current ReplicaSet"
		return 1
	fi
	for pod in $pods; do
		if ! body="$(kubectl -n "$ns" exec "$pod" -c "$ctr" -- wget -qO- http://127.0.0.1:8080/api/v1/version 2>&1)"; then
			problem="$pod: exec failed: $body"
			return 1
		fi
		case "$body" in
		*"\"version\":\"$want_version\""*) ;;
		*)
			problem="$pod reported $body, wanted version $want_version"
			return 1
			;;
		esac
		case "$body" in
		*"\"commit\":\"$want_commit\""*) ;;
		*)
			problem="$pod reported $body, wanted commit $want_commit"
			return 1
			;;
		esac
	done
}

start="$(date +%s)"
until check; do
	elapsed=$(($(date +%s) - start))
	if [ "$elapsed" -ge "$wait_seconds" ]; then
		echo "FAIL $ns/$dep after ${elapsed}s: $problem" >&2
		exit 1
	fi
	echo "wait $ns/$dep (${elapsed}s): $problem" >&2
	sleep "$interval"
done
echo "ok   $ns/$dep reports $want_version at $want_commit ($pods)"
