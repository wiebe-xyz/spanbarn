#!/bin/sh
# Prints the Playwright specs worth running before a staging deploy counts as
# green, one path per line, from the files changed since the last tag. Docs-only
# changes print nothing. The full suite runs nightly (e2e-nightly.yml).
#
# Usage: scripts/e2e-targets.sh [base-ref]
# base-ref defaults to the newest tag before HEAD; without any tag every spec is
# printed.
set -eu

ALL_SPECS=$(cd e2e && ls ./*.spec.ts | sed 's#^\./#e2e/#')
BASE=${1:-$(git describe --tags --abbrev=0 'HEAD^' 2>/dev/null || true)}
if [ -z "$BASE" ]; then
	printf '%s\n' "$ALL_SPECS"
	exit 0
fi

# classify prints the specs for one changed path, or nothing when the path
# cannot change browser-visible behaviour.
classify() {
	path=$1
	lower=$(printf '%s' "$path" | tr 'A-Z' 'a-z')
	case "$lower" in
	e2e/*.spec.ts) printf '%s\n' "$path"; return ;;
	playwright.config.ts | e2e/helpers.ts | package.json | pnpm-lock.yaml | pnpm-workspace.yaml)
		printf '%s\n' "$ALL_SPECS"; return ;;
	*.md | docs/* | specs/* | llms.txt | sdks/* | infra/* | scripts/* | .github/* | license* | .gitignore) return ;;
	*_test.go | *.test.ts | *.test.tsx | *oxlint-baseline.json) return ;;
	esac
	hit=0
	case "$lower" in *dashboard*) printf 'e2e/dashboard.spec.ts\n'; hit=1 ;; esac
	case "$lower" in *heatmap*) printf 'e2e/heatmap.spec.ts\n'; hit=1 ;; esac
	# "dashboard" contains "board", so match boards on the path without it.
	nodash=$(printf '%s' "$lower" | sed 's/dashboard//g')
	case "$nodash" in *board*) printf 'e2e/boards.spec.ts\n'; hit=1 ;; esac
	case "$lower" in *analyze*) printf 'e2e/analyze.spec.ts\n'; hit=1 ;; esac
	case "$lower" in *attribute*) printf 'e2e/attributes.spec.ts\n'; hit=1 ;; esac
	case "$lower" in *compare*) printf 'e2e/compare.spec.ts\n'; hit=1 ;; esac
	case "$lower" in *trace-health* | *tracehealth* | *trace_health*) printf 'e2e/trace-health.spec.ts\n'; hit=1 ;; esac
	case "$lower" in *login* | *auth* | *session*) printf 'e2e/login.spec.ts\n'; hit=1 ;; esac
	[ "$hit" = 1 ] && return
	case "$lower" in
	web/src/components/* | web/src/api/* | web/src/contexts/* | web/src/filters/* | web/src/utils/* | web/src/app.tsx | web/src/main.tsx | web/index.html)
		printf 'e2e/login.spec.ts\ne2e/dashboard.spec.ts\ne2e/boards.spec.ts\n'; return ;;
	esac
	# Any other code or deploy change gets the smoke pair: it proves the
	# deployment answers and a session can be opened, without the dashboard.
	printf 'e2e/health.spec.ts\ne2e/login.spec.ts\n'
}

git diff --name-only "$BASE" HEAD | while IFS= read -r changed; do
	[ -n "$changed" ] && classify "$changed"
done | sort -u
