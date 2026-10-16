# gotwamp Implementation Specification

| Item | Value |
|------|------|
| Version | 0.4 (draft) |
| Created | 2026-10-03 |
| Updated | 2026-10-05 (0.4) |
| Subject | gotwamp (a TWAMP Sender / Reflector implementation in Go) |
| Related documents | [docs/PLAN.md](PLAN.md) implementation plan, [docs/research/](research/) research notes |

This document defines the implementation specification of gotwamp. The primary sources for the protocol are RFC 5357 and RFC 4656, which it references;
this document describes "how gotwamp behaves", mapping each behaviour to the RFC requirements (MUST / SHOULD).
Where the RFCs and this document disagree, the RFCs take precedence and this document is corrected.

Notation:

- "MUST / SHOULD / MAY" are used in the sense of RFC 2119 and express requirements on gotwamp itself
- `FR-xx` denotes a functional requirement, `NFR-xx` a non-functional requirement, and `CMP-xx` a compatibility knob (a setting that absorbs vendor differences)
- Offsets in octet tables are 0-based and lengths are in octets. All multi-octet integers are in network byte order (big endian)

Version history:

| Version | Date | Summary of changes |
|----|------|----------|
| 0.1 | 2026-10-03 | Initial version (draft) |
| 0.2 | 2026-10-04 | Reflects the audit's SPEC fix proposals (9 from audit-fixes-base, 2 from audit-fixes-wire) and the "align the specification with the implementation" items of audit audit-spec §2.2. Implemented behaviour is taken as authoritative; where there was a choice, the implemented option was adopted (W5: (b), B23: (a)). The changes are listed in the table below |
| 0.3 | 2026-10-04 | Reflects M7 (authenticated modes, hardware timestamping, TCP + mTLS) and the results of maintenance reviews A / B. The changes are listed in the "Changes in 0.3" table |
| 0.4 | 2026-10-05 | (Same-day correction: aligned the SQLite storage format (version 2 compression) and the read range of `--since` in §9.4 and §13 with the implementation. Codex round 4 N2) Reflects the rest of M7 (RFC 5938 / RFC 6038 / STAMP, the SQLite result store) and the completed wiring into the daemon / CLI (A / E / M, hardware, daily rotation), and removes the "pending wiring" notes of 0.3. The Full reflector does not enforce Start Time by default (`enforce_start_time`, CMP-26). The changes are listed in the "Changes in 0.4" table |

Changes in 0.2:

| Section | Summary of changes | Source |
|----|----------|------|
| §5.1 | DSCP of the control connection: the Server uses a fixed value of 0 in v0.x. `reflector.control.dscp` is reserved (not implemented) | base proposal 3 (B18) |
| §5.3 | Accept in Server-Start: non-zero only for an invalid Mode. ACL refusal and exceeding the connection limit are signalled by a Greeting with Modes=0 | wire proposal 2 (W9) |
| §5.5 | Removed "ACL refusal (Server-Start)" from Accept 1. Added Full's `14 + Padding Length > max_packet_size` to Accept 3 | wire proposals 1, 2 (W5 (b), W9) |
| §5.6 | Timeout failure message changed to `control timeout in <stage> (after <timeout>)` | wire W6 |
| §5.10 / §6.4 | `max_padding` is at least 1. Full refuses, with Accept 3 at Request time, padding that does not fit within max_packet_size | base proposal 2, wire proposal 1 (W5 (b)) |
| §6.5 | Discarded replies are split into malformed / late / stray, with the total in `packets_stray_total`. Duplicates after the timeout are late or stray | audit-spec U5, U6 |
| §7.1 / §7.6 | If either end has Multiplier 0, `max_clock_error_unknown` is set (`max_clock_error` is the sum over the known ends only) | audit-spec U7 |
| §7.2 | Added `pending`, `late` / `stray`, `loss_split_valid`. The denominator of `loss_ratio` is now sent − pending. Stated the baseline and computation conditions of the forward/reverse path breakdown, aligned with the implementation (Codex re-review M5) | consistency with the implementation (accompanies U1) |
| §8.4 | `method_error` config keys `kernel_error` / `userspace_error`, and the handling of 0 = default. The Reflector's kernel mode uses max(userspace, kernel). When the clock cannot be read, the error is encoded as 16s (Scale 63 / Multiplier 255 are not used) | base proposals 1, 7 (B23 (a)), audit-spec U9, wire W3 |
| §9.1 | Even when `--config` is given explicitly, the daemon starts with defaults if the file does not exist. Exit code 3. RPCs during shutdown return Aborted | base proposal 6 |
| §9.2 | Added `daemon.clock.kernel_error`, `daemon.limits.max_concurrent_tests` / `max_watchers`, `sender.defaults.reflector_sequence` / `dscp_ecn_monitoring`, `reflector.light.dscp_ecn_monitoring`. `max_padding` ≥ 1, `pbkdf2_count` is a power of 2, list of reserved label names, `reflector.control.dscp` is reserved | base proposals 1, 2, 3 |
| §9.3 | continuous divides one run into windows (Full also does not reconnect per window). There is no `created` state | audit-spec U1, U3 |
| §9.4 | run_error is kept in the same ring as summary. Per-packet records use a 64-octet compressed format (the retained fields are listed) | audit-spec U2, U4 |
| §9.5 | Proto additions (`StopTest`, `RunStage.INTERNAL`, `ReflectorSequence`, `TestSpec.dscp_ecn_monitoring`, PacketResult 22–24, RunSummary 30–36, ClockInfo additions, `DscpReflected`). The `RunTest` count takes the default when unspecified (1–100,000) | base proposal 4 (B11, B21) |
| §9.6 | Full's `listener` label is the comma-separated addresses of the control listeners. Added the implemented counters (stray, negative_owd, invalid_rtt, missing TX timestamps, PTP, RFC 7750, jsonl_dropped) | base proposal 8 (B28), consistency with the implementation |
| §9.7 | Additional fields of summary / packet records, and shutdown with a deadline | consistency with the implementation (U7, RFC 7750, Codex re-review M9) |
| §10.3 | `--reflector-sequence`, `--dscp-ecn`, `--rpc-timeout`. `--ttl 0` / `-p 0` are not allowed. The fragmentation warning is decided from the resolved values in RunStarted. The unsync note goes on both lines, and an unknown error is shown as "max error unknown" | base proposal 5 (B20) |
| §10.4 | Aborted from a daemon that is shutting down yields exit code 1 | base proposal 6 |
| §12.4 | `/healthz` returns 200 if it can connect to the API socket | base proposal 9 |
| Appendix A | Added the defaults above | — |

Changes in 0.3 (2026-10-04. Reflects M7-1 authenticated modes, M7-4 hardware timestamping, M7-5 TCP + mTLS, and the results of maintenance reviews A / B. What is implemented is taken as authoritative; items whose wiring into the daemon / CLI remains are marked "pending wiring"):

| Section | Summary of changes | Source |
|----|----------|------|
| §5.2 | Encryption of A / E / M control messages (CBC chaining per direction) and HMAC verification; on mismatch, disconnect + Warn. Coverage of the first HMAC | m7-auth proposal 2 |
| §5.3 | Described Greeting Modes / Count and Set-Up-Response KeyID / Token / Client-IV for A / E / M | m7-auth proposal 1 |
| §5.4 | Bits 1–3 changed to "supported (M7, pending wiring)". Server refusal conditions (unknown KeyID, Challenge mismatch, Client-IV reuse over a history of 4096), Client mode selection order E > A > M > O and refusal of Count < 1024, Mixed, key store checks | m7-auth proposal 1 |
| §5.5 / §5.6 / §5.7 | Header length in the Accept 3 length condition (48 for A / E). Added authentication to the GREETING_WAIT / GREETING_SENT states of the state machine | m7-auth implementation |
| §5.9 / Appendix A / Appendix B | The padding that makes A / E symmetric is 64 (asymmetric with the default of 27). Whether to make 64 the default is undecided. Packet size quick reference for A / E | m7-auth proposal 5 |
| §6.2 / §6.3 | A / E formats changed to "implemented". Derivation of the test keys, encryption coverage, **the encryption coverage of the Encrypted Sender packet is 32 octets (following the reference implementation rather than the wording of RFC 5357 §4.2)**, sealing after T3 is written, S-DSCP-ECN at offset 81 | m7-auth proposal 8 |
| §6.4 | For A / E, packets shorter than 48 octets and HMAC mismatches are dropped as `malformed` and are not used for REFWAIT or for pinning under `verify_sender: none`. The A / E reply length is `max(n, 112)` (amplification factor 2.3; unusable without the key) | m7-auth proposals 3, 4 |
| §7.2 | `reordered` comparison uses RFC 1982 serial number arithmetic (correct across wraparound) | maint-A #7 |
| §8.1 / §8.4 | `hardware` mode (implemented in tstamp; pending wiring into config / API). Assumes the PHC is synchronized to CLOCK_REALTIME, ±1ms guard, NIC settings are only ever widened, wildcard sockets require an interface to be specified. `auto` does not try hardware. `hardware_error` is pending wiring | m7-hwts |
| §9.2 | Added `daemon.api.tcp.*`, `sender.keys_file`, `sender.defaults.security` / `key_id`, `reflector.control.keys_file`. `export.jsonl.rotate.daily` noted as "supported by the writer, config key not yet added". Validation rules (DSCP in decimal or 0x, required TLS items, secret files 0600 or stricter, security requirements, warning for a reflector without listeners) | m7-grpc-tcp, m7-auth, maint-A P1, maint-B #5, #6 |
| §9.3 | Reload aborts all the sessions it stops at once (instead of one by one). Reload failures are logged even when triggered via RPC. When `--log-level` is pinned, the configured level is not applied and a warning is emitted. Even when started without the file, the path is kept and a file placed later is read on reload | maint-A #5, maint-B #7, #8, #9 |
| §9.5 | Connections over UDS and TCP + mTLS (ADR 0010). Handling of `TestSpec.security` (Unimplemented in v0.3 gotwampd), `key_id = 19` | m7-grpc-tcp, m7-auth |
| §9.6 | Added `gotwamp_sender_tx_timestamp_invalid_total`. Difference in scope between `packets_stray_total` and JSONL `stray`. List of stages for run_errors_total. Breakdown of jsonl_dropped_total. HMAC mismatch is malformed (`DROP_REASON_AUTH` undecided) | maint-A #8, #9, P2, m7-auth proposal 7 |
| §9.7 | Daily rotation (writer only), reopening after a write failure (1s) and logging, location of `clock.tx_timestamps` (the implementation was already moved to match the SPEC) | maint-A #1, #2, #3, P1 |
| §9.8 | Log context (session / run_id / target / SID, timestamping mode at startup), durations as strings, logging of clock read failures, no secrets in logs | maint-A #4, maint-B #10, #11 |
| §9.10 | Exposing TCP + mTLS (handshake 10s, 64 concurrent, Accept retry), `RestartPreventExitStatus=1`, container HEALTHCHECK and socket_group, CAP_NET_ADMIN for hardware | m7-grpc-tcp, maint-B #1, #2, #15, #16, m7-hwts |
| §10.1 / §10.2 / §10.3 | `--server` / `--tls-*`, `--rpc-timeout`, error messages, session file keys, ping `--security` / `--key-id` | m7-grpc-tcp, maint-B #3, #12 |
| §12.3 / §13 | Cryptography limited to the standard library (implemented), Light is unauthenticated only. Updated Auth / hardware / remote management in §13 to implemented | m7-auth proposal 6, m7-hwts, m7-grpc-tcp |
| Appendix A | Added security, keys_file, Client-IV history, api.tcp, handshake, JSONL, hardware tolerance, etc. | — |

Changes in 0.4 (2026-10-05. Reflects M7-2 / M7-3 (RFC 5938, RFC 6038, STAMP), M7-7 (SQLite), the completed wiring into the daemon / CLI, and the Start Time issue found in interoperability testing with twping. What is implemented is taken as authoritative, and the "pending wiring" notes of 0.3 are removed. The only remaining wiring is `reflector.control.enforce_start_time`, which is explicitly marked "pending wiring"):

