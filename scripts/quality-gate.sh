#!/usr/bin/env bash
#
# quality-gate.sh — enforces that code-quality metrics do not regress.
#
# Each metric has a baseline set at the level the codebase reached after the
# July 2026 audit cleanup. CI fails if any metric gets worse. When you
# legitimately improve a metric (e.g. refactor a complex function), ratchet the
# corresponding baseline DOWN in this file so the win is locked in; the baselines
# are only ever meant to move in the improving direction.
#
# Metrics:
#   1. Coverage      — service & repository packages stay at/above a floor.
#   2. Cyclomatic    — count of functions over COMPLEXITY may not grow.
#   3. File length   — count of large files may not grow; no file may exceed a cap.
#   4. Duplication   — count of duplicate clone groups may not grow.
#
# Run locally with `make quality-gate`.
set -euo pipefail

# Pinned tool versions (avoid @latest drift across CI runs).
GOCYCLO="go run github.com/fzipp/gocyclo/cmd/gocyclo@v0.6.0"
DUPL="go run github.com/mibk/dupl@v1.1.0"

# ---- Baselines (ratchet DOWN as the codebase improves) ---------------------
COVERAGE_SERVICE_MIN=${COVERAGE_SERVICE_MIN:-65.0}
COVERAGE_REPOSITORY_MIN=${COVERAGE_REPOSITORY_MIN:-66.0}

COMPLEXITY=${COMPLEXITY:-15}          # gocyclo score considered "complex"
CYCLO_MAX_COUNT=${CYCLO_MAX_COUNT:-27} # max functions allowed over COMPLEXITY

FILE_LINES=${FILE_LINES:-500}         # a file this long counts as "large"
FILELEN_MAX_COUNT=${FILELEN_MAX_COUNT:-6} # max large files allowed
FILE_HARD_CAP=${FILE_HARD_CAP:-1200}  # no single file may exceed this

DUPL_TOKENS=${DUPL_TOKENS:-100}       # dupl clone-detection threshold (tokens)
DUPL_MAX_GROUPS=${DUPL_MAX_GROUPS:-17} # max duplicate clone groups allowed
# ---------------------------------------------------------------------------

# Directories of Go source to inspect (production + CLI).
SRC_DIRS="internal cmd"

fail=0
note() { printf '  %-42s %s\n' "$1" "$2"; }

# geq compares two decimals: returns 0 (true) if $1 >= $2.
geq() { awk -v a="$1" -v b="$2" 'BEGIN{exit !(a+0 >= b+0)}'; }

echo "== quality gate =="

# --- 1. Coverage ------------------------------------------------------------
# The trailing whitespace class ([[:space:]]) matches the package line
# (".../internal/repository<TAB>...") without matching the ".../repository/migrations" line.
cov_out=$(go test -cover ./internal/service/... ./internal/repository/... 2>/dev/null)
svc_cov=$(echo "$cov_out" | grep -E 'internal/service[[:space:]]' | grep -oE '[0-9.]+% of statements' | grep -oE '[0-9.]+' | head -1)
repo_cov=$(echo "$cov_out" | grep -E 'internal/repository[[:space:]]' | grep -oE '[0-9.]+% of statements' | grep -oE '[0-9.]+' | head -1)
: "${svc_cov:=0}"; : "${repo_cov:=0}"

if geq "$svc_cov" "$COVERAGE_SERVICE_MIN"; then
  note "coverage service:      $svc_cov% (min $COVERAGE_SERVICE_MIN%)" "OK"
else
  note "coverage service:      $svc_cov% (min $COVERAGE_SERVICE_MIN%)" "FAIL"; fail=1
fi
if geq "$repo_cov" "$COVERAGE_REPOSITORY_MIN"; then
  note "coverage repository:   $repo_cov% (min $COVERAGE_REPOSITORY_MIN%)" "OK"
else
  note "coverage repository:   $repo_cov% (min $COVERAGE_REPOSITORY_MIN%)" "FAIL"; fail=1
fi

# --- 2. Cyclomatic complexity (production code only) ------------------------
# gocyclo -over exits non-zero when it finds matches; capture output regardless.
cyclo_out=$($GOCYCLO -over "$COMPLEXITY" -ignore '_test\.go' $SRC_DIRS 2>/dev/null || true)
cyclo_count=$(printf '%s' "$cyclo_out" | grep -c . || true)
if [ "$cyclo_count" -le "$CYCLO_MAX_COUNT" ]; then
  note "functions over cyclo $COMPLEXITY: $cyclo_count (max $CYCLO_MAX_COUNT)" "OK"
else
  note "functions over cyclo $COMPLEXITY: $cyclo_count (max $CYCLO_MAX_COUNT)" "FAIL"; fail=1
  echo "    offenders:"; printf '%s\n' "$cyclo_out" | sed 's/^/      /'
fi

# --- 3. File length (production code only) ----------------------------------
big_files=$(find $SRC_DIRS -name '*.go' -not -name '*_test.go' | xargs wc -l 2>/dev/null \
  | awk -v t="$FILE_LINES" '$2!="total" && $1>t {print $1" "$2}')
big_count=$(printf '%s' "$big_files" | grep -c . || true)
over_cap=$(printf '%s\n' "$big_files" | awk -v cap="$FILE_HARD_CAP" '$1>cap {print}')
if [ "$big_count" -le "$FILELEN_MAX_COUNT" ] && [ -z "$over_cap" ]; then
  note "files over $FILE_LINES lines:    $big_count (max $FILELEN_MAX_COUNT, cap ${FILE_HARD_CAP})" "OK"
else
  note "files over $FILE_LINES lines:    $big_count (max $FILELEN_MAX_COUNT, cap ${FILE_HARD_CAP})" "FAIL"; fail=1
  [ -n "$over_cap" ] && { echo "    over hard cap ${FILE_HARD_CAP}:"; printf '%s\n' "$over_cap" | sed 's/^/      /'; }
fi

# --- 4. Duplication ---------------------------------------------------------
dupl_out=$($DUPL -threshold "$DUPL_TOKENS" $(printf './%s ' $SRC_DIRS) 2>/dev/null || true)
dupl_groups=$(printf '%s' "$dupl_out" | grep -c '^found' || true)
if [ "$dupl_groups" -le "$DUPL_MAX_GROUPS" ]; then
  note "dupl clone groups (@$DUPL_TOKENS):  $dupl_groups (max $DUPL_MAX_GROUPS)" "OK"
else
  note "dupl clone groups (@$DUPL_TOKENS):  $dupl_groups (max $DUPL_MAX_GROUPS)" "FAIL"; fail=1
fi

echo
if [ "$fail" -ne 0 ]; then
  echo "quality gate FAILED — a metric regressed past its baseline (see scripts/quality-gate.sh)."
  exit 1
fi
echo "quality gate passed."
