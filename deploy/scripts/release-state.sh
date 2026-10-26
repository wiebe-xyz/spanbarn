#!/bin/sh
# Record the running release before a deploy, and restore it if the deploy fails.
#
#   release-state.sh record  NAMESPACE STATE_FILE
#   release-state.sh restore NAMESPACE STATE_FILE
#
# record writes one line per workload in the namespace:
#   deploy  <name> <revision>            every Deployment and its current revision
#   cronjob <name> <container>=<image>   every CronJob container and init container
#
# restore puts each Deployment whose revision moved back on its recorded
# revision with `rollout undo --to-revision`, sets each CronJob container whose
# image moved back to its recorded one, then waits for each undone rollout. A
# Deployment that did not exist when the state was recorded is reported and
# left alone; a recorded one that is gone is a failure. Every workload is
# attempted; any failure makes restore exit 1, so a rollback that did not
# happen never reads as one that did.
set -eu

usage() {
	echo "usage: $0 record|restore NAMESPACE STATE_FILE" >&2
	exit 2
}
[ "$#" -eq 3 ] || usage
mode="$1"
ns="$2"
state="$3"
REV='{.metadata.annotations.deployment\.kubernetes\.io/revision}'
# Space-separated container=image pairs, init containers first.
CRON_IMAGES='{range .spec.jobTemplate.spec.template.spec.initContainers[*]}{.name}={.image}{" "}{end}{range .spec.jobTemplate.spec.template.spec.containers[*]}{.name}={.image}{" "}{end}'
ROLLOUT_TIMEOUT="${ROLLOUT_TIMEOUT:-1200s}"

record() {
	tmp="${state}.tmp"
	: >"$tmp"
	for d in $(kubectl -n "$ns" get deployment -o jsonpath='{.items[*].metadata.name}'); do
		rev="$(kubectl -n "$ns" get deployment "$d" -o jsonpath="$REV")"
		[ -n "$rev" ] || { echo "release-state: deployment/$d has no revision" >&2; exit 1; }
		echo "deploy $d $rev" >>"$tmp"
	done
	for c in $(kubectl -n "$ns" get cronjob -o jsonpath='{.items[*].metadata.name}'); do
		for img in $(kubectl -n "$ns" get cronjob "$c" -o jsonpath="$CRON_IMAGES"); do
			echo "cronjob $c $img" >>"$tmp"
		done
	done
	grep -q '^deploy ' "$tmp" || { echo "release-state: no deployments in $ns" >&2; exit 1; }
	mv "$tmp" "$state"
	echo "recorded $ns:"
	cat "$state"
}

restore() {
	[ -s "$state" ] || { echo "::error::release-state: no recorded state at $state; nothing can be rolled back" >&2; exit 1; }
	failed=0
	undone=""
	while read -r kind name val; do
		case "$kind" in
		deploy)
			if ! now="$(kubectl -n "$ns" get deployment "$name" -o jsonpath="$REV" </dev/null)"; then
				echo "::error::deployment/$name: cannot read its revision"
				failed=1
				continue
			fi
			if [ "$now" = "$val" ]; then
				echo "deployment/$name: unchanged at revision $val"
				continue
			fi
			if kubectl -n "$ns" rollout undo "deployment/$name" --to-revision="$val" </dev/null; then
				echo "deployment/$name: revision $now -> recorded $val"
				undone="$undone $name"
			else
				echo "::error::deployment/$name: undo to revision $val failed"
				failed=1
			fi
			;;
		cronjob)
			if ! cur="$(kubectl -n "$ns" get cronjob "$name" -o jsonpath="$CRON_IMAGES" </dev/null)"; then
				echo "::error::cronjob/$name: cannot read its images"
				failed=1
				continue
			fi
			case " $cur " in
			*" $val "*)
				echo "cronjob/$name: unchanged at $val"
				continue
				;;
			esac
			if kubectl -n "$ns" set image "cronjob/$name" "$val" </dev/null; then
				echo "cronjob/$name: $val"
			else
				echo "::error::cronjob/$name: could not restore $val"
				failed=1
			fi
			;;
		*)
			echo "::error::release-state: unknown line '$kind $name $val'"
			failed=1
			;;
		esac
	done <"$state"

	for d in $(kubectl -n "$ns" get deployment -o jsonpath='{.items[*].metadata.name}'); do
		grep -q "^deploy $d " "$state" || echo "::warning::deployment/$d is new in this release; left in place"
	done

	for d in $undone; do
		if ! kubectl -n "$ns" rollout status "deployment/$d" --timeout="$ROLLOUT_TIMEOUT"; then
			echo "::error::deployment/$d: rollback did not become ready"
			failed=1
		fi
	done
	[ "$failed" -eq 0 ] || exit 1
	echo "restored $ns to the recorded release"
}

case "$mode" in
record) record ;;
restore) restore ;;
*) usage ;;
esac
