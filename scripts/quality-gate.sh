#!/usr/bin/env bash
#
# quality-gate.sh — a ratchet over the code-quality metrics.
#
# Every threshold lives in scripts/quality-thresholds.conf. None of them can be
# set from the environment: the gate refuses to run if a threshold name is
# exported, so `COMPLEXITY=99 make quality-gate` fails instead of passing. The
# only way to move a number is to change the committed file, which shows up in
# the pull-request diff.
#
# The gate fails in both directions:
#   - a metric worse than its baseline is new debt
#   - a metric BETTER than its baseline is a win nobody locked in, and a
#     baseline with slack in it silently re-opens room for a regression
#     (ratchet property 3)
# Refresh with `make quality-gate-update`.
#
# Metrics:
#   1. Coverage    — service & repository packages, plus a per-file floor.
#   2. Cyclomatic  — count of functions over COMPLEXITY.
#   3. File length — count of large files, plus a hard per-file cap.
#   4. Duplication — count of duplicate clone groups.
#   5. Boundaries  — soak (scripts/boundarycheck).
#   6. Infra       — soak (scripts/infracheck).
#
# Run locally with `make quality-gate`.
set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd -- "$SCRIPT_DIR/.." && pwd)
# shellcheck source=scripts/quality-lib.sh
. "$SCRIPT_DIR/quality-lib.sh"
QG_THRESHOLDS_FILE="$SCRIPT_DIR/quality-thresholds.conf"

UPDATE=0
case "${1:-}" in
  --update-baseline) UPDATE=1 ;;
  "") ;;
  *) echo "usage: quality-gate.sh [--update-baseline]" >&2; exit 2 ;;
esac

THRESHOLD_KEYS=(
  COVERAGE_SERVICE_MIN COVERAGE_REPOSITORY_MIN MIN_FILE_COVERAGE
  COVERAGE_STALE_MARGIN COMPLEXITY CYCLO_MAX_COUNT FILE_LINES
  FILELEN_MAX_COUNT FILE_HARD_CAP DUPL_TOKENS DUPL_MAX_GROUPS
  BOUNDARIES_MODE INFRA_MODE
)
qg_guard_env "${THRESHOLD_KEYS[@]}"

COVERAGE_SERVICE_MIN=$(qg_threshold COVERAGE_SERVICE_MIN)
COVERAGE_REPOSITORY_MIN=$(qg_threshold COVERAGE_REPOSITORY_MIN)
MIN_FILE_COVERAGE=$(qg_threshold MIN_FILE_COVERAGE)
COVERAGE_STALE_MARGIN=$(qg_threshold COVERAGE_STALE_MARGIN)
COMPLEXITY=$(qg_threshold COMPLEXITY)
CYCLO_MAX_COUNT=$(qg_threshold CYCLO_MAX_COUNT)
FILE_LINES=$(qg_threshold FILE_LINES)
FILELEN_MAX_COUNT=$(qg_threshold FILELEN_MAX_COUNT)
FILE_HARD_CAP=$(qg_threshold FILE_HARD_CAP)
DUPL_TOKENS=$(qg_threshold DUPL_TOKENS)
DUPL_MAX_GROUPS=$(qg_threshold DUPL_MAX_GROUPS)
BOUNDARIES_MODE=$(qg_threshold BOUNDARIES_MODE)
INFRA_MODE=$(qg_threshold INFRA_MODE)

# Pinned tool versions (avoid @latest drift across CI runs).
GOCYCLO="go run github.com/fzipp/gocyclo/cmd/gocyclo@v0.6.0"
DUPL="go run github.com/mibk/dupl@v1.1.0"

# Directories of Go source to inspect (production + CLI).
SRC_DIRS="internal cmd"

cd "$REPO_ROOT"

fail=0
stale=0
note() { printf '  %-46s %s\n' "$1" "$2"; }
regressed() { note "$1" "FAIL"; fail=1; }
is_stale() { note "$1" "STALE"; stale=1; }

echo "== quality gate =="

# --- 1. Coverage (per-package aggregate + per-file floor) -------------------
# internal/repository is a package tree: the query layer in the root package and
# the SQLite engine in internal/repository/engine. Most engine statements run
# under the root package's tests (every repository test opens an engine.DB), so
# the repository run uses -coverpkg over the whole tree. Without it the engine
# files would only count their own few tests, and moving code into engine would
# read as lost coverage. The service run keeps go test's own per-package number.
# The repository aggregate is computed from the profile (migration DDL excluded)
# over the root and engine packages together, the same set of statements the
# root package held before the engine moved out.
cover_profile=$(mktemp)
repo_profile=$(mktemp)
trap 'rm -f "$cover_profile" "$repo_profile"' EXIT
# A failing test fails the gate and prints the go test output.
if ! cov_out=$(go test -cover -coverprofile="$cover_profile" ./internal/service/... 2>&1); then
  echo "service tests failed:"; printf '%s\n' "$cov_out" | grep -vE 'goose|  OK  ' | tail -80; exit 1
