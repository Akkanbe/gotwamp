# Configuration Reference (`gotwampd.yaml`)

Every key and default value of the gotwampd configuration file, derived from the `internal/config` implementation (types, `Default()`, `Validate()`). For how it fits into the specification, see SPEC §9.2 / Appendix A.

- Default location: `/etc/gotwamp/gotwampd.yaml` (change it with `gotwampd --config`)
- If the file is missing or empty, every key takes its default and **only the reflector role** runs. If the file is missing, a file placed there later is loaded on reload (both for the default location and for a location given with `--config`)
- Keys you omit keep their defaults. **Unknown keys are an error** (reported with the line number)
- Validation errors are reported all at once, each with the key path (example: `sender.sessions[2].interval: 500us is shorter than 1ms`). Problems that do not prevent startup are reported as warnings
- Checking changes: `gotwamp config validate -f /etc/gotwamp/gotwampd.yaml`
- Reload: `systemctl reload gotwampd` (SIGHUP). Adding, changing, and removing monitoring sessions, `daemon.log.level`, and `sender.defaults` take effect immediately. Changes to keys that open sockets or files (`daemon.api` / `metrics` / `log.format` / `timestamping` / `clock` / `realtime` / `limits`, `sender.enabled`, `sender.keys_file`, all of `reflector`, `export`) do not take effect until restart. If the new configuration fails validation, the running configuration is kept and the error is logged (a failure through `gotwamp config reload` is logged as well)

## Value Formats

| Kind | Format | Example |
|------|--------|-----|
| Duration | Go duration syntax | `100ms`, `2s`, `15m`, `50us` |
| DSCP | A number 0–63, or a name: `ef` (46), `va` (44), `csN` (N=0–7), `afXY` (X=1–4, Y=1–3), `be` / `default` (0) | `46`, `ef`, `af41`, `cs5` |
| Size | Number + unit (`B`; `KiB`/`MiB`/`GiB` are powers of 1024, `KB`/`MB`/`GB` are powers of 1000) | `100MiB` |
| Port range | `first-last` (a single port is also allowed; 1 ≤ first ≤ last ≤ 65535) | `49152-57343` |
| Listen address | `IP:port`. IPv6 in square brackets. IPv4-mapped addresses are treated as IPv4 | `0.0.0.0:862`, `[::]:862` |
| ACL | A CIDR prefix or a single host address. An empty list allows everything | `192.0.2.0/24`, `2001:db8::/32`, `198.51.100.7` |

## Minimal Configuration

```yaml
version: 1
reflector:
  enabled: true      # true by default. This alone makes the Full / Light reflector listen on 862
```

Example using the sender role and monitoring sessions:

```yaml
version: 1
sender:
  enabled: true
  defaults:
    mode: full
    dscp: ef
  sessions:
    - name: tokyo-osaka
      target: 192.0.2.10
      schedule: { type: periodic, frequency: 1m }
      labels: { region: kanto }
    - name: core-light
      target: 198.51.100.20
      mode: light
      schedule: { type: continuous, window: 1m }
reflector:
  enabled: false
```

## Top Level

| Key | Default | Description |
|------|------|------|
| `version` | `1` | Schema version. Only 1 |
| `daemon` | — | Process-wide settings |
| `sender` | — | Sender role |
| `reflector` | — | Reflector role |
| `export` | — | Result export (JSON Lines) |

At least one of `sender.enabled` and `reflector.enabled` must be `true`.

## `daemon`

