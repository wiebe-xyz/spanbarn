#!/usr/bin/env bash
# Run the race detector over the module, with the slowest package split into
# shards that run as separate processes.
#
# Usage: race-test.sh
# Env:   RACE_SHARDED_PKG  package to split (default ./internal/repository)
#        RACE_SHARDS       number of shards (default 4)
#        RACE_SLOWEST      slowest tests listed per shard (default 10)
#
# internal/repository runs its ~300 tests one after another, so under -race it
# is a single core for about 5 minutes on an idle host and past Go's 10 minute
# package timeout on a loaded one. Each shard is its own `go test` process with
# its own timeout, and the shards run beside the other packages. Tests are dealt
# round-robin by name, so every test runs in exactly one shard.
#
# The shards run with -v so each one can list its slowest tests. A passing
# shard prints only its result line and that list; a failing one prints its
# whole log.

set -euo pipefail

SHARDED="${RACE_SHARDED_PKG:-./internal/repository}"
SHARDS="${RACE_SHARDS:-4}"
SLOWEST="${RACE_SLOWEST:-10}"
OUT="$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/race-test.XXXXXX")"

sharded_path="$(go list "$SHARDED")"
others=()
while IFS= read -r p; do
	[ "$p" = "$sharded_path" ] || others+=("$p")
done < <(go list ./...)

# -race on the listing builds the race-instrumented package once, so the shards
# below start from the build cache instead of compiling it side by side.
tests=()
while IFS= read -r t; do
	tests+=("$t")
done < <(go test -list '.*' -race "$SHARDED" | grep -E '^(Test|Example|Fuzz)')
[ "${#tests[@]}" -gt 0 ] || { echo "race-test: no tests listed in $SHARDED" >&2; exit 1; }

pids=()
names=()
go test -race -count=1 "${others[@]}" >"$OUT/others.log" 2>&1 &
pids+=($!)
names+=(others)

for ((s = 0; s < SHARDS; s++)); do
	pattern=""
	for ((i = s; i < ${#tests[@]}; i += SHARDS)); do
		pattern="${pattern:+$pattern|}${tests[$i]}"
	done
	[ -n "$pattern" ] || continue
	go test -race -count=1 -v -run "^(${pattern})\$" "$SHARDED" >"$OUT/shard$s.log" 2>&1 &
	pids+=($!)
	names+=("shard$s")
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
	log="$OUT/${names[$i]}.log"
	echo "=== ${names[$i]}: $status"
	if [ "${names[$i]}" = others ] || [ "$status" = FAILED ]; then
		cat "$log"
	else
		grep -E '^ok[[:space:]]' "$log" || true
	fi
	if [ "${names[$i]}" != others ]; then
		echo "--- ${names[$i]}: slowest tests"
		slowest "$log"
	fi
done
[ "$failed" -eq 0 ] || { echo "::error::race tests failed"; exit 1; }