fi
if ! repo_out=$(go test -cover -coverpkg=./internal/repository/... -coverprofile="$repo_profile" ./internal/repository/... 2>&1); then
  echo "repository tests failed:"; printf '%s\n' "$repo_out" | grep -vE 'goose|  OK  ' | tail -80; exit 1
fi
svc_cov=$(echo "$cov_out" | grep -E 'internal/service[[:space:]]' | grep -oE '[0-9.]+% of statements' | grep -oE '[0-9.]+' | head -1)
# With -coverpkg each test binary writes its own line for a block, so a block can
# appear more than once. Count each block ($1) once: covered if any line ran it.
repo_cov=$(awk '
  NR>1 {
    split($1, a, ":"); f=a[1];
    if (index(f, "/migrations/") > 0) next;
    stmts[$1]=$2; if ($3>0) hit[$1]=1;
  }
  END {
    for (b in stmts) { total+=stmts[b]; if (b in hit) covered+=stmts[b] }
    if (total>0) printf "%.1f", 100*covered/total
  }' "$repo_profile")
: "${svc_cov:=0}"; : "${repo_cov:=0}"
# One profile for the per-file floor: the service profile plus the repository
# tree's, without the second "mode:" header.
tail -n +2 "$repo_profile" >> "$cover_profile"

# Per-file floor — aggregate the profile per file (excluding migration DDL and
# test files). min_file_cov is the lowest of them. Engine files are in it.
per_file=$(awk '
  NR>1 {
    split($1, a, ":"); f=a[1];
    if (index(f, "/migrations/") > 0) next;
    if (f ~ /_test\.go$/) next;
    stmts[$1]=$2; file[$1]=f; if ($3>0) hit[$1]=1;
  }
  END {
    for (b in stmts) { f=file[b]; total[f]+=stmts[b]; if (b in hit) covered[f]+=stmts[b] }
    for (f in total) {
      pct = total[f]>0 ? 100*covered[f]/total[f] : 100;
      printf "%.1f %s\n", pct, f;
    }
  }' "$cover_profile" | sort -n)
min_file_cov=$(printf '%s\n' "$per_file" | head -1 | awk '{print $1+0}')
: "${min_file_cov:=0}"

# --update-baseline deliberately does NOT touch the coverage floors. The
# workstation and CI measure these packages on different Go toolchains and do
# not agree: CI run 32776709389 reported internal/service at 80.5% where a local
# `go test -cover` reports 84.0%. Auto-refreshing from a local run would write a
# floor of 83.0 and turn CI red on the next push. Coverage floors are raised by
# hand from a CI measurement; the counts below are toolchain-independent and are
# refreshed automatically.

for spec in "service:$svc_cov:$COVERAGE_SERVICE_MIN" "repository:$repo_cov:$COVERAGE_REPOSITORY_MIN"; do
  name=${spec%%:*}; rest=${spec#*:}; measured=${rest%%:*}; floor=${rest#*:}
  label=$(printf 'coverage %-11s %s%% (min %s%%)' "$name:" "$measured" "$floor")
  case "$(qg_cmp_pct "$measured" "$floor" "$COVERAGE_STALE_MARGIN")" in
    ok)    note "$label" "OK" ;;
    under) regressed "$label" ;;
    stale) is_stale "$label" ;;
  esac
done

floor_label=$(printf 'per-file floor: lowest %s%% (min %s%%)' "$min_file_cov" "$MIN_FILE_COVERAGE")
case "$(qg_cmp_pct "$min_file_cov" "$MIN_FILE_COVERAGE" "$COVERAGE_STALE_MARGIN")" in
  ok)    note "$floor_label" "OK" ;;
  under)
    regressed "$floor_label"
    echo "    files at/under the floor:"
    printf '%s\n' "$per_file" | awk -v min="$MIN_FILE_COVERAGE" '$1+0 <= min+0 {print "      " $0}'
    ;;
  stale) is_stale "$floor_label" ;;
esac

# --- 2. Cyclomatic complexity (production code only) ------------------------
# gocyclo -over exits non-zero when it finds matches; capture output regardless.
cyclo_out=$($GOCYCLO -over "$COMPLEXITY" -ignore '_test\.go' $SRC_DIRS 2>/dev/null || true)
cyclo_count=$(printf '%s' "$cyclo_out" | grep -c . || true)
if [ "$UPDATE" -eq 1 ]; then qg_set_threshold CYCLO_MAX_COUNT "$cyclo_count"; fi
label="functions over cyclo $COMPLEXITY: $cyclo_count (baseline $CYCLO_MAX_COUNT)"
case "$(qg_cmp_count "$cyclo_count" "$CYCLO_MAX_COUNT")" in
  ok)    note "$label" "OK" ;;
  over)  regressed "$label"; echo "    offenders:"; printf '%s\n' "$cyclo_out" | sed 's/^/      /' ;;
  stale) is_stale "$label" ;;
