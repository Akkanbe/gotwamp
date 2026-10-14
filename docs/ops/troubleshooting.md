# Operational Notes and Troubleshooting

This document covers what to watch for when running gotwampd on a public network (its exposure) and how to narrow down common symptoms. For configuration details see [config.md](config.md); for metrics see [metrics.md](metrics.md).

## 1. Reflector exposure

A reflector sends a reply to every packet it accepts. Understand the design limits and the recommended settings before exposing one.

### Amplification factor

| Mode | Received | Reply | Maximum amplification |
|--------|------|------|-----------|
| Light | UDP payload of 14 octets or more | `max(received length, 41)` octets | 41 / 14 ≈ **2.9x** (only for 14-40 octet packets; 1x at 41 or more) |
| Full | Same as above | Same as above (replies only to the source of a negotiated session) | Same as above, but replies go only to the Sender negotiated over TWAMP-Control (`verify_sender`) |
| Light (`stamp: true`) | 14 octets or more | `max(received length, 44)` octets | 44 / 14 ≈ **3.1x** (only for 14-43 octet packets) |
| Full (Authenticated / Encrypted) | 48 octets or more, with a valid HMAC | `max(received length, 112)` octets | 112 / 48 ≈ 2.3x. Only packets from a Sender holding the key pass the HMAC check, so it cannot be abused with spoofed sources |

- The reply is never longer than the received packet (it grows to 41 only when the received length is below 41). Padding is truncated from the end to keep the same length (RFC 5357 §4.2.1).
- A Light reflector cannot verify the source (UDP sources can be spoofed). **Do not expose a Light reflector with an allow-all ACL on an Internet-facing address.** It can be used as a reflection-attack amplifier that sends replies up to 2.9x larger to a spoofed source.

### Recommended settings

| Setting | Recommendation | Reason |
|------|------|------|
| `reflector.light.acl` | Allow only the prefixes of the measuring hosts | The default allows all. Stops reflection attacks and unrelated traffic (dropped as `acl`) |
| `reflector.light.rate_limit_pps` | A few times the expected measurement rate (e.g. 500 for a 100pps measurement) | Token bucket per source address. Excess is dropped as `rate_limit` |
| `reflector.control.acl` | Allow only the prefixes of the Control-Clients | Connections that do not match get a Greeting with Modes=0 and are closed |
| `reflector.light.listeners` | Listen only on the addresses you need | Default is `0.0.0.0:862` and `[::]:862` |
| `reflector.enabled` | `false` on sender-only hosts | Note that without a config file, the default enables only the reflector |
| Firewall | Restrict 862/tcp (control), 862/udp (Light), and `test_port_range` (default 20000-29999/udp, the Full reflection ports) to the measuring hosts | Defense in depth |

### Multihomed hosts and wildcard listeners

By default the Light reflector listens on `0.0.0.0:862` and `[::]:862`. Even on a wildcard listener, gotwampd learns the destination address of each packet through IP_PKTINFO / IPV6_PKTINFO and **replies from that address**. Flows (the unit over which the Reflector Sequence Number is counted) are also keyed by the actual destination address. So on a host with several addresses (router loopbacks, VIPs, multiple NICs), the sender does not discard replies when it matches them by (reflector address, port).

- **When listing specific addresses is better**:
  - To restrict the addresses you listen on (reduce exposure).
  - To pin the NIC for `timestamping: hardware` (a wildcard listener needs `hardware_interface`).
- **When replies come from a different address**: in environments where the socket does not report the destination, the kernel picks the reply's source address from the route (the old behavior). If a multihomed host shows total loss and the sender counts replies from another address as `foreign`, list specific addresses in `reflector.light.listeners`.

### Loops between reflectors

If someone sends a single packet with its source spoofed as `reflector A:862` and its destination set to `reflector B:862`, B reflects it to A, and A reflects that reply back to B as a new test packet. The packet can keep bouncing between the two hosts (the same pattern as UDP echo: no amplification, but it does not stop).

