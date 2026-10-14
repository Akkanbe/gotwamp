#!/usr/bin/env bash
# Mode coverage of every reflector: Full and Light over IPv4 and IPv6, the
# TWAMP-Control security modes, STAMP, Individual Session Control (RFC 5938)
# and the RFC 6038 features, each as an ad-hoc measurement of 50 packets.
# A combination a reflector is not set up for must be refused.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
"$netem" all clear >/dev/null

echo "reflector1: unauthenticated Full, Light; no keys, extensions: [], no STAMP"
for ipv in 4 6; do
	attempt ok "full v$ipv unauthenticated" reflector1 -$ipv
	attempt ok "light v$ipv" reflector1 -$ipv --light
done
for mode in auth encrypted mixed; do
	attempt reject "full v4 $mode" reflector1 --security "$mode" --key-id staging
done
attempt ok "full v4 --isc (not offered: plain session)" reflector1 --isc
attempt ok "full v4 --symmetrical (not offered)" reflector1 --symmetrical -s 64
attempt reject "full v4 --reflect-octets (not offered)" reflector1 --reflect-octets 0xbeef
attempt reject "full v4 --padding-to-reflect (not offered)" reflector1 --padding-to-reflect 16 -s 64
attempt ok "stamp v4 (plain Light reflector)" reflector1 --light --stamp --ssid 9

echo "reflector2: authenticated, encrypted, mixed only"
for ipv in 4 6; do
	for mode in auth encrypted mixed; do
		attempt ok "full v$ipv $mode" reflector2 -$ipv --security "$mode" --key-id staging -s 64
	done
	attempt reject "full v$ipv unauthenticated" reflector2 -$ipv
	attempt ok "light v$ipv" reflector2 -$ipv --light
done
attempt reject "full v4 encrypted, unknown KeyID" reflector2 --security encrypted --key-id nosuch
attempt ok "full v4 encrypted + isc (not offered)" reflector2 --security encrypted --key-id staging --isc -s 64

echo "reflector3: Individual Session Control, Reflect Octets, Symmetrical Size, STAMP"
for ipv in 4 6; do
	attempt ok "full v$ipv unauthenticated" reflector3 -$ipv
	attempt ok "full v$ipv --isc" reflector3 -$ipv --isc
	attempt ok "full v$ipv --symmetrical -s 64" reflector3 -$ipv --symmetrical -s 64
	attempt ok "full v$ipv --reflect-octets 0xbeef" reflector3 -$ipv --reflect-octets 0xbeef
	attempt ok "full v$ipv --padding-to-reflect 16 -s 64" reflector3 -$ipv --padding-to-reflect 16 -s 64
	attempt ok "full v$ipv all extensions" reflector3 -$ipv --isc --symmetrical --reflect-octets 1 --padding-to-reflect 32 -s 128
	attempt ok "light v$ipv" reflector3 -$ipv --light
	attempt ok "stamp v$ipv" reflector3 -$ipv --light --stamp
	attempt ok "stamp v$ipv --ssid 65535" reflector3 -$ipv --light --stamp --ssid 65535
done
attempt reject "full v4 auth (no keys)" reflector3 --security auth --key-id staging
attempt reject "full v4 --padding-to-reflect > padding" reflector3 --padding-to-reflect 64 -s 32
finish
