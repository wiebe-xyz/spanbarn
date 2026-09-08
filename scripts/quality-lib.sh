# quality-lib.sh — threshold loading and ratchet comparison for the quality gate.
#
# Sourced by scripts/quality-gate.sh and exercised directly by
# scripts/quality-lib.test.sh. The functions here are pure: they read a
# committed thresholds file and compare numbers. Nothing in this file runs a
# linter or a test suite, which is what makes the ratchet semantics cheap to
# test.

# QG_THRESHOLDS_FILE — path to the committed thresholds file. The caller sets
# it; scripts/quality-gate.sh derives it from its own location and never from
# the environment, so the path itself is not an override either.

# qg_threshold KEY — echo the value of KEY from QG_THRESHOLDS_FILE.
# Exits 2 if the file is missing or the key is not in it. A threshold that
# cannot be read is a hard error, never a default: "did not run" must not look
# like "passed".
qg_threshold() {
  local key=$1 value
  if [ ! -f "$QG_THRESHOLDS_FILE" ]; then
    echo "quality gate: thresholds file not found: $QG_THRESHOLDS_FILE" >&2
    exit 2
  fi
  value=$(awk -F= -v k="$key" '
    /^[[:space:]]*#/ { next }
    /^[[:space:]]*$/ { next }
    $1 == k { print $2; found = 1; exit }
    END { exit !found }
  ' "$QG_THRESHOLDS_FILE") || {
    echo "quality gate: no threshold '$key' in $QG_THRESHOLDS_FILE" >&2
    exit 2
  }
  printf '%s' "$value"
}

# qg_guard_env KEY... — refuse to run if any threshold name is set in the
# environment. Thresholds are committed; an environment override would change
# what CI enforces while leaving no trace in a diff.
qg_guard_env() {
  local key found=""
  for key in "$@"; do
    if [ -n "${!key+set}" ]; then
      found="$found $key"
    fi
  done
  if [ -n "$found" ]; then
    echo "quality gate: threshold(s) set in the environment:${found}" >&2
    echo "Thresholds come from $QG_THRESHOLDS_FILE and nowhere else." >&2
    echo "Unset them, or change the committed value so the change shows up in the diff." >&2
    exit 2
  fi
}

# qg_cmp_count MEASURED BASELINE — echo ok | over | stale.
#   over  = measured exceeds the baseline (new debt)
#   stale = measured is below the baseline (a win nobody locked in)
# A count baseline must sit exactly on the measurement. This is ratchet
# property 3: a baseline with slack in it silently re-opens room for a
# regression.
qg_cmp_count() {
  local measured=$1 baseline=$2
  if [ "$measured" -gt "$baseline" ]; then
    printf 'over'
  elif [ "$measured" -lt "$baseline" ]; then
    printf 'stale'
  else
    printf 'ok'
  fi
}

# qg_cmp_pct MEASURED FLOOR MARGIN — echo ok | under | stale.
#   under = measured is below the floor
#   stale = measured sits more than MARGIN above the floor
# Percentages carry a margin because two Go toolchains in the same pipeline do
# not report identical aggregates; see COVERAGE_STALE_MARGIN in the thresholds
# file.
qg_cmp_pct() {
  awk -v m="$1" -v f="$2" -v s="$3" 'BEGIN {
    if (m + 0 < f + 0)      { printf "under" }
    else if (m + 0 > f + s) { printf "stale" }
    else                    { printf "ok" }
  }'
}

# qg_set_threshold KEY VALUE — rewrite KEY in place in the thresholds file.
# Used only by --update-baseline.
qg_set_threshold() {
  local key=$1 value=$2 tmp
  tmp=$(mktemp)
  awk -F= -v k="$key" -v v="$value" '
    $1 == k && $0 !~ /^[[:space:]]*#/ { print k "=" v; done = 1; next }
    { print }
    END { if (!done) { print k "=" v } }
  ' "$QG_THRESHOLDS_FILE" > "$tmp"
  mv "$tmp" "$QG_THRESHOLDS_FILE"
}
