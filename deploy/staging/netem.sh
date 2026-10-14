#!/usr/bin/env bash
# Impairs the traffic of a staging container with tc / netem, from the host
# (docker compose exec as root in the container; no sudo needed).
#
#   netem.sh SERVICE [--in|--out] [--peer PEER] NETEM-ARGS...
#   netem.sh SERVICE clear        remove all impairments of SERVICE
#   netem.sh SERVICE show         show the qdiscs and filters of SERVICE
#   netem.sh all clear|show       every gotwampd container
#
# Direction (default --out):
#   --out  the packets SERVICE sends (egress of eth0)
#   --in   the packets SERVICE receives (ingress of eth0, redirected to ifb0)
# For a measurement from the sender to reflectorN:
#   forward path  netem.sh reflectorN --in ...     (the test packets)
#   reverse path  netem.sh reflectorN --out ...    (the replies)
#                 netem.sh sender --in --peer reflectorN ...
# Delay on the sender's --out does not show in TWAMP: the sender's kernel
# transmit timestamp (T1) is taken after the qdisc, so the delay is spent
# before T1 (loss and duplication there do show).
# --peer PEER limits the impairment to packets to (--out) or from (--in)
# PEER's IPv4 and IPv6 addresses.
#
# NETEM-ARGS are those of tc-netem(8), combinable in one call:
#   delay 10ms 2ms [distribution normal]   delay with jitter (jitter reorders)
#   loss 1% | loss 0.3% 25%                random or correlated loss
#   reorder 5% [50%]                       reordering (needs a delay)
#   duplicate 1% | corrupt 0.1%
#   rate 10mbit                            bandwidth
#   e.g. netem.sh reflector2 delay 20ms 5ms loss 2% rate 50mbit
# A new call replaces the impairment of that service and direction.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
compose=(docker compose -f "$here/docker-compose.yml")
services=(sender reflector1 reflector2 reflector3)

usage() {
	sed -n '2,33p' "$0" | sed 's/^# \{0,1\}//' >&2
	exit 2
}

# run_in SERVICE CMD...: run CMD in SERVICE as root.
run_in() {
	local svc=$1
	shift
	"${compose[@]}" exec -T -u root "$svc" "$@"
}

clear_out() {
	run_in "$1" tc qdisc del dev eth0 root 2>/dev/null || true
}

clear_in() {
	run_in "$1" sh -c 'tc qdisc del dev eth0 ingress 2>/dev/null; ip link del ifb0 2>/dev/null; true'
}

show() {
	echo "== $1 out (eth0 egress)"
	run_in "$1" tc -s qdisc show dev eth0 root
	run_in "$1" tc filter show dev eth0 2>/dev/null | sed 's/^/   /'
	if run_in "$1" ip link show ifb0 >/dev/null 2>&1; then
		echo "== $1 in (eth0 ingress -> ifb0)"
		run_in "$1" tc -s qdisc show dev ifb0
		run_in "$1" tc filter show dev eth0 ingress 2>/dev/null | sed 's/^/   /'
	fi
}

# address SERVICE PEER FAMILY: PEER's address as SERVICE resolves it.
address() {
	run_in "$1" getent "ahostsv$3" "$2" | awk 'NR == 1 { print $1 }'
}

(($# >= 2)) || usage
svc=$1
shift
if [[ $svc == all ]]; then
	[[ $1 == clear || $1 == show ]] || usage
	for s in "${services[@]}"; do
		if [[ $1 == clear ]]; then
			clear_out "$s"
			clear_in "$s"
		else
			show "$s"
		fi
	done
	exit 0
fi

dir=out
peer=
while (($# > 0)); do
	case "$1" in
	clear)
		clear_out "$svc"
		clear_in "$svc"
		echo "$svc: impairments cleared"
		exit 0
		;;
	show)
		show "$svc"
		exit 0
		;;
	--in) dir=in ;;
	--out) dir=out ;;
	--peer)
		(($# >= 2)) || usage
		peer=$2
		shift
		;;
	-*) usage ;;
	*) break ;;
	esac
	shift
done
(($# > 0)) || usage

v4= v6=
if [[ -n $peer ]]; then
	v4=$(address "$svc" "$peer" 4)
	v6=$(address "$svc" "$peer" 6)
	[[ -n $v4 || -n $v6 ]] || {
		echo "netem.sh: $svc cannot resolve $peer" >&2
		exit 1
	}
fi

if [[ $dir == out ]]; then
	clear_out "$svc"
	if [[ -z $peer ]]; then
		run_in "$svc" tc qdisc add dev eth0 root netem "$@"
	else
		# prio sends everything to bands 1 and 2 by its priomap; band 3
		# gets what the filters pick, and netem impairs band 3 alone.
		run_in "$svc" tc qdisc add dev eth0 root handle 1: prio
		run_in "$svc" tc qdisc add dev eth0 parent 1:3 handle 30: netem "$@"
		[[ -z $v4 ]] || run_in "$svc" tc filter add dev eth0 parent 1: protocol ip prio 1 u32 match ip dst "$v4/32" flowid 1:3
		[[ -z $v6 ]] || run_in "$svc" tc filter add dev eth0 parent 1: protocol ipv6 prio 2 u32 match ip6 dst "$v6/128" flowid 1:3
	fi
else
	# The ingress qdisc cannot delay; redirect the packets to ifb0 and
	# impair them on its egress, after which ifb0 hands them back to eth0.
	clear_in "$svc"
	run_in "$svc" ip link add ifb0 type ifb
	run_in "$svc" ip link set ifb0 up
	run_in "$svc" tc qdisc add dev ifb0 root netem "$@"
	run_in "$svc" tc qdisc add dev eth0 handle ffff: ingress
	redirect=(action mirred egress redirect dev ifb0)
	if [[ -z $peer ]]; then
		run_in "$svc" tc filter add dev eth0 parent ffff: matchall "${redirect[@]}"
	else
		[[ -z $v4 ]] || run_in "$svc" tc filter add dev eth0 parent ffff: protocol ip prio 1 u32 match ip src "$v4/32" "${redirect[@]}"
		[[ -z $v6 ]] || run_in "$svc" tc filter add dev eth0 parent ffff: protocol ipv6 prio 2 u32 match ip6 src "$v6/128" "${redirect[@]}"
	fi
fi
echo "$svc --$dir${peer:+ --peer $peer ($v4 $v6)}: netem $*"