- On hosts that expose a Light reflector, restrict the measuring hosts with `reflector.light.acl`. If you do not allow the addresses of other reflectors, no loop occurs.
- With `reflector.light.rate_limit_pps` set, a loop is capped at the per-source rate.
- gotwampd does not drop all packets with source port 862. Some senders, such as SONiC, can use 862 as their source port, and dropping them could break interoperability.

### Resource limits (defaults)

| Setting | Default | When exceeded |
|------|------|-----------|
| `reflector.control.max_connections` | 64 | Rejected with a Modes=0 Greeting (`gotwamp_reflector_control_connections_total{result="rejected"}`) |
| `reflector.control.max_sessions_per_connection` | 16 | Accept=4 (permanent resource limitation) in Accept-Session |
| `reflector.control.max_sessions` | 1024 | Accept=5 (temporary resource limitation) |
| `reflector.control.test_port_range` | 20000-29999 | Accept=5 if no port is free. A requested Receiver Port below 1024 (except 862 and ports inside this range) is not honored; a port is chosen from this range instead (because the daemon holds CAP_NET_BIND_SERVICE) |
| `reflector.control.max_padding` | 9000 | Accept=3 (not supported) |
| `reflector.light.max_flows` | 65536 | New flows are reflected statelessly (the Sender Sequence Number is copied). `gotwamp_reflector_light_flows_overflow_total` increases |
| `reflector.max_packet_size` | 9000 | Dropped (`malformed`) |

### REFWAIT / SERVWAIT and timeouts

| Timer | Default | Behavior |
|----------|------|------|
| SERVWAIT (`reflector.control.servwait`) | 900s | Closes an idle control connection outside a session (after the Greeting, after Stop-Sessions). Paused while sessions are active |
| REFWAIT (`reflector.control.refwait`) | 900s | Releases a Full session that receives no test packets. When all sessions are released, SERVWAIT resumes |
| Timeout in the Request | Set by the client (gotwamp uses `timeout`, default 2s) | After Stop-Sessions (or after the control connection drops), reflection continues for this long; later packets are dropped as `drained`. **Capped at REFWAIT** (so a huge Timeout cannot hold sessions and sockets indefinitely) |
| `partial_message_timeout` | 60s | Closes the connection if the rest of a partially received control message does not arrive within this time |
| Control connection drop | - | Sessions not yet started are released immediately; started sessions are released after the Timeout |
| `reflector.light.flow_timeout` | 900s | Discards the state (Sequence Number) of an idle Light flow. The next packet starts again from 0 |

Even if a client disappears without sending Stop-Sessions, its sessions are released after REFWAIT (default 15 minutes). To shorten this, lower `refwait` (keep it well above the measurement interval).

## 2. Privileges

- Binding port 862 (below 1024) needs `CAP_NET_BIND_SERVICE`. The systemd unit (`deploy/systemd/gotwampd.service`) grants it to the dedicated user `gotwamp` with `AmbientCapabilities=CAP_NET_BIND_SERVICE`.
- When starting manually: `sudo setcap cap_net_bind_service=+ep /usr/bin/gotwampd`, or use a listen port of 1024 or higher.
- Containers: Docker by default sets the unprivileged port floor inside the container to 0 (`net.ipv4.ip_unprivileged_port_start=0`), so the image's nonroot user can bind 862. This does not apply with `--network host`; there, run as root with `--cap-add NET_BIND_SERVICE`, or change the port.
- Timestamping (SO_TIMESTAMPING), setting and reading TTL / TOS, and reading the clock state (adjtimex) need no privileges.
- The exception is `timestamping: hardware` when NIC timestamping is disabled: for gotwampd to enable it (SIOCSHWTSTAMP) it needs `CAP_NET_ADMIN`. If you enable it with `hwstamp_ctl` before startup, no privilege is needed (see "Hardware timestamping" below).
- Symptoms: `bind: permission denied` in the startup log -> do one of the above. `bind: address already in use` -> another process (twampd, for example) is using the same port (`ss -lntup | grep 862`).

