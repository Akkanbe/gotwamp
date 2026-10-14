#!/usr/bin/env bash
# Several impairments at once on reflector3 (STAMP and the RFC 6038 / 5938
# extensions): delay 5ms 1ms, 2% loss, 2% duplication and 10 Mbit/s on the
# replies. Checks that every session type still runs and the figures follow.
# netem keeps the order when rate is set, so reordering is a step of its own:
# reorder 25% sends a quarter of the packets at once, ahead of the others
# delayed 10 ms.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
trap '"$netem" reflector3 clear >/dev/null' EXIT

"$netem" reflector3 delay 5ms 1ms loss 2% duplicate 2% rate 10mbit
stamp=$(twping reflector3 --light --stamp --ssid 7 -c 1000 -i 5ms)
report "stamp      " "$stamp"
ext=$(twping reflector3 -6 --isc --symmetrical --reflect-octets 0xbeef --padding-to-reflect 16 -s 64 -c 1000 -i 5ms)
report "full ext v6" "$ext"

for r in stamp ext; do
	s=${!r}
	check "$r rtt median (ms)" 4 "$(field "$s" rtt_median)" 7
	check "$r loss ratio" 0.005 "$(field "$s" loss_ratio)" 0.05
	check "$r ipdv mean (ms)" 0.3 "$(field "$s" ipdv_mean)" 3
	check "$r reverse duplicates" 1 "$(field "$s" dup_reverse)" 60
done

"$netem" reflector3 delay 10ms reorder 25% 50%
reorder=$(twping reflector3 --light --stamp --ssid 7 -c 1000 -i 2ms)
report "stamp reorder" "$reorder"
check "reordered" 50 "$(field "$reorder" reordered)" 400
check "reordered on the reverse path" 0 "$(field "$reorder" reordered_reverse)" 400
finish
