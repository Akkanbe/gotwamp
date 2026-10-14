#!/bin/sh
# Entrypoint of the staging image: gotwampd refuses a keys file that others
# may read, and a bind-mounted file keeps the host's mode, so a keys file
# mounted at /etc/gotwamp/keys.pfs is copied with mode 0600 to
# /run/gotwamp-keys/keys.pfs first, outside the socket directory /run/gotwamp
# that the host sees. The configuration is /etc/gotwamp/gotwampd.yaml.
set -e
mkdir -p /run/gotwamp /run/gotwamp-keys
if [ -f /etc/gotwamp/keys.pfs ]; then
	install -m 600 /etc/gotwamp/keys.pfs /run/gotwamp-keys/keys.pfs
fi
exec /usr/bin/gotwampd --config /etc/gotwamp/gotwampd.yaml "$@"
