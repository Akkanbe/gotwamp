#!/usr/bin/env bash
# Interoperability between the gotwamp binaries and perfSONAR (PLAN §8.3):
#   I-02  perfSONAR twping  -> gotwampd (TWAMP-Control server + Full reflector)
#   I-01  gotwamp ping      -> perfSONAR twampd (TWAMP Full)
#   I-06  twping -A A|E|M   -> gotwampd, with the keys of conf/twamp-server-test-only.pfs
#   I-05  gotwamp ping --security authenticated|encrypted|mixed -> twampd
# over IPv4 and IPv6 loopback. The perfSONAR container from compose.yaml must
# be running (make interop starts it). Exits non-zero on the first failure.
#
# Environment:
#   GOTWAMPD, GOTWAMP      binaries to run (default: built from ./cmd into a
#                          scratch directory, so that the daemon is the process
#                          the script stops)
#   PERFSONAR_CONTAINER    container name (default gotwamp-perfsonar)
#   PERFSONAR_TWAMPD_PORT  twampd control port (default 862)
#   GOTWAMP_CLI_PORT       gotwampd control port (default 18621)
#   COUNT                  packets per case (default 100)
#   CASES                  cases to run (default "I-01 I-02 I-05 I-06")
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/../../.." && pwd)"
cd "$root"

container="${PERFSONAR_CONTAINER:-gotwamp-perfsonar}"
twampd_port="${PERFSONAR_TWAMPD_PORT:-862}"
port="${GOTWAMP_CLI_PORT:-18621}"
count="${COUNT:-100}"
cases="${CASES:-I-01 I-02 I-05 I-06}"
# The identity of conf/twamp-server-test-only.pfs (TEST ONLY keys), which
# twampd, twping and gotwampd all read; the container sees the file at
# $pfs_container.
identity=gotwamp
pfs_container=/etc/gotwamp-interop/twamp-server.pfs

work="$(mktemp -d)"
daemon_pid=""
cleanup() {
	if [[ -n "$daemon_pid" ]]; then
		kill "$daemon_pid" 2>/dev/null || true
		wait "$daemon_pid" 2>/dev/null || true
	fi
	rm -rf "$work"
}
trap cleanup EXIT

if [[ -z "${GOTWAMPD:-}" || -z "${GOTWAMP:-}" ]]; then
	CGO_ENABLED=0 go build -o "$work/bin/" ./cmd/gotwampd ./cmd/gotwamp
fi
GOTWAMPD="${GOTWAMPD:-$work/bin/gotwampd}"
GOTWAMP="${GOTWAMP:-$work/bin/gotwamp}"

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

if [[ "$(docker inspect -f '{{.State.Running}}' "$container" 2>/dev/null)" != "true" ]]; then
	fail "perfSONAR container $container is not running (make interop starts it)"
fi

# gotwampd refuses a keys file others may read.
install -m 600 "$here/conf/twamp-server-test-only.pfs" "$work/keys.pfs"
sed -e "s|@WORK@|$work|g" -e "s|@PORT@|$port|g" -e "s|@LIGHTPORT@|$((port + 1))|g" \
	"$here/gotwampd.yaml.in" >"$work/gotwampd.yaml"
$GOTWAMP config validate -f "$work/gotwampd.yaml"

$GOTWAMPD --config "$work/gotwampd.yaml" >"$work/gotwampd.log" 2>&1 &
daemon_pid=$!
for _ in $(seq 1 100); do
	[[ -S "$work/gotwampd.sock" ]] && break
	kill -0 "$daemon_pid" 2>/dev/null || { cat "$work/gotwampd.log" >&2; fail "gotwampd exited"; }
	sleep 0.2
done
[[ -S "$work/gotwampd.sock" ]] || fail "gotwampd did not open its API socket"
export GOTWAMP_SOCKET="$work/gotwampd.sock"

# twping -M prints "KEY<TAB>VALUE" lines; field KEY of file FILE.
field() { awk -F'\t' -v k="$2" '$1 == k { print $2; exit }' "$1"; }

# ping_twampd [FLAGS...]: gotwamp ping to twampd at $addr with FLAGS; every
# one of the $count packets must come back.
ping_twampd() {
	local json="$work/ping.jsonl" rc=0
	$GOTWAMP ping --output jsonl -q -c "$count" -i 20ms -p "$twampd_port" "$@" "$addr" >"$json" 2>&1 || rc=$?
	((rc == 0)) || { cat "$json" >&2; fail "gotwamp ping $* exited with $rc"; }
	if command -v jq >/dev/null; then
		local summary sent lost
		summary="$(jq -c 'select(.summary != null) | .summary' "$json" | tail -n 1)"
		sent="$(jq -r '.sent // empty' <<<"$summary")" lost="$(jq -r '.lost // 0' <<<"$summary")"
		echo "   sent=$sent lost=$lost security=$(jq -r '.spec.security // empty' <<<"$summary") rtt median=$(jq -r '.rtt.median // empty' <<<"$summary")"
		[[ "$sent" == "$count" && "$lost" == "0" ]] || fail "gotwamp ping $* -> twampd: sent=$sent lost=$lost"
	fi
}

for addr in 127.0.0.1 ::1; do
	target="$addr"
	[[ "$addr" == *:* ]] && target="[$addr]"

	if [[ " $cases " == *" I-02 "* ]]; then
		echo "== I-02 twping -> gotwampd $target:$port"
		out="$work/twping.txt"
		rc=0
		docker exec "$container" twping -M -c "$count" -i 0.02 "$target:$port" >"$out" 2>&1 || rc=$?
		((rc == 0)) || { cat "$out" >&2; fail "twping exited with $rc"; }
		sent="$(field "$out" SENT)" lost="$(field "$out" LOST)"
		echo "   SENT=$sent LOST=$lost MEDIAN=$(field "$out" MEDIAN)s TTL fwd/bck=$(field "$out" MINTTL_FWD)/$(field "$out" MINTTL_BCK)"
		[[ "$sent" == "$count" && "$lost" == "0" ]] || fail "twping -> gotwampd: SENT=$sent LOST=$lost"
	fi

	if [[ " $cases " == *" I-06 "* ]]; then
		for mode in A E M; do
			echo "== I-06 twping -A $mode -> gotwampd $target:$port"
			out="$work/twping.txt"
			rc=0
			docker exec "$container" twping -M -c "$count" -i 0.02 -A "$mode" -u "$identity" -k "$pfs_container" "$target:$port" >"$out" 2>&1 || rc=$?
			((rc == 0)) || { cat "$out" >&2; fail "twping -A $mode exited with $rc"; }
			sent="$(field "$out" SENT)" lost="$(field "$out" LOST)"
			echo "   SENT=$sent LOST=$lost MEDIAN=$(field "$out" MEDIAN)s"
			[[ "$sent" == "$count" && "$lost" == "0" ]] || fail "twping -A $mode -> gotwampd: SENT=$sent LOST=$lost"
		done
	fi

	if [[ " $cases " == *" I-01 "* ]]; then
		echo "== I-01 gotwamp ping -> twampd $target:$twampd_port"
		ping_twampd
	fi
	if [[ " $cases " == *" I-05 "* ]]; then
		for security in authenticated encrypted mixed; do
			echo "== I-05 gotwamp ping --security $security -> twampd $target:$twampd_port"
			ping_twampd --security "$security" --key-id "$identity"
		done
	fi
done
echo "PASS: perfSONAR interoperability (CLI)"
