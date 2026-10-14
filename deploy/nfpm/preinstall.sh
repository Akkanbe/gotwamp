#!/bin/sh
# Create the system user and group gotwampd runs as (SPEC §9.10).
set -e
if ! getent group gotwamp >/dev/null 2>&1; then
	groupadd --system gotwamp
fi
if ! getent passwd gotwamp >/dev/null 2>&1; then
	useradd --system --gid gotwamp --home-dir /var/lib/gotwamp --no-create-home \
		--shell /usr/sbin/nologin --comment "gotwamp TWAMP daemon" gotwamp
fi
