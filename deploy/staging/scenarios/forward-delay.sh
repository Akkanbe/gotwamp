#!/usr/bin/env bash
# Delay on one direction at a time. The containers share the host's clock,
# so the one-way delays are valid and tell the directions apart.
#  1. Forward path: netem on reflector1's ingress. Toward reflector1 (IPv4
#     and IPv6) the forward delay is about 20 ms and the reverse about 0.
#  2. Reverse path of one peer: netem on the sender's ingress, only from
#     reflector2. reflector2's reverse delay is about 15 ms; reflector1 is
#     untouched.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
trap '"$netem" reflector1 clear >/dev/null; "$netem" sender clear >/dev/null' EXIT

"$netem" reflector1 --in delay 20ms
v4=$(twping reflector1 -4 -c 100 -i 10ms)
report "reflector1 v4 full " "$v4"
v6=$(twping reflector1 -6 --light -c 100 -i 10ms)
report "reflector1 v6 light" "$v6"
r2=$(twping reflector2 --light -c 100 -i 10ms)
report "reflector2 v4 light" "$r2"
"$netem" reflector1 clear >/dev/null

for r in v4 v6; do
	s=${!r}
	check "reflector1 $r forward median (ms)" 19.5 "$(field "$s" fwd_median)" 21.5
	check "reflector1 $r reverse median (ms)" 0 "$(field "$s" rev_median)" 0.5
	check "reflector1 $r rtt median (ms)" 19.5 "$(field "$s" rtt_median)" 21.5
done
check "reflector2 rtt median (ms)" 0 "$(field "$r2" rtt_median)" 0.5

"$netem" sender --in --peer reflector2 delay 15ms
r2=$(twping reflector2 --security encrypted --key-id staging -c 100 -i 10ms)
report "reflector2 v4 full enc" "$r2"
r1=$(twping reflector1 --light -c 100 -i 10ms)
report "reflector1 v4 light   " "$r1"
check "reflector2 reverse median (ms)" 14.5 "$(field "$r2" rev_median)" 16.5
check "reflector2 forward median (ms)" 0 "$(field "$r2" fwd_median)" 0.5
check "reflector1 rtt median (ms)" 0 "$(field "$r1" rtt_median)" 0.5
finish