| Section | Summary of changes | Source |
|----|----------|------|
| §1.2 / §2.1 | Note on the scope implemented in M7. FR-06 changed to A / E / M implemented | consistency with the implementation |
| §4.2 | store (sqlite), keystore, TCP + mTLS listener | m7-persist, m7-auth |
| §5.3 | Greeting Modes (defaults of `modes` / `extensions`), Request octets_to_reflect / padding_to_reflect, Accept-Session Reflected octets (returned regardless of Accept) and Server octets, Start-N / Stop-N message formats, absence of Accept in Stop-N | m7-ext proposals 2, 4, m7-auth-wiring |
| §5.4 | Removed "pending wiring" from bits 1–3 and made bits 4–6 supported. The Server offers `extensions` (all by default); the Client uses only those offered. Bit 8 is Light only | m7-ext proposal 1, m7-auth-wiring |
| §5.5 | Added to Accept 3: the RFC 6038 padding lower bound (L < Padding Length; 27 / 64 + L when asymmetric), the Symmetrical Size header length 41 / 112, a mismatch between IPVN and the control connection's family (wire W7; missed in 0.2) | m7-ext proposal 2, spec-update unresolved |
| §5.6 / §5.7 | ISC state machine: Request during ACTIVE, commands 2 / 3 disconnect, Ack split per Accept, Stop-N on a not-yet-started session releases it, refusing a Request does not disconnect (an exception to §5.6). Reflection from Start-Sessions onward (Start Time is not checked by default) | m7-ext proposals 3, 4, interop-auth-flake |
| §5.9 | Rows for `extensions` and `octets_to_reflect` / `padding_to_reflect`. Added gotwampd's default to the Start Time row | m7-auth-wiring, interop-auth-flake |
| §6.2 / §6.3 / §6.4 | Symmetrical Size MBZ is not included in Padding Length, the reflector does not move the padding, Reflect Octets and Server octets. **The Full reflector does not enforce Start Time by default** (loss of leading packets due to twping's NTP offset), `enforce_start_time` (CMP-26, config key pending wiring) | m7-ext proposal 5, interop-auth-flake |
| §6.6 / §6.7 | STAMP changed from "not implemented in v0.x" to the implemented scope: reflector STAMP mode (SSID copy, 41–43 MBZ, max(n, 44), TLVs only for `100xxxxx`, U / M flags), sender STAMP transmission (SSID, Extra Padding TLV, other SSIDs dropped), out-of-scope features. Amplification factor 44/14 | m7-ext proposal 6, m7-auth-wiring addendum 1 |
| §7.2 | `reflected_mismatch` | m7-ext proposal 7 |
| §8.1 / §8.4 | Removed "pending wiring" from hardware. `hardware_interface` (the Light sender derives it from the route), restart required, logging of NIC results, `HardwareRejected` not yet exposed. The Sender's method error is `hardware_error` (default 1µs, 0 means kernel_error); the Reflector remains max(userspace, kernel) | m7-auth-wiring addendum (hardware), ADR 0014 |
| §9.2 | yaml example and validation rules: `hardware_interface`, `clock.hardware_error`, `extensions` / `octets_to_reflect` / `padding_to_reflect` / `stamp` / `ssid`, `reflector.control.modes` / `extensions` / `enforce_start_time` (pending wiring), `reflector.light.stamp`, `export.jsonl.rotate.daily`, `store.*`. Handling of items specific to one mode, checks of keys files and key_id | m7-auth-wiring, m7-persist, m7-docs |
| §9.3 / §9.4 | Definitions of API-originated sessions are not persisted. Behaviour of `store.backend: sqlite` (what is stored, asynchronous writer and queue, reads served from memory, retention, corrupted DB, startup errors, status checks) | m7-persist, m7-auth-wiring addendum 2 |
| §9.5 | TestSpec 20–24, `Extension` enum, RunSummary 37, `TIMESTAMPING_MODE_HARDWARE`, `GetStatusResponse.store` / `StoreStatus`, `FullReflectorSession.security`. Removed Unimplemented for security. Keys never pass through the API | m7-auth-wiring |
| §9.6 / §9.7 | `reflected_mismatch_total`, `gotwamp_store_results_*_total`, hardware for `timestamping_mode`, meaning of `not_started`, short Symmetrical Size packets are malformed. JSONL `reflected_mismatch` and `rotate.daily` | m7-ext proposal 7, m7-auth-wiring |
| §9.10 | mTLS details (TLS 1.3, CN / SAN, revocation not checked, changes require a restart), SQLite in StateDirectory, CAP_NET_ADMIN not included in the shipped unit, ownership of keys files | m7-docs (config.md), m7-grpc-tcp |
| §10.2 / §10.3 | Session file keys, store line in status, SECURITY column in reflector sessions. ping `--isc` / `--symmetrical` / `--reflect-octets` / `--padding-to-reflect` / `--stamp` / `--ssid`, removed "refused" for `--security`, handling of mode-specific flags | m7-auth-wiring (including addendum 3), m7-docs |
| §11.2 / §11.3 | Added gotwampd's default to CMP-02, corrected CMP-13 to match the feature bits, `light.stamp` in CMP-15, added **CMP-26 `enforce_start_time`**, CMP-24 changed to "not implemented". Updated the unverified items (perfSONAR authentication confirmed) | interop-auth-flake, m7-ext |
| §12.3 / §13 | Exceptions to the reply length (STAMP 44, A / E 112). Updated the reservations in §13 to implemented and corrected an error in the RFC 6038 reservation (counting MBZ as padding) | m7-ext proposal 5 |
| Appendix A / Appendix B | Defaults of the new keys (extensions, stamp, modes, enforce_start_time, hardware_error, hardware_interface, rotate.daily, store.*, SQLite writer). Packet sizes for Symmetrical Size and STAMP | — |

---

## Table of Contents

- 1. Overview
- 2. Requirements
- 3. Terminology and Measurement Model
- 4. Overall Architecture
- 5. TWAMP-Control Specification
- 6. TWAMP-Test Specification
- 7. Measurement Model and Statistics
- 8. Timestamping and Clocks
- 9. gotwampd Specification
- 10. gotwamp CLI Specification
- 11. Interoperability Requirements and Compatibility Knobs
- 12. Non-Functional Requirements in Detail
- 13. Design Reservations for Future Extensions
- 14. Appendices

---

## 1. Overview

### 1.1 What is gotwamp

gotwamp is a Go implementation of RFC 5357 TWAMP (Two-Way Active Measurement Protocol). It fills all of the following roles.

| Role (RFC 5357 §1.2) | Implementation in gotwamp | Overview |
|----------------------|------------------|------|
| Control-Client | `gotwampd` sender role (`internal/control/client`) | Requests, starts and stops sessions with the Server over TWAMP-Control (TCP) |
| Session-Sender | `gotwampd` sender role (`internal/sender`) | Sends TWAMP-Test (UDP) packets and computes delay etc. from the reflected packets |
| Server | `gotwampd` reflector role (`internal/control/server`) | Accepts TWAMP-Control and starts Session-Reflectors |
| Session-Reflector | `gotwampd` reflector role (`internal/reflector`) | Timestamps received TWAMP-Test packets and returns them to the sender |
| TWAMP Light Sender / Reflector | Light mode of the above sender / reflector roles | Runs only TWAMP-Test between preconfigured ports, without the control protocol |

The Sender side is split into the resident daemon `gotwampd` and the CLI `gotwamp`. `gotwampd` continuously measures the configured sessions
and outputs to Prometheus / JSON Lines. `gotwamp` connects to `gotwampd` over gRPC (Unix domain socket)
to run ad hoc measurements, manage monitoring sessions, and display statistics and status.

### 1.2 Scope

The scope of the initial releases (v0.1–v0.3) is defined below. For the planned phases, see PLAN.md §6.

| Category | Included | Not included (later) |
|------|------|----------------|
| Protocol | TWAMP-Control (RFC 5357), TWAMP-Test, TWAMP Light, port 862 (RFC 8545) | RFC 5618 Mixed, RFC 5938 Individual Session Control, RFC 6038 Reflect Octets / Symmetrical Size, RFC 7717, RFC 7750 DSCP/ECN, RFC 8186 PTP timestamp |
| Security modes | Unauthenticated | Authenticated, Encrypted (extension points are reserved in the design: §13) |
| Addresses | IPv4, IPv6 (dual stack) | — |
| Platforms | Linux (amd64, arm64) | macOS, Windows |
| Timestamping | SO_TIMESTAMPING software, user-space fallback | hardware timestamping |
| Output | gRPC streaming, Prometheus, JSON Lines, structured logs | OTLP, persistent DB |

In M7 (SPEC 0.3–0.4), the following items from the "Not included" column were implemented: Authenticated / Encrypted / Mixed (RFC 4656 / 5618), RFC 5938, RFC 6038, RFC 7750 (Light), STAMP (the base packet of RFC 8762 / 8972, §6.7), hardware timestamping (§8.1), result persistence with SQLite (§9.4), and gRPC over TCP + mTLS (§9.5). RFC 7717, RFC 8186 and OTLP remain out of scope.

### 1.3 Design Principles

1. **RFC compliance by default, with deviations absorbed by compatibility knobs**: vendor differences are handled through explicit settings (`CMP-xx`), and the default behaviour follows the RFC
2. **Keep the protocol layer pure**: `internal/twamp` does not depend on I/O, time or random numbers, and only converts between byte sequences and structs and validates them. It is hardened with fuzz and golden tests
3. **Guarantee measurement accuracy by design**: transmit and receive times are obtained from the kernel, removing jitter caused by the Go runtime from the calculations. When they cannot be obtained, this is honestly reflected in the Error Estimate
4. **The daemon runs stably for long periods**: lifetimes are managed with per-session goroutines and contexts, and a reload does not stop unrelated sessions
5. **Build a structure that can be extended later**: security modes, extension RFCs, hardware timestamping and STAMP are anticipated at the level of types and extension points

---

## 2. Requirements

The results of the interview (2026-10-03) are organised here as requirements. For the background of the decisions, see PLAN.md §2.

### 2.1 Functional Requirements

| ID | Requirement | Rationale |
|----|------|------|
| FR-01 | Can establish, start and stop sessions as a TWAMP-Control Control-Client, following the procedure of RFC 5357 §3 | Full support |
| FR-02 | As a TWAMP-Control Server, accepts connections from multiple Control-Clients concurrently and handles multiple sessions per connection | Full support |
| FR-03 | As a Session-Sender, sends packets per RFC 5357 §4.1, matches the reflected packets, and computes RTT, one-way delay, jitter, loss, duplicates and reordering | measurement |
| FR-04 | As a Session-Reflector, generates packets per RFC 5357 §4.2 and reflects the received TTL and DSCP | Full / Light support |
| FR-05 | As TWAMP Light, reflects and sends on configured UDP ports without the control protocol. Can listen on multiple ports and multiple addresses | interoperability with vendor equipment |
| FR-06 | Implements Unauthenticated mode. Authenticated / Encrypted / Mixed are implemented in M7 (§5.4; whether they are offered depends on the configuration and keys file) | phased introduction of security modes |
| FR-07 | `gotwampd` can enable the sender / reflector roles individually in the configuration file | two-binary architecture |
| FR-08 | `gotwampd` can run monitoring sessions periodically (periodic) or continuously (continuous), and aggregates and retains the results | continuous monitoring |
| FR-09 | The `gotwamp` CLI connects to `gotwampd` via gRPC over a Unix domain socket, runs ad hoc measurements, and displays the results in real time | ad hoc measurement |
| FR-10 | The `gotwamp` CLI can add, delete, start, stop and list monitoring sessions and show their statistics, show daemon and reflector status, and validate and reload the configuration | operations |
| FR-11 | `gotwampd` exposes metrics in Prometheus format and can write results as JSON Lines | output destinations |
| FR-12 | Operates as sender / reflector over both IPv4 / IPv6, and automatically selects the address family of the destination | — |
| FR-13 | Source address, source port, DSCP, TTL, padding length, send interval, packet count and timeout can be specified per measurement | twping-equivalent usability |
| FR-14 | The reflector has ACLs by source CIDR, limits on the number of connections, sessions and flows, and inactivity timeouts (SERVWAIT / REFWAIT) | protection of the exposed surface |
| FR-15 | A configuration reload (SIGHUP / gRPC) applies only the differences and does not interrupt unchanged sessions | continuous monitoring |

### 2.2 Non-Functional Requirements

| ID | Requirement | Target / Notes |
|----|------|--------------|
| NFR-01 | Measurement accuracy: with kernel timestamps, the added RTT error (error attributable to gotwamp itself) is below 50µs at the median and below 200µs at p99 (veth + netem environment, guideline) | evaluated in PLAN.md M4 |
| NFR-02 | Send interval accuracy: the p99 deviation of send times from the configured interval is below 1ms (when interval ≥ 10ms) | scheduling based on absolute time |
| NFR-03 | Scale: handles 100 sender sessions × 10pps, and 10,000 pps on the reflector, on a single core | guideline |
| NFR-04 | Stability: memory and goroutines do not keep growing over 24 hours of continuous operation | soak test |
| NFR-05 | Startup/shutdown: on SIGTERM, correctly ends in-progress runs with Stop-Sessions and exits within 5 seconds | graceful shutdown |
| NFR-06 | Observability: every run failure reason appears in metrics (reason label) and in logs | — |
| NFR-07 | Security: runs as a non-root user and can bind port 862 with only CAP_NET_BIND_SERVICE. The UDS is 0660 | — |
| NFR-08 | Distribution: statically linked single binary (CGO_ENABLED=0), linux/amd64 and linux/arm64, container image | — |
| NFR-09 | Compatibility: interoperates bidirectionally with perfSONAR twampd / twping. Pitfalls of the researched vendor implementations can be absorbed with compatibility knobs | §11 |
| NFR-10 | Testability: the protocol layer can be tested without I/O, and time-dependent logic can be tested deterministically with `testing/synctest` | — |

---

## 3. Terminology and Measurement Model

| Term | Meaning |
|------|------|
| Control-Client | The side that initiates the TWAMP-Control connection and requests sessions (gotwamp sender) |
| Server | The side that accepts TWAMP-Control connections (gotwamp reflector role) |
| Session-Sender | The side that sends TWAMP-Test packets |
| Session-Reflector | The side that reflects TWAMP-Test packets |
| TWAMP Full | Ordinary TWAMP, in which sessions are negotiated through TWAMP-Control |
| TWAMP Light | A form in which the Session-Sender and Session-Reflector run from preconfiguration without TWAMP-Control (RFC 5357 Appendix I) |
| Session | A single test stream. In Full, it corresponds to one Request-TW-Session. A "monitoring session" in gotwampd is a unit defined in the configuration that repeatedly generates runs |
| run | One execution of a monitoring session (in Full, one control connection; in Light, sending count packets). An ad hoc measurement is one run |
| window | The time span that divides statistics in a continuous session |
| flow | A logical session that the Light reflector identifies by (source address, source port, destination address, destination port) |
| T1 | Sender's transmit time (Timestamp of the Sender packet) |
| T2 | Reflector's receive time (Receive Timestamp of the Reflector packet) |
| T3 | Reflector's transmit time (Timestamp of the Reflector packet) |
| T4 | Sender's receive time (obtained locally) |
| RTT | (T4 − T1) − (T3 − T2). Round-trip delay excluding the processing time inside the reflector |
| One-way delay (OWD) | Forward path T2 − T1, reverse path T4 − T3. Valid only when the clocks of both ends are synchronized |
| IPDV | Delay difference between two consecutive packets (RFC 3393). No synchronization is needed because the clock offset cancels out |
| PDV | Difference between each packet's delay and the minimum delay (RFC 5481) |

```
  Session-Sender                              Session-Reflector
       |                                             |
  T1 --|---- Sender packet (seq, T1, errest) ------->|-- T2 (receive)
       |                                             |   reflector processing
       |<--- Reflector packet (seq', T3, T2,        |-- T3 (send)
  T4 --|      sender seq, T1, sender errest, TTL) ---|
       |                                             |
  RTT = (T4 - T1) - (T3 - T2)
```

---

## 4. Overall Architecture

### 4.1 Process Structure

```
 +------------------------------------------------------------------+
 |  gotwampd (resident daemon)                                      |
 |                                                                  |
 |  sender role                        reflector role               |
 |  +----------------------------+     +--------------------------+ |
 |  | scheduler (mon. sessions)  |     | control server (TCP 862) | |
 |  |   |  run                   |     |   | Request/Start/Stop   | |
 |  |   v                        |     |   v                      | |
 |  | runner                     |     | session reflector (UDP)  | |
 |  |  - control client (TCP)    |     +--------------------------+ |
 |  |  - session sender (UDP)    |     | light reflector(s) (UDP) | |
 |  |   |                        |     +--------------------------+ |
 |  |   v                        |                                  |
 |  | result store (ring buffer) |                                  |
 |  +----------------------------+                                  |
 |        |              |                                          |
 |  Prometheus /metrics  JSON Lines writer                          |
 |                                                                  |
 |  gRPC API server (Unix domain socket)  <--------+                |
 +------------------------------------------------|-----------------+
                                                  |
                                     +------------+-------------+
                                     | gotwamp (CLI, gRPC client)|
                                     +---------------------------+
```

- A single `gotwampd` process can enable both the sender and reflector roles at the same time (FR-07). Each role is switched on/off independently in the configuration file
- `gotwamp` acts only as a gRPC client and never sends or receives packets itself. Ad hoc measurements are also performed by the daemon on its behalf through the `RunTest` RPC (FR-09)
- Public `sender` and `reflector` packages expose curated embedding APIs over the internal engines. The root package owns shared public values; wire codecs, control state machines, socket backends and accumulation remain internal (ADR 0015, [library guide](LIBRARY.md)).

### 4.2 Internal Components of gotwampd

| Component | Package | Responsibility |
|----------------|-----------|------|
| config | `internal/config` | YAML loading, inheritance of defaults, validation, diff computation |
| scheduler | `internal/schedule` | Launches runs for each monitoring session. periodic (frequency) / continuous (back-to-back windows) |
| runner | `internal/sender` | Executes one run: for Full, negotiate as Control-Client → send/receive as Session-Sender → Stop-Sessions. For Light, send/receive only |
| control client | `internal/control/client` | TWAMP-Control Control-Client state machine (§5.6) |
| session sender | `internal/sender` | Transmit scheduler, receive matching, timeouts, per-run statistics (§6.5, §7) |
| control server | `internal/control/server` | TCP listener, per-connection Server state machine (§5.7), Receiver Port allocation |
| session reflector | `internal/reflector` | Per-session reflection in Full mode (§6.4) |
| light reflector | `internal/reflector` | Stateless/stateful reflection on configured UDP listeners (§6.6) |
| tstamp | `internal/tstamp` | UDP socket abstraction. SO_TIMESTAMPING / cmsg (TTL, TOS) / fallback (§8) |
| clock | `internal/clock` | Monitoring of synchronization status and maximum error via adjtimex (§8.3) |
| stats | `internal/stats` | Metric computation (§7) |
| store | `internal/store`, `internal/store/sqlite` | Ring buffer of run summaries / window summaries, subscriptions (watch). With `store.backend: sqlite`, persistence to SQLite (§9.4) |
| keystore | `internal/control/keystore` | Loading of the keys file (pfs format) for A / E / M (§5.4) |
| export/prom | `internal/export/prom` | Prometheus collector (§9.6) |
| export/jsonl | `internal/export/jsonl` | JSON Lines writer (§9.7) |
| api | `internal/daemon` | gRPC service implementation (§9.5), UDS creation and permission setup, TCP + mTLS listener |
| daemon | `internal/daemon` | Wiring, signal handling (SIGTERM/SIGHUP), graceful shutdown |

### 4.3 Dependency Direction

```
cmd/gotwampd, cmd/gotwamp
   └─ internal/daemon, internal/cli
        └─ internal/{schedule, store, export/*, config, api(gen)}
             └─ internal/{control, sender, reflector}
                  └─ internal/{tstamp, clock, stats}
                       └─ internal/twamp/{wire, ntptime, errest}   ← standard library only, no I/O
```

- Public `sender` / `reflector` entry points depend on the corresponding internal engine; engines and codecs depend on canonical root values. The root package uses the standard library only and performs no I/O. Embedding does not pull in the daemon, scheduler, storage or exporters.
- Full reflector composition is owned by `internal/reflector.FullService`, shared by the daemon and public API. Pure validation precedes binding; responder cancellation/Close joins owned workers. Public types and lifecycle are specified in [LIBRARY.md](LIBRARY.md).
- `internal/twamp` does not depend on `net`, `time.Now`, or randomness. Random data (padding / SID / Challenge) is injected by the caller as an `io.Reader`
- `internal/tstamp` and `internal/clock` are Linux-specific (`//go:build linux`). Stubs are provided for non-Linux targets, but their operation is not guaranteed (NFR: Linux only)

### 4.4 Concurrency Model

| Target | goroutine structure | Lifetime management |
|------|---------------|----------|
| Monitoring session | One scheduler goroutine per session. When a run executes, a send goroutine and a receive goroutine are started | The session's `context.Context`. Cancelled on deletion or reload |
| Ad hoc measurement (RunTest) | A runner is started per RPC. Events are sent to the stream | The RPC's context. On client disconnect, the run is aborted and Stop-Sessions is sent |
| control server | listener goroutine + 1 goroutine per connection (state machine) + 1 reflection goroutine per session | Connection context. On disconnect, the sessions under it are released after Timeout |
| light reflector | 1 receive goroutine per listener (N workers if needed). Replies are sent from the same goroutine | daemon context |
| clock monitor | 1 goroutine, 1-second period | daemon context |
| exporter | Prometheus: HTTP server goroutine; JSONL: 1 writer goroutine + buffered channel | daemon context |

Principles:

- Avoid allocations on the hot path (send, receive, reflect). Packet buffers come from `sync.Pool`, and result records are written into preallocated slices
- Hand-offs between goroutines use channels, and shared state is aggregated only at the end of a run (the send side and receive side do not share per-packet flags)
- Receive goroutines always watch the stop channel in a `select`, so that they can stop even if the result channel is blocked
- Sending does not use `time.Ticker`; it aligns to absolute deadlines (`start + i × interval`) with `time.Sleep` + a short spin (§6.5)

---

## 5. TWAMP-Control Specification

This chapter is based on RFC 5357 §3 and RFC 4656 §3. For detailed rationale and Errata, see [research/rfc-twamp-protocol.md](research/rfc-twamp-protocol.md) §2, §8, §9.

### 5.1 Transport and Ports

| Item | Specification |
|------|------|
| Transport | TCP. The Server listens on 862/tcp by default (RFC 8545). The port is configurable |
| Message boundaries | The first 16 octets determine the type and length. The Server reads 16 octets, branches on octet 0 (Command Number), and reads the rest |
| Partial messages | If a complete message does not arrive within `partial_message_timeout` (default 60s), the connection is closed (RFC 4656 §3 says "several minutes, with 30 minutes as the upper limit") |
| DSCP of the control connection | The Control-Client can set an arbitrary DSCP (`control_dscp`, default 0). The Server SHOULD use the DSCP of the SYN for subsequent packets, but in v0.x gotwampd does not set a DSCP and sends with 0. The config key `reflector.control.dscp` is reserved (not implemented, §9.2) |
| TCP keepalive | Enabled on both sides (default: idle 120s / interval 5s / count 6, matching the Junos defaults) |

### 5.2 Common Rules

- Multi-byte integers are in network byte order
- MBZ fields are 0 on send and ignored on receive
- HMAC fields (16 octets) are sent as 0 in Unauthenticated mode and are not checked on receive
- In Authenticated / Encrypted / Mixed, control messages from Server-Start onward are encrypted with AES-128-CBC (one chain per direction continues across messages), and HMAC-SHA1 (truncated to 16 octets) is verified. If the HMAC does not match, the connection is closed immediately and a Warn log is emitted (RFC 4656 §6.10). The first HMAC covers the Start-Time block of Server-Start and Accept-Session 0–31 / Request-TW-Session 0–95; each subsequent HMAC covers from just after the previous HMAC up to just before that message's HMAC field (research §3.3; confirmed with golden data and interop with perfSONAR)
- Unknown Modes bits are ignored (MUST). Unknown Accept values are treated as 1 (Failure)

### 5.3 Message Formats

#### Server Greeting (64 octets, Server → Client)

| Offset | Length | Field | gotwampd value |
|--------|------|-----------|---------------|
| 0 | 12 | Unused | 0 |
| 12 | 4 | Modes | OR of the offered security modes (`reflector.control.modes`; by default Unauthenticated only if there is no keys file, all four if there is) and extensions (`reflector.control.extensions`; bits 4–6 by default) (§5.4). `0` means the connection is refused |
| 16 | 16 | Challenge | `crypto/rand`. Generated even for Unauth |
| 32 | 16 | Salt | `crypto/rand` |
| 48 | 4 | Count | Default 2048 (power of 2, ≥1024). Used as the PBKDF2 iteration count for A / E / M |
| 52 | 12 | MBZ | 0 |

#### Set-Up-Response (164 octets, Client → Server)

| Offset | Length | Field | gotwamp sender value |
|--------|------|-----------|---------------------|
| 0 | 4 | Mode | The selected mode (Client rules in §5.4). If there is no common mode, send Mode `0` and close the connection |
| 4 | 80 | KeyID | 0 for Unauth. For A / E / M, the key identity (UTF-8, up to 80 octets, zero-padded) |
| 84 | 64 | Token | 0 for Unauth. For A / E / M, Challenge(16), AES key(16), and HMAC key(32) encrypted with AES-CBC (IV 0) under the key derived by PBKDF2 |
| 148 | 16 | Client-IV | 0 for Unauth. `crypto/rand` for A / E / M |

#### Server-Start (48 octets, Server → Client)

| Offset | Length | Field | Notes |
|--------|------|-----------|------|
| 0 | 15 | MBZ | |
| 15 | 1 | Accept | 0 = accepted. gotwampd returns 1 for an invalid Mode (a mode not offered in the Greeting). ACL refusal and exceeding the connection limit are signalled not by Server-Start but by a Greeting with Modes=0 (§5.7) |
| 16 | 16 | Server-IV | `crypto/rand` (unused in Unauth, but generated) |
| 32 | 8 | Start-Time | Start time of the gotwampd process (NTP format). 0 when Accept ≠ 0 |
| 40 | 8 | MBZ | |

#### Request-TW-Session (112 octets, Client → Server)

| Offset | Length | Field | gotwamp sender decision rule (§5.9) |
|--------|------|-----------|-----------------------------------|
| 0 | 1 | Command Number | 5 |
| 1 | 1 | MBZ (upper 4 bits) / IPVN (lower 4 bits) | Address family of the control connection. IPv4-mapped IPv6 is normalized to IPv4 (4) |
| 2 | 1 | Conf-Sender | 0 (MUST) |
| 3 | 1 | Conf-Receiver | 0 (MUST) |
| 4 | 4 | Number of Schedule Slots | 0 (MUST) |
| 8 | 4 | Number of Packets | 0 (MUST). The packet count is managed locally |
| 12 | 2 | Sender Port | The UDP send port bound beforehand (default: chosen at random from `sender_port_range` 49152–57343 and bound) |
| 14 | 2 | Receiver Port | The requested reflector-side port (default: same value as Sender Port) |
| 16 | 16 | Sender Address | Local address of the control connection. For IPv4, the first 4 octets, with the remaining 12 set to 0 (MUST). All zeros with `zero_addresses` |
| 32 | 16 | Receiver Address | The Server's address. Same as above |
| 48 | 16 | SID | 0 (MUST) |
| 64 | 4 | Padding Length | Configured number of padding octets (default 27) |
| 68 | 8 | Start Time | Default: 0 (start immediately). With `start_time: now`, the current time at the moment of sending. With 0, packets are not dropped as "arrived before Start Time" even if the Server's clock lags |
| 76 | 8 | Timeout | Seconds in NTP format. Same value as the loss timeout (default 2s) |
| 84 | 4 | Type-P Descriptor | `DSCP << 24` (upper 2 bits 00, next 6 bits are the DSCP). All zeros for DSCP 0 |
| 88 | 2 | Octets to be reflected (RFC 6038) | `octets_to_reflect` (default 0). Can be non-zero only when Reflect Octets has been negotiated |
| 90 | 2 | Length of padding to reflect (RFC 6038) | `padding_to_reflect` (default 0). Same as above. For the lower bound, see §5.5 Accept 3 |
| 92 | 4 | MBZ | 0 |
| 96 | 16 | HMAC | 0 (Unauth) |

#### Accept-Session (48 octets, Server → Client)

| Offset | Length | Field | gotwampd value |
|--------|------|-----------|---------------|
| 0 | 1 | Accept | 0 = accepted. Refusal reasons are in §5.7 |
| 1 | 1 | MBZ | |
| 2 | 2 | Port | On accept: the Receiver Port actually bound (an alternate port different from the requested one may be returned). On refusal: 0 (MUST) |
| 4 | 16 | SID | `IPv4 address(4) ‖ NTP timestamp(8) ‖ random(4)`. In IPv6-only environments, the lower 4 octets of the IPv6 address |
| 20 | 2 | Reflected octets (RFC 6038) | If Reflect Octets has been negotiated, the requested Octets to be reflected is returned as is, regardless of the Accept value (SHALL). 0 if not negotiated. The Client fails if they do not match (`server did not reflect the octets to be reflected`) |
| 22 | 2 | Server octets (RFC 6038) | 0 (gotwampd does not request any). If the Client receives a non-zero value, it places them at the start of the padding to be reflected when sending |
| 24 | 8 | MBZ | |
| 32 | 16 | HMAC | 0 (Unauth) |

#### Start-Sessions / Start-Ack / Stop-Sessions (32 octets each)

| Message | Offset 0 | Offset 1 | 2–3 | 4–7 | 8–15 | 16–31 |
|-----------|----------|----------|-----|-----|------|-------|
| Start-Sessions (C→S) | 2 | MBZ (15) | | | | HMAC |
| Start-Ack (S→C) | Accept | MBZ (15) | | | | HMAC |
| Stop-Sessions (C→S) | 3 | Accept | MBZ | Number of Sessions | MBZ | HMAC |

- Number of Sessions in Stop-Sessions MUST match "the number of sessions that have been started and not yet stopped". On a mismatch, the Server closes the connection
- TWAMP has no reply to Stop-Sessions (only RFC 5938's Stop-N-Ack is a reply)
- Fetch-Session (Command 4) does not exist in TWAMP

#### Start-N-Sessions / Start-N-Ack / Stop-N-Sessions / Stop-N-Ack (RFC 5938, 32 + 16N octets each)

Used only on connections that have negotiated Individual Session Control (Modes bit 4).

| Offset | Length | Field | Notes |
|--------|------|-----------|------|
| 0 | 1 | Command Number | Start-N-Sessions 7, Start-N-Ack 8, Stop-N-Sessions 9, Stop-N-Ack 10 |
| 1 | 1 | Accept (Ack only) / MBZ | |
| 2 | 10 | MBZ | |
| 12 | 4 | Number of Sessions (N) | 1 or more |
| 16 | 16 × N | List of SIDs | |
| 16 + 16N | 16 | HMAC | 0 (Unauth) |

- Stop-N-Sessions has no Accept field. Therefore, under ISC, the stop that "invalidates the results", done via the Accept of Stop-Sessions (RFC 5357 §3.8), cannot be sent
- Acks may be sent split by Accept value (RFC 5938 §3.3). The Client matches them per SID and also accepts split Acks. An Ack for an SID that was not requested, or an Ack of a different kind, is a protocol violation and the connection is closed

### 5.4 Modes

| Bit | Value | Meaning | gotwamp v0.x |
|--------|----|------|--------------|
| 0 | 1 | Unauthenticated | **Supported** |
| 1 | 2 | Authenticated | **Supported (M7)**. gotwampd uses `reflector.control.modes` and `keys_file`; the sender uses `security` / `key_id` |
| 2 | 4 | Encrypted | **Supported (M7)**. Same as above |
| 3 | 8 | Mixed (Unauth test / Encrypted control, RFC 5618) | **Supported (M7)**. Same as above |
| 4 | 16 | Individual Session Control (RFC 5938) | **Supported (M7)**. `extensions: [individual_session_control]` |
| 5 | 32 | Reflect Octets (RFC 6038) | **Supported (M7)**. `reflect_octets`. Requested automatically if either `octets_to_reflect` or `padding_to_reflect` is non-zero |
| 6 | 64 | Symmetrical Size (RFC 6038) | **Supported (M7)**. `symmetrical_size` |
| 7 | 128 | IKEv2 Derived (RFC 7717) | Not supported |
| 8 | 256 | DSCP and ECN Monitoring (RFC 7750) | Not supported in Full (supported in Light only; §9.2) |
| 9 | 512 | PTPv2 Timestamp (RFC 8186) | Not supported |

- Server: offers the configured security modes (`reflector.control.modes`; if empty, Unauthenticated only when there is no `reflector.control.keys_file`, all four when there is). Offering A / E / M requires a key store (perfSONAR pfs format, identity → passphrase). A configuration that contains no security mode at all is a configuration error. For feature bits, it offers `reflector.control.extensions` (by default all of bits 4, 5 and 6) (the Client is the one that chooses, so offering them does no harm to Clients that do not support them). The offered modes and extensions are written to the startup log. If the Set-Up-Response hits any of the following, the Server returns Server-Start Accept = 1, closes the connection, and logs the reason: a bit that was not offered, not exactly one security bit, an unknown KeyID, a mismatched Challenge after decrypting the Token (wrong passphrase), or reuse of a Client-IV (the most recent 4096 are remembered). On successful authentication, Info `client authenticated` (mode, key_id)
- Client: among the modes it accepts, selects one offered by the Server in the order **Encrypted > Authenticated > Mixed > Unauthenticated** (same as twping). If there is no common mode, it sends Mode 0, closes the connection, and fails with the error `no common mode`. In an authenticated mode, if Count is less than 1024, it sends Mode 0 and closes the connection (`greeting count too small`). It also closes the connection if Count exceeds 32768 (RFC 5357 §6)
- In Mixed, the control connection is protected as Encrypted, and test packets are sent in the Unauthenticated format (RFC 5618)
- Feature bits: of the requested extensions (`extensions`), the Client sets in the Set-Up-Response Mode, and uses, only those the Server offered. Extensions that were not offered are not used, and the measurement continues (it does not fail). However, if `octets_to_reflect` / `padding_to_reflect` is specified and Reflect Octets is not offered, it is an error before the Request
- When loading the key store, duplicate identities, non-hexadecimal or empty passphrases, and identities exceeding 80 octets are reported as errors with line numbers. Errors do not include the passphrase

### 5.5 Accept Values

| Value | Meaning | When gotwampd returns it |
|----|------|---------------------|
| 0 | OK | |
| 1 | Failure, reason unspecified | Invalid Mode (Server-Start), Stop/Start consistency error |
| 2 | Internal error | Socket creation failure, etc. |
| 3 | Some aspect of request is not supported | Invalid Command, Conf-* ≠ 0, Slots/Packets ≠ 0, IPVN ∉ {4,6}, IPVN differs from the address family of the control connection, bits other than DSCP in Type-P, Padding Length exceeding the limit (`max_padding`), in Full `header length + Padding Length > max_packet_size` (a length that cannot be reflected; header length is 14 for Unauth and Mixed, 48 for A / E; with Symmetrical Size, 41 / 112 respectively; §6.4), violation of the RFC 6038 padding lower bound (with L as Length of padding to reflect, Padding Length ≤ L; if not Symmetrical Size, additionally Padding Length < 27 + L, or 64 + L for A / E; errata EID 6409) |
| 4 | Permanent resource limitation | Configured limit (max_sessions_per_connection) reached |
| 5 | Temporary resource limitation | No free port, daemon-wide session limit reached |

The Client treats every non-zero value as a failure and records the value in the result (`run_error.accept`).

### 5.6 Control-Client State Machine

```
 CLOSED --connect--> GREETING_WAIT --Greeting(64)--> SETUP_SENT --Server-Start(48)--> IDLE
   IDLE --Request-TW-Session--> ACCEPT_WAIT --Accept-Session(48)--> IDLE
   IDLE --Start-Sessions--> START_ACK_WAIT --Start-Ack(32)--> ACTIVE
   ACTIVE --(all packets sent + timeout elapsed)--> STOP_SENT --Stop-Sessions--> CLOSED
   any --error/timeout--> CLOSED
```

| State | Input | Action | Next state |
|------|------|------|--------|
| CLOSED | Run start | TCP connect (within `control_timeout`). Set DSCP if needed | GREETING_WAIT |
| GREETING_WAIT | Greeting received | Modes==0 → fail `server refused`. No common mode → send Mode 0, fail `no common mode`. Authenticated mode with Count<1024 → send Mode 0, fail `greeting count too small`. Count>32768 → fail. OK → send Set-Up-Response (the chosen mode; for A / E / M with KeyID, Token, Client-IV) | SETUP_SENT |
| SETUP_SENT | Server-Start received | Accept≠0 → fail `server-start accept=N`. Record Start-Time | IDLE |
| IDLE | Session request | Bind the Sender Port, then send Request-TW-Session | ACCEPT_WAIT |
| ACCEPT_WAIT | Accept-Session received | Accept≠0 → fail `accept-session accept=N` (the connection is closed). Port differs from the request → adopt it if `accept_alternate_port` (default true), fail if false. Record SID. Point the UDP socket at (server addr, Port) | IDLE |
| IDLE | All sessions accepted | Send Start-Sessions | START_ACK_WAIT |
| START_ACK_WAIT | Start-Ack received | Accept≠0 → fail. Start the Session-Sender (start time = max(now, Start Time)) | ACTIVE |
| ACTIVE | Sending complete + loss timeout elapsed, or abort (SIGTERM / RPC cancel) | Send Stop-Sessions (Accept 0, Number of Sessions = number started), TCP close | CLOSED |
| ACTIVE | TCP disconnect detected | End the run as `control lost`. Results collected so far are saved with the `invalid=true` flag | CLOSED |
| any | Read timeout (`control_timeout`, default 15s) | Fail `control timeout in <stage> (after <timeout>)` (e.g. `control timeout in greeting (after 15s)`; stage uses the same names as `RunError.stage`), TCP close | CLOSED |

- While ACTIVE, nothing is sent on the control connection (RFC 4656 §3.4). However, reception continues in order to detect disconnection by the Server
- The default is one control connection and one session per run. Multiple sessions on the same connection and connection reuse are deferred (§13)
- There is a setting to wait `end_delay` (default 0s; twping uses 1s) before sending Stop-Sessions

Client behavior when Individual Session Control (RFC 5938) has been negotiated:

- Use Start-N-Sessions / Stop-N-Sessions instead of Start-Sessions / Stop-Sessions (commands 2 / 3) (MUST NOT in RFC 5938 §3.1). Calling Start / Stop during ISC is a state error (`ErrState`)
- Request-TW-Session and Start-N / Stop-N can be sent even while ACTIVE. During each exchange, disconnect monitoring is paused, and it resumes when the exchange finishes
- If a Request-TW-Session is refused, the connection is not closed (because other sessions are running). This is an exception to "the connection is closed" in ACCEPT_WAIT in the table above
- If some sessions are refused in a Start-N-Ack, return the sessions that could be started together with an error carrying the Accept value
- Because Stop-N-Sessions has no Accept field, a stop that "invalidates the results" cannot be sent even for an aborted run (§5.3)
- Since the gotwampd sender uses one session per run, even with ISC negotiated it only starts and stops one session with Start-N / Stop-N

### 5.7 Server State Machine

```
 LISTEN --accept--> GREETING_SENT --Set-Up-Response--> STARTED --(send Server-Start)--> REQUEST_WAIT
   REQUEST_WAIT --cmd 5--> (send Accept-Session) --> REQUEST_WAIT
   REQUEST_WAIT --cmd 2--> (send Start-Ack, start reflecting) --> ACTIVE
   ACTIVE --cmd 3--> (release after Timeout) --> REQUEST_WAIT
   ACTIVE --REFWAIT expired for all sessions--> REQUEST_WAIT
   REQUEST_WAIT --SERVWAIT expired--> CLOSED
   any --invalid message / disconnect--> CLOSED
```

| State | Input | Action | Next state |
|------|------|------|--------|
| LISTEN | TCP accept | ACL check (if refused, send a Greeting with Modes=0 and close). Same if the connection limit is reached. Send Greeting. Start SERVWAIT | GREETING_SENT |
| GREETING_SENT | Set-Up-Response (164) | Mode==0 → close. Mode not offered, or A / E / M authentication failure (§5.4) → Server-Start Accept=1 → close. OK → Server-Start Accept=0 (for A / E / M, subsequent control messages are encrypted and HMAC-verified) | REQUEST_WAIT |
| REQUEST_WAIT | cmd 5 (Request-TW-Session) | Validate (Accept 3 conditions in §5.5). If Sender Address is 0, use the peer address of the control connection. Allocate the Receiver Port (§5.8), bind the UDP socket, generate the SID, send Accept-Session | REQUEST_WAIT |
| REQUEST_WAIT | cmd 2 (Start-Sessions) | If there are no accepted sessions, Start-Ack Accept=1. Otherwise, make all sessions ACTIVE and send Start-Ack Accept=0. Stop SERVWAIT, start REFWAIT for each session | ACTIVE |
| REQUEST_WAIT | cmd 3 (Stop-Sessions) | If Number of Sessions is 0, ignore it and continue. If ≠0, invalid → close | REQUEST_WAIT / CLOSED |
| REQUEST_WAIT | Other Commands (0,1,4,6,7–10,11,unknown) | Send Accept-Session Accept=3, then close because the length is unknown | CLOSED |
| REQUEST_WAIT | SERVWAIT (default 900s) expired | close | CLOSED |
| ACTIVE | cmd 3 | Number of Sessions ≠ number started → close. Match → move each session to DRAINING (keep reflecting for Timeout, ignore afterwards) and resume SERVWAIT | REQUEST_WAIT |
| ACTIVE | Anything other than cmd 3 | Protocol violation → close | CLOSED |
| ACTIVE | REFWAIT (default 900s) expired for all sessions | Release sessions, resume SERVWAIT | REQUEST_WAIT |
| any | TCP disconnect / read error | Release the sessions under it after Timeout elapses | CLOSED |

- After Stop-Sessions, a new Request-TW-Session is accepted on the same connection (the RFC does not state this explicitly, but this matches OWAMP semantics and the behavior of the Junos client (which keeps using the same connection with test-count 0))
- Test session states: `ACCEPTED` (SID issued, socket bound) → `ACTIVE` (reflecting) → `DRAINING` (reflecting after Stop until Timeout) → `RELEASED`. `ACTIVE → RELEASED` happens when REFWAIT expires
- Test packets that arrive before Start-Sessions are not reflected (unspecified in the RFC; interpreted conservatively). From Start-Sessions onward, packets that arrive before the Start Time of the Request-TW-Session are also reflected by default (§6.4, CMP-26)

Server behavior on connections that have negotiated Individual Session Control (RFC 5938):

| State | Input | Action |
|------|------|------|
| REQUEST_WAIT / ACTIVE | cmd 5 | Accepted even while ACTIVE (reflection of other sessions continues) |
| REQUEST_WAIT / ACTIVE | cmd 7 (Start-N-Sessions) | Start the accepted-but-not-started sessions and send Start-N-Ack Accept=0. Unknown SIDs and already-started sessions get Accept=1 in a separate Start-N-Ack (split by Accept value). If even one session is started, stop SERVWAIT |
| REQUEST_WAIT / ACTIVE | cmd 9 (Stop-N-Sessions) | Move started sessions to DRAINING (reflecting for Timeout), release accepted-but-not-started sessions, and send Stop-N-Ack Accept=0. Unknown or already-stopped ones get Accept=1. Once all sessions have stopped, resume SERVWAIT |
| any | cmd 7 / 9 with N = 0, or N > `max_sessions_per_connection` | Protocol violation → close |
| any | cmd 2 / 3 | Protocol violation → close (RFC 5938 §3.1) |

- On connections that have not negotiated ISC, commands 7–10 are handled as before: send Accept-Session Accept=3 and close
- The per-connection receive buffer is `max(164, 32 + 16 × max_sessions_per_connection)` octets (default 288)

### 5.8 Receiver Port Allocation (Server)

1. If the requested Receiver Port is non-zero and can be bound, use that value (`honor_requested_port: true`, default)
2. If it cannot be used, or the request is 0, pick at random from `test_port_range` (default `20000-29999`) and scan until a bind succeeds. Return the alternate port with Accept=0 (permitted by RFC 5357 §3.5)
3. If there is no free port in the range, Accept=5, Port=0

The bind address is the same address, in the same address family, as the local address of the control connection (if a Receiver Address is specified it takes precedence, but if it is not an address of the local host, Accept=3).

### 5.9 Request-TW-Session Field Decision Rules (Client)

| Field | Default | Config key | Notes |
|-----------|------|---------|------|
| Sender Port | Chosen at random from `sender_port_range` (default 49152–57343) and bound | `sender_port`, `sender_port_range` | The range is the intersection of 862 or 49152–65535, accepted by SR Linux / Junos Evolved, and the IOS-XR upper limit of 57343. If the peer defines test-sessions by 4-tuple, pin it with `sender_port` |
| Receiver Port | Same value as Sender Port | `receiver_port` | Same behavior as twping / Junos. Requesting `862` is also possible (RFC 8545) |
| Sender/Receiver Address | Addresses of both ends of the control connection | `zero_addresses` | Set to 0 for NAT traversal (equivalent to twping `-Z`) |
| Padding Length | 27 | `padding` | 0–9000. To make the length equal to the reflected packet, 27 or more for Unauth and 64 or more for A / E (with the default 27 left as is, A / E becomes asymmetric: sender 75 octets, reflector 112 octets; the measurement itself is still correct). Whether to default to 64 when padding is not specified for A / E is undecided (a decision for the daemon / config side; see the note in Appendix A) |
| Start Time | 0 | `start_time` (`zero` / `now`) | twping sends the real time (now + delay), but on a Server that enforces Start Time, packets are dropped as arriving before Start Time if the clock lags (SR Linux's `enforce-test-session-start-time` defaults to true). 0 means immediate. The gotwampd reflector does not enforce it by default (§6.4, CMP-26) |
| Timeout | 2s | `timeout` | Shared with the loss timeout |
| Type-P | DSCP 0 | `dscp` | 0–63. Names such as `ef`, `af41`, `cs5` are also accepted |
| Modes feature bits | None | `extensions` (`individual_session_control` / `reflect_octets` / `symmetrical_size`) | Full only. Only those offered by the Server are used (§5.4) |
| Octets to be reflected / Length of padding to reflect | 0 / 0 | `octets_to_reflect` / `padding_to_reflect` (0–65535) | Full only. If either is non-zero, Reflect Octets is requested. `padding_to_reflect` must be smaller than `padding`, and if not Symmetrical Size, `padding` ≥ 27 + L (64 + L for A / E) |

### 5.10 Error Handling and Robustness

- The Server applies `partial_message_timeout` to reads on each connection, and closes the connection immediately on an HMAC check failure (after Auth is introduced) or an invalid length
- The Server limits the number of connections (`max_connections`, default 64), sessions per connection (`max_sessions_per_connection`, default 16), and daemon-wide sessions (`max_sessions`, default 1024) (the resource-limiting requirement of RFC 4656 §6.5)
- The upper limit of Padding Length is `max_padding` (default 9000, at least 1). Exceeding it gives Accept=3. In Full, a request with `14 + Padding Length > max_packet_size` is also given Accept=3 (with both at their defaults, 8987–9000 fall into this; if accepted, every packet would be dropped for exceeding the length)
- Fuzz tests ensure that invalid control messages do not cause a panic. No tests send malformed packets to other vendors' equipment (because of DoS vulnerabilities such as Cisco CVE-2025-20154)

---

## 6. TWAMP-Test Specification

This chapter is based on RFC 5357 §4 and RFC 4656 §4. Rationale: research/rfc-twamp-protocol.md §4–§7.

### 6.1 Transport and IP Layer

| Item | Specification |
|------|------|
| Transport | UDP. The Session-Sender sends and receives on the Sender Port; the Session-Reflector sends and receives on the Receiver Port |
| TTL / Hop Limit | The Sender sends with 255 (SHOULD). `ttl` can change it to 1–255 (for comparison with, e.g., the Junos client default of 64). The Reflector replies with 255 |
| DSCP | The Sender sets Type-P (the configured `dscp`) via IP_TOS / IPV6_TCLASS. The Reflector follows the policy in §6.4. ECN is always Not-ECT (00) |
| Source verification | The Sender accepts reflected packets only from (reflector address, Receiver Port). For the Reflector, see §6.4 / §6.6 |
| Socket | One socket per session. No `connect()`; `sendmsg`/`recvmsg` are used so that ICMP errors do not make reads fail (the same decision as perfSONAR) |
| DF bit | Not set (some devices do not reflect fragmented packets, so keeping padding within the MTU is the user's responsibility. The CLI warns above 1472) |

### 6.2 Session-Sender Packet (Unauthenticated, minimum 14 octets)

| Offset | Length | Field | Content |
|--------|------|-----------|------|
| 0 | 4 | Sequence Number | Starts at 0 and increments by 1 within the session |
| 4 | 8 | Timestamp | T1. The current time taken immediately before sending (NTP 64-bit) |
| 12 | 2 | Error Estimate | Computed as in §8.4 |
| 14 | Padding Length | Packet Padding | `padding_fill: auto` (default) uses pseudo-random data in Full (ChaCha8 from `math/rand/v2`; the RFC SHOULD) and all zeros in Light (same as STAMP; random data is risky because Nokia reflectors detect STAMP TLVs by the bit pattern `100xxxxx` at offset 44). `random` / `zero` can be set explicitly (all zeros is an option the RFC makes MUST) |

The Authenticated / Encrypted format (minimum 48 octets: Seq(4) MBZ(12) Timestamp(8) ErrEst(2) MBZ(6) HMAC(16) Padding) is implemented in M7 (`wire.AuthSenderPacket`).

- Test packet keys are derived from the SID (the AES key via AES-ECB, the HMAC key via AES-CBC with IV 0; RFC 5357 §4.1.1). The key schedule is built only once per session
- Authenticated encrypts the first 16 octets with AES-ECB and appends the HMAC. Encrypted encrypts octets 0–31 of the Sender packet with AES-CBC and appends the HMAC
- **Deviation from the text of RFC 5357 §4.2**: the text can be read as making the encrypted range of an Encrypted Sender packet 96 octets, but we use 32 octets to match the reference implementation (perfSONAR) (research §4.7). This interpretation has been confirmed by bidirectional Encrypted interop with perfSONAR
- T1 is written before sealing (encryption and HMAC). When resending after ENOBUFS, the packet is sealed again
- Mixed-mode test packets use the Unauthenticated format

When RFC 6038 extensions are negotiated (§5.4):

- **Symmetrical Size**: 27 octets of MBZ (64 for A / E) are inserted right after the header, followed by Padding Length octets of padding. The sender's packet is 41 + padding (112 + padding for A / E), the same length as the reflector's reply. **Padding Length does not include this MBZ** (RFC 6038 §4.2, errata EID 5549 / 6408 / 6409 / 6410)
- **Reflect Octets**: the first `padding_to_reflect` octets of the padding are to be reflected. If the Server octets (Accept-Session) are non-zero, those 2 octets are placed at the start of the octets to be reflected. A reply whose padding does not start with the octets to be reflected is counted in `reflected_mismatch` (it is still used for measurement; §7.2)

### 6.3 Session-Reflector Packet (Unauthenticated, minimum 41 octets)

| Offset | Length | Field | Content |
|--------|------|-----------|------|
| 0 | 4 | Sequence Number | The Reflector's own per-session counter (from 0). In Light without state, a copy of the Sender's value (§6.6) |
| 4 | 8 | Timestamp | T3. Taken immediately before sending |
| 12 | 2 | Error Estimate | The Reflector's error. Applies to both T2 and T3 |
| 14 | 2 | MBZ | |
| 16 | 8 | Receive Timestamp | T2. Receive time (kernel timestamp preferred) |
| 24 | 4 | Sender Sequence Number | Copy of the received packet's Sequence Number |
| 28 | 8 | Sender Timestamp | Copy of the received packet's Timestamp |
| 36 | 2 | Sender Error Estimate | Copy of the received packet's Error Estimate |
| 38 | 2 | MBZ | |
| 40 | 1 | Sender TTL | TTL / Hop Limit of the received IP header (IP_RECVTTL / IPV6_RECVHOPLIMIT). 255 if unavailable (MUST) |
| 41 | variable | Packet Padding | Truncation rule in §6.4 |

With Symmetrical Size the reply format is the same, and the padding is not moved (§6.4 step 5).

The Authenticated / Encrypted format is minimum **112** octets (the 104 in RFC 5357 is corrected by Errata 5045; perfSONAR also uses 112). Implemented in M7 (`wire.AuthReflectorPacket`). Encrypted encrypts octets 0–95 with AES-CBC and appends the HMAC. T3 is within the protected range, so the packet is sealed after T3 is written (when resending after ENOBUFS, T3 is retaken and the packet is sealed again). The RFC 7750 S-DSCP-ECN is at offset 81.

### 6.4 Session-Reflector Behavior Rules

The following is done for each received packet. The order is fixed for accuracy.

1. Take T2 at the moment of reception (kernel RX timestamp, or `time.Now()` right after `recvmsg` if unavailable)
2. Verify the source (Full: §6.4.1, Light: §6.6). On mismatch, drop and increment `dropped_total{reason}`
3. Verify the length. Unauth drops anything shorter than 14 octets. Authenticated / Encrypted test packets shorter than 48 octets or with an HMAC mismatch are dropped and counted as `malformed` (for whether an HMAC mismatch is split into a separate reason, see the note in §9.6). Such packets do not count as session reception (they do not extend REFWAIT) and are not used to pin the source under `verify_sender: none` (so forged packets cannot hijack a session). The upper bound is `max_packet_size` (default 9000). Full sessions are refused with Accept=3 at Request-TW-Session time when `header length + Padding Length > max_packet_size` (header length is 14 for Unauth and Mixed, 48 for A / E; with Symmetrical Size, 41 / 112 including the MBZ) (§5.5), so valid packets of an accepted session fit within this bound. All other lengths are accepted, and everything after the first 14 octets is treated as padding (this also lets the 44-octet packets of a STAMP sender be reflected)
4. Build the reply in the receive buffer: copy the Sender Sequence Number / Timestamp / Error Estimate, set Sender TTL from the cmsg, and zero the MBZ fields
5. Padding: copy the sent packet's padding to offset 41 onward and **truncate from the end (the higher-numbered side)** so that the reply length equals the received length (RFC 5357 §4.2.1 SHOULD). If the received length is less than 41, the reply is 41 octets (no padding); i.e. `reply_len = max(recv_len, 41)`. For Authenticated / Encrypted, the padding is moved to offset 112 onward and the last 64 octets are truncated, so `reply_len = max(recv_len, 112)`. Since the received length is at least 48, the amplification factor is at most 112/48 ≈ 2.3×, but only a Sender holding the key can pass the HMAC, so this cannot be used for reflection attacks. In Symmetrical Size (RFC 6038) sessions, the padding after the sender's MBZ (27 / 64) is placed as is at offset 41 (112 for A / E) onward, with no moving or truncation (reply length = received length). Reflect Octets is satisfied by this rule of keeping the start and truncating the end, together with the lower-bound length check at Request time (§5.5)
6. Set the Sequence Number (per-session counter)
7. Determine the DSCP: in Full, the Type-P from Request-TW-Session (MUST). In Light, `dscp_policy` (default `copy`: read the received packet's DSCP from IP_RECVTOS / IPV6_RECVTCLASS and set it with a per-packet cmsg; `fixed: N` is also possible)
8. Take T3, write it into Timestamp, and `sendmsg` immediately. No other processing is done between taking T3 and sending
9. If sending fails with ENOBUFS / EAGAIN, retake T3 and resend exactly once

Other rules:

- Every received packet is reflected, even duplicates (the Reflector keeps no records)
- Packets arriving more than Timeout after Stop-Sessions is received, and packets after REFWAIT expires, are not reflected
- Full sessions reflect from the moment Start-Sessions (Start-N-Sessions in ISC) is received. Packets arriving before Start-Sessions are dropped as `not_started`. **By default the Start Time of Request-TW-Session is not checked**: Start Time is on the sender's clock while the arrival time is on our clock, so if the sender's clock is ahead, the first packet sent right after Start Time becomes an "arrival before Start Time". As a real example, perfSONAR twping (owamp) adds the kernel's in-progress NTP offset (the adjtimex offset, a few ms) to its own time, so it ran a few ms ahead of our clock and lost the first 1–2 packets (interop testing in 2026-10). The sender cannot send before Start-Ack, so reflecting from Start-Sessions onward is sufficient
- The compatibility knob `reflector.control.enforce_start_time: true` (CMP-26, default false) restores the old behavior of dropping packets as `not_started` until Start Time (equivalent to SR Linux's `enforce-test-session-start-time`). The reflector package (`FullConfig.EnforceStartTime`) is implemented, but wiring to the config key is not complete (as of v0.4)
- Packets whose Error Estimate Multiplier is 0 are "corrupt" per the RFC, but because of a known NX-OS bug (CSCwa79808) the Reflector reflects them, and the Sender handles them by the rules in §7.6

#### 6.4.1 Source Verification in Full Mode

`verify_sender` (default `address`):

| Value | Acceptance condition |
|----|----------|
| `address` | Source IP matches the Sender Address (the control connection peer if 0) |
| `address_port` | In addition to the above, the source port matches the Sender Port |
| `none` | No verification (for PAT environments; thereafter pinned to the first source received) |

### 6.5 Session-Sender Behavior Rules

Send goroutine:

1. Fix the run start time `t0`, and set the scheduled send time of the i-th packet to `t0 + i × interval` (an absolute schedule with no accumulated error)
2. `time.Sleep` until `spin_before` (default 200µs) before the scheduled time, then wait the rest with a spin interleaved with `runtime.Gosched`
3. Take T1, encode the packet, and `sendmsg`. If sending fails (ENOBUFS), retake T1 and resend once. If it still fails, record it as `send_error` (distinct from lost)
4. Write the send record (seq, T1, user-space send time) to a ring buffer
5. If kernel TX timestamps are enabled, read the errqueue and record T1' for the matching seq (§8.2)
6. After sending `count` packets, wait `timeout` and end the run. In continuous mode, aggregation is split per window

Receive goroutine:

1. Receive with `recvmsg` and take T4 immediately (kernel RX timestamp preferred)
2. Drop if the source is not the expected (address, port)
3. Replies that cannot be decoded (e.g. shorter than 41) are dropped as `malformed`, replies arriving after `T1 + timeout` as `late` (that seq stays lost), and replies whose Sender Sequence Number matches no packet in flight (not yet sent, or a seq whose state has already been discarded) as `stray`. Duplicates arriving after the timeout also become `late` if the seq state still exists, or `stray` if it has been discarded (they are not counted as duplicates). The sum of the three is `packets_stray_total` (§9.6), and the summary reports `late` / `stray` separately
4. If the seq has already been accepted, record it as a duplicate (reverse-path duplicate if the Reflector Sequence Number is identical, forward-path duplicate if it differs)
5. On first acceptance, finalize the packet record and update the metrics

Timeout decision: each sent packet is considered lost if it has not been accepted by `T1 + timeout`. The receive goroutine makes this decision periodically (the shorter of `interval` or 100ms) and finalizes packets in order.

Run end condition: all sends complete and (all packets finalized, or final send + timeout elapsed). On abort, unfinalized packets are not counted as lost but treated as `aborted`.

### 6.6 TWAMP Light Specifics

Light has no control protocol, so the following are given by configuration.

| Role | Configuration |
|------|------|
| Sender | Destination address, destination UDP port (default 862), source address/port (optional), padding, DSCP, TTL, interval, count/continuous, timeout |
| Reflector | List of listeners (address:port) (default `[::]:862` and `0.0.0.0:862`), ACL, DSCP policy, flow management parameters |

Reflector flow management:

- Flow key = (source address, source port, destination address, destination port)
- `sequence: per_flow` (default): the Reflector Sequence Number counts from 0 per flow. A flow is discarded after `flow_timeout` (default 900s = REFWAIT) of inactivity. When `max_flows` (default 65536) is exceeded, new flows are treated as stateless (copying the Sender's value) and `flows_overflow_total` is incremented
- `sequence: copy_sender`: always copy the Sender Sequence Number (the stateless behavior of RFC 5357 Appendix I; the same as STAMP stateless)
- Per-flow statistics (received count, reflected count, last receive time) are available via `ReflectorService.ListReflectorSessions`

Reflector protection:

- ACL (`acl: [cidr...]`; empty allows all) and a per-source token bucket (`rate_limit_pps`, default 0 = unlimited)
- The reply length does not exceed the received length (41 only when received length < 41). The amplification factor is at most 41/14 ≈ 2.9×, and the documentation recommends configuring an ACL or rate limit. In STAMP mode (§6.7) the reply length is max(received length, 44), so at most 44/14 ≈ 3.1×
- Binding destination port 862 requires CAP_NET_BIND_SERVICE (§9.10)

Notes for the Light Sender:

- When the peer defines a test-session by 4-tuple, as with Cisco IOS-XE TWAMP Light test-sessions, Huawei, and SONiC, fix `sender_port`. With one socket per session, the source port does not change during a run
- The Nokia SR OS Light reflector listens only on UDP 862 or 64364–64373 and requires an allow list of source prefixes. SR OS / SR Linux senders send from source ports 64374–64383 with default destination 862. gotwampd's default Light listener (862) can receive these
- The peer reflector's flow state may expire quickly (SR OS inactivity-timeout is 10–100s, default 100s). If the Reflector Sequence Number rolls back during a run, the Sender sets `loss_split_valid=false` to disable reverse-path loss estimation, without affecting duplicate classification
- Nokia reflectors reply according to the Z bit (timestamp format) of the received packet. The v0.x Sender sends only the NTP format

### 6.7 STAMP (RFC 8762 / RFC 8972)

M7 implements STAMP base packet compatibility as a switch on TWAMP Light (ADR 0012). Unauthenticated only.

**STAMP mode of the Light reflector** (`reflector.light.stamp: true`, default false):

- Replies use the format of RFC 8762 §4.3.1: field positions are the same as TWAMP, with offsets 41–43 being MBZ (0). The SSID of RFC 8972 §3 (offset 14–15) is copied from the received packet (MUST)
- The reply length is `max(received length, 44)`. For requests shorter than 44 octets, a zero-filled base (44) is returned; anything after the base is copied as is
- TLVs: the octets right after the base are treated as TLVs only when they match the sender TLV flag pattern `100xxxxx` (U=1, M=0, I=0) (the same rule as SR Linux). TLV contents are not processed; the U flag is set on TLVs that are not understood, and the M flag is set on a TLV with an invalid length, after which nothing further is processed (RFC 8972 §4.1). Any other pattern is left untouched as TWAMP padding
- The Sequence Number follows `light.sequence` (per_flow = stateful, copy_sender = stateless)
- Cannot be combined with `light.dscp_ecn_monitoring` (STAMP makes offset 41 MBZ; configuration error)

**STAMP sending by the Light sender** (test parameters `stamp: true` / `ssid`, CLI `gotwamp ping --light --stamp [--ssid N]`):

- Sends a 44-octet base carrying the SSID (`ssid`, 0–65535; 0 means none). `padding` must be 0 or at least 4 and is sent as a single Extra Padding TLV (including the 4-octet TLV header; RFC 8972 §4.2, with the U flag). The packet length is 44 + padding
- Replies are read as TWAMP replies (the same format on the wire). A reply whose SSID is neither 0 nor our own SSID is dropped as a reply for another session (handled the same as a reply from a different source)
- `ssid` requires `stamp`. `stamp` cannot be used in Full and cannot be combined with `dscp_ecn_monitoring`

**The Light reflector in TWAMP mode** (default) also does not refuse by received length, so it can reflect a STAMP sender's base packets (the "on the wire compatibility" of RFC 8972 §3, CMP-15). In that case a 44-octet request is treated as "14 octets + 30 octets of padding", and the reply is 44 octets with offsets 41–43 holding the first 3 octets of the request's padding (MBZ = 0 in STAMP). The SSID is not copied.

Still out of scope: STAMP authenticated mode (HMAC-SHA-256), actual TLV processing (generating and interpreting TLVs other than Extra Padding), reflector-side session identification by SSID, and combination with Full (TWAMP-Control). Interop with real SR Linux hardware is unverified.

---

## 7. Measurement Model and Statistics

### 7.1 Per-Packet Metrics

The following are computed each time a reflected packet is accepted. T1..T4 are defined in §3.

| Metric | Formula | Requirement | Notes |
|------|----|------|------|
| `rtt` | (T4 − T1) − (T3 − T2) | Sender clock only | Primary metric. Excludes processing time inside the Reflector |
| `rtt_raw` | T4 − T1 | Same as above | Same definition as pscheduler's `rtt`. Output in JSON only |
| `fwd_delay` (forward-path one-way delay) | T2 − T1 | Clock synchronization at both ends | Invalidity conditions in §7.4 |
| `rev_delay` (reverse-path one-way delay) | T4 − T3 | Same as above | |
| `reflector_delay` | T3 − T2 | Reflector clock only | Abnormally large values indicate Reflector load |
| `clock_offset_hint` | ((T2 − T1) − (T4 − T3)) / 2 | — | Clock offset + path asymmetry/2. Reference value |
| `fwd_hops` | 255 − Sender TTL (offset 40 of the reflected packet) | Sender sends with TTL 255 | `ttl − Sender TTL` if `ttl` is changed |
| `rev_hops` | 255 − TTL of the received IP header | Reflector sends with 255 | Meaningless with devices that reply with 64, such as Cisco RSP3. Reference value |
| `max_clock_error` | Sender ErrEst + Reflector ErrEst (in seconds) | — | Upper bound on one-way delay error. If either end's Multiplier is 0 (error unknown, §7.6), it becomes the sum of only the known ends and is no longer an upper bound, so `max_clock_error_unknown=true` is attached. In the summary, true if even one accepted packet is affected |

For T1, the kernel TX timestamp (T1') is used when available; otherwise the user-space time written into the packet is used (§8.2).

### 7.2 Loss, Duplicates, and Reordering

| Item | Definition |
|------|------|
| `sent` | Number of packets for which sending was attempted (excluding send errors) |
| `received` | Number of unique Sender Sequence Numbers accepted |
| `lost` | Number of sent packets not accepted by `T1 + timeout` (round-trip loss per RFC 6673) |
| `pending` | Number of sent packets that have not yet reached `T1 + timeout` at aggregation time (window summaries, aborted runs). Counted as neither lost nor received |
| `loss_ratio` | lost / (sent − pending). If the run ends normally, pending = 0, so lost / sent |
| `late` / `stray` | Number of replies dropped in step 3 of the receive procedure in §6.5. Not counted as loss or duplicates |
| `reflected_mismatch` | With RFC 6038 Reflect Octets, the number of replies in which the padding requested for reflection (`padding_to_reflect` octets) did not come back at the start of the reply's padding (§6.2). The reply is used for measurement (counted in received) |
| `dup_forward` | Number of additional packets with the same Sender Sequence Number but a different Reflector Sequence Number (duplicated on the forward path) |
| `dup_reverse` | Number of additional packets with both the Sender and Reflector Sequence Numbers identical (duplicated on the reverse path) |
| `reordered` | RFC 4737 §3.3: look at Sender Sequence Numbers in arrival order and count those smaller than `NextExp` as reordered. Comparison uses RFC 1982 serial number arithmetic, so it is correct even when numbers wrap past 2^32 |
| `reordered_reverse` | The same procedure applied to the Reflector Sequence Number (valid only with a stateful reflector). Reverse-path reordering |
| `lost_forward` / `lost_reverse` (estimated) | Estimated for stateful reflectors following the approach of RFC 8762 §4. With the first reflected number of the window (or run) as base B: `reflected = (R_max − B + 1) − number of forward-path duplicate copies`, `lost_forward = sent − reflected`, `lost_reverse = reflected − received` (R_max is the largest accepted Reflector Sequence Number). How the base is determined and when it cannot be computed are described below. `loss_split_valid=true` only when it could be computed |
| `loss_split_valid` | Whether `lost_forward` / `lost_reverse` are valid. Always false with a `copy_sender` reflector |

Delay metrics use only the first-arriving packet for each Sender Sequence Number (RFC 7679 §3.5).

Forward-path loss does not consume a reflected number, while reverse-path loss leaves a gap in reflected numbers. When there is loss, whether packets at the start or end were reflected with only their replies lost cannot be determined from the received replies, so the forward/reverse breakdown is computed conservatively by the following rules.

1. Base B is determined only when (a) it is the first window of a new Full session (B = 0), (b) the end of the previous window is determined (B = previous window's R_max + 1), or (c) the window has neither loss nor pending (B = smallest accepted R; the breakdown is trivially 0 / 0). Otherwise `loss_split_valid=false`. In Light, the sender has no way to know where a flow's reflected numbers start, so no breakdown is reported for windows with loss
2. The window end (R_max + 1) is carried over to the next window only when the base is known, there is neither loss nor pending, and the observed R values run contiguously from B to R_max (including forward-path duplicate copies). A window with loss does not pass a base to the next window
3. No breakdown is computed when there is an R smaller than B (overtaking across a window boundary), R rolls back, there is loss while R = S throughout (indistinguishable from copy_sender), or there are pending packets
4. Packets reflected at the end whose replies were lost are counted as forward-path loss (same as RFC 8762 §4). A copy duplicated on the forward path whose reply was also lost cannot be observed, so it is counted as reverse-path loss

### 7.3 Jitter (Delay Variation)

Metrics that do not require synchronization are primary, and each name makes its definition explicit.

| Name | Definition | Source |
|------|------|------|
| `ipdv_rtt` | Difference in rtt between 2 packets accepted consecutively in send order: D(i) = rtt(i) − rtt(i−1). Aggregated as mean(|D|), max(|D|), p95(|D|) | RFC 3393 / RFC 5481 §4.1 |
| `ipdv_fwd` / `ipdv_rev` | The same procedure applied to fwd_delay / rev_delay. Clock offset cancels out in the difference, so no synchronization is needed (frequency error remains) | RFC 3393 §1.3 |
| `pdv_rtt` | rtt − min(rtt). Aggregated as p95, p99, p99.9 | RFC 5481 §4.2 |
| `jitter_rfc3550` | In arrival order, J = J + (|D| − J)/16 (D is the difference between the arrival interval and the send interval = the difference in rtt). J at the end of the run | RFC 3550 §6.4.1 (same as twampy) |
| `rtt_stddev` | Sample standard deviation of rtt | Same as common exporters |

perfSONAR twping's "jitter = P95 − P50" is nearly synonymous with the p95 of `pdv_rtt`, so it is included in the summary under the alias `jitter_p95_p50`.

### 7.4 One-Way Delay Validity

`fwd_delay` / `rev_delay` are `valid=true` only when all of the following hold.

1. The S bit of the Sender's Error Estimate is 1 (the local host's clock is synchronized)
2. The S bit of the Reflector's Error Estimate is 1
3. The value is not negative (a negative value indicates clock offset; counted in `negative_owd_total`)

Values are output even when `valid=false`, but the CLI displays them with `(unsync)` and they are not observed in Prometheus histograms. The IPDV metrics do not depend on this check.

### 7.5 Aggregation Units and Statistics

- The aggregation unit is the run (periodic / ad hoc) or the window (continuous)
- For each delay metric, min / mean / median / max / p95 / p99 / stddev are computed. Percentiles are obtained by sorting the samples (sample count limit `max_samples_per_run`, default 100,000; above that, samples are thinned at even intervals)
- When there are no samples, percentiles and median are undefined (`null` in JSON, no observation in Prometheus)
- The run summary includes start time, end time, configuration (TestSpec), actual source/destination addresses and ports, SID (Full), synchronization state of both ends, and the timestamping mode

### 7.6 Interpreting the Error Estimate

- Value = Multiplier × 2^(Scale − 32) seconds. S bit = synchronized, Z bit = timestamp format (0: NTP, 1: PTPv2. v0.x does not drop Z=1 packets as `unsupported_timestamp_format`, and with a warning does not treat them as NTP: the values are recorded but excluded from delay computation)
- Multiplier = 0 is corrupt per the RFC, but to cope with known Reflector-side bugs, with `tolerate_zero_multiplier: true` (default) it is accepted as "error unknown" and the one-way delays are set to `valid=false`. The summary's `max_clock_error_unknown` is set to true (§7.1). If false, the packet is dropped

---

## 8. Timestamping and Clocks

### 8.1 Modes and Selection

| Mode | T4 (Sender receive) / T2 (Reflector receive) | T1 / T3 (send) | Conditions |
|------|----------------------------------------|----------------|------|
| `kernel` | SCM_TIMESTAMPING cmsg from SO_TIMESTAMPING (SOF_TIMESTAMPING_RX_SOFTWARE \| SOFTWARE). If unavailable, SCM_TIMESTAMPNS from SO_TIMESTAMPNS | `time.Now()` immediately before sending. The Sender additionally obtains the TX time from MSG_ERRQUEUE with SOF_TIMESTAMPING_TX_SOFTWARE \| OPT_ID \| OPT_TSONLY and uses it as T1' | Linux. No privileges required |
| `hardware` | NIC hardware timestamp (SOF_TIMESTAMPING_RX_HARDWARE \| RAW_HARDWARE, ts[2] of SCM_TIMESTAMPING). If absent for that packet, the kernel software timestamp | `time.Now()` immediately before sending. The Sender's T1' is the NIC's transmit time (TX_HARDWARE; OPT_TX_SWHW is not set, so one entry per packet; software if the NIC does not take one) | The NIC supports the receive filter `HWTSTAMP_FILTER_ALL` and the PHC is synchronized to CLOCK_REALTIME (phc2sys). If NIC timestamping is disabled, it is enabled (SIOCSHWTSTAMP, requires CAP_NET_ADMIN). Falls back to `kernel` if unusable. Configured with `daemon.timestamping: hardware` and `daemon.hardware_interface` (ADR 0014) |
| `userspace` | `time.Now()` right after `recvmsg` | `time.Now()` immediately before sending | All environments |
| `auto` (default) | At startup, try setting the socket options; `kernel` on success, `userspace` on failure. `hardware` is not tried (it changes NIC-wide settings, so only when explicitly configured) | | |

- The timestamping mode used is recorded in the run summary and the metric `gotwamp_timestamping_mode`
- All times use CLOCK_REALTIME (wall clock). T1 is carried in the packet to the peer, so a monotonic clock cannot be used. Kernel software timestamps are also based on CLOCK_REALTIME
- With `hardware`, the NIC time is the PHC time as is, and the difference between the PHC and CLOCK_REALTIME is not corrected (v0.x assumes "the PHC is synchronized to CLOCK_REALTIME"; correction via PTP_SYS_OFFSET is a future extension). As a safeguard, if the NIC time and software time of the same datagram differ by more than ±1ms, the software time is used (if there is no software time, a NIC time 1ms or more in the future of the user-space receive time is discarded). A PHC aligned to TAI by ptp4l (off by 37 seconds) is not used because of this safeguard
- When enabling the NIC for `hardware`, settings are only ever widened (receive filter to ALL; transmit turned ON only when it is OFF; transmit modes such as one-step PTP are preserved). They are not restored on exit
- With `hardware`, sockets bound to a wildcard address need the interface on which timestamps are taken to be specified. The interface is `daemon.hardware_interface`; for a Light sender, if it is empty, the interface of the route to the destination is used. If it cannot be determined, it falls back to `kernel`. Sockets bound to a specific address use that address's interface. Changing `hardware_interface` requires a restart (it is not applied on reload). Specifying `hardware_interface` when `timestamping` is not `hardware` produces a warning
- The NIC result for `hardware` is logged only once per combination of interface and result: a Warn with the reason if unusable, or an Info with the PHC device (`/dev/ptpN`, the target for phc2sys) if usable. The mode shown in the daemon status (`GetStatus`) is determined from the socket opened on the `hardware_interface` wildcard
- The number of NIC times discarded on receive (`RxMeta.HardwareRejected`) is, as of v0.4, exposed in neither metrics nor logs (future work). Inconsistencies on the send side are visible via `tx_timestamp_invalid` (§8.2)
- A clock step during a run can produce a negative RTT. Negative `rtt` values are counted in `invalid_rtt_total` and excluded from statistics

### 8.2 Handling TX Timestamps

- The Timestamp in the packet (T1) must be fixed before sending, so the user-space time is written
- The kernel TX timestamp T1' arrives asynchronously in the errqueue after sending. On each iteration of the send loop, the Sender reads the errqueue non-blocking (`MSG_ERRQUEUE | MSG_DONTWAIT`), maps `sock_extended_err.ee_data` (the send counter from OPT_ID) to the seq, and records T1'
- If T1' has not been obtained when the reflected packet arrives, wait at most `tx_timestamp_wait` (default 1ms); if still absent, use the user-space T1 and increment `tx_timestamp_missing_total`
- RTT computation prefers T1'. If there is an inconsistency, such as T1' being 100µs or more older than the user-space T1, fall back to the user-space value (the same sanity check as the DoubleZero implementation)
- The Reflector must write T3 into the packet, so it cannot use TX timestamps. The Reflector's Error Estimate includes the user-space error of T3

### 8.3 Obtaining Clock Synchronization State

- `internal/clock` calls `unix.Adjtimex` every second and considers the clock synchronized if `state != TIME_ERROR` and `status & STA_UNSYNC == 0`. It keeps `maxerror` (µs) as the maximum error
- chrony / ntpd / systemd-timesyncd all update the kernel synchronization state, but in some environments it is not reflected, so it can be overridden in configuration: `clock.sync: auto | synced | unsynced`, `clock.max_error: <duration>`
- The synchronization state and maximum error are exposed in metrics (`gotwamp_clock_synchronized`, `gotwamp_clock_max_error_seconds`) and in `gotwamp status`

### 8.4 Computing the Error Estimate

1. `err = clock.max_error + method_error`. `method_error` is `kernel_error` (default 5µs) for `kernel` and `userspace_error` (default 50µs) for `userspace`. The config keys are `daemon.clock.kernel_error` / `daemon.clock.userspace_error`, and 0 means the default (there is no way to specify 0 µs). The Reflector takes T3 in user space, so even with `kernel` it uses `max(userspace_error, kernel_error)`. A `hardware` Sender uses `daemon.clock.hardware_error` (default 1µs; `kernel_error` if 0). The Reflector takes T3 in user space even with `hardware`, so it stays at `max(userspace_error, kernel_error)`
2. `e = ceil(err / 2^-32)` (an integer in units of 2^-32 seconds). `scale = 0`. While `e > 255`, repeat `e = ceil(e / 2)`, `scale++`
3. `Multiplier = e` (1–255), `Scale` (0–63), `S` = 1 if synchronized, `Z` = 0
4. Multiplier/Scale are computed as above even when not synchronized. If `clock.max_error` cannot be read via adjtimex, or maxerror is out of range, it is encoded as 16s (the kernel's NTP_PHASE_LIMIT; the same as maxerror when the time source stops updating). Scale 63 / Multiplier 255, meaning "unknown", is not used. Multiplier 0 is never sent

### 8.5 NTP Timestamp Conversion

- Encoding: `sec = uint32((unix_sec + 2208988800) mod 2^32)`, `frac = uint32(round(nsec × 2^32 / 10^9))`
- Decoding: choose the era that falls within a ±2^31-second window centered on the receive time (reference time). This keeps times continuous across the era rollover on February 7, 2036
- Conversion is confined to `internal/twamp/ntptime`, and unit tests pin the boundaries (1900 epoch, 2036 rollover, frac rounding)
- Leap seconds: follow CLOCK_REALTIME. The 1-second offset at a leap second insertion is not detected (it is excluded as a negative RTT)

### 8.6 Send Timing Accuracy

- Absolute schedule + `time.Sleep` + a short spin (§6.5). `time.Ticker` is not used because it drops ticks under load and drifts
- `realtime.lock_os_thread: true` (default false) pins the send and receive goroutines to OS threads
- Operational recommendations (documented): `GOMAXPROCS ≥ 2`, a higher `GOGC` (e.g. 400) or setting `GOMEMLIMIT`, optionally systemd `CPUSchedulingPolicy=fifo` / `CPUSchedulingPriority`, raising `net.core.rmem_max`, and the Sender's receive socket buffer (`SetReadBuffer`, default 1MiB)

---

## 9. gotwampd Specification

### 9.1 Startup, Roles, and Signals

| Item | Specification |
|------|------|
| Startup | `gotwampd --config /etc/gotwamp/gotwampd.yaml [--log-level debug]`. If the configuration file does not exist, only the reflector role is enabled, with default values (the same applies when `--config` is given explicitly: a Warn log is emitted, and a SIGHUP after the file is put in place loads it) |
| Roles | Enabled independently with `sender.enabled` / `reflector.enabled`. If both are false, startup fails |
| SIGTERM / SIGINT | Graceful shutdown: stop starting new runs, send Stop-Sessions for in-progress runs, close the reflector's control connections, flush the exporters, and exit within 5 seconds (NFR-05) |
| SIGHUP | Configuration reload (§9.3) |
| Exit code | 0 normal, 1 configuration error, 2 socket/permission error at startup, 3 a component (reflector, API, metrics, etc.) failed after startup |
| RPCs during shutdown | RunTest and similar calls made after graceful shutdown has begun are rejected with gRPC `Aborted` (CLI exit code 1, §10.4) |
| User | Assumes running as the dedicated user `gotwamp` (§9.10) |

### 9.2 Configuration File (YAML)

A complete example. Defaults are in Appendix A.

```yaml
version: 1

daemon:
  api:
    socket: /run/gotwamp/gotwampd.sock   # gRPC over UDS
    socket_mode: "0660"
    socket_group: gotwamp
    tcp:                                 # Also expose the same API over TCP (ADR 0010). Disabled if listen is empty
      listen: ""                         # e.g. "192.0.2.10:9443". mTLS is mandatory when set
      ca: /etc/gotwamp/tls/clients-ca.pem   # CA for client certificates
      cert: /etc/gotwamp/tls/server.pem
      key: /etc/gotwamp/tls/server-key.pem  # Startup fails unless 0600 or stricter
      allowed_names: [admin]             # CN / SAN (DNS, IP, URI, email). Empty = any certificate signed by the CA (warning)
  metrics:
    listen: "127.0.0.1:9862"             # Prometheus /metrics, /healthz. Disabled if empty
    rtt_buckets: [50us, 100us, 200us, 500us, 1ms, 2ms, 5ms, 10ms, 20ms, 50ms, 100ms, 200ms, 500ms, 1s]
  log:
    level: info                          # debug | info | warn | error
    format: json                         # json | text
  timestamping: auto                     # auto | hardware | kernel | userspace (§8.1)
  hardware_interface: ""                 # With hardware, the NIC for sockets bound to a wildcard address. Changes require a restart
  clock:
    sync: auto                           # auto | synced | unsynced
    max_error: 0s                        # 0 = use adjtimex's maxerror
    userspace_error: 50us                # 0 = default (50us)
    kernel_error: 5us                    # 0 = default (5us). §8.4
    hardware_error: 1us                  # Sender method error with hardware. 0 = kernel_error
  realtime:
    lock_os_thread: false
  limits:
    max_samples_per_run: 100000
    result_history: 1440                 # Number of run/window summaries and run_errors kept per session
    max_concurrent_tests: 16             # Ad hoc measurements (RunTest) that can run concurrently
    max_watchers: 32                     # WatchResults streams that can be open concurrently

sender:
  enabled: true
  keys_file: ""                          # Keys for A / E / M (pfs format, 0600 or stricter). Read at startup (changes require a restart)
  defaults:                              # Inherited by every session
    mode: full                           # full | light
    control_port: 862
    receiver_port: 0                     # full: 0 = request the same value as Sender Port. light: 0 = 862
    sender_port: 0                       # 0 = chosen randomly from sender_port_range
    sender_port_range: "49152-57343"
    count: 100
    interval: 100ms
    timeout: 2s
    padding: 27
    padding_fill: auto                   # auto (full: random, light: zero) | random | zero
    dscp: 0                              # 0-63 or ef/af11../cs0..
    ttl: 255
    control_timeout: 15s
    control_dscp: 0
    end_delay: 0s
    zero_addresses: false
    start_time: zero                     # zero | now
    accept_alternate_port: true
    tolerate_zero_multiplier: true
    tx_timestamp_wait: 1ms
    reflector_sequence: stateful         # stateful | copy_sender (how the peer reflector assigns Sequence Numbers. §7.2)
    dscp_ecn_monitoring: false           # Read RFC 7750 S-DSCP-ECN. light only
    security: unauthenticated            # unauthenticated | authenticated | encrypted | mixed (Full only)
    key_id: ""                           # KeyID for A / E / M (an identity in sender.keys_file)
    extensions: []                       # Full only. individual_session_control | reflect_octets | symmetrical_size (§5.4)
    octets_to_reflect: 0                 # Full only. RFC 6038 Octets to be reflected (0-65535)
    padding_to_reflect: 0                # Full only. RFC 6038 Length of padding to reflect (0-65535)
    stamp: false                         # Light only. Send as STAMP (§6.7)
    ssid: 0                              # Light only. STAMP SSID (0-65535, 0 = not set). Requires stamp
  sessions:
    - name: tokyo-osaka
      target: 192.0.2.10                 # Host names allowed. ipv: auto | 4 | 6
      ipv: auto
      source: ""                         # Source address (empty = chosen from the route)
      mode: full
      schedule:
        type: periodic
        frequency: 60s                   # Run start interval (must be longer than count × interval + timeout)
        jitter: 5s                       # Random delay before the first start (spreads simultaneous starts)
      count: 100
      interval: 100ms
      dscp: ef
      labels: {site: tokyo, link: primary}   # Prometheus labels (up to 8)
    - name: tokyo-osaka-light
      target: 2001:db8::10
      mode: light
      receiver_port: 862
      sender_port: 50000
      schedule:
        type: continuous
        window: 60s                      # Width of each statistics window. count is derived as window / interval
      interval: 1s
      padding: 27

reflector:
  enabled: true
  control:
    listen: ["0.0.0.0:862", "[::]:862"]
    acl: []                              # CIDR allow list. Empty = allow all
    max_connections: 64
    max_sessions_per_connection: 16
    max_sessions: 1024
    servwait: 900s
    refwait: 900s
    partial_message_timeout: 60s
    test_port_range: "20000-29999"
    honor_requested_port: true
    verify_sender: address               # address | address_port | none
    max_padding: 9000                    # At least 1. Full also returns Accept 3 when 14 + padding > max_packet_size (§5.10)
    pbkdf2_count: 2048                   # Count in the Server Greeting. A power of 2 in 1024-32768
    keys_file: ""                        # Keys for A / E / M (pfs format, 0600 or stricter). Read at startup (changes require a restart)
    modes: []                            # Security modes to offer. Empty = [unauthenticated] without keys_file, all 4 with it
    extensions: [individual_session_control, reflect_octets, symmetrical_size]   # Extensions to offer (all by default)
    # enforce_start_time: false          # Drop arrivals before Start Time (CMP-26). Implemented in the reflector; the config key is pending wiring
  light:
    listeners:
      - listen: "0.0.0.0:862"
      - listen: "[::]:862"
    acl: []
    sequence: per_flow                   # per_flow | copy_sender
    flow_timeout: 900s
    max_flows: 65536
    rate_limit_pps: 0                    # Per source. 0 = unlimited
    dscp_policy: copy                    # copy | fixed
    dscp_fixed: 0
    dscp_ecn_monitoring: false           # Write the received DSCP/ECN into the reply's S-DSCP-ECN (RFC 7750)
    stamp: false                         # Reply in STAMP format (§6.7). Cannot be combined with dscp_ecn_monitoring
  ttl: 255
  max_packet_size: 9000

export:
  jsonl:
    enabled: false
    path: /var/lib/gotwamp/results.jsonl
    records: [run_summary, window_summary, run_error]   # Add packet for per-packet output
    rotate:
      max_size: 100MiB
      max_files: 10
      daily: false                       # Switch to a new file on the first record of each day (§9.7)

store:
  backend: memory                        # memory | sqlite (§9.4). Changes require a restart
  sqlite:
    path: /var/lib/gotwamp/results.db    # Absolute path. The parent directory must exist (systemd StateDirectory)
    retention: 720h                      # 30 days. 0 = unlimited
    max_size: 0                          # Warn when DB plus WAL exceeds this. 0 = no warning
    on_corrupt: reset                    # reset (move aside and start empty) | fail (do not start)
```

Validation rules (excerpt):

- `name` is unique across sessions and matches `[a-z0-9][a-z0-9_-]{0,62}`
- periodic: `frequency ≥ count × interval + timeout + 1s`. An error if not satisfied
- continuous: `window / interval ≥ 10`
- `padding ≤ max_padding on the reflector side` cannot be verified, so values above 1472 (the non-fragmenting limit for IPv4 with MTU 1500) produce a warning
- `dscp` is 0–63 or a name. `ttl` 1–255. `interval ≥ 1ms`. `count` 1–1,000,000 (periodic)
- `labels` keys match `[a-zA-Z_][a-zA-Z0-9_]*`. The metric label names `session` / `target` / `mode` / `direction` / `stat` / `kind` / `result` / `stage` / `le` are reserved
- `dscp_ecn_monitoring` can be specified only with `mode: light` (negotiating Modes bit 8 in Full is not implemented)
- Items that apply to only one mode (Full only: `security`, `extensions`, `octets_to_reflect`, `padding_to_reflect`. Light only: `stamp`, `ssid`, `dscp_ecn_monitoring`) produce a `does not apply to … mode` error when written on the session itself, and are ignored when inherited from `sender.defaults` (TWAMP Light is unauthenticated only)
- `extensions` and `reflector.control.extensions` accept only `individual_session_control` / `reflect_octets` / `symmetrical_size`. `octets_to_reflect` / `padding_to_reflect` / `ssid` are 0–65535
- `stamp` cannot be combined with `dscp_ecn_monitoring` (for both the sender and `reflector.light`). With `stamp`, `padding` is 0 or at least 4. `ssid` requires `stamp`
- `reflector.control.max_padding` is at least 1, and `pbkdf2_count` is a power of 2 in 1024–32768
- `reflector.control.dscp` (the DSCP of control connections) is a reserved key and is not accepted in v0.x (§5.1)
- DSCP values such as `dscp` are decimal (a leading 0 does not make them octal: `010` is 10), hexadecimal with a `0x` prefix, or a name
- If `daemon.api.tcp.listen` is specified, `ca` / `cert` / `key` are required and must be absolute paths. A warning if `allowed_names` is empty. A warning if `listen` is empty but TLS items are present
- Secret files (`daemon.api.tcp.key`, `sender.keys_file`, `reflector.control.keys_file`) must exist and be unreadable by anyone other than the owner (0600 or stricter). A violation is a configuration error
- If `security` is anything other than unauthenticated, `key_id` and `sender.keys_file` are required. If the keys file cannot be read, or the `key_id` in defaults is not in the file, it is a configuration error (exit code 1). If a session in the configuration file has an unknown `key_id`, startup fails, and a reload fails as a whole (InvalidArgument for RunTest / the session API). The keys file read at startup is retained and is not re-read on reload
- `reflector.control.modes` takes the same 4 values as `security`. If it includes anything other than unauthenticated, `reflector.control.keys_file` is required
- `daemon.hardware_interface` is an interface name (15 octets or fewer, without `/` or whitespace)
- `store.backend` is memory or sqlite. With sqlite, `store.sqlite.path` is an absolute path, `retention` and `max_size` are 0 or greater, and `on_corrupt` is reset or fail
- A warning if the reflector is enabled but both `control.listen` and `light.listeners` are empty (nothing is reflected)

### 9.3 Monitoring Sessions and the Scheduler

| Type | Behavior |
|------|------|
| periodic | Starts a run every `frequency`. If the previous run has not finished, that iteration is skipped and `runs_skipped_total` is incremented. The first run starts after a random delay in `[0, jitter)` |
| continuous | A single run sends continuously, and statistics are cut every `window` to emit a window summary (waiting for replies may cross window boundaries). Full uses one control connection and one test session for the entire run and does not re-establish them per window. Reflector sequence numbers continue across windows, so the forward/reverse breakdown carries over the base point from the previous window under the rules of §7.2. If a run fails, a new run starts after waiting 5s (ADR 0007) |

- Session lifecycle: on creation it is immediately `running` → `stopped` (`gotwamp session stop`) → `running` (start) → `deleted`. There is no `created` (created but not started) state. A session whose spec changes on reload is re-created, carrying over its previous state (`running` / `stopped`)
- Sessions from the configuration (`source: config`) are distinguished from sessions from the API (`source: api`). Reload targets only config sessions; API sessions are kept until the daemon restarts (session definitions are not persisted. Even with `store.backend: sqlite`, only results survive. §9.4)
- Applying reload differences: sessions are matched by name, and only those whose spec changed are stopped and re-created. Sessions being stopped are aborted together before waiting for them to finish (Full Stop-Sessions are not waited for serially). Reflector listener changes involve re-binding, so a warning is logged
- Reload failures are logged by the daemon whether triggered by SIGHUP or `ReloadConfig` (`gotwamp config reload`). If the level is fixed with `gotwampd --log-level`, a reload does not apply `daemon.log.level` and returns a warning
- If the daemon starts without a configuration file (whether at the default location or at the location given with `--config`), it keeps that path and loads a file placed there later on reload

### 9.4 Result Store

- Per session, run / window summaries and run_errors are kept in a single ring buffer (`result_history` entries) (the failure history is also returned by GetSessionStats). In addition, the per-packet records of the most recent run (`max_samples_per_run` entries) are kept
- Per-packet records use a 64-octet compact format holding seq, Reflector Sequence Number, window, T4, TTL, received DSCP/ECN, the 4 delays, and kind and flags. T1–T3, Error Estimate, clock_offset_hint, and hop counts are not kept (WatchResults per-packet events carry all fields)
- Subscriptions (`WatchResults`) deliver summaries and, if requested, per-packet events. Slow subscribers drop events and are notified with `dropped`
- Memory estimate: summaries about 1KiB × 1440 × number of sessions + per-packet 64B × 100,000 (most recent run only)

`store.backend` selects where results are kept (default `memory`. Changes require a restart).

| backend | Contents |
|---------|------|
| `memory` | Only the in-memory rings above. Lost on restart |
| `sqlite` | Keeps the memory above in front and writes run / window summaries and run_errors to SQLite (ADR 0013). The driver is the pure-Go modernc.org/sqlite (keeping `CGO_ENABLED=0`) |

`sqlite` behavior:

- **What is stored**: run / window summaries and run_errors of named sessions (from the configuration and from the API). Per-packet records and ad hoc measurement results are not stored. Summaries and run_errors are represented as JSON (Go field names; fields added later have zero values in older rows), and in schema version 2 fields equal to their type's zero value are omitted and the result is compressed with DEFLATE using a fixed dictionary (the first octet is the format; about 0.6KB per entry. Version 1 JSON rows are still read as is. The formats are described in the `payload_formats` table. Compressed rows cannot be read directly with `sqlite3`). Session descriptions (target, mode, labels, definition, etc.) are stored as one row when identical, and remain JSON readable with `sqlite3`
- **Writing**: Publish hands results to memory and subscribers immediately; a dedicated writer writes to the DB in batches of up to 256 entries per transaction. If the queue (4096 entries) overflows, that result is not stored but counted (`results_dropped`), and a Warn is emitted. Measurement does not stop
- **Reading**: If `GetSessionStats` has `since` (`gotwamp stats --since`), it is answered with the store's time-range query (`Range`). With sqlite, the DB is queried after waiting for the writer to catch up, and can go back as far as `retention` (with memory, the range of the `result_history` history entries). With only `--since`, everything in the range is returned; with `--last N`, the most recent N entries. Without `since`, the most recent `last` entries (CLI default 10) from the most recent `result_history` entries in memory. WatchResults and the session list are answered from memory. At startup, the most recent `result_history` entries per session are loaded from the DB
- **Maintenance**: WAL, `synchronous=NORMAL`. Rows older than `retention` (default 720h = 30 days, 0 = unlimited) are deleted at startup and every hour. A Warn when DB plus WAL exceeds `max_size` (0 = no warning)
- **Schema**: Has a version table and migrates one version at a time at startup. A DB with a newer version is not opened (startup fails)
- **Corrupt DB**: `on_corrupt: reset` (default) moves it aside to `<path>.corrupt-<time>` and starts empty. `fail` does not start
- **Startup and shutdown**: If the DB cannot be opened (corrupt with fail, newer schema, parent directory missing), it is a startup error (exit code 2). On shutdown, after the scheduler and JSONL, the queue is flushed within the remaining shutdown budget before closing (a Warn if it does not finish in time)
- **Status**: Check with `store` in `GetStatus` (backend, path, results_written / dropped / failed), the `store` line in `gotwamp status`, and the metrics `gotwamp_store_results_{written,dropped,failed}_total` (§9.6)
- After a restart, the API sessions themselves are gone, but their results remain and can be read under the same names
- memory and sqlite pass the same contract test (`internal/store/storetest`)

### 9.5 gRPC API

Package `gotwamp.v1`. Connections are over UDS (default) and, when `daemon.api.tcp` is configured, TCP + mTLS (TLS 1.3, client certificates required, ADR 0010). The same gRPC server answers on both listeners. Authorization is by file permissions for UDS, and by the CA signature and `allowed_names` for TCP (there are no per-RPC permissions).

```proto
syntax = "proto3";
package gotwamp.v1;

service DaemonService {
  rpc GetStatus(GetStatusRequest) returns (GetStatusResponse);          // version, uptime, roles, clock, timestamping
  rpc ReloadConfig(ReloadConfigRequest) returns (ReloadConfigResponse); // Returns a summary of the differences
}

service SenderService {
  rpc RunTest(RunTestRequest) returns (stream TestEvent);               // Ad hoc measurement (streaming)
  rpc StopTest(StopTestRequest) returns (StopTestResponse);             // Stops an ad hoc measurement by run_id. RunTest streams an aborted summary and closes (ADR 0006)
  rpc ListSessions(ListSessionsRequest) returns (ListSessionsResponse);
  rpc GetSession(GetSessionRequest) returns (GetSessionResponse);
  rpc CreateSession(CreateSessionRequest) returns (CreateSessionResponse);
  rpc UpdateSession(UpdateSessionRequest) returns (UpdateSessionResponse);
  rpc DeleteSession(DeleteSessionRequest) returns (DeleteSessionResponse);
  rpc StartSession(StartSessionRequest) returns (StartSessionResponse);
  rpc StopSession(StopSessionRequest) returns (StopSessionResponse);
  rpc GetSessionStats(GetSessionStatsRequest) returns (GetSessionStatsResponse); // The most recent N summaries
  rpc WatchResults(WatchResultsRequest) returns (stream TestEvent);     // Subscription
}

service ReflectorService {
  rpc GetReflectorStatus(GetReflectorStatusRequest) returns (GetReflectorStatusResponse);
  rpc ListReflectorSessions(ListReflectorSessionsRequest) returns (ListReflectorSessionsResponse); // Full sessions + Light flows
}

message TestSpec {
  string target = 1;                 // Host name or IP
  Mode mode = 2;                     // MODE_FULL | MODE_LIGHT
  AddressFamily ipv = 3;             // AUTO | IPV4 | IPV6
  string source_address = 4;
  uint32 control_port = 5;
  uint32 receiver_port = 6;
  uint32 sender_port = 7;
  uint32 count = 8;                  // 0 = continuous (not allowed in RunTest)
  google.protobuf.Duration interval = 9;
  google.protobuf.Duration timeout = 10;
  uint32 padding = 11;
  PaddingFill padding_fill = 12;
  uint32 dscp = 13;
  uint32 ttl = 14;
  SecurityMode security = 15;        // Full only. A / E / M require key_id to be in sender.keys_file (InvalidArgument otherwise)
  google.protobuf.Duration control_timeout = 16;
  CompatOptions compat = 17;         // zero_addresses, start_time, accept_alternate_port, tolerate_zero_multiplier, control_dscp, end_delay, sender_port_range, reflector_sequence (ReflectorSequence: UNSPECIFIED | STATEFUL | COPY_SENDER)
  optional bool dscp_ecn_monitoring = 18;  // RFC 7750 (Light only)
  string key_id = 19;                // KeyID for A / E / M
  repeated Extension extensions = 20;      // Full only. RFC 5938 / RFC 6038 (§5.4)
  optional uint32 octets_to_reflect = 21;  // Full only. RFC 6038 Octets to be reflected
  optional uint32 padding_to_reflect = 22; // Full only. RFC 6038 Length of padding to reflect
  optional bool stamp = 23;                // Light only. Send as STAMP (§6.7)
  optional uint32 ssid = 24;               // Light only. STAMP SSID
}

enum Extension {                     // Unknown values are InvalidArgument
  EXTENSION_UNSPECIFIED = 0;
  EXTENSION_INDIVIDUAL_SESSION_CONTROL = 1;  // Modes bit 4
  EXTENSION_REFLECT_OCTETS = 2;              // Modes bit 5
  EXTENSION_SYMMETRICAL_SIZE = 3;            // Modes bit 6
}

message PacketResult {
  uint32 seq = 1;                    // Sender Sequence Number
  uint32 reflector_seq = 2;
  google.protobuf.Timestamp t1 = 3;  // The T1 actually used (kernel TX if available)
  google.protobuf.Timestamp t2 = 4;
  google.protobuf.Timestamp t3 = 5;
  google.protobuf.Timestamp t4 = 6;
  google.protobuf.Duration rtt = 7;
  google.protobuf.Duration rtt_raw = 8;
  google.protobuf.Duration fwd_delay = 9;
  google.protobuf.Duration rev_delay = 10;
  google.protobuf.Duration reflector_delay = 11;
  bool owd_valid = 12;
  uint32 sender_ttl_at_reflector = 13;
  uint32 reflector_ttl_received = 14;
  uint32 dscp_received = 15;
  bool duplicate = 16;
  DuplicateKind duplicate_kind = 17;
  bool reordered = 18;
  ErrorEstimate sender_errest = 19;
  ErrorEstimate reflector_errest = 20;
  bool t1_from_kernel = 21;
  google.protobuf.Duration clock_offset_hint = 22;
  optional uint32 dscp_reflected = 23;  // RFC 7750: DSCP received by the reflector
  optional uint32 ecn_reflected = 24;   // Likewise for ECN
}

message RunSummary {
  string session = 1;                // Empty for ad hoc
  string run_id = 2;                 // ULID
  TestSpec spec = 3;
  Endpoint local = 4;                // Actual address and port
  Endpoint remote = 5;
  bytes sid = 6;                     // Full only
  google.protobuf.Timestamp started_at = 7;
  google.protobuf.Timestamp ended_at = 8;
  uint32 sent = 9;  uint32 received = 10;  uint32 lost = 11;  double loss_ratio = 12;
  uint32 dup_forward = 13;  uint32 dup_reverse = 14;  uint32 reordered = 15;  uint32 reordered_reverse = 16;
  int64 lost_forward_est = 17;  int64 lost_reverse_est = 18;  bool loss_split_valid = 19;
  DelayStats rtt = 20;  DelayStats fwd_delay = 21;  DelayStats rev_delay = 22;  DelayStats reflector_delay = 23;
  bool owd_valid = 24;
  JitterStats jitter = 25;           // ipdv_rtt_{mean,max,p95}, ipdv_fwd_mean, ipdv_rev_mean, pdv_rtt_{p95,p99,p999}, jitter_rfc3550, rtt_stddev, jitter_p95_p50
  HopStats hops = 26;                // fwd_min/max, rev_min/max
  ClockInfo clock = 27;              // sender_synced, reflector_synced, max_clock_error, timestamping_mode, max_clock_error_unknown, tx_timestamps
  bool aborted = 28;  bool invalid = 29;
  SummaryKind kind = 30;             // RUN | WINDOW
  uint64 negative_owd = 31;  uint64 invalid_rtt = 32;
  uint64 tx_timestamp_missing = 33;  uint64 tx_timestamp_invalid = 34;
  DscpReflected dscp_reflected = 35; // RFC 7750 aggregate (only when there is data)
  uint64 unsupported_timestamp_format = 36;
  uint64 reflected_mismatch = 37;    // RFC 6038: replies that did not return the padding requested for reflection (§7.2)
}

message DscpReflected {
  uint64 count = 1;                  // Number of replies whose S-DSCP-ECN could be read
  uint32 mode = 2;                   // Most frequent DSCP
  uint64 remarked = 3;               // Number that reached the reflector with a DSCP different from the sent DSCP
  uint64 ce = 4;                     // Number with ECN = CE (11)
}

message TestEvent {
  oneof event {
    RunStarted started = 1;
    PacketResult packet = 2;
    PacketLost lost = 3;             // seq, t1
    RunSummary summary = 4;
    RunError error = 5;              // stage (connect|greeting|setup|request|start|test|stop|internal), message, accept code
  }
}
```

- If `count` in `RunTest` is unspecified (0), `sender.defaults.count` is used. If the resolved value is not 1–100,000, InvalidArgument (continuous is not allowed. ADR 0005). Cancelling the RPC aborts the run and sends Stop-Sessions. Concurrency is limited to `max_concurrent_tests`, and WatchResults to `max_watchers`
- Failures inside the daemon (recovered panics, etc.) are returned as a RunError with stage `internal`, and the daemon does not stop
- Specifying an item that applies to only one mode (§9.2) in a request for the other mode is InvalidArgument (Light `security` and extensions, Full `stamp` and `ssid`, etc.)
- Other additions (0.4): `TIMESTAMPING_MODE_HARDWARE = 3` in `TimestampingMode`. `GetStatusResponse.store = 12` (`StoreStatus{backend, path, results_written, results_dropped, results_failed}`, §9.4). `FullReflectorSession.security = 13` (the SECURITY column of `gotwamp reflector sessions`)
- The same RPCs are available over the TCP + mTLS API. Keys for authenticated modes exist only in gotwampd's keys files; requests carry only `key_id` (keys never travel over the API)
- Session CRUD takes the same schema as an element of `sessions[]` in the configuration (`SessionSpec = TestSpec + schedule + labels + name`)
- API backward compatibility: fields in `gotwamp.v1` are reserved rather than deleted. `buf breaking` runs in CI

### 9.6 Prometheus Metrics

Labels: `session`, `target`, `mode` (+ user-defined labels). Ad hoc measurements are not included in metrics.

| Name | Type | Description |
|------|----|------|
| `gotwamp_sender_packets_sent_total` | counter | Packets sent |
| `gotwamp_sender_packets_received_total` | counter | Packets accepted (unique) |
| `gotwamp_sender_packets_lost_total` | counter | Packets lost |
| `gotwamp_sender_packets_duplicate_total{direction="forward\|reverse"}` | counter | Duplicates |
| `gotwamp_sender_packets_reordered_total` | counter | Reordering |
| `gotwamp_sender_rtt_seconds` | histogram | rtt (buckets are configurable) |
| `gotwamp_sender_forward_delay_seconds` / `gotwamp_sender_reverse_delay_seconds` | histogram | One-way delay (observed only when valid) |
| `gotwamp_sender_reflector_delay_seconds` | histogram | T3 − T2 |
| `gotwamp_sender_last_run_rtt_seconds{stat="min\|mean\|median\|max\|p95\|p99\|stddev"}` | gauge | Most recent run/window |
| `gotwamp_sender_last_run_forward_delay_seconds{stat}` / `..._reverse_delay_seconds{stat}` | gauge | Most recent (when valid) |
| `gotwamp_sender_last_run_jitter_seconds{kind="ipdv_mean\|ipdv_max\|pdv_p95\|pdv_p99\|rfc3550\|p95_p50"}` | gauge | Most recent |
| `gotwamp_sender_last_run_loss_ratio` | gauge | Most recent |
| `gotwamp_sender_last_run_timestamp_seconds` | gauge | End time of the most recent run |
| `gotwamp_sender_last_run_success` | gauge | 1 = control and send/receive completed, 0 = failed |
| `gotwamp_sender_last_run_owd_valid` | gauge | Validity of one-way delay |
| `gotwamp_sender_path_hops{direction="forward\|reverse"}` | gauge | Mode of the most recent run |
| `gotwamp_sender_runs_total{result="success\|error\|skipped"}` | counter | Number of runs |
| `gotwamp_sender_run_errors_total{stage}` | counter | By failure stage (stage is connect / greeting / setup / request / start / test / stop / internal) |
| `gotwamp_sender_control_setup_seconds` | histogram | From connect to Start-Ack (Full) |
| `gotwamp_sender_packets_stray_total` | counter | Discarded replies (malformed + late + stray, §6.5). JSONL `stray` counts only the stray part of this; `late` is a separate field and malformed is not output (same name, different scope. Splitting into `replies_discarded_total{reason}` is at the proposal stage) |
| `gotwamp_sender_negative_owd_total` / `gotwamp_sender_invalid_rtt_total` | counter | Negative one-way delay (§7.4) / negative rtt |
| `gotwamp_sender_tx_timestamp_missing_total` | counter | Transmissions for which no kernel TX timestamp was obtained (§8.2) |
| `gotwamp_sender_tx_timestamp_invalid_total` | counter | TX timestamps discarded by the sanity check (§8.2) |
| `gotwamp_sender_unsupported_timestamp_format_total` | counter | Replies with Z=1 (PTP) (§7.6) |
| `gotwamp_sender_dscp_remarked_total` / `gotwamp_sender_ecn_ce_total` | counter | RFC 7750: number that arrived with a DSCP different from the one sent / number of ECN CE |
| `gotwamp_sender_last_run_dscp_reflected` | gauge | RFC 7750: most frequent DSCP of the most recent run (only when there is data) |
| `gotwamp_sender_reflected_mismatch_total` | counter | RFC 6038: replies that did not return the padding requested for reflection (§7.2) |
| `gotwamp_reflector_packets_received_total{kind="full\|light",listener}` | counter | `listener` is the listener address for Light, and a comma-separated list of control listener addresses for Full (e.g. `0.0.0.0:862,[::]:862`; the counters of the per-session sockets are summed) |
| `gotwamp_reflector_packets_reflected_total{kind,listener}` | counter | |
| `gotwamp_reflector_packets_dropped_total{kind,reason="acl\|malformed\|unknown_session\|rate_limit\|not_started\|drained"}` | counter | `not_started` is arrival before Start-Sessions (with `enforce_start_time`, also before Start Time. §6.4) |
| `gotwamp_reflector_control_connections_active` / `_total{result}` | gauge / counter | |
| `gotwamp_reflector_sessions_active{kind}` | gauge | Number of Full sessions / Light flows |
| `gotwamp_reflector_light_flows_overflow_total` | counter | |
| `gotwamp_clock_synchronized` | gauge | 0/1 |
| `gotwamp_clock_max_error_seconds` | gauge | |
| `gotwamp_timestamping_mode{mode="hardware\|kernel\|userspace"}` | gauge | 1 |
| `gotwamp_build_info{version,commit,go_version}` | gauge | 1 |
| `gotwamp_store_results_written_total` / `_dropped_total` / `_failed_total` | counter | Only with `store.backend: sqlite`. Results written to the DB / results not stored because the writer fell behind / results the DB rejected (§9.4) |
| `gotwamp_jsonl_dropped_total` | counter | Records dropped while writing JSON Lines (buffer overflow, write failure, shutdown deadline exceeded. §9.7) |

HMAC mismatches in authenticated modes are counted as `dropped_total{reason="malformed"}` on the reflector and as malformed (stray) on the sender. Whether to split them into `DROP_REASON_AUTH` (label `auth`) is undecided. In Symmetrical Size sessions, packets shorter than the header including MBZ (41 / 112) are also `malformed`. Security modes and extensions are not metric labels (check them with the SECURITY column of `gotwamp reflector sessions` and the startup log).

Cardinality controls: per-packet values go only into histograms, and labelled gauges are per run. Up to 8 user labels.

### 9.7 JSON Lines

One record per line. Common fields: `type`, `time` (RFC 3339 with nanoseconds), `session`, `run_id`.

```json
{"type":"run_summary","time":"2026-10-03T12:00:10.123456789+09:00","session":"tokyo-osaka","run_id":"01J...","spec":{"target":"192.0.2.10","mode":"full","count":100,"interval":"100ms","padding":27,"dscp":46},"local":{"address":"203.0.113.5","port":51234},"remote":{"address":"192.0.2.10","control_port":862,"port":51234},"sid":"c0000205...","started_at":"...","ended_at":"...","sent":100,"received":100,"lost":0,"loss_ratio":0,"dup_forward":0,"dup_reverse":0,"reordered":0,"rtt":{"min":0.00812,"mean":0.00831,"median":0.00829,"max":0.00912,"p95":0.00861,"p99":0.00901,"stddev":0.000102},"fwd_delay":{...},"rev_delay":{...},"reflector_delay":{...},"owd_valid":true,"jitter":{"ipdv_rtt_mean":0.00004,"ipdv_rtt_max":0.00031,"pdv_rtt_p95":0.00049,"jitter_rfc3550":0.00005,"jitter_p95_p50":0.00032},"hops":{"fwd":4,"rev":4},"clock":{"sender_synced":true,"reflector_synced":true,"max_clock_error":0.00021,"timestamping":"kernel"}}
{"type":"packet","time":"...","session":"tokyo-osaka","run_id":"01J...","seq":0,"reflector_seq":0,"t1":"...","t2":"...","t3":"...","t4":"...","rtt":0.00812,"fwd_delay":0.00401,"rev_delay":0.00410,"reflector_delay":0.00001,"owd_valid":true,"sender_ttl":251,"reflector_ttl":251,"dup":false,"reordered":false}
{"type":"run_error","time":"...","session":"tokyo-osaka","run_id":"01J...","stage":"request","message":"accept-session accept=5 (temporary resource limitation)","accept":5}
```

- Time values are in seconds (floating point). `window_summary` has the same shape as `run_summary` with a different `type`
- In addition to the example above, summaries include `pending`, `late`, `stray`, `lost_forward_est` / `lost_reverse_est` / `loss_split_valid`, `negative_owd`, `invalid_rtt`, `tx_timestamp_missing` / `tx_timestamp_invalid`, `unsupported_timestamp_format`, `reflected_mismatch`, `aborted` / `invalid`, `clock.max_clock_error_unknown` / `clock.tx_timestamps`, and `dscp_reflected: {count, mode, remarked, ce}` (null if absent). Packet records include `dscp_reflected` / `ecn_reflected` (null if absent)
- On shutdown, remaining records are written out. Once the deadline passes, the rest are dropped and counted in `jsonl_dropped_total`, the file is closed, and the call returns (a blocking destination does not prevent shutdown)
- Rotation is by size (`max_size`) and number of generations (`max_files`). With `export.jsonl.rotate.daily: true` (default false), it additionally switches to a new file on the first record of each day (daily rotation)
- Writing is asynchronous; on buffer overflow, records are dropped and `jsonl_dropped_total` is incremented
- If a write or flush fails, the file is released and reopened on the next record after 1 second (recovering from transient ENOSPC or NFS disconnects). Records in the meantime are dropped and counted. The start of the failure is logged once at Warn, and the recovery once at Info
- `clock.tx_timestamps` is under `clock` in the summary (not at the top level)

### 9.8 Logging

- `log/slog`. JSON (default) or text. Levels debug/info/warn/error
- Run start, end, and failure, control connection accept/reject (with reason), configuration reload differences, and clock synchronization state changes are logged at info. Per-packet events are at debug
- Peer address, port, and SID are output as structured fields. Packet content dumps are debug only
- Monitoring session logs carry session, run_id, and target (plus SID for Full); ad hoc measurement logs carry run_id, target, and mode. The startup log includes the actual timestamping mode
- Time values are output as strings of the form `43.5ms` in both JSON and text (not as nanosecond integers)
- Failures to read the clock state are logged once at Warn when they begin and once at Info on recovery (even if the state itself does not change)
- Secrets (keys, passphrases, pfs contents) appear in neither logs nor errors

### 9.9 Reflector Operational Parameters

| Parameter | Default | Meaning |
|-----------|------|------|
| `control.acl` / `light.acl` | Allow all | CIDR allow list. Rejection is Greeting Modes=0 (control) / drop (light) |
| `control.max_connections` | 64 | Concurrent control connections (same default as Junos maximum-connections) |
| `control.max_sessions_per_connection` | 16 | |
| `control.max_sessions` | 1024 | Daemon-wide |
| `control.servwait` / `control.refwait` | 900s | RFC 5357 default |
| `control.test_port_range` | 20000-29999 | Range for alternate ports |
| `light.flow_timeout` | 900s | Expiry of per-flow state |
| `light.max_flows` | 65536 | |
| `light.rate_limit_pps` | 0 | Per-source token bucket |
| `max_packet_size` | 9000 | UDP payloads larger than this are dropped |

### 9.10 Privileges and Hardening

- Dedicated user/group `gotwamp`. `/run/gotwamp` (UDS) and `/var/lib/gotwamp` (JSONL, SQLite result store) are created with systemd `RuntimeDirectory=` / `StateDirectory=`. In the container image, `/var/lib/gotwamp` is owned by nonroot
- Binding port 862 (tcp/udp) requires `AmbientCapabilities=CAP_NET_BIND_SERVICE`. SO_TIMESTAMPING, IP_TTL, IP_TOS, IP_RECVTTL, IP_RECVTOS, and adjtimex (read-only) require no privileges
- The systemd unit sets `NoNewPrivileges=yes`, `ProtectSystem=strict`, `ProtectHome=yes`, `PrivateTmp=yes`, `RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK`
- The UDS is `0660` + group `gotwamp`. Users of the CLI are added to the group
- gRPC is exposed over TCP only when explicitly configured with `daemon.api.tcp`, and mTLS is mandatory (ADR 0010). TLS 1.3 only, and client certificates must be signed by the CA in `ca`. If `allowed_names` is set, a connection is allowed only when the certificate's CN or any SAN (DNS, IP, URI, email) matches. Certificate revocation (CRL / OCSP) is not checked, so to revoke a client, remove it from `allowed_names` or replace the CA. The `daemon.api.tcp` items (certificate, key, CA, `allowed_names`) are read at startup, and changes require a restart. TLS handshakes must complete within 10 seconds, with up to 64 concurrent (excess connections are closed immediately, with at most one Warn line per second). Transient Accept failures (EMFILE, etc.) are retried with a 5ms–1s backoff. Rejected connections are logged at Warn with the reason
- The Prometheus listen address is loopback by default
- The systemd unit uses `Restart=on-failure` but with `RestartPreventExitStatus=1`, so it does not restart repeatedly on configuration errors
- The container image uses `gotwamp status --rpc-timeout 3s` as its HEALTHCHECK (healthy if the API responds). The image's configuration uses `socket_group: ""` (because the image has no gotwamp group)
- `CAP_NET_ADMIN` is needed only when gotwampd is to change NIC settings for `hardware` timestamping. It is unnecessary if timestamping is enabled beforehand with `hwstamp_ctl`. The distributed systemd unit does not grant `CAP_NET_ADMIN` (operators who need it add it)
- Keys files (`sender.keys_file`, `reflector.control.keys_file`) and TLS private keys are 0600 or stricter and owned by the gotwamp user (§9.2). The CLI does not handle keys

---

## 10. gotwamp CLI Specification

### 10.1 Common

- `gotwamp [global flags] <command> [args]`. Implemented with cobra
- Global flags: `--socket PATH` (default `/run/gotwamp/gotwampd.sock`, environment variable `GOTWAMP_SOCKET`), `--server HOST:PORT` with `--tls-ca` / `--tls-cert` / `--tls-key` / `--tls-server-name` (API over TCP + mTLS. Environment variables `GOTWAMP_SERVER` / `GOTWAMP_TLS_CA` / `GOTWAMP_TLS_CERT` / `GOTWAMP_TLS_KEY`. `--server` requires all three of the CA, certificate and key), `--output table|json|jsonl` (default table), `--timeout DURATION` / `--rpc-timeout DURATION` (RPC timeout, default 30s. `ping` extends it automatically according to the measurement duration), `--no-color`
- For errors that the daemon explains (NotFound, FailedPrecondition, Aborted, etc.), the CLI prints the message without the gRPC code name. On an RPC timeout it points the user to `--rpc-timeout`
- If the CLI cannot connect to the daemon, it prints `cannot connect to gotwampd at <socket>: ...` and exits with exit code 3

### 10.2 Command List

| Command | Description |
|---------|------|
| `gotwamp ping TARGET [flags]` | Ad hoc measurement. Streams per-packet lines and prints a summary at the end |
| `gotwamp session list` | List monitoring sessions (name, target, mode, schedule, state, last result) |
| `gotwamp session show NAME` | Details (spec and the most recent summary) |
| `gotwamp session add -f FILE.yaml` / `gotwamp session add NAME --target ... [same flags as ping] --frequency 60s` | Add (API-originated) |
| `gotwamp session update NAME -f FILE.yaml` | Update (recreate). FILE.yaml accepts the same keys as `sender.sessions[]` (including `reflector_sequence`, `dscp_ecn_monitoring`, `security`, `key_id`, `extensions`, `octets_to_reflect`, `padding_to_reflect`, `stamp`, `ssid`). `tx_timestamp_wait` cannot be specified for API sessions (taken from sender.defaults) |
| `gotwamp session remove NAME` | Delete |
| `gotwamp session start NAME` / `stop NAME` | Pause / resume |
| `gotwamp stats NAME [--last N] [--since DURATION]` | Recent run/window summaries |
| `gotwamp watch [NAME ...] [--packets]` | Stream results |
| `gotwamp status` | Daemon status (version, uptime, roles, timestamping, clock sync, max error, number of sessions, store). The store line is `memory` or `sqlite <path> (N results written, N dropped, N failed)` |
| `gotwamp reflector status` | Listeners, number of connections, number of flows, counters |
| `gotwamp reflector sessions` | List of Full sessions (the SECURITY column shows the mode) and Light flows |
| `gotwamp config validate -f FILE` | Validate a configuration file (no daemon required) |
| `gotwamp config reload` | Equivalent to SIGHUP (shows the diff) |
| `gotwamp version` | Version, commit, Go version |

### 10.3 `gotwamp ping` Flags

Follows twping's short options and adds long names alongside them.

| Flag | Default | Meaning |
|--------|------|------|
| `--light` | off | TWAMP Light (no control) |
| `-p, --control-port N` | 862 | Full control port |
| `-r, --port N` / `--receiver-port N` | 0 (Full: same as Sender Port / Light: 862) | Reflector-side UDP port |
| `--sender-port N` | 0 (chosen from 49152–57343) | Source UDP port |
| `-c, --count N` | 100 | Number of packets |
| `-i, --interval D` | 100ms | Send interval (`0.1`, `100ms`, `1s`) |
| `-L, --timeout D` | 2s | Loss timeout / Timeout field |
| `-s, --padding N` | 27 | Padding octets |
| `--padding-fill auto\|random\|zero` | auto | Padding contents (auto: random for Full, 0 for Light) |
| `-D, --dscp VALUE` | 0 | DSCP value or name |
| `--ttl N` | 255 | |
| `-S, --source ADDR` | automatic | Source address |
| `-4` / `-6` | auto | Address family |
| `-Z, --zero-addresses` | off | Set the addresses in Request-TW-Session to 0 (NAT) |
| `--start-time zero\|now` | zero | Value of the Start Time field |
| `--control-timeout D` | 15s | |
| `--reflector-sequence stateful\|copy_sender` | stateful | How the peer reflector assigns Sequence Numbers (§7.2) |
| `--dscp-ecn` | off | Read the RFC 7750 S-DSCP-ECN (Light only) |
| `--rpc-timeout D` (common to all commands) | 30s | Timeout for RPCs to the daemon |
| `--security unauthenticated\|auth\|encrypted\|mixed` | unauthenticated | TWAMP-Control mode (`auth` is short for `authenticated`). Full only (combining it with `--light` is an error) |
| `--key-id ID` | (empty) | KeyID for A / E / M. The key is looked up in gotwampd's `sender.keys_file` (the CLI does not handle keys). Cannot be combined with `--light` |
| `--isc` | off | Request RFC 5938 Individual Session Control (Full only. Not used if the Server does not offer it) |
| `--symmetrical` | off | Request RFC 6038 Symmetrical Size (Full only. Same as above) |
| `--reflect-octets N` | 0 | RFC 6038 Octets to be reflected: the 2-octet value the Server returns unchanged in Accept-Session (0–65535, hexadecimal with `0x` also allowed. Full only) |
| `--padding-to-reflect N` | 0 | RFC 6038 Length of padding to reflect: the length of padding the reflector returns unchanged (0–65535, must be smaller than `-s`. Full only). Specifying either this or `--reflect-octets` requests Reflect Octets |
| `--stamp` | off | Send as STAMP (§6.7. `--light` only) |
| `--ssid N` | 0 | STAMP SSID (0–65535. Requires `--stamp`) |
| `-q, --quiet` | off | Summary only |
| `--json` | off | Short for `--output json` |

Sample output (table):

```
TWAMP full 192.0.2.10:862 -> reflector 192.0.2.10:51234, 100 packets, interval 100ms, padding 27, dscp ef
seq=0   rtt=8.12ms  fwd=4.01ms  rev=4.10ms  refl=11us  ttl=251/251
seq=1   rtt=8.31ms  fwd=4.09ms  rev=4.21ms  refl=10us  ttl=251/251
seq=2   *LOST*
...
--- 192.0.2.10 TWAMP statistics ---
100 sent, 1 lost (1.000%), 0 dup (fwd 0 / rev 0), 0 reordered
rtt      min/median/mean/max = 8.12/8.29/8.31/9.12 ms, p95 8.61 ms, stddev 0.10 ms
forward  min/median/max = 4.01/4.05/4.52 ms   (clocks synchronized, max error 0.21 ms)
reverse  min/median/max = 4.10/4.22/4.61 ms
reflector processing min/max = 9/15 us
jitter   ipdv mean 0.04 ms, max 0.31 ms; pdv p95 0.49 ms; rfc3550 0.05 ms
hops     forward 4, reverse 4
timestamps: kernel (sender), reflector synced: yes
```

When one-way delay is invalid, both the forward and reverse lines get `(unsync: clocks not synchronized)`. When the clocks are synchronized but the error is unknown (`max_clock_error_unknown`), the forward line reads `(clocks synchronized, max error unknown)`.

- `--ttl` is 1–255 and `-p` is 1–65535 (0 is not allowed)
- If a Full-only flag (`--security` other than unauthenticated, `--key-id`, `--isc`, `--symmetrical`, `--reflect-octets`, `--padding-to-reflect`) is used with `--light`, or a Light-only flag (`--stamp`, `--ssid`) is used with Full, the CLI rejects it with exit code 2. `--dscp-ecn` specified with Full is rejected by the daemon with InvalidArgument
- If `reflected_mismatch` is non-zero, the summary includes a `reflect octets: N replies did not return …` line
- The CLI warns on stderr for test packets exceeding 1472 (IPv4) / 1452 (IPv6) octets. For ping, it decides based on the resolved padding received in RunStarted and the peer's address family

### 10.4 Exit Codes

| Code | Meaning |
|--------|------|
| 0 | Success (for ping, at least one packet received) |
| 1 | All packets lost in ping, or a general error (including when a daemon that is shutting down rejects an RPC with `Aborted`) |
| 2 | Invalid arguments or configuration |
| 3 | Cannot connect to the daemon |
| 4 | Protocol/control error (Server refusal, timeout, Modes mismatch) |
| 130 | Interrupted (SIGINT). ping prints the summary up to the point of interruption |

---

## 11. Interoperability Requirements and Compatibility Knobs

Requirements derived from the research notes (research/oss-implementations.md, research/vendor-*.md). The default is RFC-compliant; deviations are made through explicit configuration.

### 11.1 Characteristics of Interoperability Targets (Summary)

| Peer | Role | What gotwamp needs to be aware of | Source |
|------|------|--------------------------|------|
| perfSONAR twampd / twping | Full Server+Reflector / Client+Sender. **No TWAMP Light support** | twping requests a Receiver Port equal to its own Sender Port, default padding 27, real time in Start Time, Timeout ≈ RTT upper bound + 2s, addresses 0 with `-Z`. twampd returns an alternate port if the requested port is outside the `testports` range. Reflected packet length = max(received length, 41). Only the Sender address is verified. Authentication modes A/E/O (+Mixed) | oss §1 |
| Cisco IOS-XE / IOS-XR / NX-OS | **Reflector only** (no RFC 5357 Control-Client). Light on some XE platforms and XR, plus the TWAMP Light sender of PM (Performance Measurement) | Unauth only. Test port as requested (XR is documented to fail above 57343). XE uses the negotiated value for reflected DSCP, BE for Light. Older NX-OS returns Error Estimate Multiplier=0. XE RSP3 uses Hop Limit 64 on IPv6 reflected packets. timer inactivity / timeout default 900s. XE Light requires an exact match on remote-ip/port | vendor-cisco |
| Juniper Junos (testable with vJunos-router) | Full Client+Server, Light Client+Server (21.1R1+). Evolved is Unauth only | The Server requires `client-list` (ACL). The Client uses Sender Port = Receiver Port, TTL default 64, padding 60–1400 (pseudo-random), and with test-count 0 keeps using the same control connection, repeating Request/Start/Stop. TCP keepalive 120/5/6. Known ACX bug where Start-Ack is delayed by 10 seconds. Full on Junos OS is IPv4 only | vendor-juniper |
| Nokia SR Linux (free container) | **Full Server+Reflector** (default network-instance only, offers Unauth + mode bits 16/32/64), STAMP reflector (23.7+), STAMP sender (24.7+, OAM-PM IP). No Control-Client | Full: accepts only Receiver Port 862 or 49152–65535. Refuses/drops non-zero MBZ/SID, excessive Timeout, non-local Receiver Address, and arrivals before Start Time (`enforce-test-session-start-time`). `client-connection` prefix required. Padding is assumed to be 27 or more. The STAMP reflector uses ports 862 / 64364–64373 and identifies STAMP TLVs by `100xxxxx` at offset 44. Whether the features are enabled in the container version is unconfirmed; without a license there is a 1000pps cap | vendor-others §1 |
| Nokia SR OS (vSIM requires a license) | Full Server+Reflector, Light reflector (ports 862 / 64364–64373 only, prefix required, inactivity 10–100s default 100s, stateful), OAM-PM Light sender (source ports 64374–64383) | DSCP is reply-in-kind (copies the received value). Replies in the format matching the Z bit. IPv6 UDP checksum 0 is dropped by default | vendor-others §3 |
| Arista EOS / cEOS-lab | Only link-delay for IS-IS FlexAlgo (TWAMP Light, R3-series hardware only, IPv4 only). No general-purpose TWAMP server/reflector | **Cannot be used as a test target with cEOS-lab** | vendor-others §2 |
| Huawei NE/CX (no free image) | Full Server+Reflector, Light sender/responder (session defined by 4-tuple, period 10/100/1000/30000ms, default padding 128, timeout 5s) | `client acl`, `tcp port` default 862. The Light responder requires a match down to the sender's IP and port | vendor-others §4 |
| SONiC (VS image; whether a twamp container is included is unconfirmed) | Light sender/reflector (HLD 2023-06). Ports 862/863/1025–65535, interval 10/100/1000ms, timeout 1–10s, NTP/PTP format | Session defined by 4-tuple | vendor-others §5 |
| VyOS (rolling is free) | Full server (862) via perfSONAR twampd | Same behavior as perfSONAR | vendor-others §5 |

### 11.2 Compatibility Knob List

| ID | Setting | Default | Purpose | Origin |
|----|------|------|------|------|
| CMP-01 | `zero_addresses` | false | Set the Sender/Receiver Address in Request-TW-Session to 0 to traverse NAT | twping `-Z`, twamp-gui #15/#16 |
| CMP-02 | `start_time` | zero | Whether Start Time is 0 (immediate) or the real time. Default 0 to avoid drops caused by a lagging Server clock on Servers that enforce Start Time (SR Linux, etc.). gotwampd's reflector does not enforce Start Time by default, so if the peer is gotwampd neither value causes drops (CMP-26) | SR Linux `enforce-test-session-start-time`, twping |
| CMP-03 | `receiver_port` | 0 (= same as Sender Port) | Explicit Receiver Port to request. Requesting 862 is also possible | twping, Junos, RFC 8545 |
| CMP-04 | `accept_alternate_port` | true | Adopt the alternate port returned by the Server | RFC 5357 §3.5, perfSONAR |
| CMP-05 | `tolerate_zero_multiplier` | true | Do not drop packets with Error Estimate Multiplier=0 | NX-OS CSCwa79808 |
| CMP-06 | `ttl` | 255 | Sender's IP TTL. For comparison with the Junos default of 64 and for path constraints | Junos `ttl` |
| CMP-07 | `padding_fill` | auto | Random for Full, 0 for Light. Avoids collision with the Nokia reflector's STAMP detection (`100xxxxx` at offset 44), and an all-zero option that the RFC makes a MUST | RFC 5357 §4.2.1, RFC 8762, Nokia OAM guide, Junos `data-fill-with-zeros` |
| CMP-08 | reflector `light.dscp_policy` | copy | Choose the DSCP of Light reflected packets between copying the received value and a fixed value | RFC 7750, Cisco XE uses fixed BE |
| CMP-09 | reflector `control.verify_sender` | address | Strength of source verification (none in PAT environments) | perfSONAR #99/#100 |
| CMP-10 | reflector `light.sequence` | per_flow | Choose the Light Sequence Number between an own counter and copying the Sender's | RFC 5357 App. I, RFC 8762 stateful/stateless |
| CMP-11 | `control_timeout` | 15s | Tolerate a slow Start-Ack (the 10-second ACX delay) | Junos PR1556829 |
| CMP-12 | (fixed behavior) | — | Accept a reflected packet whose length differs from the sent length as long as it is at least 41 octets | Differences in truncation among vendors |
| CMP-13 | (fixed behavior) | — | Ignore unknown Modes bits and look only at the security mode (bits 0–3) and the feature bits we support (bits 4–6) | RFC 4656 §3.1, tcaine #24 |
| CMP-14 | `sender_port` | 0 | Fix the source port (when the peer defines the test session by 4-tuple) | Cisco XE Light, Huawei, SONiC |
| CMP-15 | (fixed behavior) / reflector `light.stamp` | false | The Light reflector does not refuse based on received length and also reflects 44-octet STAMP packets. With `light.stamp: true` it replies in STAMP format (copying the SSID, MBZ at 41–43, TLV flags) (§6.7) | RFC 8762 §4.6, RFC 8972 |
| CMP-16 | `control_dscp` | 0 | DSCP of the control connection | RFC 5357 §3.1, perfSONAR #100 |
| CMP-17 | reflector `control.honor_requested_port` | true | Use the requested port if it is free even when outside the range (perfSONAR only within the range) | perfSONAR |
| CMP-18 | `end_delay` | 0s | Wait between the last packet and Stop-Sessions (twping uses 1s) | twping `-E` |
| CMP-19 | (fixed behavior) | — | For IPv4 addresses in Request-TW-Session, always set the remaining 12 octets to 0 (a case where Junos MX refused) | GEANT report (UNVERIFIED) |
| CMP-20 | (fixed behavior) | — | The Server accepts a new Request-TW-Session on the same connection after Stop-Sessions | Junos client behavior |
| CMP-21 | `sender_port_range` | 49152-57343 | Range of the default source port and the requested Receiver Port. SR Linux / Junos Evolved accept only 862 or 49152–65535, and IOS-XR is documented to fail above 57343 | SR Linux YANG, Junos Evolved, Cisco XR |
| CMP-22 | reflector `light.listeners` | 862 | Receives the Nokia sender's default destination 862. Arbitrary ports, such as 64364–64373 for SR OS reflector compatibility, can be added | Nokia SR OS / SR Linux |
| CMP-23 | (fixed behavior) | — | The Light sender does not change its source port during a run (one socket per session) | Huawei / SONiC 4-tuple sessions |
| CMP-24 | (not implemented) | — | The Reflector replies in the timestamp format matching the Z bit of the received packet (reply-in-kind). v0.x is NTP only | Nokia SR OS, SONiC (PTP format) |
| CMP-25 | (fixed behavior) | — | Always compute the IPv6 UDP checksum (Nokia drops 0 by default). Go UDP sockets always compute it | Nokia `allow-ipv6-udp-checksum-zero` |
| CMP-26 | reflector `control.enforce_start_time` | false | Whether the Full reflector drops packets arriving before the Start Time of Request-TW-Session as `not_started`. The default false reflects everything after Start-Sessions, preventing loss of the first packets due to the sender's clock running ahead (twping runs a few ms ahead due to an NTP offset being adjusted) (§6.4). true matches the SR Linux default behavior. Implemented in the reflector package; wiring to the config key is not yet done (as of v0.4) | Interoperability testing with perfSONAR twping (2026-10), SR Linux `enforce-test-session-start-time` |

### 11.3 Unverified Items to Confirm by Testing

- Each Cisco OS: Start Time=0 vs. real time, response to Modes=2/4, multiple sessions on the same connection, acceptance of Sender Address=0
- Junos: how connections from outside client-list are refused, DSCP/TTL of reflected packets, Modes value in the Server Greeting
- perfSONAR: handling of requested ports outside `testports` (authentication modes A / E / M have been confirmed in both directions: twping → gotwampd and gotwamp → twampd)
- RFC 5938 / RFC 6038 / STAMP: no interoperable implementation is at hand (perfSONAR does not support them). Confirm with SR Linux (mode bits 16/32/64 and STAMP reflector)
- Hardware timestamping: confirmation on a physical NIC (the development environment is virtio)
- General: consistency of IPVN and Receiver Address when listening on IPv4-mapped IPv6
- SR Linux: whether the `twamp` / `stamp` / `oam-pm-ip` feature flags are enabled in the container version (`info from state system features`), the threshold for the Full server's `timeout-too-large`, DSCP/TTL reflection by the STAMP reflector
- SONiC-VS: whether the image includes the twamp container
- vJunos-router: whether the RPM TWAMP client/server works in a virtual environment (no primary source)

---

## 12. Non-Functional Requirements in Detail

### 12.1 Performance Targets

| Item | Target | Measurement method |
|------|------|----------|
| Sender scale | 100 sessions × 10pps (1,000pps total) at less than 50% of one CPU core | Nightly load test |
| Reflector throughput | Light 10,000pps (padding 27) with zero misses on one core | netns + a pktgen-like sending tool |
| Send interval accuracy | Deviation p99 < 1ms (interval ≥ 10ms, no load) | Statistics of send records |
| Added error | Median RTT error < 50µs with kernel timestamps | Comparison against a known delay with veth + netem |
| Memory | RSS < 200MiB with 100 resident sessions | soak test |
| Startup time | Within 5 seconds with 1,000 configured sessions | — |

### 12.2 Reliability

- In a 24-hour soak, memory, goroutines and fds do not grow continuously
- The Reflector does not panic on malformed packets, mass connections or malformed control messages (fuzz + load)
- A run failure is contained within its session and does not propagate to other sessions or the daemon (recover and log per goroutine)

### 12.3 Security

- External input (control messages, test packets, configuration, gRPC requests) is validated for length and range
- Reflector's exposed surface: ACL, limits, rate limit, reply length ≤ received length (except below 41; below 44 in STAMP mode, below 112 for A / E. §6.4, §6.6)
- Dependency vulnerabilities are detected with govulncheck. The cryptography for Auth/Encrypted is limited to the standard library (`crypto/aes`, `crypto/hmac`, `crypto/sha1`, `crypto/pbkdf2`) (M7)
- TWAMP Light is unauthenticated only (RFC 5357 Appendix I does not define key distribution)

### 12.4 Operability

- Every configuration item has a default, and gotwampd runs with a minimal configuration (`reflector.enabled: true`)
- Health can be checked with `gotwamp status` and `/healthz`. `/healthz` returns 200 if it can connect to the API socket
- The reason for a run failure is always recorded in logs and metrics (NFR-06)

---

## 13. Design Reservations for Future Extensions

| Extension | Reservation |
|------|----------|
| Authenticated / Encrypted / Mixed (M7) | **Implemented (M7-1, §5.2–§5.4, §6.2–§6.4)**. wire / control / reflector / sender, key store (`internal/control/keystore`, perfSONAR pfs format), gotwampd's `security` / `key_id` / `keys_file` / `reflector.control.modes`, the CLI's `--security` / `--key-id` (ADR 0011). The cryptographic layer is separated from the "plaintext bytes ⇄ message" layer, and is an identity transform for Unauth |
| RFC 6038 Reflect Octets / Symmetrical Size | **Implemented (M7-2, §5.3–§5.5, §6.2, §6.4)**. `wire.PaddingLayout` (`SenderHeaderSize`, `Validate`). The Symmetrical Size MBZ (27, 64 for A / E) is not included in Padding Length; the sender's header is longer by that amount. The reflector does not move padding (the original reservation "include the MBZ in the padding" was wrong and was corrected in M7-2) |
| RFC 7750 DSCP/ECN monitoring | **Implemented for Light** (`dscp_ecn_monitoring`, S-DSCP-ECN at offset 41). Negotiation of Modes bit 8 in Full is not implemented |
| RFC 8186 PTPv2 timestamps | A `ptptime` conversion alongside `ntptime` and interpretation of the Z bit. Modes bit 9 |
| RFC 5938 Individual Session Control | **Implemented (M7-2, §5.3, §5.6, §5.7)**. Start-N / Stop-N (`wire.SessionControl`), per-SID start/stop on the server, `StartN` / `StopN` on the client. gotwampd's sender remains one session per run |
| STAMP (RFC 8762/8972) | **Base packet compatibility implemented (M7-3, §6.7, ADR 0012)**. STAMP mode of the Light reflector and STAMP sending by the Light sender (`internal/twamp/wire/stamp.go`). Authenticated mode, actual TLV processing and session identification by SSID are out of scope. The hook on the Light reply path (`reflector.Deps.Hook`) is kept |
| Hardware timestamping | **Implemented (M7-4, §8.1, §8.4, ADR 0014)**. `tstamp`'s `ModeHardware`, `Options.Interface`, `RxMeta.Hardware` / `HardwareRejected`, `HardwareReporter`. Settings `timestamping: hardware` / `hardware_interface` / `clock.hardware_error`, proto `TIMESTAMPING_MODE_HARDWARE`. Correcting the difference between the PHC and CLOCK_REALTIME (PTP_SYS_OFFSET) and making `HardwareRejected` visible are future work |
| Control connection reuse / multiple sessions | The Control-Client is kept as a state machine that can send multiple Request-TW-Session messages in the IDLE state (§5.6). If ISC (RFC 5938) is negotiated, sessions can also be added while ACTIVE (the client package supports this). gotwampd's sender remains one connection and one session per run (continuous uses a single connection for the whole run) |
| Remote management (gRPC over TCP + mTLS), OTLP | gRPC over TCP + mTLS is **implemented (M7-5, ADR 0010, §9.5, §9.10)**. OTLP is deferred. Exports can be added via the `ResultSink` interface |
| Result persistence | **Implemented with SQLite (M7-7, §9.4, ADR 0013)**. The `store.Store` interface is unchanged, and `sqlite.Store` has `store.Memory` in front of it. `since` in `GetSessionStats` reaches back as far as `retention` using the DB's `Range`. Persistence of per-packet records and automatic VACUUM are out of scope |

---

## 14. Appendices

### Appendix A. Default Values

| Category | Item | Default |
|------|------|------|
| Common | Control port | 862/tcp |
| Common | Light reflection port | 862/udp |
| Sender | count / interval / timeout | 100 / 100ms / 2s |
| Sender | padding / padding_fill | 27 / auto (Full: random, Light: zero) |
| Sender | dscp / ttl | 0 / 255 |
| Sender | sender_port_range | 49152-57343 |
| Sender | receiver_port (Full) | Same as Sender Port |
| Sender | start_time | zero |
| Sender | control_timeout / end_delay | 15s / 0s |
| Sender | tx_timestamp_wait | 1ms |
| Sender | reflector_sequence / dscp_ecn_monitoring | stateful / false |
| Sender | security / key_id / keys_file | unauthenticated / (empty) / (empty) |
| Sender | extensions / octets_to_reflect / padding_to_reflect | (none) / 0 / 0 |
| Sender | stamp / ssid | false / 0 |
| Sender | padding (A / E) | 27 (64 for symmetry. Whether to make the default 64 for A / E is undecided) |
| Reflector | servwait / refwait / partial_message_timeout | 900s / 900s / 60s |
| Reflector | max_connections / max_sessions_per_connection / max_sessions | 64 / 16 / 1024 |
| Reflector | test_port_range | 20000-29999 |
| Reflector | verify_sender | address |
| Reflector | light.sequence / flow_timeout / max_flows | per_flow / 900s / 65536 |
| Reflector | light.dscp_policy / light.dscp_ecn_monitoring | copy / false |
| Reflector | max_packet_size / max_padding | 9000 / 9000 (Full returns Accept 3 when header length (14 / 48 for A and E) + padding > max_packet_size) |
| Reflector | DSCP of the control connection | 0 (fixed. `control.dscp` is reserved) |
| Reflector | Greeting Count (pbkdf2_count) | 2048 |
| Reflector | Offered security modes (control.modes) / control.keys_file | empty = unauthenticated only if there is no keys_file, all four if there is one / (empty) |
| Reflector | Offered extensions (control.extensions) | individual_session_control, reflect_octets, symmetrical_size |
| Reflector | control.enforce_start_time | false (everything after Start-Sessions is reflected. The config key is pending wiring) |
| Reflector | light.stamp | false |
| Reflector | Client-IV reuse detection | Most recent 4096 |
| daemon | timestamping / clock.userspace_error / clock.kernel_error / clock.hardware_error | auto / 50µs / 5µs / 1µs |
| daemon | hardware_interface | (empty. For the Light sender, the interface on the route to the destination) |
| daemon | UDS / metrics listen | /run/gotwamp/gotwampd.sock / 127.0.0.1:9862 |
| daemon | api.tcp.listen | (empty = disabled) |
| daemon | API over TCP: TLS handshake deadline / concurrency | 10s / 64 |
| daemon | result_history / max_samples_per_run | 1440 / 100,000 |
| daemon | max_concurrent_tests / max_watchers | 16 / 32 |
| daemon | export.jsonl: path / records / rotate.max_size / rotate.max_files / rotate.daily | /var/lib/gotwamp/results.jsonl / run_summary, window_summary, run_error / 100MiB / 10 / false |
| daemon | store.backend | memory |
| daemon | store.sqlite: path / retention / max_size / on_corrupt | /var/lib/gotwamp/results.db / 720h (30 days, 0 = unlimited) / 0 (no warning) / reset |
| daemon | SQLite writer: queue / records per transaction / retention deletion interval | 4096 records / 256 records / 1h (and at startup) |
| daemon | JSONL reopen wait / shutdown deadline | 1s / remainder of the shutdown budget (4.5s) |
| daemon | Tolerance between NIC time and software time with timestamping: hardware | ±1ms |
| CLI | RPC timeout | 30s |

### Appendix B. Packet Size Quick Reference (Unauthenticated)

| padding | Sender UDP payload | Reflector UDP payload | Notes |
|---------|----------------------|--------------------------|------|
| 0 | 14 | 41 | Reflected side is 27 octets larger |
| 27 | 41 | 41 | Default. Symmetric |
| 100 | 114 | 114 | Reflected side has 73 octets of padding |
| 1458 | 1472 | 1472 | Upper limit without fragmentation at IPv4 MTU 1500 |
| 1438 | 1452 | 1452 | Upper limit without fragmentation at IPv6 MTU 1500 |

Authenticated / Encrypted:

| padding | Sender UDP payload | Reflector UDP payload | Notes |
|---------|----------------------|--------------------------|------|
| 0 | 48 | 112 | Minimum. Reflected side is 64 octets larger (amplification factor 2.3) |
| 27 | 75 | 112 | With the default padding |
| 64 | 112 | 112 | Symmetric |
| 1424 | 1472 | 1472 | Upper limit without fragmentation at IPv4 MTU 1500 |

RFC 6038 Symmetrical Size and STAMP (padding denoted P):

| Format | Sender UDP payload | Reflector UDP payload | Notes |
|------|----------------------|--------------------------|------|
| Symmetrical Size (Unauth / Mixed) | 41 + P | 41 + P | The 27-octet MBZ is not included in Padding Length. Padding is not moved |
| Symmetrical Size (A / E) | 112 + P | 112 + P | MBZ 64 |
| STAMP (Light, `stamp`) | 44 + P (P is 0 or at least 4, Extra Padding TLV) | 44 + P | A reflector in STAMP mode uses max(received length, 44) |

### Appendix C. References

- RFC 5357, 4656, 5618, 5938, 6038, 7717, 7750, 8186, 8545, 8762, 8972 (protocols)
- RFC 2681, 3393, 3550, 4737, 5481, 6673, 7679, 7680 (metrics)
- docs/research/rfc-twamp-protocol.md (RFC review, Errata, UNVERIFIED list)
- docs/research/oss-implementations.md (survey of perfSONAR and other OSS implementations)
- docs/research/vendor-cisco.md, vendor-juniper.md, vendor-others.md (survey of vendor implementations)
- Linux: `socket(7)` SO_TIMESTAMPING (Documentation/networking/timestamping.rst), `adjtimex(2)`, `ip(7)` IP_RECVTTL / IP_TOS, `ipv6(7)` IPV6_RECVHOPLIMIT / IPV6_TCLASS
