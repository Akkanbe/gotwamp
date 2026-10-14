#!/usr/bin/env bash
# Configuration reload of the sender (config/sender.yaml is bind-mounted and
# rewritten in place, keeping its inode):
#  1. add a session (r2-light-v6) and change one (r2-light: 100 ms -> 50 ms),
#     gotwamp config reload: both take effect, the session made through the
#     API (api-r3) is kept;
#  2. an invalid file (interval 10us): the reload fails and the running
#     configuration stays;
#  3. the original file back, SIGHUP: r2-light-v6 is gone, r2-light back to
#     100 per window.
# The original file is restored and reloaded whatever happens.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
cfg="$staging/config/sender.yaml"
orig=$(mktemp)
cp "$cfg" "$orig"
sender() { "${compose[@]}" exec -T sender "$@"; }
restore() {
	cat "$orig" >"$cfg"
	sender gotwamp config reload >/dev/null 2>&1 || true
	sender gotwamp session remove api-r3 >/dev/null 2>&1 || true
	rm -f "$orig"
}
trap restore EXIT
"$netem" all clear >/dev/null
has() { sender gotwamp session list | awk -v n="$1" '$1 == n { f = 1 } END { exit !f }'; }
# window_sent SESSION: packets sent in the latest result of SESSION.
window_sent() { sender gotwamp stats "$1" --last 1 --output jsonl | jq -r '.summaries[-1].sent'; }

sender gotwamp session add api-r3 --target reflector3 --light --stamp --continuous --window 10s >/dev/null
has api-r3 && echo "  ok   api-r3 added through the API" || { echo "  FAIL api-r3 not added"; failures=$((failures + 1)); }

echo "1: add r2-light-v6, r2-light interval 50ms"
python3 - "$cfg" <<'PY'
import sys
p = sys.argv[1]
s = open(p).read()
import re
s, n = re.subn(r"(    - name: r2-light\n(?:      .*\n)*?      interval: )100ms", r"\g<1>50ms", s, count=1)
assert n == 1, "r2-light interval not found"
add = """    - name: r2-light-v6
      target: reflector2
      ipv: "6"
      mode: light
      interval: 100ms
      schedule: {type: continuous, window: 10s}
      labels: {reflector: reflector2, family: ipv6}

"""
i = s.index("\nreflector:")
s = s[:i + 1].rstrip("\n") + "\n" + add + s[i + 1:]
open(p, "w").write(s)
PY
out=$(sender gotwamp config reload 2>&1) && rc=0 || rc=$?
echo "  reload: exit $rc: $out"
check "reload exit status" 0 "$rc" 0
has r2-light-v6 && echo "  ok   r2-light-v6 running" || { echo "  FAIL r2-light-v6 missing"; failures=$((failures + 1)); }
has api-r3 && echo "  ok   api-r3 kept" || { echo "  FAIL api-r3 dropped by the reload"; failures=$((failures + 1)); }
echo "  (waiting 25s for new windows)"
sleep 25
check "r2-light sent per window (50ms)" 199 "$(window_sent r2-light)" 201
check "r2-light-v6 sent per window" 99 "$(window_sent r2-light-v6)" 101
m=$(metric gotwamp_sender_last_run_rtt_seconds 'session="r2-light-v6",stat="median"')
check "r2-light-v6 in /metrics (rtt median, s)" 0 "${m:--1}" 0.0005

echo "2: invalid file"
# Every write keeps the inode: sed -i or an editor that replaces the file
# would leave the container's bind mount on the old file.
bad=$(sed 's/interval: 50ms/interval: 10us/' "$cfg")
cat <<<"$bad" >"$cfg"
out=$(sender gotwamp config reload 2>&1) && rc=0 || rc=$?
echo "  reload: exit $rc: $(head -1 <<<"$out")"
check "reload of an invalid file fails (exit status)" 1 "$rc" 255
has r2-light-v6 && echo "  ok   running configuration kept" || { echo "  FAIL sessions changed"; failures=$((failures + 1)); }
sender gotwamp status >/dev/null && echo "  ok   gotwampd still answers"

echo "3: original file, SIGHUP"
cat "$orig" >"$cfg"
"${compose[@]}" kill -s HUP sender >/dev/null 2>&1
sleep 3
has r2-light-v6 && { echo "  FAIL r2-light-v6 still there"; failures=$((failures + 1)); } || echo "  ok   r2-light-v6 removed"
has api-r3 && echo "  ok   api-r3 kept" || { echo "  FAIL api-r3 dropped by SIGHUP"; failures=$((failures + 1)); }
echo "  (waiting 25s for new windows)"
sleep 25
check "r2-light sent per window (100ms again)" 99 "$(window_sent r2-light)" 101
stale=$(metric gotwamp_sender_last_run_rtt_seconds 'session="r2-light-v6",stat="median"')
echo "  /metrics series of the removed session: ${stale:-none}"
finish
