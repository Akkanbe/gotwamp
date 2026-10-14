#!/bin/sh
# Forget the removed unit. The gotwamp user, /etc/gotwamp and
# /var/lib/gotwamp are kept, as their contents belong to the operator.
set -e
if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
	systemctl daemon-reload || true
fi
