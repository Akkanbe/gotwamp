#!/usr/bin/env bash
# Large padding under a bandwidth limit on reflector3's replies.
#  1. rate 2mbit, 50 packets/s: a 1,400-octet padding makes the replies about
#     1,470 octets on the wire, 5.9 ms each at 2 Mbit/s, under the rate, so
#     the RTT rises by the serialization time and nothing queues. The
#     default padding (27) adds about 0.4 ms.
#  2. rate 1mbit, 200 packets/s of 1,400 octets (2.4 Mbit/s): the queue
#     grows through the run, the RTT climbs past the 2 s timeout, and the
#     late replies count as lost.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
trap '"$netem" reflector3 clear >/dev/null' EXIT
"$netem" all clear >/dev/null

"$netem" reflector3 rate 2mbit
small=$(twping reflector3 -c 200 -i 20ms)
report "full -s 27   at 2mbit" "$small"
big=$(twping reflector3 -c 200 -i 20ms -s 1400)
report "full -s 1400 at 2mbit" "$big"
sym=$(twping reflector3 -6 -c 200 -i 20ms -s 1400 --symmetrical)
report "full v6 -s 1400 symmetrical" "$sym"
light=$(twping reflector3 --light --stamp -c 200 -i 20ms -s 1400)
report "stamp -s 1400 at 2mbit" "$light"
check "padding 27 rtt median (ms)" 0.2 "$(field "$small" rtt_median)" 0.8
check "padding 1400 rtt median (ms)" 5.5 "$(field "$big" rtt_median)" 6.6
check "padding 1400 reverse median (ms)" 5.5 "$(field "$big" rev_median)" 6.6
check "padding 1400 loss" 0 "$(field "$big" lost)" 0
check "v6 symmetrical rtt median (ms)" 5.5 "$(field "$sym" rtt_median)" 6.8
check "stamp padding 1400 rtt median (ms)" 5.5 "$(field "$light" rtt_median)" 6.6

"$netem" reflector3 rate 1mbit
over=$("${compose[@]}" exec -T sender gotwamp ping reflector3 -q --output jsonl -c 400 -i 5ms -s 1400 |
	jq -c 'select(.summary) | .summary | def ms: rtrimstr("s") | tonumber * 1000;
		{sent, received, lost, loss_ratio, rtt_min: (.rtt.min | ms), rtt_median: (.rtt.median | ms), rtt_max: (.rtt.max | ms)}')
echo "  overload 2.4 Mbit/s into 1 Mbit/s: $over"
check "overload rtt max (ms)" 1500 "$(field "$over" rtt_max)" 2100
check "overload rtt min (ms)" 5 "$(field "$over" rtt_min)" 30
check "overload late replies counted lost" 1 "$(field "$over" lost)" 300
finish