| Key | Default | Validation | Description |
|------|------|------|------|
| `api.socket` | `/run/gotwamp/gotwampd.sock` | Absolute path, at most 107 bytes | Unix domain socket of the gRPC API. The `gotwamp` CLI connects here (`--socket` / `GOTWAMP_SOCKET`) |
| `api.socket_mode` | `"0660"` | Octal permissions | Socket permissions |
| `api.socket_group` | `gotwamp` | — | Socket group. Add users of the CLI to this group. If empty, the process group is used. If the default `gotwamp` does not exist (or cannot be applied), a warning is logged and startup continues with the process group; if an explicitly set group does not exist, startup fails |
| `api.tcp.listen` | (empty) | IP address and port | Also expose the gRPC API over TCP (remote management, ADR 0010). Empty disables it. **Never exposed without mTLS** (the three keys below are required). The UDS keeps working as before |
| `api.tcp.ca` | (empty) | Absolute path. Required when `listen` is set | Certificate(s) of the CA that signed the client certificates (PEM, multiple allowed) |
| `api.tcp.cert` | (empty) | Absolute path. Required when `listen` is set | gotwampd server certificate (PEM, may include intermediate certificates) |
| `api.tcp.key` | (empty) | Absolute path. Required when `listen` is set. **Permissions 0600 or stricter** | Private key of the server certificate (PEM). Startup fails if it is readable by group or others |
| `api.tcp.allowed_names` | `[]` | Empty strings not allowed | Names of client certificates allowed to connect. A match on the Common Name, or on any SAN DNS name (case-insensitive), IP, URI, or email address is enough. If empty, every certificate signed by the CA is allowed (a warning is logged) |
| `metrics.listen` | `127.0.0.1:9862` | `host:port`, port 1–65535 | Prometheus `/metrics` and `/healthz`. Empty disables it |
| `metrics.rtt_buckets` | `[50us, 100us, 200us, 500us, 1ms, 2ms, 5ms, 10ms, 20ms, 50ms, 100ms, 200ms, 500ms, 1s]` | Positive and strictly increasing | Bucket boundaries of the delay histograms |
| `log.level` | `info` | `debug` / `info` / `warn` / `error` | Can be overridden with `--log-level`. When overridden, a reload does not revert to the file's value (a warning is logged) |
| `log.format` | `json` | `json` / `text` | Can be overridden with `--log-format` |
| `timestamping` | `auto` | `auto` / `hardware` / `kernel` / `userspace` | Receive timestamping mode. `auto` tries kernel timestamping and falls back to userspace if unavailable. `hardware` uses NIC hardware timestamping and falls back to kernel if the NIC does not support it (`auto` does not try hardware). Assumes the PHC is synchronized to CLOCK_REALTIME ([troubleshooting.md](troubleshooting.md) §3) (SPEC §8.1) |
| `hardware_interface` | (empty) | Interface name | With `timestamping: hardware`, the NIC used to timestamp sockets bound to a wildcard address (`0.0.0.0` / `[::]`). If empty, sockets bound to a specific address use the interface owning that address, the Light sender uses the interface on the route to the destination, and wildcard reflector listeners fall back to kernel timestamping |
| `clock.sync` | `auto` | `auto` / `synced` / `unsynced` | Clock synchronization state. `auto` follows the result of adjtimex. Override it in environments where the state of chrony or similar is not reflected in the kernel |
| `clock.max_error` | `0s` | 0 or greater | Maximum clock error. 0 uses the adjtimex maxerror |
| `clock.userspace_error` | `50us` | 0 or greater | Method error of userspace timestamps. Added to the Error Estimate (SPEC §8.4) |
| `clock.kernel_error` | `5us` | 0 or greater | Method error of kernel timestamps. Added to the Error Estimate (SPEC §8.4) |
| `clock.hardware_error` | `1us` | 0 or greater | Method error of hardware timestamps. Added to the Error Estimate (SPEC §8.4). The reflector takes T3 in userspace, so even with `hardware` it uses the larger of this and `userspace_error` |
| `realtime.lock_os_thread` | `false` | — | Pin the send/receive goroutines to OS threads (SPEC §8.6) |
| `limits.max_samples_per_run` | `100000` | 1–10,000,000 | Maximum number of values kept per run / window for percentile calculation |
| `limits.result_history` | `1440` | 1–1,000,000 | Number of run / window summaries kept per monitoring session |
| `limits.max_concurrent_tests` | `16` | 1–1,024 | Number of ad hoc measurements (`RunTest`) that can run concurrently. Excess requests get `ResourceExhausted` |
| `limits.max_watchers` | `32` | 1–1,024 | Number of `WatchResults` (`gotwamp watch`) streams that can be open concurrently. Excess requests get `ResourceExhausted` |

### Remote Management (`daemon.api.tcp`)

Exposing the gRPC API over TCP lets you operate gotwampd with `gotwamp` from another host (ADR 0010). TLS 1.3 mutual authentication (mTLS) is required: connections without a client certificate, certificates signed by a different CA, and names not in `allowed_names` are rejected during the handshake, and `API client refused` is logged as a warning.
The full API is available, the same as over the UDS, so list in `allowed_names` only the parties you trust with management.