## 3. Timestamps and clocks

### Timestamping fallback

- `daemon.timestamping: auto` (default) enables kernel timestamping (SO_TIMESTAMPING) on the socket at startup, and falls back to user-space timestamps (the time right after `recvmsg`) if that fails.
- The method actually in use is shown in the `timestamping` line of `gotwamp status`, the metric `gotwamp_timestamping_mode{mode}`, and the run summary.
- If `kernel` is set explicitly and is not available, startup fails. If container or kernel constraints result in `userspace`, `daemon.clock.userspace_error` (default 50µs) is added to the Error Estimate.
- User-space timestamps are affected by CPU load and scheduling. If you need µs-level precision, make sure the method is kernel, and consider `realtime.lock_os_thread: true`.

### Hardware timestamping (`timestamping: hardware`)

The NIC stamps receive (and transmit) times with its PTP hardware clock (PHC). This is more precise than kernel software timestamps by the amount of interrupt and driver latency.

**Prerequisite: the PHC must be synchronized to CLOCK_REALTIME.** gotwamp uses the PHC time as-is for T2 / T4 (and T1' on transmit) and does not correct for the offset between the PHC and CLOCK_REALTIME. T1 / T3, on the other hand, come from the system clock, so any PHC offset shifts the delays by the same amount.

- Hosts whose system clock is set by NTP: sync the PHC to the system clock with `phc2sys -s CLOCK_REALTIME -c <interface> -O 0`.
- Hosts where ptp4l syncs the PHC to a grandmaster: the PHC runs on TAI (37 seconds ahead of UTC), and gotwamp (v0.x) does not correct for this. The protection below only makes receive times fall back to software, so use `kernel`. Measuring and correcting the PHC/CLOCK_REALTIME offset with PTP_SYS_OFFSET is a possible future extension.
- Protection: if a received packet's NIC time differs from the kernel software time of the same packet by 1ms or more, the NIC time is discarded and the software time is used (the PHC is not synchronized, or is still on TAI). The transmit time T1' is discarded by the sender's sanity check (SPEC §8.2) and counted as `tx_timestamp_invalid`. If one-way delay is off by tens of seconds, or `tx_timestamp_invalid` keeps increasing, check the PHC synchronization.

**Checking NIC support** (`ethtool -T <interface>`):

```
Capabilities:
	hardware-transmit
	hardware-receive
	hardware-raw-clock
PTP Hardware Clock: 0
Hardware Transmit Timestamp Modes:
	off
	on
Hardware Receive Filter Modes:
	none
	all
```

- Receive needs `hardware-receive` / `hardware-raw-clock` and the receive filter `all`. NICs that only offer PTP filters such as `ptpv2-event` (i40e, for example) do not stamp TWAMP UDP packets and cannot be used.
- Transmit (the sender's T1') needs `hardware-transmit` and transmit mode `on`. Without them, transmit uses kernel software timestamps.
- Virtual NICs (virtio_net, for example) and loopback are not supported.

**Enabling NIC timestamping**:

- At startup gotwampd reads the NIC configuration (SIOCGHWTSTAMP). If the receive filter is not `all`, it changes it to `all`; if the sender uses transmit times and transmit is `off`, it changes it to `on` (SIOCSHWTSTAMP, requires `CAP_NET_ADMIN`).
- Settings are only ever widened. Transmit modes such as ptp4l's one-step are left alone, so a ptp4l instance on the same NIC is not disturbed. Settings are not restored on exit either (other sockets may be using them).
- To run without granting privileges, enable it before startup: `hwstamp_ctl -i eth0 -r 1 -t 1` (linuxptp).
- To let gotwampd enable it under the systemd unit, use `AmbientCapabilities=CAP_NET_BIND_SERVICE CAP_NET_ADMIN` (and add it to `CapabilityBoundingSet` as well).

**Which sockets use NIC timestamps**:

- Sockets bound to a specific address (Full sender and reflector test sessions, Light listeners with listed addresses): the interface that owns that address.
- Sockets bound to a wildcard: the interface in `daemon.hardware_interface`. If it is empty, a Light sender uses the interface of the route to the destination, and a wildcard Light listener falls back to kernel timestamps.
- Packets arriving on any other interface use software timestamps, per packet.

**How to verify**:

- `timestamping` in `gotwamp status` and `gotwamp_timestamping_mode{mode="hardware"}`. If the NIC is not supported, the startup log gives the reason at Warn level (`the card gives no receive timestamps`, `SIOCSHWTSTAMP needs CAP_NET_ADMIN`, and so on), and gotwampd runs with `kernel`.
- `daemon.clock.hardware_error` (default 1µs) is added to the Error Estimate. The reflector takes T3 in user space, so even with `hardware` the reflector's error stays at `userspace_error`.

**Manual verification procedure** (a host with two supported NIC ports, or two hosts):

1. On both ends, check `ethtool -T` and start `phc2sys -s CLOCK_REALTIME -c <if> -O 0 -m`. Wait until the offset in the `phc2sys` log settles within a few hundred ns.
2. Reflector side: list the NIC address in `reflector.light.listeners` and start gotwampd with `timestamping: hardware`. Confirm there is no Warn in the startup log and that `gotwamp status` shows `hardware`.
3. Set the sender side to `timestamping: hardware` as well and run `gotwamp ping --light <reflector NIC address> -c 1000 -i 10ms`. Confirm the output shows `timestamps: hardware`.
4. Run the same measurement with `timestamping: kernel` and compare: the rtt stddev and ipdv should be smaller with hardware.
5. Stop `phc2sys` and offset the PHC (`phc_ctl <if> adj 0.01` for 10ms). Confirm that receive times fall back to software (the receive-time difference protection) and that `tx_timestamp_invalid` increases.

### One-way delay missing / invalid

- One-way delay (forward / reverse) is **valid only when the clocks on both ends are synchronized**. If the S bit of either Error Estimate is 0, `gotwamp_sender_last_run_owd_valid = 0` and metrics such as `..._forward_delay_seconds` are not exported. RTT and IPDV are valid even when the clocks are not synchronized.
- gotwampd reads the synchronization state with adjtimex (`gotwamp_clock_synchronized`). Even with chrony / ntpd / systemd-timesyncd running, some environments do not reflect it in the kernel. In that case, override it with `daemon.clock.sync: synced` and `daemon.clock.max_error`.
- For the known bug where the peer reflector returns Multiplier=0 in the Error Estimate (older Cisco NX-OS releases), the default (`tolerate_zero_multiplier: true`) accepts the reply and only invalidates one-way delay.

### Drops at high packet rates

The reflector receives and replies in one goroutine per listener. Per-packet processing measured over loopback is about 20µs (sendmsg about 13µs, recvmsg about 5µs, gotwampd's own processing about 2µs, no allocations), so a single listener tops out at roughly 50,000 pps.

- **Receive buffer**: gotwampd requests 1 MiB for SO_RCVBUF (the Light default), but the kernel caps it at `net.core.rmem_max` (208 KiB on many distributions). That holds only a few hundred small test packets, so if the host is busy and the reflector goroutine runs a few ms late, the kernel silently drops packets (`receive buffer errors` in `netstat -su`). For reflectors receiving high rates, `sysctl -w net.core.rmem_max=8388608` is recommended (persist it in `/etc/sysctl.d/`).
- **CPU contention**: do not run other heavy workloads on the measurement host. Consider `realtime.lock_os_thread`. If that is still not enough, raise gotwampd's priority as described in "Scheduling priority" below.
- **Split listeners**: receive rates above the limit of one listener on multiple listeners with different ports or addresses.

### Scheduling priority (Nice / SCHED_FIFO)

The bundled unit (`deploy/systemd/gotwampd.service`) prioritizes hardening and sets `RestrictRealtime=yes`. With this setting gotwampd cannot use real-time scheduling. If reflector drops or delay jitter become a problem on a busy host, raise the priority with a drop-in (`systemctl edit gotwampd` creates `/etc/systemd/system/gotwampd.service.d/override.conf`).

Try `Nice=` first. It is not real-time, so `RestrictRealtime=yes` can stay.

```ini
[Service]
Nice=-10
```

Use SCHED_FIFO only if that is not enough. Set `RestrictRealtime=no` **in the same drop-in**.

```ini
[Service]
RestrictRealtime=no
CPUSchedulingPolicy=fifo
CPUSchedulingPriority=10
```

- **Applying**: after a change, run `systemctl daemon-reload && systemctl restart gotwampd` (a reload does not apply it).
- **Checking that it is in effect**: `chrt -p $(systemctl show -p MainPID --value gotwampd)` shows `SCHED_FIFO` and the priority. All Go runtime threads inherit this policy.
- **Priority**: keep it low (1-10). FIFO threads always run before normal processes (including `ksoftirqd` and sshd). If they keep the CPU busy, packet reception itself and management access can be delayed. Kernel RT throttling (`kernel.sched_rt_runtime_us`, by default 0.95 seconds per second) is the last safety valve, but do not rely on it.
- **Checking the effect**: it depends on the environment, so compare `gotwamp_reflector_packets_received_total` and `receive buffer errors` in `netstat -su` before and after the change. Raising the receive buffer (`rmem_max`) often helps first.

### Negative RTT

If a clock step (ntpdate, chrony makestep, a leap second) happens during a run, RTT can become negative. Negative values are excluded from delay statistics and only counted as invalid RTTs. If this happens often, configure the clock to slew instead of step.

## 4. Cannot connect / measurement fails (sender side)

When `gotwamp ping` exits with code 4 (or `gotwamp_sender_run_errors_total{stage}` increases), narrow it down by the stage that failed.

| Stage / message | Cause | Fix |
|-------------------|------|------|
| `connect` | The control connection (TCP 862) cannot be established | Check the route, firewalls, and whether the TWAMP server is enabled on the peer |
| `greeting: server refused the connection (Modes 0)` | Rejected by the peer's ACL (Junos `client-list`, SR Linux `client-connection`, and so on) or connection limit | Allow the source on the peer |
| `greeting: no common mode` | The peer does not offer the mode set in `security` (e.g. unauthenticated against a peer that requires authentication, or authenticated against a peer that offers none) | Set `security` to a mode the peer offers (and `sender.keys_file` and `key_id`). Authentication failures (unknown KeyID, wrong passphrase) fail at the `setup` stage, because the peer returns Server-Start with Accept 1 |
| `setup: rejected by server: server-start accept=1 (failure)` (`security` other than unauthenticated) | The peer rejected authentication: the KeyID is not in the peer's keys file, the passphrase is wrong, or the Client-IV was reused | Compare the identity and passphrase in the keys files on both ends. If the peer is gotwampd, the `reason` of `control server: set-up refused` in its log is one of `unknown key id` / `token does not hold the challenge: wrong passphrase` / `client-iv used before` |
| `greeting: greeting count too small` | The peer offered a PBKDF2 Count below 1024 (a violation of RFC 4656). Rejected in authenticated modes | Set the peer's setting (`pbkdf2_count` in twampd, for example) to 1024 or more |
| `... control message hmac mismatch` | A control message HMAC did not match (wrong key, modification in transit), so the connection was closed (RFC 4656 §6.10) | Check the keys. Suspect a middlebox rewriting TCP. gotwampd logs Warn `message failed its integrity check` |
| `request: rejected by server: accept-session accept=3` (with `octets_to_reflect` / `padding_to_reflect` set) | For Reflect Octets (RFC 6038), the padding is too short for the length to be reflected (`padding` must be longer than `padding_to_reflect`, and without Symmetrical Size at least 27 + `padding_to_reflect`, or 64 + in authenticated modes) | Increase `padding`. gotwamp also checks this before sending |
| `request: server did not reflect the octets to be reflected` | The peer did not return Octets to be reflected in Accept-Session. It claims RFC 6038 but does not implement it | Check the peer's implementation. Removing `octets_to_reflect` lets the measurement run |
| `request: rejected by server: accept-session accept=3` | Part of the request is not acceptable (padding too large, Receiver Port restrictions, and so on) | Review `padding` and `receiver_port`. SR Linux / Junos Evolved accept only 862 or 49152-65535 as the Receiver Port |
| `accept=4` / `accept=5` | Peer resource limits | Reduce concurrent sessions, or raise the peer's limits |
| `request: server offered an alternate port` | `accept_alternate_port: false` and the peer returned a different port | Revert to the default (`true`), or set a port the peer accepts with `receiver_port` |
| `... control timeout after 15s` | The peer does not respond. Juniper ACX has a known bug where Start-Ack is delayed by about 10 seconds | Increase `control_timeout` |
| `test: control connection lost` | The control connection dropped during the measurement (peer restart, NAT timeout) | The result is treated as invalid. Across NAT, check the keepalive interval |
| Total packet loss (exit code 1) | Test packets do not arrive, or replies do not come back | See "Total loss" below |

### TWAMP extensions (RFC 5938 / RFC 6038) and STAMP

- **Extension negotiation**: extensions listed in `extensions` are used only if the peer's Server Greeting offers them. If the peer does not, the measurement silently runs as plain RFC 5357.
- **Stop with ISC**: in runs that use Individual Session Control, Stop-N-Sessions has no Accept field. So even if a socket fails during the measurement, gotwamp cannot send the peer a Stop that "invalidates the results" (gotwamp records that run as failed on its side).
- **`reflected_mismatch` increases**: the peer accepted Reflect Octets but the reflected padding does not match the request. This is a defect in the peer's implementation; the measurement still runs.
- **STAMP replies counted as "foreign"**: when sending with `stamp` and `ssid`, the SSID in the reply is neither your own SSID nor 0. These are replies for another sender sharing the same reflector, so pick non-overlapping `ssid` values.
- **Reply length of a reflector in STAMP mode**: with `reflector.light.stamp: true`, a 41-octet TWAMP request also gets a 44-octet reply (the STAMP base packet). For senders that compare reply lengths strictly, use a request padding of 30 or more.

### Total loss (control succeeds but no replies come back)

- **Firewall**: allow UDP to the peer's reflection port (the Port in Accept-Session; testports for twampd, `test_port_range` for gotwampd) and from your source ports (`sender_port_range`, default 49152-57343).
- **NAT**: the address in Request-TW-Session is the one inside the NAT, and the peer discards packets after checking the source. Try `zero_addresses: true`. If gotwampd is the reflector, consider `verify_sender: none` (for PAT).
- **Start Time**: with `start_time: now` and a peer whose clock is behind, packets are discarded as arriving before the Start Time (SR Linux `enforce-test-session-start-time`). Use the default `zero` (start immediately).
- **Peer defines sessions by 4-tuple**: Cisco IOS-XE Light, Huawei, SONiC and others require the source port to match as well. Fix `sender_port`.
- **MTU**: a large `padding` causes fragmentation, and fragments may not be reflected. Keep padding at or below 1458 on IPv4 and 1438 on IPv6 (a warning is printed otherwise).
- **IPv6 UDP checksum**: gotwamp always computes it. Combined with implementations that send 0, packets may be discarded on the peer side (Nokia's default).

## 5. Packets are not reflected (reflector side)

Check `gotwamp_reflector_packets_dropped_total{reason}`.

| reason | Meaning | Fix |
|--------|------|------|
| `acl` | Rejected by the Light ACL | Add the source to `reflector.light.acl` |
| `rate_limit` | Per-source rate exceeded | Raise `rate_limit_pps`. Check the measurement rate |
| `malformed` | Under 14 octets, or over `max_packet_size`. For Full Authenticated / Encrypted, also an HMAC mismatch | Check the sender's padding. If it keeps increasing in authenticated modes, the test packet key (derived from the control connection key and the SID) does not match. Suspect an interoperability problem in the peer's implementation |
| `unknown_session` | Full: the source does not match the session's Sender | Behind NAT / PAT use `verify_sender: none`; if only the source port changes, use `address` (default) |
| `not_started` | Full: arrived before Start-Sessions (and, when Start Time is enforced, before the Start Time) | The sender is sending without waiting for Start-Ack. If Start Time is enforced, the first packets are dropped when the sender's clock is ahead (during an NTP adjustment, for example), so stop enforcing it (default) |
| `drained` | Full: arrived after Stop-Sessions + Timeout | Increase the sender's Timeout |

If the Light Sequence Number resets to 0 mid-run, the sender is sending at intervals longer than `flow_timeout` (default 900s), or `max_flows` has been exceeded.

### Reflector in authenticated modes (`reflector.control.modes`)

- **Startup log**: the offered modes are logged at Info level. With `modes` empty and `keys_file` set, all four modes (including unauthenticated) are offered. To require authentication, remove unauthenticated, e.g. `modes: [authenticated, encrypted]`.
- **Per-connection logs**:
  - Success: Info `control server: client authenticated` (mode, key_id).
  - Rejection: Info `control server: set-up refused` (reason).
  - Reason for closing: one Info line per connection, `control server: connection closed` (`EOF`, `servwait expired`, `protocol violation ...`, and so on).
- **Connections from twping are rejected**: check that the identity and passphrase in `twping -A E -u <identity> -k <pfs>` match gotwampd's `reflector.control.keys_file`. twping prints `Server denied access`.

## 6. Checking logs and state

- `gotwamp status`: version, role, timestamping mode, clock synchronization state, session counts.
- `gotwamp reflector`: reflector listeners, counters, and the list of Full sessions and Light flows (`ListReflectorSessions`).
- `journalctl -u gotwampd`: bind errors at startup, configuration warnings, reasons for run failures. `daemon.log.level: debug` logs details such as send failures (applied on reload).
- `/healthz`: 200 while the daemon is running.
- If you cannot connect over remote management (`daemon.api.tcp`), check the reason in gotwampd's Warn `API client refused`. The common reasons are: no client certificate, a different CA, not in `allowed_names`, or the handshake deadline (10 seconds) expired. The CLI exits with code 3 (connection or authentication failure), or 2 for a malformed `--server` or missing TLS file options.

## 7. Result persistence (SQLite)

Operational notes for `store.backend: sqlite` (ADR 0013, [config.md, "`store`"](config.md#store)):

- **`result database is corrupt` at startup**: the DB file is corrupt or is not an SQLite file.
  - With `on_corrupt: reset` (default), the file is moved aside to `<path>.corrupt-<time>`, gotwampd starts with an empty DB, and logs a Warn. The moved file can sometimes be recovered with `sqlite3 <file> .recover`.
  - With `fail`, gotwampd does not start.
- **`result database has a newer schema` at startup**: a DB created by a newer gotwampd was opened by an older one. The file is not modified. Go back to the newer version, or use a different path.
- **`sqlite store: writer behind; results not kept`**: the disk is slow and writes cannot keep up. To avoid stopping measurements, those results are not saved. Reduce the disk load or move the DB to another disk.
- **`result database above its size limit`**: shorten the retention. Free space in the DB is not reclaimed automatically, so stop gotwampd and run `sqlite3 results.db VACUUM`.
- **Backup**: while running, use `sqlite3 results.db ".backup /path/backup.db"` (gives a consistent copy even in WAL mode). A plain file copy misses the latest results unless it includes the `-wal` file.
- **Inspecting the DB**: read results with `gotwamp stats NAME --since 720h` (from the DB, back to the retention limit). With `sqlite3`, the table `infos` (session descriptions) is readable as JSON, but the result rows `results.data` are compressed and cannot be read directly (the format is in the table `payload_formats`; counts and times can be seen with `SELECT session, COUNT(*), datetime(MAX(time_ns)/1e9, 'unixepoch') FROM results GROUP BY session`).
