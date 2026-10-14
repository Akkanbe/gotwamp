#!/bin/sh
# Stop the service before its files go away. On deb upgrades ($1 = upgrade)
# and rpm upgrades ($1 >= 1) keep it running; postinstall restarts it.
set -e
case "$1" in
upgrade | 1 | 2) exit 0 ;;
esac
if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
	systemctl disable --now gotwampd >/dev/null 2>&1 || true
fi
