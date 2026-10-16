# gotwamp documentation

This directory holds the design and operations documents of gotwamp. Start with the
[project README](../README.md) for installation and a quick tour; come here for the details.

## Index

### Specification and plan

- [SPEC.md](SPEC.md): the implementation specification. How gotwamp behaves on the wire (TWAMP-Control,
  TWAMP-Test, STAMP), how it measures, and the daemon, API, CLI and defaults (Appendix A). The reference
  for any behaviour question; it follows the RFCs and records where it deliberately deviates.
- [PLAN.md](PLAN.md): the implementation plan. Goals, design decisions from the initial interview,
  repository layout, milestones M0–M7, test strategy, the interoperability test plan and quality criteria.

### Research notes ([research/](research/))

Background surveys written before the implementation (2026-10-03); the specification cites them by section.

- [rfc-twamp-protocol.md](research/rfc-twamp-protocol.md): the TWAMP protocol family (RFC 5357, 4656 and
  extensions, STAMP) message by message, with errata and the ambiguities the specification had to decide.
- [oss-implementations.md](research/oss-implementations.md): open-source TWAMP / STAMP implementations
  (perfSONAR and others), their designs, and the pitfalls and bugs seen in them.
- [vendor-cisco.md](research/vendor-cisco.md): TWAMP and TWAMP Light on Cisco IOS-XE, IOS-XR and NX-OS.
- [vendor-juniper.md](research/vendor-juniper.md): TWAMP and TWAMP Light on Junos OS, Junos OS Evolved and
  cRPD, and the free lab images.
- [vendor-others.md](research/vendor-others.md): Nokia SR Linux and SR OS, Arista EOS, Huawei, SONiC, VyOS,
  OpenWrt and containerlab.

### Architecture Decision Records ([adr/](adr/))

One file per decision: the context, the decision, the options considered and the consequences.
New records start from [0000-template.md](adr/0000-template.md).

| ADR | Decision |
|-----|----------|
| [0001](adr/0001-grpc-over-uds.md) | The daemon and the CLI talk gRPC over a Unix domain socket |
| [0002](adr/0002-timestamping-strategy.md) | Prefer kernel software timestamps (SO_TIMESTAMPING), fall back to user space |
| [0003](adr/0003-light-reflector-per-flow-sequence.md) | The TWAMP Light reflector numbers its replies per flow |
| [0004](adr/0004-declscope.md) | declscope is a required lint |
| [0005](adr/0005-grpc-api-field-presence.md) | Unset API fields take the daemon defaults; scalars where 0 matters are `optional` |
| [0006](adr/0006-stoptest.md) | A StopTest RPC stops an ad hoc measurement midway |
| [0007](adr/0007-full-continuous-control-connection.md) | A continuous TWAMP Full session keeps one control connection |
| [0008](adr/0008-reload-scope.md) | What a configuration reload applies |
| [0009](adr/0009-adhoc-not-exported.md) | Ad hoc measurement results are not stored or exported |
| [0010](adr/0010-remote-management.md) | The gRPC API is also served over TCP with mTLS |
| [0011](adr/0011-auth-layer-and-keystore.md) | Authenticated modes as a separate crypto layer; keys in pfs-format files |
| [0012](adr/0012-stamp-compatibility-scope.md) | STAMP as base-packet compatibility of TWAMP Light |
| [0013](adr/0013-sqlite-result-store.md) | Results persist in SQLite behind an in-memory front |
| [0014](adr/0014-hardware-timestamping-phc.md) | Hardware timestamping only when asked for; the PHC must follow CLOCK_REALTIME |

### Operations ([ops/](ops/))

- [config.md](ops/config.md): the configuration reference: every key of `gotwampd.yaml`, its default and
  validation, keys files and certificates.
- [metrics.md](ops/metrics.md): the Prometheus metrics and example alerts; an example Grafana dashboard is
  [grafana-dashboard.json](ops/grafana-dashboard.json).
- [troubleshooting.md](ops/troubleshooting.md): exposing a reflector safely, privileges, timestamping and
  clocks, and how to isolate common failures.
