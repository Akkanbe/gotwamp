#!/usr/bin/env bash
# 10% random loss on reflector1, on the reverse path (its replies) and then on
# the forward path (its ingress). TWAMP Full with the stateful reflector
# sequence splits the loss: about 10% lost, all of it estimated on the
# impaired direction. The monitoring session r1-full shows it in /metrics.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
trap '"$netem" reflector1 clear >/dev/null' EXIT

"$netem" reflector1 loss 10%
full=$(twping reflector1 -c 1000 -i 5ms)
report "full  loss 10% (reverse)" "$full"
check "loss ratio" 0.05 "$(field "$full" loss_ratio)" 0.15
# Loss on the reverse path alone leaves every reply with R = S, which a
# stateless (copy_sender) reflector also returns, so the split is withheld.
check "split withheld (reverse-only loss)" 0 "$(field "$full" 'loss_split_valid | if . then 1 else 0 end')" 0

light=$(twping reflector1 --light -6 -c 1000 -i 5ms)
report "light loss 10% (reverse)" "$light"
check "light loss ratio" 0.05 "$(field "$light" loss_ratio)" 0.15

"$netem" reflector1 clear >/dev/null
"$netem" reflector1 --in loss 10%
fwd=$(twping reflector1 -c 1000 -i 5ms)
report "full  loss 10% (forward)" "$fwd"
check "loss ratio" 0.05 "$(field "$fwd" loss_ratio)" 0.15
check "forward loss estimate = lost" "$(field "$fwd" lost)" "$(field "$fwd" lost_fwd)" "$(field "$fwd" lost)"
check "reverse loss estimate" 0 "$(field "$fwd" lost_rev)" 0
"$netem" reflector1 clear >/dev/null

# Loss both ways: the reflector's sequence now differs from the sender's, and
# the split is valid with both estimates near 5%.
"$netem" reflector1 --in loss 5%
"$netem" reflector1 --out loss 5%
both=$(twping reflector1 -c 2000 -i 2ms)
report "full  loss 5% + 5%      " "$both"
check "loss ratio" 0.05 "$(field "$both" loss_ratio)" 0.15
check "forward loss estimate" 50 "$(field "$both" lost_fwd)" 160
check "reverse loss estimate" 50 "$(field "$both" lost_rev)" 160
check "forward + reverse = lost" "$(field "$both" lost)" "$(field "$both" 'lost_fwd + .lost_rev')" "$(field "$both" lost)"
"$netem" reflector1 clear >/dev/null

"$netem" reflector1 loss 10%
echo "  (waiting 12s for the next r1-full run)"
sleep 12
l=$(metric gotwamp_sender_last_run_loss_ratio 'session="r1-full"')
echo "  /metrics r1-full: loss ratio $l"
check "metrics r1-full loss ratio" 0.02 "$l" 0.22
finish
