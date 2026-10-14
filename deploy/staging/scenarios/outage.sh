#!/usr/bin/env bash
# Reflector outages and the sender's behavior, then recovery. Each step reads
# the sender's /metrics (runs_total by result, last-run loss) and its log.
#  1. stop reflector1 for 20 s: Docker drops its DNS name, the periodic Full
#     run fails at connect (error), the continuous Light session keeps the
#     resolved address and loses the window; both recover.
#  2. pause reflector3 for 25 s (the process hangs, TCP still accepts): the
#     Full run fails at the greeting after control_timeout (15 s) and the
#     next run is skipped; STAMP loses the windows; both recover.
#  3. cut reflector2 for 30 s (netem loss 100% both ways): connect timeout
#     or no route to host; both sessions recover.
#  4. kill reflector3 during an ad-hoc Full measurement: the sender sees the
#     control connection close and reports the run aborted and invalid.
#  5. restart the sender: the sessions resume, the SQLite history of before
#     the restart is still read by gotwamp stats, and a session made through
#     the API is gone (by design: API session definitions are not persisted,
#     SPEC §9.3 / §9.4).
set -euo pipefail
. "$(dirname "$0")/lib.sh"
trap '"$netem" all clear >/dev/null; "${compose[@]}" unpause reflector3 >/dev/null 2>&1 || true; "${compose[@]}" start reflector1 reflector3 >/dev/null 2>&1 || true' EXIT
"$netem" all clear >/dev/null
runs() { metric gotwamp_sender_runs_total "session=\"$1\",result=\"$2\""; }
since() { "${compose[@]}" logs --since "$1" sender 2>/dev/null | grep -E "session=$2( |$)" | grep -E 'failed|skipped|window ended|run ended|run summary' |
	sed -E 's/.*msg="([^"]+)".*(stage=[a-z]+ error=.*|received=[0-9]+ lost=[0-9]+).*/    \1 \2/; s/.*msg="(run skipped[^"]*)".*/    \1/' | cut -c1-150; }
recovered() {
	# recovered SESSION: the latest result after the outage is complete.
	check "$1 recovered: last run success" 1 "$(metric gotwamp_sender_last_run_success "session=\"$1\"")" 1
	check "$1 recovered: last loss ratio" 0 "$(metric gotwamp_sender_last_run_loss_ratio "session=\"$1\"")" 0
}

echo "1: reflector1 stopped for 20s"
t=$(date -u +%Y-%m-%dT%H:%M:%SZ)
e0=$(runs r1-full error)
"${compose[@]}" stop reflector1 >/dev/null 2>&1
sleep 20
"${compose[@]}" start reflector1 >/dev/null 2>&1
sleep 25
since "$t" r1-full
since "$t" r1-light-v6
check "r1-full failed runs" 1 "$(($(runs r1-full error) - e0))" 3
recovered r1-full
recovered r1-light-v6

echo "2: reflector3 paused for 25s"
t=$(date -u +%Y-%m-%dT%H:%M:%SZ)
e0=$(runs r3-full-ext error)
k0=$(runs r3-full-ext skipped)
"${compose[@]}" pause reflector3 >/dev/null 2>&1
sleep 25
"${compose[@]}" unpause reflector3 >/dev/null 2>&1
sleep 25
since "$t" r3-full-ext
since "$t" r3-stamp
check "r3-full-ext failed runs" 1 "$(($(runs r3-full-ext error) - e0))" 2
check "r3-full-ext skipped runs" 0 "$(($(runs r3-full-ext skipped) - k0))" 2
recovered r3-full-ext
recovered r3-stamp

echo "3: reflector2 cut for 30s"
t=$(date -u +%Y-%m-%dT%H:%M:%SZ)
e0=$(runs r2-full-enc error)
"$netem" reflector2 --in loss 100% >/dev/null
"$netem" reflector2 --out loss 100% >/dev/null
sleep 30
"$netem" reflector2 clear >/dev/null
sleep 25
since "$t" r2-full-enc
since "$t" r2-light
check "r2-full-enc failed runs" 1 "$(($(runs r2-full-enc error) - e0))" 3
recovered r2-full-enc
recovered r2-light

echo "4: reflector3 killed during an ad-hoc Full measurement"
out=$(mktemp)
rcf=$(mktemp)
("${compose[@]}" exec -T sender gotwamp ping reflector3 -c 500 -i 10ms -q --output jsonl >"$out" 2>/dev/null && echo 0 >"$rcf" || echo $? >"$rcf") &
sleep 2
"${compose[@]}" kill reflector3 >/dev/null 2>&1
wait
jq -r 'select(.summary) | .summary | "    sent \(.sent) received \(.received) aborted \(.aborted) invalid \(.invalid)"' "$out"
jq -r 'select(.error) | "    error at \(.error.stage): \(.error.message)"' "$out"
echo "    exit status $(cat "$rcf")"
check "ad-hoc run aborted" 1 "$(jq -r 'select(.summary) | .summary.aborted | if . then 1 else 0 end' "$out")" 1
check "ad-hoc run invalid" 1 "$(jq -r 'select(.summary) | .summary.invalid | if . then 1 else 0 end' "$out")" 1
check "ad-hoc exit status" 4 "$(cat "$rcf")" 4
rm -f "$out" "$rcf"
"${compose[@]}" start reflector3 >/dev/null 2>&1
sleep 25
recovered r3-full-ext
recovered r3-stamp

echo "5: sender restarted"
"${compose[@]}" exec -T sender gotwamp session add api-r1 --target reflector1 --light --continuous --window 10s >/dev/null
n0=$("${compose[@]}" exec -T sender gotwamp stats r1-full --since 15m --output jsonl | jq '.summaries | length')
"${compose[@]}" restart sender >/dev/null 2>&1
sleep 25
n1=$("${compose[@]}" exec -T sender gotwamp stats r1-full --since 15m --output jsonl | jq '.summaries | length')
echo "    r1-full results of the last 15 min: $n0 before, $n1 after the restart"
check "history read back after the restart" "$((n0 - 5))" "$n1" "$((n0 + 5))"
for s in r1-full r1-light-v6 r2-full-enc r2-light r3-full-ext r3-stamp; do
	recovered "$s"
done
if "${compose[@]}" exec -T sender gotwamp session list | awk '$1 == "api-r1" { f = 1 } END { exit !f }'; then
	echo "    api-r1 (made through the API) survived the restart"
	"${compose[@]}" exec -T sender gotwamp session remove api-r1 >/dev/null
else
	echo "    api-r1 (made through the API) is gone after the restart"
fi
finish
