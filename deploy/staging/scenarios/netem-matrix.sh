#!/usr/bin/env bash
# Impairments combined and by direction:
#  1. Both directions of reflector2 at once, every kind of impairment: the
#     forward path (--in) delay 8ms 1ms, loss 3%, duplicate 2%; the reverse
#     path (--out) delay 4ms 1ms, loss 3%, reorder 10%. The one-way delays,
#     the loss split, the forward duplicates and the reordering must each
#     land on their own direction.
#  2. Per peer on the sender: loss 20% only to reflector1 (--out --peer) and
#     delay 12ms only from reflector3 (--in --peer); reflector2 untouched.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
trap '"$netem" all clear >/dev/null' EXIT
"$netem" all clear >/dev/null

"$netem" reflector2 --in delay 8ms 1ms loss 3% duplicate 2%
"$netem" reflector2 --out delay 4ms 1ms loss 3% reorder 10%
for sec in "auth" "encrypted"; do
	s=$(twping reflector2 --security "$sec" --key-id staging -s 64 -c 2000 -i 2ms)
	report "reflector2 $sec, both ways" "$s"
	check "$sec forward median (ms)" 7.5 "$(field "$s" fwd_median)" 8.6
	check "$sec reverse median (ms)" 3.0 "$(field "$s" rev_median)" 4.6
	check "$sec rtt median (ms)" 11 "$(field "$s" rtt_median)" 13
	check "$sec loss ratio" 0.03 "$(field "$s" loss_ratio)" 0.09
	check "$sec loss split valid" 1 "$(field "$s" 'loss_split_valid | if . then 1 else 0 end')" 1
	check "$sec forward loss estimate" 25 "$(field "$s" lost_fwd)" 110
	check "$sec reverse loss estimate" 25 "$(field "$s" lost_rev)" 110
	check "$sec forward duplicates" 15 "$(field "$s" dup_forward)" 70
	check "$sec reverse duplicates" 0 "$(field "$s" dup_reverse)" 0
	check "$sec reordered" 20 "$(field "$s" reordered)" 600
	check "$sec ipdv forward mean (ms)" 0.3 "$(field "$s" ipdv_fwd_mean)" 2
done
s=$(twping reflector2 --light -6 -c 2000 -i 2ms)
report "reflector2 light v6, both ways" "$s"
check "light rtt median (ms)" 11 "$(field "$s" rtt_median)" 13
check "light loss ratio" 0.03 "$(field "$s" loss_ratio)" 0.09
check "light forward duplicates" 15 "$(field "$s" 'dup_forward + .dup_reverse')" 70
"$netem" reflector2 clear >/dev/null

"$netem" sender --out --peer reflector1 loss 20%
"$netem" sender --in --peer reflector3 delay 12ms
r1=$(twping reflector1 -c 1000 -i 2ms)
report "reflector1 (sender --out loss 20%)" "$r1"
r2=$(twping reflector2 --light -c 500 -i 2ms)
report "reflector2 (untouched)            " "$r2"
r3=$(twping reflector3 -6 -c 500 -i 2ms)
report "reflector3 (sender --in 12ms)     " "$r3"
check "reflector1 loss ratio" 0.14 "$(field "$r1" loss_ratio)" 0.26
check "reflector1 forward loss = lost" "$(field "$r1" lost)" "$(field "$r1" lost_fwd)" "$(field "$r1" lost)"
check "reflector2 loss" 0 "$(field "$r2" lost)" 0
check "reflector2 rtt median (ms)" 0 "$(field "$r2" rtt_median)" 0.5
check "reflector3 reverse median (ms)" 11.5 "$(field "$r3" rev_median)" 13
check "reflector3 forward median (ms)" 0 "$(field "$r3" fwd_median)" 0.5
check "reflector3 loss" 0 "$(field "$r3" lost)" 0
finish
