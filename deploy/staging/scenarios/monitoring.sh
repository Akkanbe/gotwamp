#!/usr/bin/env bash
# The monitoring sessions follow netem changes, read back from Prometheus
# (profile observability). Periodic sessions (r1-full, r2-full-enc,
# r3-full-ext: every 10 s) and continuous ones (r1-light-v6, r2-light,
# r3-stamp: 10 s windows) are checked after each change has had time for a
# whole run or window and a scrape.
#   A. reflector1 --out delay 30ms; reflector2 --in loss 10%;
#      reflector3 --out delay 5ms 2ms
#   B. reflector1 --out delay 10ms; reflector2 and reflector3 cleared
#   C. everything cleared
set -euo pipefail
. "$(dirname "$0")/lib.sh"
trap '"$netem" all clear >/dev/null' EXIT
settle=${SETTLE:-27}

[[ -n $(prom 'up{job="gotwampd"}') ]] || {
	echo "Prometheus is not up: make staging-up PROFILES=observability" >&2
	exit 1
}

rtt() { prom "gotwamp_sender_last_run_rtt_seconds{session=\"$1\",stat=\"median\"}"; }
loss() { prom "gotwamp_sender_last_run_loss_ratio{session=\"$1\"}"; }
ipdv() { prom "gotwamp_sender_last_run_jitter_seconds{session=\"$1\",kind=\"ipdv_mean\"}"; }
fresh() {
	# fresh SESSION SINCE: the latest run or window ended after SINCE.
	local t
	t=$(prom "gotwamp_sender_last_run_timestamp_seconds{session=\"$1\"}")
	check "$1 result is newer than the change (s)" "$2" "${t%.*}" 9999999999
}
pause() {
	echo "  (waiting ${settle}s for a run or window and a scrape)"
	sleep "$settle"
}

"$netem" all clear >/dev/null
echo "A: reflector1 +30ms reverse, reflector2 10% forward loss, reflector3 5ms±2ms reverse"
t=$(date +%s)
"$netem" reflector1 --out delay 30ms
"$netem" reflector2 --in loss 10%
"$netem" reflector3 --out delay 5ms 2ms
pause
for s in r1-full r1-light-v6; do
	fresh "$s" "$t"
	check "$s rtt median (s)" 0.029 "$(rtt "$s")" 0.032
done
for s in r2-full-enc r2-light; do
	check "$s loss ratio" 0.02 "$(loss "$s")" 0.22
	check "$s rtt median (s)" 0 "$(rtt "$s")" 0.0005
done
for s in r3-full-ext r3-stamp; do
	check "$s rtt median (s)" 0.004 "$(rtt "$s")" 0.006
	check "$s ipdv mean (s)" 0.0005 "$(ipdv "$s")" 0.004
done
h=$(prom 'histogram_quantile(0.5, sum by (le) (rate(gotwamp_sender_rtt_seconds_bucket{session="r1-light-v6"}[15s])))')
check "r1-light-v6 histogram median over 15s (s)" 0.02 "$h" 0.05
l=$(prom 'increase(gotwamp_sender_packets_lost_total{session="r2-light"}[20s])')
check "r2-light lost packets in 20s" 5 "$l" 50

echo "B: reflector1 +10ms, reflector2 and reflector3 cleared"
t=$(date +%s)
"$netem" reflector1 --out delay 10ms
"$netem" reflector2 clear >/dev/null
"$netem" reflector3 clear >/dev/null
pause
for s in r1-full r1-light-v6; do
	fresh "$s" "$t"
	check "$s rtt median (s)" 0.0095 "$(rtt "$s")" 0.011
done
for s in r2-full-enc r2-light r3-full-ext r3-stamp; do
	fresh "$s" "$t"
	check "$s rtt median (s)" 0 "$(rtt "$s")" 0.0005
	check "$s loss ratio" 0 "$(loss "$s")" 0
done

echo "C: all cleared"
t=$(date +%s)
"$netem" all clear >/dev/null
pause
for s in r1-full r1-light-v6 r2-full-enc r2-light r3-full-ext r3-stamp; do
	fresh "$s" "$t"
	check "$s rtt median (s)" 0 "$(rtt "$s")" 0.0005
	check "$s loss ratio" 0 "$(loss "$s")" 0
	check "$s last run success" 1 "$(prom "gotwamp_sender_last_run_success{session=\"$s\"}")" 1
done
finish
