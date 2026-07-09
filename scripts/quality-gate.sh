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
COVERAGE_SERVICE_MIN=${COVERAGE_SERVICE_MIN:-79.5}
COVERAGE_REPOSITORY_MIN=${COVERAGE_REPOSITORY_MIN:-69.0}
# Per-file floor: no single source file in the gated packages may sit at/under
# this coverage, so a fully-untested file can't hide behind well-covered
# siblings in the same package aggregate. Migration DDL and test files are
# excluded.
MIN_FILE_COVERAGE=${MIN_FILE_COVERAGE:-57.5}

COMPLEXITY=${COMPLEXITY:-15}          # gocyclo score considered "complex"
CYCLO_MAX_COUNT=${CYCLO_MAX_COUNT:-24} # max functions allowed over COMPLEXITY

FILE_LINES=${FILE_LINES:-500}         # a file this long counts as "large"
FILELEN_MAX_COUNT=${FILELEN_MAX_COUNT:-5} # max large files allowed
FILE_HARD_CAP=${FILE_HARD_CAP:-1200}  # no single file may exceed this

DUPL_TOKENS=${DUPL_TOKENS:-100}       # dupl clone-detection threshold (tokens)
DUPL_MAX_GROUPS=${DUPL_MAX_GROUPS:-16} # max duplicate clone groups allowed
# ---------------------------------------------------------------------------

# Directories of Go source to inspect (production + CLI).
SRC_DIRS="internal cmd"

fail=0
note() { printf '  %-42s %s\n' "$1" "$2"; }

# geq compares two decimals: returns 0 (true) if $1 >= $2.
geq() { awk -v a="$1" -v b="$2" 'BEGIN{exit !(a+0 >= b+0)}'; }

echo "== quality gate =="

# --- 1. Coverage (per-package aggregate + per-file floor) -------------------
# The trailing whitespace class ([[:space:]]) matches the package line
# (".../internal/repository<TAB>...") without matching the ".../repository/migrations" line.
cover_profile=$(mktemp)
trap 'rm -f "$cover_profile"' EXIT
cov_out=$(go test -cover -coverprofile="$cover_profile" ./internal/service/... ./internal/repository/... 2>/dev/null)
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

# Per-file floor — aggregate the profile per file (excluding migration DDL and
# test files) and flag any at/under MIN_FILE_COVERAGE.
under_floor=$(awk -v min="$MIN_FILE_COVERAGE" '
  NR>1 {
    split($1, a, ":"); f=a[1];
    if (index(f, "/migrations/") > 0) next;
    if (f ~ /_test\.go$/) next;
    total[f]+=$2; if ($3>0) covered[f]+=$2;
  }
  END {
    for (f in total) {
      pct = total[f]>0 ? 100*covered[f]/total[f] : 100;
      if (pct <= min+0) printf "%.1f%%  %s\n", pct, f;
    }
  }' "$cover_profile" | sort -n)
if [ -z "$under_floor" ]; then
  note "per-file floor $MIN_FILE_COVERAGE% (no 0% files):" "OK"
else
  note "per-file floor $MIN_FILE_COVERAGE% (no 0% files):" "FAIL"; fail=1
  echo "    files at/under floor:"; printf '%s\n' "$under_floor" | sed 's/^/      /'
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