```yaml
daemon:
  api:
    tcp:
      listen: "192.0.2.10:9443"
      ca: /etc/gotwamp/tls/clients-ca.pem
      cert: /etc/gotwamp/tls/gotwampd.pem
      key: /etc/gotwamp/tls/gotwampd-key.pem   # chmod 600
      allowed_names: [noc-admin, ops.example.net]
```

#### Creating Certificates (Example: openssl)

If you have an internal CA, use it. Otherwise, create a small CA for management and sign the gotwampd server certificate and the client certificates with it. Put the host name or IP address that the CLI passes to `--server` in the SAN of the server certificate.

```sh
# Management CA (keep the private key in a safe place)
openssl req -x509 -new -nodes -newkey ec -pkeyopt ec_paramgen_curve:P-256 -days 3650 \
  -subj "/CN=gotwamp admin CA" -keyout ca-key.pem -out ca.pem

# gotwampd server certificate (address and name in the SAN)
openssl req -new -nodes -newkey ec -pkeyopt ec_paramgen_curve:P-256 \
  -subj "/CN=gotwampd" -keyout gotwampd-key.pem -out gotwampd.csr
openssl x509 -req -in gotwampd.csr -CA ca.pem -CAkey ca-key.pem -CAcreateserial -days 825 \
  -extfile <(printf 'subjectAltName=IP:192.0.2.10,DNS:probe1.example.net\nextendedKeyUsage=serverAuth') -out gotwampd.pem

# Operator client certificate (put the CN in allowed_names)
openssl req -new -nodes -newkey ec -pkeyopt ec_paramgen_curve:P-256 \
  -subj "/CN=noc-admin" -keyout noc-admin-key.pem -out noc-admin.csr
openssl x509 -req -in noc-admin.csr -CA ca.pem -CAkey ca-key.pem -CAcreateserial -days 825 \
  -extfile <(printf 'extendedKeyUsage=clientAuth') -out noc-admin.pem

sudo install -o gotwamp -g gotwamp -m 600 gotwampd-key.pem /etc/gotwamp/tls/
sudo install -o root -g gotwamp -m 644 gotwampd.pem ca.pem /etc/gotwamp/tls/
```

- Set `daemon.api.tcp.ca` to **the CA that signed the client certificates** (the same `ca.pem` in the example above).
- Set the CLI's `--tls-ca` to **the CA that signed the gotwampd certificate**.
- There is no certificate revocation mechanism (CRL / OCSP). For a leaked client certificate, remove it from `allowed_names` and restart, or replace the whole CA.

On the CLI side, specify `--server` and the three TLS files (the environment variables `GOTWAMP_SERVER`, `GOTWAMP_TLS_CA`, `GOTWAMP_TLS_CERT`, and `GOTWAMP_TLS_KEY` also work). The gotwampd certificate is verified against the host name in `--server` (an IP SAN for an IP address); use `--tls-server-name` to verify against a different name.

```sh
gotwamp --server 192.0.2.10:9443 --tls-ca ca.pem --tls-cert noc-admin.pem --tls-key noc-admin-key.pem status
```

Certificate replacement takes effect on restart (changes to `daemon.api` do not take effect on reload).

## `sender`

| Key | Default | Description |
|------|------|------|
| `enabled` | `false` | Enable the sender role (ad hoc measurement with `gotwamp ping` and monitoring sessions) |
| `keys_file` | (empty) | File holding the shared secret per KeyID for Authenticated / Encrypted / Mixed modes (perfSONAR pfs format: one `identity hex-passphrase` per line, `#` starts a comment). **Permissions 0600 or stricter**; startup fails if it does not exist. Read only once at startup (changes take effect on restart). Startup fails if a `key_id` in a session or in `defaults` is not in this file |
| `defaults` | See table below | Test parameters inherited by every session and by ad hoc measurements |
| `sessions` | `[]` | List of monitoring sessions |

### Keys File (`sender.keys_file` / `reflector.control.keys_file`)

Shared secrets for the Authenticated / Encrypted / Mixed modes (RFC 4656 §3.1, RFC 5618) go in a file in perfSONAR pfs format (ADR 0011). The file used for twampd's `twamp-server.pfs` or passed to twping's `-k` works as is.

```text
# identity (KeyID)  passphrase encoded in hex
noc-sender          73656372657470617373
```

