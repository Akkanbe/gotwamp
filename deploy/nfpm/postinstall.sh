#!/bin/sh
# Make systemd see the unit. The service is not enabled or started here:
# review /etc/gotwamp/gotwampd.yaml first, then
#   systemctl enable --now gotwampd
set -e
if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
	systemctl daemon-reload || true
	# Restart on upgrade if it was running.
	if systemctl is-active --quiet gotwampd; then
		systemctl try-restart gotwampd || true
	fi
fi
