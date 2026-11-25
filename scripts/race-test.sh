#!/usr/bin/env bash
# Run the race detector over the module, with the slowest packages split into
# shards that run as separate processes.
#
# Usage: race-test.sh
# Env:   RACE_SHARDED  packages to split, as "pkg:shards" pairs separated by
#                      spaces (default: repository 4, api 4, metricrollup 2,
#                      repository/engine 2)
#        RACE_OTHERS_P packages the unsplit remainder builds and runs at once
#                      (default 2)
#        RACE_SLOWEST  slowest tests listed per shard (default 10)
#        RACE_JOBS     go test processes running at once (default 4)
#        RACE_TIMEOUT  go test -timeout of each process (default 20m)
#
# A package runs its tests one after another, so under -race a big one is a
# single core for minutes on an idle host and past Go's 10 minute package
# timeout on a loaded one. On 2026-11-21 internal/api (355 s on a quiet host),
# internal/metricrollup and internal/repository/engine hit that timeout twice
# at host load above 200. Each shard is its own `go test` process with its own
# timeout, so a shard has the full 10 minutes for a fraction of the tests.
# Tests are dealt round-robin by name, so every test runs in exactly one shard.
#
# On 2026-11-25 the 11 processes started together shared a 10-core host with
# other repos' jobs at load 44 to 80; two repository shards, cmd/spanbarn and
# internal/auth ran 100 times slower than on a quiet host and hit the 10 minute
# limit with no failing test. At most RACE_JOBS processes run at once, so a
# process is not starved by its own siblings, and RACE_TIMEOUT keeps the
# process limit inside the 30 minute job budget.
#
# The shards run with -v so each one can list its slowest tests. A passing
# shard prints only its result line and that list; a failing one prints its
# whole log.

set -euo pipefail

SHARDED="${RACE_SHARDED:-./internal/repository:4 ./internal/api:4 ./internal/metricrollup:2 ./internal/repository/engine:2}"
OTHERS_P="${RACE_OTHERS_P:-2}"
SLOWEST="${RACE_SLOWEST:-10}"
JOBS="${RACE_JOBS:-4}"
TIMEOUT="${RACE_TIMEOUT:-20m}"
OUT="$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/race-test.XXXXXX")"

sharded_paths=()
sharded_shards=()
sharded_labels=()
for spec in $SHARDED; do
	label="${spec%%:*}"
	label="${label#./}"
	sharded_labels+=("${label#internal/}")
	sharded_paths+=("$(go list "${spec%%:*}")")
	sharded_shards+=("${spec##*:}")
done

others=()
while IFS= read -r p; do
	skip=0
	for sp in "${sharded_paths[@]}"; do
		[ "$p" = "$sp" ] && skip=1
	done
	[ "$skip" -eq 1 ] || others+=("$p")
done < <(go list ./...)

# throttle waits until fewer than JOBS of this script's processes are running.
throttle() {
	while [ "$(jobs -rp | wc -l | tr -d ' ')" -ge "$JOBS" ]; do
		sleep 1
	done
}

pids=()
names=()
throttle
go test -race -count=1 -timeout "$TIMEOUT" -p "$OTHERS_P" "${others[@]}" >"$OUT/others.log" 2>&1 &
pids+=($!)
names+=(others)

for i in "${!sharded_paths[@]}"; do
	pkg="${sharded_paths[$i]}"
	shards="${sharded_shards[$i]}"
	label="${sharded_labels[$i]}"
	# -race on the listing builds the race-instrumented package once, so the
	# shards below start from the build cache instead of compiling side by side.
	tests=()
	while IFS= read -r t; do
		tests+=("$t")
	done < <(go test -list '.*' -race "$pkg" | grep -E '^(Test|Example|Fuzz)')
	[ "${#tests[@]}" -gt 0 ] || { echo "race-test: no tests listed in $pkg" >&2; exit 1; }
	echo "${#tests[@]}" >"$OUT/expected_${label//\//_}"
	for ((s = 0; s < shards; s++)); do
		pattern=""
		for ((j = s; j < ${#tests[@]}; j += shards)); do
			pattern="${pattern:+$pattern|}${tests[$j]}"
		done
		[ -n "$pattern" ] || continue
		name="${label//\//_}#$s"
		throttle
		go test -race -count=1 -timeout "$TIMEOUT" -v -run "^(${pattern})\$" "$pkg" >"$OUT/${name//#/.}.log" 2>&1 &
		pids+=($!)
		names+=("$name")
	done
done

# slowest lists the top-level tests in a `go test -v` log, slowest first.
slowest() {
	awk '/^--- (PASS|FAIL|SKIP): /{d=$4; gsub(/[()s]/, "", d); printf "%9.2fs  %s\n", d, $3}' "$1" |
		sort -rn | sed -n "1,${SLOWEST}p"
}

failed=0
for i in "${!pids[@]}"; do
	if wait "${pids[$i]}"; then
		status=ok
	else
		status=FAILED
		failed=1
	fi
	name="${names[$i]}"
	log="$OUT/${name//#/.}.log"
	echo "=== $name: $status"
	if [ "$name" = others ] || [ "$status" = FAILED ]; then
		cat "$log"
	else
		grep -E '^ok[[:space:]]' "$log" || true
	fi
	if [ "$name" != others ]; then
		echo "--- $name: slowest tests"
		slowest "$log"
	fi
done

# A split that drops a test is a false green. Each split package must report
# a top-level result for every test the listing named, summed over its shards.
echo "=== coverage of the shards"
for i in "${!sharded_labels[@]}"; do
	key="${sharded_labels[$i]//\//_}"
	want="$(cat "$OUT/expected_$key")"
	ran="$(cat "$OUT/${key}".*.log | grep -cE '^--- (PASS|FAIL|SKIP): ' || true)"
	if [ "$ran" -eq "$want" ]; then
		echo "${sharded_labels[$i]}: $ran of $want listed tests ran in ${sharded_shards[$i]} shards"
	else
		echo "::error::${sharded_labels[$i]}: $ran tests ran, $want listed"
		failed=1
	fi
done
[ "$failed" -eq 0 ] || { echo "::error::race tests failed"; exit 1; }
