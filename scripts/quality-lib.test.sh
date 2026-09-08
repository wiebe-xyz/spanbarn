#!/usr/bin/env bash
#
# quality-lib.test.sh — tests for the quality gate's ratchet semantics.
#
# The gate itself runs a test suite, gocyclo and dupl, which is far too slow to
# use as a test harness. The decision logic lives in quality-lib.sh precisely so
# it can be driven with plain numbers, and that is what this exercises:
# violation present, violation baselined, baseline beatable, clean tree, plus
# the environment-override guard.
#
# Run with `make quality-gate-test`.
set -uo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd -- "$SCRIPT_DIR/.." && pwd)
# shellcheck source=scripts/quality-lib.sh
. "$SCRIPT_DIR/quality-lib.sh"

pass=0
fail=0
check() {
  local name=$1 want=$2 got=$3
  if [ "$want" = "$got" ]; then
    pass=$((pass + 1))
    printf '  ok   %s\n' "$name"
  else
    fail=$((fail + 1))
    printf '  FAIL %s: want %q, got %q\n' "$name" "$want" "$got"
  fi
}

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
cat > "$TMP/thresholds.conf" <<'CONF'
# a comment
COMPLEXITY=15
CYCLO_MAX_COUNT=23
COVERAGE_SERVICE_MIN=79.5
CONF

echo "== quality-lib =="

# --- qg_threshold -----------------------------------------------------------
QG_THRESHOLDS_FILE="$TMP/thresholds.conf"
check "reads a committed threshold" "23" "$(qg_threshold CYCLO_MAX_COUNT)"
check "reads a decimal threshold" "79.5" "$(qg_threshold COVERAGE_SERVICE_MIN)"
check "skips comments" "15" "$(qg_threshold COMPLEXITY)"

(qg_threshold NOT_A_KEY >/dev/null 2>&1)
check "a missing key is a hard error" "2" "$?"

(QG_THRESHOLDS_FILE="$TMP/nope.conf" qg_threshold COMPLEXITY >/dev/null 2>&1)
check "a missing thresholds file is a hard error" "2" "$?"

# --- qg_guard_env -----------------------------------------------------------
(qg_guard_env COMPLEXITY CYCLO_MAX_COUNT >/dev/null 2>&1)
check "clean environment passes the guard" "0" "$?"

(export COMPLEXITY=99; qg_guard_env COMPLEXITY CYCLO_MAX_COUNT >/dev/null 2>&1)
check "an exported threshold is refused" "2" "$?"

# --- qg_cmp_count: the four ratchet cases -----------------------------------
check "violation present (count over baseline) fails" "over"  "$(qg_cmp_count 24 23)"
check "violation baselined (count equals baseline) passes" "ok" "$(qg_cmp_count 23 23)"
check "baseline beatable (count under baseline) is stale" "stale" "$(qg_cmp_count 22 23)"
check "clean tree (zero against zero) passes" "ok" "$(qg_cmp_count 0 0)"
check "clean tree against a stale baseline is stale" "stale" "$(qg_cmp_count 0 3)"

# --- qg_cmp_pct -------------------------------------------------------------
check "coverage below the floor fails" "under" "$(qg_cmp_pct 78.4 79.5 5.0)"
check "coverage on the floor passes" "ok" "$(qg_cmp_pct 79.5 79.5 5.0)"
check "coverage inside the margin passes" "ok" "$(qg_cmp_pct 84.0 79.5 5.0)"
check "coverage past the margin is stale" "stale" "$(qg_cmp_pct 85.0 79.5 5.0)"

# --- qg_set_threshold -------------------------------------------------------
qg_set_threshold CYCLO_MAX_COUNT 21
check "update rewrites in place" "21" "$(qg_threshold CYCLO_MAX_COUNT)"
check "update leaves other keys alone" "15" "$(qg_threshold COMPLEXITY)"
qg_set_threshold BRAND_NEW_KEY 7
check "update appends an unknown key" "7" "$(qg_threshold BRAND_NEW_KEY)"

# --- the real gate refuses an environment override --------------------------
# The adversarial case this whole file exists for: before the thresholds moved
# into a committed file, `COMPLEXITY=99 make quality-gate` passed silently.
out=$(cd "$REPO_ROOT" && COMPLEXITY=99 bash scripts/quality-gate.sh 2>&1)
code=$?
check "COMPLEXITY=99 quality-gate.sh is refused" "2" "$code"
case "$out" in
  *"set in the environment"*) check "and says why" "yes" "yes" ;;
  *) check "and says why" "yes" "no: $out" ;;
esac

# --- every threshold the gate reads exists in the committed file ------------
QG_THRESHOLDS_FILE="$REPO_ROOT/scripts/quality-thresholds.conf"
missing=""
for key in $(grep -oE '^\s+[A-Z_]+=\$\(qg_threshold [A-Z_]+\)' "$REPO_ROOT/scripts/quality-gate.sh" \
             | grep -oE 'qg_threshold [A-Z_]+' | awk '{print $2}'); do
  (qg_threshold "$key" >/dev/null 2>&1) || missing="$missing $key"
done
check "the committed file defines every threshold the gate reads" "" "$missing"

echo
if [ "$fail" -ne 0 ]; then
  echo "quality-lib: $fail failed, $pass passed"
  exit 1
fi
echo "quality-lib: $pass passed"