- [interop.md](ops/interop.md): interoperability tests (perfSONAR automated; SR Linux and vJunos-router in
  containerlab) and the known differences between implementations.
- [staging.md](ops/staging.md): a docker compose test bench with a sender, three reflectors and impaired links.

## Glossary

The definitions are in [SPEC §3](SPEC.md) (terms, T1–T4, RTT and delay metrics). This list fixes the
spelling the documents use and separates terms that are easy to confuse.

| Term (preferred spelling) | Meaning | Not used / notes |
|---------------------------|---------|------------------|
| **TWAMP Full**, Full | TWAMP with TWAMP-Control (RFC 5357 §3–§4); `mode: full` | not "Full TWAMP", "TWAMP-Full" |
| **TWAMP Light**, Light | TWAMP-Test only, configured on both ends (RFC 5357 Appendix I); `mode: light` | not "TWAMP-Light" (kept only inside quotations of vendor documents) |
| **STAMP** | RFC 8762 / RFC 8972; gotwamp implements it as a mode of TWAMP Light (`stamp`) | |
| **sender** | The gotwampd role that measures: Control-Client and Session-Sender in Full, Session-Sender in Light; the `sender` section of the configuration | Capitalized at the start of a sentence or when naming the protocol endpoint; same meaning |
| **reflector** | The gotwampd role that answers: Server and Session-Reflector in Full, a Session-Reflector on configured ports in Light; the `reflector` section | gotwamp does not use "responder"; it appears only as the product term of other implementations (Cisco IP SLA responder, Huawei responder, twampy `responder`) |
| **Control-Client**, Client | The RFC 5357 endpoint that opens TWAMP-Control and requests sessions. In SPEC §5, "Client" is short for Control-Client and pairs with "Server" | Not the gRPC client: the CLI and other API users are "API clients" |
| **Server** | The RFC 5357 endpoint that accepts TWAMP-Control (part of the reflector role) | |
| **Session-Sender**, **Session-Reflector** | The RFC 5357 endpoints of TWAMP-Test | always hyphenated |
| **test session** | One TWAMP test stream, identified by its SID in Full (one Request-TW-Session) | |
| **monitoring session** | A named measurement defined in the configuration or through the API, run on a schedule | "session" alone where the context is clear |
| **ad hoc measurement** | A one-off measurement started by `gotwamp ping` (RunTest); not stored or exported | not "ad-hoc", "adhoc" (except in identifiers) |
| **run** | One execution of a measurement: in Full one control connection, in Light `count` packets. Periodic sessions start one run per `frequency`; an ad hoc measurement is one run | |
| **window** | The span (`window`) that cuts a continuous run into statistics; it ends in a window summary | |
| **run summary**, **window summary**, **run error** | The results of a run or window; JSON Lines types `run_summary`, `window_summary`, `run_error` | |
| **flow** | What the TWAMP Light reflector keeps state for: (source address, source port, destination address, destination port) | |
| **timestamping mode** | How receive (and the sender's transmit) times are taken: `auto`, `kernel`, `hardware`, `userspace` (`daemon.timestamping`, metric `gotwamp_timestamping_mode`, proto `TimestampingMode`). Write the values in code font | not "timestamping method" or "timestamp source" |
| **hardware timestamping** | The `hardware` timestamping mode: timestamps from the NIC's PTP hardware clock | "timestamp" is one word |
| **method error** | The part of the Error Estimate that a timestamping mode adds (`userspace_error`, `kernel_error`, `hardware_error`) | |
| **security mode** | `unauthenticated`, `authenticated`, `encrypted`, `mixed` (A / E / M for the last three) | "authenticated modes" means A, E and M together |
| **extension** | A TWAMP feature negotiated in the Modes field: Individual Session Control (ISC, RFC 5938), Reflect Octets and Symmetrical Size (RFC 6038) | |
| **compatibility knob** | A setting that absorbs a difference between implementations, numbered `CMP-xx` (SPEC §11.2) | |

## Embedding

- [Go library API and capability boundaries](LIBRARY.md)
