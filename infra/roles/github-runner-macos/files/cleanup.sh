#!/usr/bin/env bash
set -euo pipefail

DRY_RUN=false
for arg in "$@"; do [[ "$arg" == "--dry-run" ]] && DRY_RUN=true; done

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
STAMP_FILE="${SCRIPT_DIR}/.cleanup_last_run"
LOG_FILE="${SCRIPT_DIR}/cleanup.log"

FREE_SPACE_THRESHOLD_GB=10
RELAXED_INTERVAL_HOURS=24
URGENT_INTERVAL_HOURS=1

log() { echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*" | tee -a "$LOG_FILE" >&2; }

free_gb() {
  df -g "$SCRIPT_DIR" 2>/dev/null | awk 'NR==2 {print $4}' || \
    df -k "$SCRIPT_DIR" | awk 'NR==2 {printf "%d", $4/1024/1024}'
}

hours_since_last_run() {
  [[ -f "$STAMP_FILE" ]] || { echo 9999; return; }
  echo $(( ($(date +%s) - $(cat "$STAMP_FILE")) / 3600 ))
}

should_run() {
  local avail_gb elapsed
  avail_gb=$(free_gb)
  elapsed=$(hours_since_last_run)

  if (( avail_gb > FREE_SPACE_THRESHOLD_GB )); then
    if (( elapsed < RELAXED_INTERVAL_HOURS )); then
      log "Skipping: ${avail_gb}GB free, last ran ${elapsed}h ago (relaxed interval: ${RELAXED_INTERVAL_HOURS}h)"
      return 1
    fi
    log "Running relaxed cleanup: ${avail_gb}GB free, last ran ${elapsed}h ago"
  else
    if (( elapsed < URGENT_INTERVAL_HOURS )); then
      log "Skipping: ${avail_gb}GB free, last ran ${elapsed}h ago (urgent interval: ${URGENT_INTERVAL_HOURS}h)"
      return 1
    fi
    log "Running urgent cleanup: only ${avail_gb}GB free (<= ${FREE_SPACE_THRESHOLD_GB}GB threshold)"
  fi
}

clean_work_dirs() {
  log "Cleaning runner work directories..."
  local total_freed=0

  for runner_dir in "$SCRIPT_DIR"/*/; do
    [[ -d "${runner_dir}_work" ]] || continue
    for repo_dir in "${runner_dir}_work"/*/; do
      [[ -d "$repo_dir" ]] || continue
      local dir_name
      dir_name=$(basename "$repo_dir")
      if [[ "$dir_name" == _* ]]; then
        continue
      fi
      if find "$repo_dir" -maxdepth 2 -mmin -30 2>/dev/null | grep -q .; then
        log "  Skipping ${repo_dir} (recently active)"
        continue
      fi
      local size
      size=$(du -sk "$repo_dir" 2>/dev/null | awk '{print $1}')
      if $DRY_RUN; then
        log "  [dry-run] Would remove: $repo_dir (${size}KB)"
      else
        rm -rf "$repo_dir"
        log "  Removed: $repo_dir (${size}KB)"
      fi
      total_freed=$(( total_freed + size ))
    done
  done

  log "Work dirs freed: $(( total_freed / 1024 ))MB"
}

clean_tool_caches() {
  log "Cleaning tool caches..."
  local total_freed=0

  for pattern in \
    "$SCRIPT_DIR"/*/_work/_tool \
    "$SCRIPT_DIR"/*/hostedtoolcache \
    "$SCRIPT_DIR"/*/_diag
  do
    for cache_dir in $pattern; do
      [[ -d "$cache_dir" ]] || continue
      local size
      size=$(du -sk "$cache_dir" 2>/dev/null | awk '{print $1}')
      if $DRY_RUN; then
        log "  [dry-run] Would remove: $cache_dir (${size}KB)"
      else
        rm -rf "$cache_dir"
        log "  Removed: $cache_dir (${size}KB)"
      fi
      total_freed=$(( total_freed + size ))
    done
  done

  while IFS= read -r -d '' logfile; do
    local size
    size=$(du -sk "$logfile" 2>/dev/null | awk '{print $1}')
    if $DRY_RUN; then
      log "  [dry-run] Would remove old log: $logfile (${size}KB)"
    else
      rm -f "$logfile"
      log "  Removed old log: $logfile"
    fi
    total_freed=$(( total_freed + size ))
  done < <(find "$SCRIPT_DIR" -name "*.log" -not -name "cleanup.log" -mtime +7 -print0 2>/dev/null)

  log "Caches freed: $(( total_freed / 1024 ))MB"
}

main() {
  should_run || exit 0

  $DRY_RUN && log "=== Runner cleanup started (DRY RUN) ===" || log "=== Runner cleanup started ==="
  log "Disk free before: $(free_gb)GB"

  $DRY_RUN || date +%s > "$STAMP_FILE"

  clean_work_dirs
  clean_tool_caches

  log "Disk free after:  $(free_gb)GB"
  log "=== Runner cleanup complete ==="
}

main "$@"
