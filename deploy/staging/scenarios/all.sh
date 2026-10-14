#!/usr/bin/env bash
# Runs every scenario in turn; exits non-zero if any failed. monitoring.sh
# needs the observability profile and is skipped without Prometheus.
#   all.sh [SCENARIO...]    e.g. all.sh modes loss
set -uo pipefail
cd "$(dirname "$0")"
scenarios=("$@")
((${#scenarios[@]} > 0)) || scenarios=(modes delay-jitter forward-delay loss combined netem-matrix rate-padding monitoring reload outage)
rc=0
for s in "${scenarios[@]}"; do
	echo "### $s"
	if [[ $s == monitoring ]] && ! curl -fsS -o /dev/null "${PROM_URL:-http://127.0.0.1:19090}/-/ready" 2>/dev/null; then
		echo "skipped: Prometheus is not up (make staging-up PROFILES=observability)"
		echo
		continue
	fi
	./"$s".sh || rc=1
	echo
done
exit $rc