- **Format**: one identity per line. An identity is UTF-8 of at most 80 octets (the KeyID of RFC 4656). The passphrase is written in hex, so it can contain any characters.
- **Comments and blank lines**: everything from `#` to the end of the line, and blank lines, are ignored.
- **Load errors**: duplicate identities, non-hex passphrases, and empty passphrases are errors reported with the line number. The passphrase is never included in errors.
- **Permissions**: readable only by the gotwampd user (`gotwamp`). Startup fails if permissions are broader than 0600.

Example of creating one (enter the passphrase with `read -s` so it is not left in the shell history):

```sh
read -rs PASS && printf 'noc-sender %s\n' "$(printf %s "$PASS" | od -An -tx1 | tr -d ' \n')" | sudo tee -a /etc/gotwamp/keys.pfs >/dev/null
sudo chown gotwamp:gotwamp /etc/gotwamp/keys.pfs && sudo chmod 600 /etc/gotwamp/keys.pfs
```

Files written by perfSONAR's `pfstore` use the same format.

- **Sender side** (`sender.keys_file`): the session's `key_id` selects the identity. gotwampd picks the mode given in `security` from among the modes the peer offers.
- **Reflector side** (`reflector.control.keys_file` and `modes`): selects the modes to offer. If the KeyID sent by the Control-Client is unknown, the passphrase is wrong (the Challenge in the Token does not match), or a Client-IV is reused, the server answers with Server-Start Accept 1, disconnects, and logs the reason.
- **Updating keys**: both files are read only once at startup. Restart after changing keys.

### Test Parameters (`sender.defaults` and Every Session)

