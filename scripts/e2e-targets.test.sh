#!/bin/bash
# Tests scripts/e2e-targets.sh against a throwaway git repository.
set -eu

SCRIPT=$(cd "$(dirname "$0")" && pwd)/e2e-targets.sh
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
cd "$WORK"
git init -q .
git config user.email t@example.invalid
git config user.name t
git config commit.gpgsign false
mkdir e2e web web/src web/src/pages internal docs
for s in analyze attributes boards compare dashboard health heatmap login trace-health; do
	: >"e2e/$s.spec.ts"
done
: >README.md
git add -A
git commit -q -m base
git tag v0.0.1
fail=0

# check <name> <expected specs, space separated> <path>...
check() {
	name=$1
	want=$2
	shift 2
	for p in "$@"; do
		mkdir -p "$(dirname "$p")"
		echo "$name" >>"$p"
	done
	git add -A
	git commit -q -m "$name"
	got=$("$SCRIPT" v0.0.1 | sed 's#^e2e/##; s#\.spec\.ts$##' | tr '\n' ' ' | sed 's/ $//')
	if [ "$got" != "$want" ]; then
		echo "FAIL $name: got '$got', want '$want'"
		fail=1
	else
		echo "ok   $name"
	fi
	git reset -q --hard v0.0.1
}

check "docs only" "" README.md docs/guide.md
check "go unit test only" "" internal/thing_test.go
check "dashboard page" "dashboard" web/src/pages/DashboardPage.tsx
check "dashboard query" "dashboard" internal/repository/repo_dashboard.go
check "heatmap and boards" "boards heatmap" web/src/pages/HeatmapPage.tsx web/src/pages/BoardsPage.tsx
check "unmapped backend" "health login" internal/ingest/handler.go
check "shared frontend" "boards dashboard login" web/src/components/Chart.tsx
check "spec edit" "compare" e2e/compare.spec.ts
check "playwright config" "analyze attributes boards compare dashboard health heatmap login trace-health" playwright.config.ts

exit "$fail"
