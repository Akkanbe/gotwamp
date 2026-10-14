# Changelog

This file records the notable changes of the project. Its format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and versioning follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Breaking changes are allowed during v0.x; when one happens, it is stated here.

## [Unreleased]

Work in progress toward the first release (v0.1.0).

### Added

- **Protocol**: the Unauthenticated mode of TWAMP (RFC 5357). TWAMP-Control Control-Client / Server, Session-Sender / Session-Reflector (Full), and the TWAMP Light sender / reflector (per-flow Reflector Sequence Number, ACL, rate limiting, DSCP reflection; ADR 0003). IPv4 / IPv6
- **Measurement**: RTT, one-way delays (valid only when both ends are synchronized), IPDV / PDV / RFC 3550 jitter, loss (round-trip and estimated per direction), duplication, reordering, hop counts. Kernel transmit and receive timestamps (SO_TIMESTAMPING, T1' included) with a fallback to user space (ADR 0002), and the Error Estimate from adjtimex
- **gotwampd**:
  - Configuration file (YAML: defaults, inheritance from `sender.defaults`, validation of every problem at once, with paths)
  - Reflector role (TWAMP-Control server + Full reflector, Light reflector)
  - Monitoring sessions (periodic / continuous; a continuous Full session keeps one control connection, ADR 0007)
  - Result store, Prometheus (`/metrics`, `/healthz`), JSON Lines (size / daily rotation, with the SID of Full runs)
  - Changes applied by SIGHUP / `ReloadConfig` (ADR 0008), graceful shutdown
- **gRPC API** `gotwamp.v1` over a Unix domain socket (ADR 0001, 0005): `DaemonService`, `SenderService` (`RunTest`, `StopTest` (ADR 0006), CRUD / Start / Stop of monitoring sessions, `GetSessionStats`, `WatchResults`), `ReflectorService`
- **gotwamp CLI**:
  - `ping` (Full by default, Light with `--light`; Ctrl-C stops the measurement and prints the summary)
  - `session list|show|add|update|remove|start|stop`, `stats`, `watch`, `status`, `reflector status|sessions`, `config validate|reload`, `version`
  - `--output table|json|jsonl`. Exit statuses as in SPEC §10.4
  - Results of ad hoc measurements are not exported (ADR 0009)
- **Distribution**: systemd unit, distroless container image (`make docker`), tar.gz / deb / rpm by goreleaser
- **Tests**:
  - Golden tests from the RFC figures, fuzzing of the decoders
  - Loopback integration tests, netns + netem component tests (`make test-component`)
  - Soak test (`make soak`)
  - Interoperability tests with perfSONAR (`make interop`; both directions, the released binaries included)
- **Development infrastructure**: Makefile, golangci-lint / declscope (ADR 0004) / buf, GitHub Actions (build / test / component / soak / lint / proto / vulncheck, nightly interop)
- **Remote management** (ADR 0010):
  - `daemon.api.tcp` also serves the gRPC API over TCP. Mutual TLS 1.3 is required, and `allowed_names` narrows the allowed names (CN / SAN). Refused connections are logged. The UDS stays the default
  - The CLI gains `--server host:port`, `--tls-ca` / `--tls-cert` / `--tls-key` / `--tls-server-name` (environment variables `GOTWAMP_SERVER` / `GOTWAMP_TLS_*`)
  - gotwampd does not start when a private key or a keys file is accessible beyond mode 0600
- **Security modes** (Authenticated / Encrypted of RFC 4656 / 5357, Mixed of RFC 5618; TWAMP Full only):
  - Keys come from `sender.keys_file` and `reflector.control.keys_file` (perfSONAR pfs compatible, read at startup). A session naming a KeyID without a key is refused at startup and on reload
  - Sender: `sender.defaults.security` / `key_id` and per-session settings, `TestSpec.security` / `key_id` in the API, `--security auth|encrypted|mixed` and `--key-id` in ping
  - Reflector: `reflector.control.modes` selects the modes offered (by default all four when a keys file is set). `gotwamp reflector sessions` shows the mode of each session
  - Interoperability with perfSONAR (twampd / twping) checked in both directions (`make interop INTEROP_CLI=1` gains the cases through the CLI)
- **Hardware timestamping**: `daemon.timestamping: hardware`, `daemon.hardware_interface`, `daemon.clock.hardware_error`. A NIC without support falls back to kernel timestamps, with the reason logged. Shown by `gotwamp status` and the metric `gotwamp_timestamping_mode{mode="hardware"}`
- **Daily rotation of JSON Lines**: `export.jsonl.rotate.daily`
- **TWAMP extensions and STAMP compatibility**: Individual Session Control (RFC 5938), Reflect Octets / Symmetrical Size (RFC 6038), STAMP (RFC 8762 / the SSID of RFC 8972)
  - Sender: the test parameters `extensions`, `octets_to_reflect`, `padding_to_reflect` (Full), `stamp`, `ssid` (Light). `--isc`, `--symmetrical`, `--reflect-octets`, `--padding-to-reflect`, `--stamp`, `--ssid` in ping
  - Reflector: `reflector.control.extensions` (all three offered by default), `reflector.light.stamp`
  - Results: `reflected_mismatch` (API, JSON Lines, `gotwamp_sender_reflected_mismatch_total`)
- **Result persistence (SQLite, ADR 0013)**: `store.backend: sqlite` keeps the summaries of runs and windows and the run errors in SQLite (`internal/store/sqlite`, on the pure-Go modernc.org/sqlite; `CGO_ENABLED=0` still holds).
  - Settings: `store.sqlite.path` / `retention` / `max_size` / `on_corrupt`.
  - After a restart, the latest `result_history` entries and time-range queries remain available; `gotwamp stats --since` reads back from the database up to the retention.
  - WAL, schema versioning, deletion by retention, a warning at the size limit, and moving a corrupt database aside.
  - Writes are asynchronous and never hold up a measurement (what falls behind is counted).
  - Result rows are stored without their zero-valued fields, compressed with DEFLATE and a preset dictionary (schema version 2, about 0.6 KB per entry; rows in the JSON of version 1 are still read).
  - `gotwamp status` shows the store, and the metrics `gotwamp_store_results_*_total` are added.
  - The same contract tests (`internal/store/storetest`) run against both memory and SQLite.
- **ADRs**: 0011 (separation of the security layer, and the key store), 0012 (scope of STAMP compatibility), 0013 (persistence in SQLite), 0014 (hardware timestamping and the PHC assumptions)
- **Documentation**: docs/ops gains:
  - How to make a keys file
  - How to make the mTLS certificates
  - The procedure for authenticated interoperability
  - Troubleshooting of the extensions, STAMP and the security modes
  - Rows for STAMP / the security modes in the amplification table
  - Alert examples (`tx_timestamp_invalid`, `reflected_mismatch`)

### Changed

- JSON Lines: `tx_timestamps` moved to `clock.tx_timestamps`, as SPEC §9.7 says.
- A periodic run now logs two lines, "run started" and "run ended" (with the summary). A continuous session logs "window ended" for each window.
- The TWAMP-Control server logs why a connection ended (disconnect, SERVWAIT, partial message timeout, protocol violation) in one Info line per connection. Released sessions (packets received and reflected) are logged at Info too. An HMAC mismatch is a Warn.
- SIDs in the logs are hexadecimal strings (they were arrays of numbers in the JSON logs).
- Durations (time.Duration) in the JSON logs are strings such as `"43.5ms"`.
- `dscp` in the configuration accepts decimal or `0x`-prefixed hexadecimal only (a value with a leading 0 was read as octal).
- CLI error messages no longer show the gRPC code name. `DeadlineExceeded` points to `--rpc-timeout` instead.
- systemd unit: a configuration error (exit status 1) is not restarted over and over (`RestartPreventExitStatus=1`). The container image has a HEALTHCHECK.
- Filling in the build information (version / commit / Go version) moved to `internal/buildinfo`, shared by gotwampd and the CLI. ULID generation is in `internal/ulid` alone.

### Fixed

Found by the reviews from the maintenance and operations point of view (A: statistics and monitoring, B: daemon / CLI / configuration / distribution, C: protocol and data path).

- JSON Lines: one failed write (a transient ENOSPC, for example) stopped the output until a restart. The file is now opened again a second later, and the failure and the recovery are logged.
- The RFC 4737 reordering check was wrong across a wrap of the Sequence Number (2^32), which long-running continuous sessions reach.
- A reload stopped the removed and changed sessions one at a time, so reloads were slow with many Full sessions.
- `gotwamp_sender_tx_timestamp_invalid_total` was missing from Prometheus.
- Failures to read adjtimex, and their recovery, were not logged.
- API over TCP + mTLS: a transient Accept failure (running out of file descriptors, for example) left the remote API stopped. Concurrent handshakes had no limit.
- `session add/update -f` refused `reflector_sequence`, `dscp_ecn_monitoring`, `security` and `key_id` as unknown keys.
- A failed reload through `gotwamp config reload` (RPC) was not logged by the daemon. A level overridden with `--log-level` was silently reverted by a reload.
- A configuration file placed at the default location after startup could not be read by SIGHUP.
- Sender: on hardware-timestamping sockets, the user-space error (50µs) was added to the Error Estimate.
- Reflector: persistent socket errors logged up to 100 Warn lines a second (now thinned to once a minute, with the number of lines left out).
- Reflector: the default listener list was shared between configurations (a change by a caller could leak into the defaults).
- Component tests: tcpdump was left running after a failure.
- Light reflector: a wildcard listener replied from the address the route chose rather than from the destination it was reached at (senders on multihomed hosts discarded the replies).
- Full reflector: sessions whose padding did not fit max_packet_size were accepted and lost every packet (now refused with Accept 3).

### Security

- Go 1.26.9 (`go` directive) and golang.org/x/net v0.61.0: fixes GO-2026-6607 (crypto/tls), GO-2026-6608 (net/textproto), GO-2026-6611 / 6612 / 6613 / 6617 (net/http and the HTTP/2 framer that gRPC uses). govulncheck reports nothing.

### TODO

- OTLP export (ADR 0010; to be added as an exporter implementing `store.ResultSink`)
- Update `google.golang.org/grpc` to v1.85.0 once released (GO-2026-6443 was fixed in its development branch; govulncheck v1.8.0 no longer reports it against v1.84.0, and the current code does not call the affected path)
- Report the NIC timestamps discarded in hardware timestamping (`HardwareRejected`, a sign of an unsynchronized PHC) in metrics or logs (ADR 0014)
- Check interoperability of STAMP / RFC 6038 / RFC 5938 with real devices (SR Linux and others); perfSONAR does not support them
