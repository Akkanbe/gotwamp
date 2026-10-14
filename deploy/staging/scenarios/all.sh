#!/usr/bin/env bash
# Runs every scenario in turn; exits non-zero if any failed.
set -uo pipefail
cd "$(dirname "$0")"
rc=0
for s in delay-jitter forward-delay loss combined; do
	echo "### $s"
	./"$s".sh || rc=1
	echo
done
exit $rc
