#!/usr/bin/env bash
# Smoke-test a deployed environment through its public hostname.
#
# Usage: smoke.sh <base-url> <want-version> <want-commit>
#   e.g. smoke.sh https://spanbarn.wiebe.xyz v0.3.432 50f91f4a0f6d030c8e2047ec9af64637e6fc0be5
# Env:   SMOKE_ATTEMPTS (default 12), SMOKE_WAIT seconds between attempts (default 10)
#
# Every check is a read that changes nothing, and all must pass in one attempt:
#   1. GET /api/v1/health answers 200 with status ok and the wanted version
#   2. GET /api/v1/version answers 200 with the wanted version and commit, so
#      the pods the ingress routes reads to run the release
#   3. GET /api/v1/projects answers exactly 401: the authenticated API is
#      routed and refuses a request without a session (a 5xx fails)
#   4. GET / answers 200 and references an /assets/*.js bundle, which loads
#
# Only 2xx counts as up: curl -f fails every 4xx and 5xx except where a check
# wants a status of its own. Retries cover pods that are still warming up
# behind the ingress. A deploy workflow fails when this exits non-zero, and
# production then rolls back. Never add /api/v1/setup here: a GET there
# creates a pending project for an unknown slug.
# Modelled on wiebe-xyz/qrcodes scripts/smoke.sh.

set -uo pipefail

[ "$#" -eq 3 ] || { echo "usage: smoke.sh <base-url> <want-version> <want-commit>" >&2; exit 2; }
BASE="${1%/}"
WANT_VERSION="$2"
WANT_COMMIT="$3"
ATTEMPTS="${SMOKE_ATTEMPTS:-12}"
WAIT="${SMOKE_WAIT:-10}"

get() {
	curl -fsS --max-time 15 -H 'User-Agent: spanbarn-release-smoke' "$1"
}

attempt() {
	local body bundle code

	body="$(get "${BASE}/api/v1/health")" || { echo "  /api/v1/health: request failed"; return 1; }
	case "$body" in
	*'"status":"ok"'*) ;;
	*) echo "  /api/v1/health: unexpected body: ${body:0:200}"; return 1 ;;
	esac
	case "$body" in
	*"\"version\":\"${WANT_VERSION}\""*) ;;
	*) echo "  /api/v1/health: wanted version ${WANT_VERSION}, got ${body:0:200}"; return 1 ;;
	esac

	body="$(get "${BASE}/api/v1/version")" || { echo "  /api/v1/version: request failed"; return 1; }
	case "$body" in
	*"\"version\":\"${WANT_VERSION}\""*"\"commit\":\"${WANT_COMMIT}\""*) ;;
	*) echo "  /api/v1/version: wanted ${WANT_VERSION} at ${WANT_COMMIT}, got ${body:0:200}"; return 1 ;;
	esac

	code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 15 -H 'User-Agent: spanbarn-release-smoke' "${BASE}/api/v1/projects")" ||
		{ echo "  /api/v1/projects: request failed"; return 1; }
	[ "$code" = "401" ] || { echo "  /api/v1/projects: wanted 401 without a session, got ${code}"; return 1; }

	body="$(get "${BASE}/")" || { echo "  /: request failed"; return 1; }
	bundle="$(printf '%s' "$body" | grep -oE 'src="/assets/[A-Za-z0-9_.-]+\.js"' | head -1 | cut -d'"' -f2)"
	[ -n "$bundle" ] || { echo "  /: no /assets/*.js in the page"; return 1; }
	get "${BASE}${bundle}" >/dev/null || { echo "  ${bundle}: request failed"; return 1; }

	echo "  ok: health and version report ${WANT_VERSION} at ${WANT_COMMIT}, projects 401, page, ${bundle}"
}

for i in $(seq 1 "$ATTEMPTS"); do
	echo "smoke ${BASE} (attempt ${i}/${ATTEMPTS})"
	if attempt; then
		exit 0
	fi
	[ "$i" -lt "$ATTEMPTS" ] && sleep "$WAIT"
done
echo "::error::smoke failed for ${BASE} after ${ATTEMPTS} attempts"
exit 1