esac

# --- 3. File length (production code only) ----------------------------------
big_files=$(find $SRC_DIRS -name '*.go' -not -name '*_test.go' | xargs wc -l 2>/dev/null \
  | awk -v t="$FILE_LINES" '$2!="total" && $1>t {print $1" "$2}')
big_count=$(printf '%s' "$big_files" | grep -c . || true)
over_cap=$(printf '%s\n' "$big_files" | awk -v cap="$FILE_HARD_CAP" '$1>cap {print}')
if [ "$UPDATE" -eq 1 ]; then qg_set_threshold FILELEN_MAX_COUNT "$big_count"; fi
label="files over $FILE_LINES lines: $big_count (baseline $FILELEN_MAX_COUNT, cap $FILE_HARD_CAP)"
if [ -n "$over_cap" ]; then
  regressed "$label"
  echo "    over hard cap $FILE_HARD_CAP:"; printf '%s\n' "$over_cap" | sed 's/^/      /'
else
  case "$(qg_cmp_count "$big_count" "$FILELEN_MAX_COUNT")" in
    ok)    note "$label" "OK" ;;
    over)  regressed "$label"; echo "    large files:"; printf '%s\n' "$big_files" | sed 's/^/      /' ;;
    stale) is_stale "$label" ;;
  esac
fi

# Headroom against the hard cap, printed every run. cmd/spanbarn/main.go sits a
# few dozen lines under it; the fix is decomposition, not a bigger cap.
printf '%s\n' "$big_files" | sort -rn | head -3 | while read -r lines path; do
  if [ -z "$lines" ]; then continue; fi
  printf '    %5d  %-38s %d lines under the cap\n' "$lines" "$path" "$((FILE_HARD_CAP - lines))"
done

# --- 4. Duplication ---------------------------------------------------------
dupl_out=$($DUPL -threshold "$DUPL_TOKENS" $(printf './%s ' $SRC_DIRS) 2>/dev/null || true)
dupl_groups=$(printf '%s' "$dupl_out" | grep -c '^found' || true)
if [ "$UPDATE" -eq 1 ]; then qg_set_threshold DUPL_MAX_GROUPS "$dupl_groups"; fi
label="dupl clone groups (@$DUPL_TOKENS): $dupl_groups (baseline $DUPL_MAX_GROUPS)"
case "$(qg_cmp_count "$dupl_groups" "$DUPL_MAX_GROUPS")" in
  ok)    note "$label" "OK" ;;
  over)  regressed "$label" ;;
  stale) is_stale "$label" ;;
esac

# --- 5. Boundaries (soak) ---------------------------------------------------
# --- 6. Infra (soak) --------------------------------------------------------
# Both run their own ratchet. While the mode is `report` they print and never
# fail the build; flipping the mode to `enforce` in the thresholds file is the
# whole of the change that makes them blocking.
run_soak() {
  local name=$1 mode=$2; shift 2
  local args=()
  if [ "$mode" = report ]; then args+=(-report-only); fi
  if [ "$UPDATE" -eq 1 ]; then args+=(-update); fi
  echo
  echo "-- $name soak (mode: $mode)"
  if ! "$@" ${args[@]+"${args[@]}"}; then
    fail=1
  fi
}

run_soak boundaries "$BOUNDARIES_MODE" go run ./scripts/boundarycheck
run_soak infra "$INFRA_MODE" go run ./scripts/infracheck

echo
if [ "$UPDATE" -eq 1 ]; then
  echo "baselines refreshed in scripts/quality-thresholds.conf — commit the diff."
  exit 0
fi
if [ "$stale" -ne 0 ]; then
  echo "quality gate FAILED — a baseline is beatable and was left stale."
  echo "A baseline with slack in it re-opens room for a regression nobody sees."
  echo "Counts:   make quality-gate-update    (then commit scripts/quality-thresholds.conf)"
  echo "Coverage: raise the COVERAGE_* floor in scripts/quality-thresholds.conf by hand,"
  echo "          to the CI-measured percentage minus 1.0pp — a local run measures higher."
  fail=1
fi
if [ "$fail" -ne 0 ]; then
  echo "quality gate FAILED — see scripts/quality-thresholds.conf for the thresholds."
  exit 1
fi
echo "quality gate passed."
