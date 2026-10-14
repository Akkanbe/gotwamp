#!/usr/bin/env bash
# Delay and jitter on the reverse path of reflector2: netem delay 10ms 2ms on
# reflector2's replies. The RTT median and the reverse delay rise by about
# 10 ms, the forward delay stays near 0, and the IPDV / RFC 3550 jitter rise
# to the order of the 2 ms jitter. The monitoring session r2-light shows the
# same in /metrics.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
trap '"$netem" reflector2 clear >/dev/null' EXIT

"$netem" reflector2 clear >/dev/null
base=$(twping reflector2 --light -c 200 -i 10ms)
report "baseline       " "$base"

"$netem" reflector2 delay 10ms 2ms
light=$(twping reflector2 --light -c 300 -i 10ms)
report "light +10ms±2ms" "$light"
full=$(twping reflector2 --security encrypted --key-id staging -c 300 -i 10ms)
report "full  +10ms±2ms" "$full"

for r in light full; do
	s=${!r}
	check "$r rtt median (ms)" 9 "$(field "$s" rtt_median)" 12
	check "$r reverse median (ms)" 9 "$(field "$s" rev_median)" 12
	check "$r forward median (ms)" 0 "$(field "$s" fwd_median)" 0.5
	check "$r ipdv mean (ms)" 0.8 "$(field "$s" ipdv_mean)" 4
	check "$r reverse ipdv mean (ms)" 0.8 "$(field "$s" ipdv_rev_mean)" 4
	check "$r loss" 0 "$(field "$s" lost)" 0
done

wait_window
m=$(metric gotwamp_sender_last_run_rtt_seconds 'session="r2-light",stat="median"')
j=$(metric gotwamp_sender_last_run_jitter_seconds 'session="r2-light",kind="ipdv_mean"')
echo "  /metrics r2-light: rtt median ${m}s, ipdv mean ${j}s"
check "metrics r2-light rtt median (s)" 0.009 "$m" 0.012
check "metrics r2-light ipdv mean (s)" 0.0008 "$j" 0.004
finish