| Key | Default | Validation | Description | Compatibility knob |
|------|------|------|------|-----------|
| `mode` | `full` | `full` / `light` | TWAMP Full (negotiated over TWAMP-Control) or Light | |
| `control_port` | `862` | 1–65535 | Full control port | |
| `receiver_port` | `0` | 0–65535 | Reflector UDP port. 0 requests the same value as the Sender Port for Full, and means 862 for Light | CMP-03 |
| `sender_port` | `0` | 0–65535 | Fixed source UDP port. 0 picks one from `sender_port_range` | CMP-14 |
| `sender_port_range` | `49152-57343` | Port range | Range from which the source port is picked | CMP-21 |
| `count` | `100` | 1–1,000,000 (not checked for continuous) | Number of packets per run | |
| `interval` | `100ms` | 1ms or more | Send interval | |
| `timeout` | `2s` | Positive | Loss timeout. For Full, also used as the Timeout in Request-TW-Session | |
| `padding` | `27` | 0–9000 (warning above 1458: fragmentation at IPv4 MTU 1500) | Number of Packet Padding octets. 27 makes both directions the same length | |
| `padding_fill` | `auto` | `auto` / `random` / `zero` | Padding content. `auto` is random for Full and all zeros for Light | CMP-07 |
| `dscp` | `0` | DSCP | DSCP of test packets. For Full, also conveyed to the reflector as Type-P | |
| `ttl` | `255` | 1–255 | Send TTL / Hop Limit | CMP-06 |
| `control_timeout` | `15s` | Positive | Wait time for each stage of TWAMP-Control | CMP-11 |
| `control_dscp` | `0` | DSCP | DSCP of the control connection | CMP-16 |
| `end_delay` | `0s` | 0 or greater | Wait between the final packet being settled and Stop-Sessions (twping uses 1s) | CMP-18 |
| `zero_addresses` | `false` | — | Set the Sender/Receiver Address in Request-TW-Session to 0 (NAT traversal) | CMP-01 |
| `start_time` | `zero` | `zero` / `now` | Start Time in Request-TW-Session. `zero` means start immediately | CMP-02 |
| `accept_alternate_port` | `true` | — | Accept an alternate Receiver Port returned by the Server | CMP-04 |
| `tolerate_zero_multiplier` | `true` | — | Accept replies with Error Estimate Multiplier=0 as "error unknown" | CMP-05 |
| `tx_timestamp_wait` | `1ms` | 0 or greater | Maximum time to wait for a kernel TX timestamp (SPEC §8.2) | |
| `reflector_sequence` | `stateful` | `stateful` / `copy_sender` | How the peer reflector assigns Sequence Numbers. With `copy_sender` (a stateless reflector that copies the Sender's value), per-direction loss and reverse-path reordering are not computed (SPEC §7.2) | CMP-10 |
| `dscp_ecn_monitoring` | `false` | `light` only | Read the DSCP / ECN received by the peer reflector from the RFC 7750 reply. The reflector side must also enable it (`reflector.light.dscp_ecn_monitoring` for gotwampd) | |
| `security` | `unauthenticated` | `unauthenticated` / `authenticated` / `encrypted` / `mixed` | TWAMP-Control mode (Full only). Anything other than unauthenticated requires `key_id` and `sender.keys_file`. Fails with `no common mode` if the peer does not offer the mode. TWAMP Light supports unauthenticated only: a value inherited from `defaults` is ignored, and setting it on the session itself is an error | |
| `key_id` | (empty) | UTF-8, at most 80 octets. Required for anything other than unauthenticated | KeyID in `sender.keys_file`. For anything other than unauthenticated, `sender.keys_file` is also required | |
| `extensions` | `[]` | `individual_session_control` / `reflect_octets` / `symmetrical_size`, no duplicates. Full only | TWAMP extensions to use if the peer offers them. Individual Session Control (RFC 5938) starts and stops each SID individually. Symmetrical Size (RFC 6038) makes sender packets the same length as replies (27 MBZ octets, plus 64 in authenticated modes) | |
| `octets_to_reflect` / `padding_to_reflect` | `0` / `0` | 0–65535. Full only | Request fields of Reflect Octets (RFC 6038 §4.2). `octets_to_reflect` is a 2-octet value the server returns unchanged in Accept-Session (Octets to be reflected; if it is not returned, the request fails). `padding_to_reflect` is the length of padding the reflector returns unchanged (Length of padding to reflect; replies that do not return it are counted in `reflected_mismatch`). If either is nonzero, `reflect_octets` is requested. `padding` must be longer than `padding_to_reflect` | |
| `stamp` | `false` | Light only. Cannot be combined with `dscp_ecn_monitoring`. Padding must be 0 or at least 4 | Send STAMP (RFC 8762) Session-Sender packets (44-octet base, padding as an Extra Padding TLV) | |
| `ssid` | `0` | 0–65535. Requires `stamp` | STAMP Session-Sender Identifier (RFC 8972). 0 means none | |

Full-only keys (`security`, `extensions`, `octets_to_reflect`, `padding_to_reflect`) and Light-only keys (`stamp`, `ssid`) are ignored in the other mode when inherited from `defaults`, and are an error when set on the session itself.


### Monitoring Sessions (`sender.sessions[]`)

In addition to the test parameters, each session has the following keys. Test parameters you omit are inherited from `sender.defaults`.

| Key | Default | Validation | Description |
|------|------|------|------|
| `name` | (required) | `^[a-z0-9][a-z0-9_-]{0,62}$`, unique | Session name. The `session` label in metrics |
| `target` | (required) | Not empty | Host name or IP address of the reflector |
| `ipv` | `auto` | `auto` / `4` / `6` | Address family |
| `source` | (empty) | IP address | Source address |
| `schedule.type` | `periodic` | `periodic` / `continuous` | Run periodically, or send continuously and aggregate per window |
| `schedule.frequency` | — | Positive for periodic. At least `count × interval + timeout + 1s` | Interval between runs |
| `schedule.jitter` | `0s` | 0 or greater | Maximum random offset of the run start time |
| `schedule.window` | — | Positive for continuous. At least 10 times `interval` | Aggregation boundary. The packet count is `window / interval` |
| `labels` | `{}` | Up to 8. Names match `^[a-zA-Z_][a-zA-Z0-9_]*$`. `session` / `target` / `mode` / `direction` / `stat` / `kind` / `result` / `stage` / `le` are reserved. Values are UTF-8 of at most 256 octets | User labels attached to metrics |

## `reflector`

| Key | Default | Validation | Description |
|------|------|------|------|
| `enabled` | `true` | — | Enable the reflector role (TWAMP-Control server, Full / Light reflector) |
| `ttl` | `255` | 1–255 | TTL / Hop Limit of reflected packets |
| `max_packet_size` | `9000` | 14–65507 | UDP payloads larger than this are dropped (`malformed`) |

### `reflector.control` (TWAMP Full: Server and Session-Reflector)

| Key | Default | Validation | Description | Compatibility knob |
|------|------|------|------|-----------|
| `listen` | `["0.0.0.0:862", "[::]:862"]` | Listen addresses, no duplicates | TCP listeners for TWAMP-Control | |
| `acl` | `[]` (allow all) | ACL | Allowed Control-Clients. Other connections get a Server Greeting with Modes=0 and are closed | |
| `max_connections` | `64` | 1–1,000,000 | Concurrent control connections. Excess connections are rejected with Modes=0 | |
| `max_sessions_per_connection` | `16` | 1–1,000,000 | Sessions per connection. Excess gets Accept=4 | |
| `max_sessions` | `1024` | 1–1,000,000 | Sessions across the whole daemon. Excess gets Accept=5 | |
| `servwait` | `900s` | Positive | Time before closing a control connection that is idle outside of sessions (RFC 5357 SERVWAIT) | |
| `refwait` | `900s` | Positive | Time before releasing a session that receives no test packets (REFWAIT). Also caps how long reflection continues after Stop-Sessions / disconnect (the client-specified Timeout) | |
| `partial_message_timeout` | `60s` | Positive | Time to wait for the rest of a partially received control message | |
| `test_port_range` | `20000-29999` | Port range | Range from which an alternate port is picked when the requested port is unavailable | |
| `honor_requested_port` | `true` | — | Use the requested Receiver Port if it is free (even outside the range). Ports below 1024 are honored only for 862 and ports within `test_port_range` | CMP-17 |
| `verify_sender` | `address` | `address` / `address_port` / `none` | Source validation of test packets. `none` locks to the first source (for PAT environments) | CMP-09 |
| `max_padding` | `9000` | 1–65507 | Maximum accepted Padding Length. Excess gets Accept=3 | |
| `pbkdf2_count` | `2048` | Power of 2 in 1024–32768 | Count in the Server Greeting (PBKDF2 iteration count for authenticated modes) | |
| `keys_file` | (empty) | **Permissions 0600 or stricter**, must exist | File holding the Control-Client shared secret per KeyID (compatible with perfSONAR's `twampd.pfs`). Read at startup | |
| `extensions` | All three | `individual_session_control` / `reflect_octets` / `symmetrical_size`, no duplicates | TWAMP extensions to offer (RFC 5938, RFC 6038). The client chooses whether to use them, so offering all of them does no harm. `[]` offers none | |
| `modes` | (empty) | A list of `unauthenticated` / `authenticated` / `encrypted` / `mixed`, no duplicates. Anything other than unauthenticated requires `keys_file` | Security modes to offer. If empty: unauthenticated only without `keys_file`, all four with it (equivalent to twampd's default AEMO). To require authentication, write e.g. `[authenticated, encrypted]` | |

### `reflector.light` (TWAMP Light Reflector)

| Key | Default | Validation | Description | Compatibility knob |
|------|------|------|------|-----------|
| `listeners[].listen` | `0.0.0.0:862`, `[::]:862` | Listen addresses, no duplicates | UDP listeners. Ports such as 64364–64373 can be added for Nokia SR OS compatibility. **On hosts with multiple addresses, list the specific addresses** ([troubleshooting.md](troubleshooting.md)) | CMP-22 |
| `acl` | `[]` (allow all) | ACL | Allowed sources. Other packets are dropped (`acl`) | |
| `sequence` | `per_flow` | `per_flow` / `copy_sender` | Count the Reflector Sequence Number per flow, or copy the Sender's value (stateless) | CMP-10 |
| `flow_timeout` | `900s` | Positive | Time before discarding the state of an idle flow | |
| `max_flows` | `65536` | 1–100,000,000 | Maximum number of stateful flows. New flows beyond this are stateless (copy the Sender's value) | |
| `rate_limit_pps` | `0` (unlimited) | 0–100,000,000 | Token bucket per source address (burst of 1 second). Excess packets are dropped (`rate_limit`) | |
| `dscp_policy` | `copy` | `copy` / `fixed` | DSCP of reflected packets. `copy` uses the received value (ECN set to Not-ECT), `fixed` uses `dscp_fixed` | CMP-08 |
| `dscp_fixed` | `0` | DSCP | DSCP when `dscp_policy: fixed` | |
| `dscp_ecn_monitoring` | `false` | — | Reply in the RFC 7750 format (44-octet header) and write the received DSCP / ECN at offset 41. For Light this is determined by configuration (RFC 7750 §2.2.3), so configure the Sender side the same way | |
| `stamp` | `false` | Cannot be combined with `dscp_ecn_monitoring` | Reply in the STAMP (RFC 8762) format: copy the SSID (RFC 8972), set offsets 41–43 to MBZ, and answer requests shorter than 44 octets with 44 octets. TLVs are returned with the U flag set for those not understood and the M flag set for those with an invalid length. Data is treated as TLVs only when the byte right after the base is `100xxxxx`, so TWAMP Light senders are still answered as before | |

## `export.jsonl`

| Key | Default | Validation | Description |
|------|------|------|------|
| `enabled` | `false` | — | Export results to JSON Lines |
| `path` | `/var/lib/gotwamp/results.jsonl` | Required when `enabled` | Output file |
| `records` | `[run_summary, window_summary, run_error]` | `run_summary` / `window_summary` / `run_error` / `packet` | Record types to export. Adding `packet` also exports per-packet records |
| `rotate.max_size` | `100MiB` | Size | Size at which the file is rotated |
| `rotate.max_files` | `10` | 0–1,000,000 | Number of rotated files to keep |
| `rotate.daily` | `false` | — | Also rotate on the first record after the date changes (combined with the size limit) |

## `store`

The result store (run / window summaries and run_error). The default is in memory, which is lost on restart. With `sqlite`, summaries are kept in a SQLite file and remain readable with `gotwamp stats` after a restart (per-packet records are kept in memory only).

| Key | Default | Validation | Description |
|------|------|------|------|
| `backend` | `memory` | `memory` / `sqlite` | Result store type |
| `sqlite.path` | `/var/lib/gotwamp/results.db` | Absolute path; the parent directory must exist | DB file (under the systemd `StateDirectory`). `-wal` / `-shm` are created in the same place |
| `sqlite.retention` | `720h` | 0 or greater. 0 means forever | How long results are kept. Old entries are deleted at startup and every hour |
| `sqlite.max_size` | `0` | Size. 0 disables the warning | A warning is logged when the DB plus WAL exceeds this (a hint to shorten `retention`) |
| `sqlite.on_corrupt` | `reset` | `reset` / `fail` | Handling of a corrupt DB. `reset` moves it aside to `<path>.corrupt-<UTC time>` and starts with an empty DB (Warn). `fail` does not start (exit code 2). A DB with a schema from a newer version does not start in either case |

- Right after a restart, the most recent `daemon.limits.result_history` entries per session are loaded from the DB into memory.
- `gotwamp stats NAME --since 24h` (`since` in the API's `GetSessionStats`) goes back as far as `retention` in the DB with sqlite. With memory, it covers only the `result_history` entries of history. `--since` alone returns everything in the range; `--last N` narrows it to the most recent N entries. Without `--since`, the in-memory history is used (the most recent 10 entries by default).
- Writes are asynchronous; entries that overflow are counted in `gotwamp_store_results_dropped_total`. On shutdown, the queue is flushed before closing.
- The `store` line of `gotwamp status` shows the backend, the path, and the number of entries written.
- **Capacity estimate**: about **0.6KB** per summary / run_error entry (including the row, the compressed payload, and indexes; in a soak test of 100 sessions × 10pps, 3,000 entries took 1.7MiB).
  - Capacity ≈ number of sessions × (86,400 ÷ `frequency` or `window` in seconds) × days of retention × 0.6KB
  - Example: 100 sessions every 60 seconds with the default `retention: 720h` (30 days) is about 4.3M entries ≈ **2.6GB**. With continuous `window: 10s`, it is 6 times that (about 15GB). 1 session every 10 seconds for 7 days is about 36MB
  - If you carry over a DB from schema version 1 (before compression), existing rows stay as JSON (about 4KB per entry) and are deleted once they pass the retention period. Space freed by deletion is reused within the file, but the file itself does not shrink (to shrink it, run `sqlite3 results.db VACUUM` while the daemon is stopped)
  - Shorten `retention` to fit your disk, and set `max_size` to get a warning
- Result rows (`results.data`) are compressed, so their contents cannot be read directly with `sqlite3` (the format is in the `payload_formats` table). Use `gotwamp stats` or the API to read them

## Configuration Files Shipped with the Distribution

| File | Purpose |
|----------|------|
| `deploy/config/gotwampd.yaml` | Installed by deb/rpm as `/etc/gotwamp/gotwampd.yaml` (not overwritten on upgrade). Metrics on loopback only |
| `deploy/docker/gotwampd.yaml` | Default for the container image. Exposes metrics on `0.0.0.0:9862` |
| `test/interop/perfsonar/gotwampd.yaml.in` | For the interoperability scripts |
