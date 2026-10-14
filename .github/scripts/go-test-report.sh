#!/usr/bin/env bash
# go-test-report.sh LOG: after a failed `go test` step, make the failure
# readable without the raw log, which GitHub serves to signed-in users only.
# Each failure becomes an error annotation (shown on the run page and through
# the public check-runs API), and the step summary gets the context.
#
# Failures: "--- FAIL" and "FAIL <package>" lines, test log lines
# (file_test.go:N:), panics, data races and fatal errors of the race runtime
# (ThreadSanitizer).
set -euo pipefail

log="${1:?usage: go-test-report.sh LOG}"
[[ -f "$log" ]] || { echo "::error title=go test::no log at $log"; exit 0; }

pattern='^(--- FAIL|FAIL[[:space:]]|panic:|fatal error:)|WARNING: DATA RACE|ThreadSanitizer|FATAL:|_test\.go:[0-9]+:'

# annotate TITLE TEXT: one error annotation; newlines and % escaped as the
# workflow command syntax wants, and the text capped.
annotate() {
	local title="$1" text="${2:0:4000}"
	text="${text//'%'/'%25'}"
	text="${text//$'\r'/'%0D'}"
	text="${text//$'\n'/'%0A'}"
	echo "::error title=${title}::${text}"
}

# Up to five annotations: a data race or a race runtime failure with the
# lines that follow it, then the failing tests with their messages.
n=0
while IFS=: read -r lineno _; do
	annotate "race detector" "$(sed -n "${lineno},$((lineno + 40))p" "$log")"
	n=$((n + 1))
done < <(grep -nE 'WARNING: DATA RACE|ThreadSanitizer|^FATAL:' "$log" | head -n 4)

fails="$(grep -E "$pattern" "$log" | grep -vE 'WARNING: DATA RACE|ThreadSanitizer' | head -n 60 || true)"
if [[ -n "$fails" && $n -lt 5 ]]; then
	annotate "go test failures" "$fails"
fi

{
	echo "### go test failures"
	echo
	echo '```'
	grep -nE -A 12 "$pattern" "$log" | head -n 400 || true
	echo '```'
} >>"${GITHUB_STEP_SUMMARY:-/dev/stdout}"
