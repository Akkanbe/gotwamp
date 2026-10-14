# gotwamp Implementation Plan

| Item | Value |
|------|------|
| Version | 0.1 (draft) |
| Created | 2026-10-03 |
| Target specification | [docs/SPEC.md](SPEC.md) v0.1 |
| Research notes | [docs/research/](research/) (RFC review / OSS implementation survey / vendor implementation survey) |

---

## 1. Purpose and Goals

### 1.1 Purpose

Develop **gotwamp**, an implementation of TWAMP (Two-Way Active Measurement Protocol, RFC 5357) in Go.
It takes on both the Sender (Control-Client + Session-Sender) and Reflector (Server + Session-Reflector) roles,
and supports both TWAMP Full, which uses the control protocol, and TWAMP Light, which does not.
The Sender side is split into a resident daemon (`gotwampd`) and a CLI (`gotwamp`), providing both continuous monitoring like IP SLA / RPM
and ad hoc measurement like ping / twping.

### 1.2 Goals (scope achieved in the initial releases v0.1–v0.3)

- RFC 5357-compliant TWAMP-Control / TWAMP-Test (Unauthenticated mode) Sender and Reflector implementation
- TWAMP Light Sender / Reflector (equivalent to RFC 5357 Appendix I, with RFC 8545 port 862 support)
- `gotwampd`: a resident daemon whose sender / reflector roles can be enabled via configuration
- `gotwamp`: a CLI that operates the daemon via gRPC over a Unix domain socket
- Continuous monitoring sessions (periodic / continuous execution) and ad hoc measurement from the CLI (streaming display)
- Kernel timestamp acquisition via Linux SO_TIMESTAMPING (software), with fallback on unsupported environments
- Prometheus exporter and JSON Lines output
- Interoperability verification with perfSONAR (twampd / twping) and freely available NOS images (Nokia SR Linux, Juniper vJunos-router, etc.)

### 1.3 Non-goals (not covered in the initial releases)

- Authenticated / Encrypted / Mixed (RFC 5618) modes → later phase (M7). However, extension points are reserved in the design from the start
- Implementation of OWAMP (RFC 4656) itself (only the parts TWAMP reuses are implemented)
- Support for OSes other than Linux (macOS / Windows)
- Hardware timestamping (NIC PHC) → later phase
- STAMP (RFC 8762 / 8972) TLV extensions → to be considered later. However, base packet compatibility is taken into account
- GUI / Web UI, long-term history DB (left to Prometheus)

---

## 2. Interview Results and Design Decisions

Decisions confirmed in the interview on 2026-10-03. See SPEC §2 for the detailed rationale.

| # | Item | Decision | Reason / impact |
|---|------|------|-----------|
| 1 | Supported modes | **TWAMP Full + TWAMP Light** | To interoperate with both vendor equipment (mostly Light) and perfSONAR (Full). Implement both the Full Server/Control-Client and the Light stateless reflector |
| 2 | Security modes | **Unauthenticated first**, Auth/Encrypted in a later phase | Most real-world deployments are Unauth. A mode abstraction is placed in the packet encoding layer from the start so that key derivation (PBKDF2) / AES-CBC / HMAC-SHA1 can be added later |
| 3 | Sender use cases | **Both continuous monitoring and ad hoc measurement** | The daemon has a session scheduler, result store and exporter; ad hoc measurements from the CLI are also performed by the daemon on its behalf and streamed back |
| 4 | Interop targets | **Freely available OSS / containers** (perfSONAR, Nokia SR Linux, Arista cEOS, etc.) | Do not assume physical hardware; build a reproducible test environment with containerlab + Docker. Research showed cEOS has no general-purpose TWAMP, so it is out of scope and vJunos-router is added instead (§8) |
| 5 | daemon–CLI IPC | **gRPC over Unix domain socket** | Typed API and server streaming. Extensible to remote management over TCP+TLS in the future |
| 6 | Measurement result output | **Prometheus exporter + JSON Lines** | Supports both visualization in Grafana and ingestion into other systems. OTLP to be considered later |
| 7 | Timestamps | **SO_TIMESTAMPING (SW) + time.Now() fallback** | Eliminates user-space jitter. The Error Estimate value also varies with the timestamping mode |
| 8 | Platform | **Linux only** (amd64 / arm64) | Focus on SO_TIMESTAMPING, systemd and container distribution |
| 9 | Binary layout | **2 binaries: `gotwampd` + `gotwamp`** | The daemon enables roles (sender / reflector / both) via configuration. The CLI is a thin gRPC client |
| 10 | Language | **Documentation in Japanese; code / comments / commits in English** | Keep it readable when published as OSS |
| 11 | Dependency policy | **Well-established libraries are allowed** (cobra, grpc/protobuf, prometheus/client_golang, x/sys, yaml.v3) | Protocol implementation (encoding, cryptography) is written with the standard library only, keeping dependencies out of the inner layers |
| 12 | License | **Apache-2.0** | Go module path is `github.com/Akkanbe/gotwamp` (finalized 2026-10-04. Public repository) |

---

## 3. Deliverables

| Deliverable | Description |
|--------|------|
| `gotwampd` | Resident daemon. Sender role (Control-Client / Session-Sender / scheduler / result store / exporter), reflector role (Server / Session-Reflector / Light reflector), gRPC API, Prometheus HTTP, JSON Lines writer |
| `gotwamp` | CLI. `ping` (ad hoc measurement), `session` (monitoring session management), `stats`, `status`, `reflector`, `config validate`, `version` |
| `api/proto/gotwamp/v1/*.proto` | gRPC API definitions (generated code included) |
| `internal/twamp` and other libraries | Protocol implementation. Structured so it can later become a public API under `pkg/` |
| `deploy/systemd/gotwampd.service` | systemd unit (CAP_NET_BIND_SERVICE, dedicated user) |
| `deploy/docker/Dockerfile` | distroless-based container image |
| `deploy/containerlab/*.clab.yml` | Topologies for interop testing (perfSONAR / SR Linux / vJunos-router and others) |
| `docs/` | Specification (SPEC.md), this plan, research notes (research/), ADRs (adr/), operations guide |
| CI | GitHub Actions: build / unit test (race) / lint / vuln check / interop test (nightly) / release |

---

## 4. Development Environment and Toolchain

| Category | Tool | Notes |
|------|--------|------|
| Language | Go 1.26 (development machine: go1.26.7 linux/amd64) | The `go` directive in `go.mod` is 1.26. No CGO required (`CGO_ENABLED=0`) |
| Main dependencies | `google.golang.org/grpc`, `google.golang.org/protobuf`, `github.com/spf13/cobra`, `github.com/prometheus/client_golang`, `golang.org/x/sys`, `gopkg.in/yaml.v3` | The protocol layer (`internal/twamp`) uses the standard library only |
| proto generation | `buf` (`buf generate`, `buf lint`, `buf breaking`) | `protoc-gen-go`, `protoc-gen-go-grpc` |
| Static analysis | `golangci-lint`, `go vet`, `govulncheck`, `gofumpt` | Required in CI |
| Testing | `go test -race`, `go test -fuzz`, `testing/synctest` (Go 1.25+) | Time-dependent logic is verified deterministically with synctest |
| Packet verification | `tcpdump`, `tshark` (Wireshark's `twamp` dissector) | Used for third-party verification of the on-wire format |
| Interoperability | Docker, containerlab, vrnetlab, `perfsonar/testpoint` image, Juniper vJunos-router, Nokia SR Linux (`ghcr.io/nokia/srlinux`), Arista cEOS-lab | See §8 for details |
| Network emulation | `tc netem` (injecting delay, loss, reordering, duplication) | Used in component tests |
| Release | `goreleaser` (linux/amd64, linux/arm64, deb/rpm, containers) | SBOM (syft) is optional |

---

## 5. Repository Layout (planned)

```
gotwamp/
├── cmd/
│   ├── gotwampd/            # daemon entry point (wiring only)
│   └── gotwamp/             # CLI entry point (cobra)
├── api/
│   └── proto/gotwamp/v1/    # gRPC / protobuf definitions (managed by buf)
│       ├── daemon.proto     # DaemonService
│       ├── sender.proto     # SenderService (RunTest, Session*, Stats, Watch)
│       ├── reflector.proto  # ReflectorService
│       └── types.proto      # TestSpec, PacketResult, Summary, etc.
├── internal/
│   ├── twamp/               # protocol core (pure functions, no I/O, standard library only)
│   │   ├── wire/            # encode/decode of Control / Test messages, Modes, Accept, etc.
│   │   ├── ntptime/         # NTP 64-bit timestamp conversion (era-aware)
│   │   └── errest/          # Error Estimate computation and encoding
│   ├── control/             # TWAMP-Control: client (Control-Client) / server (Server) state machines
│   ├── sender/              # Session-Sender: send/receive loop, seq matching, timeouts, per-run aggregation
│   ├── reflector/           # Session-Reflector (Full) and Light reflector (flow table, seq)
│   ├── tstamp/              # socket abstraction: SO_TIMESTAMPING / cmsg(TTL,TOS) / fallback
│   ├── clock/               # sync state and maximum error via adjtimex
│   ├── stats/               # RTT/OWD/IPDV/PDV/loss/reorder/dup, percentile
│   ├── schedule/            # periodic / continuous scheduler for monitoring sessions
│   ├── store/               # result ring buffer (run summary / window summary)
│   ├── export/
│   │   ├── prom/            # Prometheus collector
│   │   └── jsonl/           # JSON Lines writer (rotation)
│   ├── config/              # YAML schema, validation, diff application
│   ├── daemon/              # gotwampd service wiring, gRPC server implementation, signals/reload
│   └── cli/                 # gotwamp subcommand implementations (gRPC client + display)
├── deploy/
│   ├── systemd/gotwampd.service
│   ├── docker/Dockerfile
│   └── containerlab/        # interop lab definitions and NOS configurations
├── test/
│   ├── golden/              # fixed byte sequences derived from RFC figures (encode/decode verification)
│   ├── component/           # integration tests using loopback / netns + netem
│   └── interop/             # interop test scripts and expected values
├── docs/
│   ├── SPEC.md              # implementation specification
│   ├── PLAN.md              # this document
│   ├── adr/                 # Architecture Decision Records
│   ├── research/            # RFC / OSS / vendor research
│   └── ops/                 # operations guide (config examples, metrics list, troubleshooting)
├── Makefile                 # build / test / lint / proto / interop / release
├── buf.yaml, buf.gen.yaml
├── .golangci.yml
├── .github/workflows/
├── go.mod, go.sum
├── LICENSE (Apache-2.0)
└── README.md
```

Dependency direction principle: `cmd` → `daemon`/`cli` → (`control`, `sender`, `reflector`, `schedule`, `store`, `export`) → (`tstamp`, `clock`, `stats`) → `twamp` (innermost layer).
The `twamp` package is a pure encoding layer that does not depend even on `net`, and is hardened with fuzz tests and golden tests.

---

## 6. Milestones and Phases

Each milestone is delimited by "something that works". Effort is an estimate for one person working on it full-time (S: up to 2 days, M: 3–5 days, L: 1–2 weeks).

### M0: Project foundation (S)

- **Tasks**: `go mod init`, directory skeleton, Makefile, `buf` configuration and empty proto, golangci-lint / govulncheck configuration, GitHub Actions (build / test / lint), LICENSE, README, ADR template, CLAUDE.md (development conventions)
- **Deliverables**: an empty repository on which CI is green
- **Definition of Done (DoD)**: `make build test lint proto` succeeds locally and in CI

### M1: Protocol core (M)

- **Tasks**:
  - `internal/twamp/wire`: encode/decode of Server Greeting / Set-Up-Response / Server-Start / Request-TW-Session / Accept-Session / Start-Sessions / Start-Ack / Stop-Sessions (Unauth. Fields for Auth/Encrypted are also kept in the types, with HMAC zero-filled)
  - encode/decode of Sender / Reflector test packets (Unauth format). For the Auth/Encrypted formats, only types and size constants are defined up front
  - `ntptime`: `time.Time` ⇔ NTP 64-bit conversion (1900 epoch, 2036 era rollover, monotonicity)
  - `errest`: Error Estimate computation (S/Z/Scale/Multiplier) and decoding (conversion to seconds)
  - Modes / Accept / Command constants and IANA-registered values
  - `stats`: computation of RTT / one-way delay / IPDV / PDV / loss / reorder (RFC 4737) / duplicate, percentile
- **Deliverables**: unit tests (golden byte sequences), fuzz tests, known-input tests for `stats`
- **DoD**: all golden vectors derived from RFC figures match, and fuzzing of the decoders runs for 10+ minutes without crashing. tshark correctly interprets generated packets as TWAMP

### M2: TWAMP Light (the first end-to-end working form) (L)

- **Tasks**:
  - `tstamp`: UDP socket abstraction (user-space timestamp version). Setting IP_TTL/IPV6_UNICAST_HOPS=255 and IP_TOS/IPV6_TCLASS, receiving IP_RECVTTL/IPV6_RECVHOPLIMIT and IP_RECVTOS/IPV6_RECVTCLASS
  - `reflector`: Light reflector (multiple listeners, flow table and per-flow sequence, DSCP reflection, TTL reflection, ACL, flow expiry equivalent to REFWAIT)
  - `sender`: Session-Sender loop (send schedule based on absolute time, receive matching, timeout determination, per-run aggregation)
  - `daemon`: minimal `gotwampd` (config loading, reflector role, gRPC `RunTest` streaming, `GetStatus`)
  - `cli`: `gotwamp ping` (streaming display of per-packet lines + final summary, `--json`), `gotwamp status`
- **Deliverables**: `gotwampd` (reflector) ⇄ `gotwamp ping --light` works over loopback and netns
- **DoD**: integration tests that inject delay/loss/duplication/reordering with netem produce the expected statistics. Light measurement works against the SR Linux STAMP session-reflector (if the feature is enabled in the container), and gotwampd's Light reflector replies to packets from the SR Linux STAMP sender

### M3: TWAMP Full (control protocol) (L)

- **Tasks**:
  - `control/client`: Control-Client state machine (Greeting → Set-Up-Response → Server-Start → Request-TW-Session → Accept-Session → Start-Sessions → Start-Ack → measurement → Stop-Sessions), per-stage timeouts, Modes evaluation, error formatting
  - `control/server`: Server (TCP 862 listener, per-connection state machine, SERVWAIT, Modes=0 refusal handling, ACL, connection and session count limits, Receiver Port allocation and port range configuration)
  - `reflector`: Full-mode Session-Reflector (UDP socket bound to the session, source validation and NAT relaxation option, REFWAIT, cleanup on Stop-Sessions / disconnect)
  - `sender`: Full-mode run (one control connection per run, pre-binding and announcing the Sender Port, defaults and overrides for Request fields)
  - CLI: make Full the default for `gotwamp ping <host>`, Light with `--light`
- **Deliverables**: bidirectional interop of perfSONAR `twping` → `gotwampd` reflector and `gotwamp ping` → perfSONAR `twampd`. If possible, bidirectional with vJunos-router's RPM TWAMP client/server as well
- **DoD**: bidirectional interop tests with perfSONAR run automatically in CI (Docker) and succeed. Also succeeds against the SR Linux TWAMP Full server (if enabled) and vJunos-router's RPM TWAMP server/client. Wireshark correctly interprets both control and test traffic

### M4: Measurement accuracy (M)

- **Tasks**:
  - `tstamp`: implementation of SO_TIMESTAMPING (RX: SCM_TIMESTAMPING, TX: MSG_ERRQUEUE + OPT_ID/OPT_TSONLY), capability detection and automatic fallback, setting `timestamping: auto|kernel|userspace`
  - `clock`: sync determination (S bit) and maximum error (maxerror) via adjtimex, reflected in Error Estimate
  - Improving send-loop accuracy (absolute deadlines, buffer pool, eliminating allocations on the hot path), GC tuning guidelines
  - Validity determination for one-way delay (shown as valid only when S=1 at both ends)
- **Deliverables**: accuracy evaluation report (error over loopback / veth + netem, kernel vs userspace comparison)
- **DoD**: median RTT error under 50µs against veth + netem 10ms delay (with kernel timestamps, as a guideline). Tests exist for the fallback path

### M5: Continuous monitoring daemon features (L)

- **Tasks**:
  - `config`: monitoring session definitions (periodic / continuous), default inheritance, validation, reload via SIGHUP / gRPC with diff application
  - `schedule`: per-session scheduler, startup jitter spreading, prevention of overlapping runs
  - `store`: ring buffers for run summary / window summary, queries for recent statistics
  - `export/prom`: metric definitions (histograms + recent-summary gauges + counters + runtime info), `/metrics` `/healthz`
  - `export/jsonl`: summary / packet record output, rotation (size / daily)
  - gRPC: `CreateSession/UpdateSession/DeleteSession/ListSessions/GetSession/StartSession/StopSession/GetSessionStats/WatchResults`
  - CLI: `gotwamp session ...`, `gotwamp stats`, `gotwamp config validate|reload`
- **Deliverables**: a daemon that continuously measures multiple destinations defined in the configuration file and can be visualized in Grafana
- **DoD**: 100 sessions × 10pps run continuously for 24 hours with no memory/goroutine leaks (soak test). Configuration reload does not interrupt unrelated sessions

### M6: Operability, distribution, interop suite (M)

- **Tasks**:
  - systemd unit (dedicated user, `AmbientCapabilities=CAP_NET_BIND_SERVICE`, hardening), Dockerfile, goreleaser, deb/rpm
  - containerlab topologies (perfSONAR / SR Linux / vJunos-router / optionally XRd, CSR1000v, SONiC, VyOS) and `make interop`
  - Operations documentation (configuration reference, metrics list, example Grafana dashboard, troubleshooting)
  - Release v0.1.0
- **DoD**: pushing a tag produces each artifact, and the interop suite runs nightly

### M7 (later): Extensions

Priorities are revisited based on usage.

1. Authenticated / Encrypted modes (RFC 4656 key derivation, AES-CBC, HMAC-SHA1, RFC 5618 Mixed), a keys file equivalent to `twampd.pfs`, authenticated interop with perfSONAR
2. RFC 6038 Reflect Octets / Symmetrical Size, RFC 7750 DSCP/ECN monitoring, RFC 5938 Individual Session Control
3. STAMP (RFC 8762) base packet compatible reflector / sender mode
4. Hardware timestamping (SOF_TIMESTAMPING_RX/TX_HARDWARE, PHC)
5. Exposing gRPC over TCP + mTLS (remote management), OTLP export
6. Whether to have a daemon-independent CLI mode (`gotwamp ping --direct`)
7. Result persistence (SQLite, etc.), long-term history

---

## 7. Test Strategy

| Layer | Target | Method | Where run |
|--------|------|------|----------|
| Unit (golden) | `twamp/wire`, `ntptime`, `errest` | Exact match against byte sequences derived from RFC figures, boundary values (minimum length / maximum padding / IPv6), round-trip property | `go test` (every PR) |
| Unit (fuzz) | All decoders | `go test -fuzz`, short in CI, long in nightly | Every PR / nightly |
| Unit (logic) | `stats`, `schedule`, `store`, `config` | Expected values for known inputs, time determinism via `testing/synctest` | Every PR |
| Component | sender ⇄ reflector (Light / Full) | Same-process loopback, `netns` + `veth` + `tc netem` (delay, jitter, loss, duplication, reordering), resilience to source spoofing and malformed packets | Every PR (loopback) / nightly (netns, requires root or CAP_NET_ADMIN) |
| Interoperability | perfSONAR, NOS containers | Bring up the §8 topologies with containerlab / docker compose, measure bidirectionally and check against thresholds | nightly / manual |
| Accuracy | `tstamp`, send loop | Distribution of differences against known delay (netem), kernel vs userspace, degradation under load (CPU contention) | Manual + report |
| Performance / endurance | Whole daemon | Scaling of sessions × pps, 24h soak, allocation checks with pprof, `-race` | nightly / before release |
| Static | Everything | golangci-lint, govulncheck, buf lint/breaking | Every PR |

Test data handling: pcaps and expected values obtained from interop are stored in `test/interop/fixtures/` and used as the basis for regression judgments.

---

## 8. Interoperability Test Plan

### 8.1 Targets and priorities (updated with research results)

Research notes: research/oss-implementations.md, research/vendor-cisco.md, research/vendor-juniper.md, research/vendor-others.md (supplementary research).

| Priority | Target | How to obtain | Roles that can be paired with gotwamp | Notes |
|------|------|----------|-------------------------------|------|
| Required | perfSONAR `twampd` / `twping` | Docker `perfsonar/testpoint` (free) | **Full only**: twping → gotwampd reflector, gotwamp sender → twampd | OSS reference. **Does not support TWAMP Light**, so it cannot be used for Light verification. Main target for CI automation |
| Required | gotwamp ⇄ gotwamp | — | All Full / Light combinations | Foundation for regression tests. Fault injection with netem |
| High | Juniper vJunos-router | juniper.net (free download, containerized with vrnetlab) | **Full Client+Server, Light Client+Server** (vMX-based, Light is 21.1R1+) | The only freely available device that "has an RFC 5357 Control-Client". The Server requires `client-list` (allowed CIDRs). Full on Junos OS is IPv4 only. Behavior on vJunos is not confirmed by primary sources (UNVERIFIED) |
| High | Nokia SR Linux | `ghcr.io/nokia/srlinux` (free, no account required) | **Full Server+Reflector** (Unauth, default network-instance only), **STAMP reflector** (23.7+), **STAMP sender** (24.7+, OAM-PM IP). No Control-Client. STAMP is wire-compatible with TWAMP Light | Unconfirmed whether the `twamp` / `stamp` / `oam-pm-ip` feature flags are enabled in the container version (check with `info from state system features`). Without a license, the datapath is limited to 1000pps with weekly restarts. Receiver Port must be 862 or 49152–65535 |
| Out of scope | Arista cEOS-lab | arista.com (free account) | EOS TWAMP is only link-delay for IS-IS FlexAlgo (TWAMP Light, 7280R3/7500R3/7800R3 hardware only, IPv4 only); there is no general-purpose server/reflector | Cannot be verified on cEOS-lab, so it is removed from the plan |
| Medium | Cisco XRd (Control-Plane) | cisco.com (containerlab documentation states a service account is required) | PM (Performance Measurement) **TWAMP Light sender/responder** is supported with software timestamps from 24.3.1. Presence of `ipsla server/responder twamp` is unconfirmed | Cisco OSes **do not have an RFC 5357 Control-Client** (reflector only). Can act as a Light sender to verify the gotwampd reflector |
| Medium | Cisco CSR1000v / C8000V | cisco.com (evaluation license) | IP SLA TWAMP responder (**Full Server+Reflector**). TWAMP Light is not supported on the ASR1k family | For verifying gotwamp sender → Cisco reflector (Full). Unauthenticated only |
| Medium | Juniper vJunosEvolved | juniper.net (free) | `services monitoring twamp` (Evolved). STAMP (RFC 8762) as well | Timestamping on the virtual PFE (RE time with `offload-type none`) needs to be checked |
| Low | Cisco N9Kv | cisco.com | Presence of an NX-OS TWAMP responder is unconfirmed | Known bug of Error Estimate Multiplier=0 in older versions. IPv6 reflection defect (10.2(4)) |
| Low | SONiC-VS | Free (Azure pipeline artifact) | TWAMP Light sender/reflector (HLD 2023-06, ports 862/863/1025–65535, interval 10/100/1000ms) | Unconfirmed whether the VS image includes the twamp container |
| Low | VyOS (rolling) | Free | Full server (862) via perfSONAR twampd | Equivalent to perfSONAR. optional |
| Low | Nokia SR OS (vSIM) | License required | Full server, Light reflector (862 / 64364–64373, prefix required), OAM-PM Light sender | Only if a license is available |

Key points:

- The only peers with an external Control-Client that can verify gotwampd's **Server/Reflector (Full)** are perfSONAR twping and the Junos client (Cisco is reflector only)
- gotwampd's **Light reflector** is verified with the Cisco XR/XE PM TWAMP Light querier, the Junos light client, the SR Linux STAMP sender (24.7+) and the SONiC sender. cEOS is out of scope
- gotwamp's **Light sender** is verified with the SR Linux STAMP reflector (port 862), the Junos light server and the Cisco XR twamp-light responder
- Some free images still require an account or entitlement on the vendor site, so in CI perfSONAR and gotwamp-to-gotwamp are required, while NOS tests "run if the image is available"

### 8.2 Topology

```
                 containerlab bridge (mgmt + data)
   +-----------+        +-----------+        +-----------+
   | gotwamp   |--------|  netem    |--------| peer      |
   | (sender/  |        | (optional)|        | perfSONAR |
   |  reflector|        |           |        | vJunos    |
   +-----------+        +-----------+        | SR Linux  |
                                             | XRd ...   |
                                             +-----------+
```

- Assign both IPv4 / IPv6 on the data plane and run each case in both address families (Full on Junos OS is IPv4 only)
- gotwamp participates as a container rather than on the host network, so the distributed image itself is verified
- NOS-side configurations (Junos `client-list`, Cisco `ip sla responder twamp`, Light test-sessions, etc.) are stored in `deploy/containerlab/configs/`

### 8.3 Case list (excerpt)

| ID | Direction | Mode | Content | Pass criteria |
|----|------|--------|------|------|
| I-01 | gotwamp → perfSONAR twampd | Full | Default parameters, 100 packets | loss 0, RTT consistent with ping |
| I-02 | perfSONAR twping → gotwampd | Full | Default, variable `-s` padding, `-D` DSCP, `-Z` | twping exits normally and the `-M` statistics are reasonable |
| I-03 | gotwamp → perfSONAR | Full | IPv6 | Same as above |
| I-04 | gotwamp → perfSONAR | Full | Invalid Modes / timeout / mid-session disconnect | Exits with an appropriate error and no goroutines remain |
| I-05 | gotwamp → NOS Light reflector | Light | Port 862 / vendor default port, padding 0 / 27 / 1400 | The reflector replies and TTL/DSCP reflection matches the expectations in SPEC §11 |
| I-06 | NOS Light sender (Cisco PM querier / Junos light client / SR Linux, etc.) → gotwampd | Light | Session configured on the NOS side | NOS-side statistics are reasonable |
| I-07 | gotwamp ⇄ gotwamp | Full / Light | netem loss 5% / duplication / reordering / delay 10ms±2ms | Statistics within ±1% of injected values, median RTT error < 50µs (kernel timestamp) |
| I-08 | any → gotwampd | Full | 50 concurrent connections / 4 sessions each | Limit settings work, and excess is refused with Accept=4/5 |
| I-09 | vJunos client (test-count 0) → gotwampd | Full | Long-running test repeating Request/Start/Stop on the same control connection, TCP keepalive | Sessions are established every time with no leaks |
| I-10 | gotwamp → vJunos server | Full | Inside/outside client-list, port 862/non-862, multiple sessions, server-inactivity-timeout | Accepted / refused as expected |
| I-11 | gotwamp → Cisco IP SLA TWAMP responder | Full | Start Time real time/0, Modes=2/4, explicit Receiver Port | Record the acceptance conditions and update SPEC §11.3 |
| I-12 | STAMP sender (stamp-suite / teaparty) → gotwampd light | Light | 44-octet base packet | Reflected, and the sender obtains RTT |

### 8.4 Automation

- `make interop` brings up the containerlab topology, runs each case via `test/interop/run.sh`, and aggregates the results as JSON
- Images that are free but require account registration (vJunos, cEOS, XRd, CSR1000v) are run only if they can be obtained via CI secrets; otherwise they are recorded as skipped
- Captured pcaps are analyzed with tshark's `twamp` dissector and checked against expected field values

---
## 9. Quality Criteria and Development Process

- **Coding conventions**: `gofumpt` formatting, golangci-lint defaults + `errcheck`, `gosec`, `revive`. Doc comments on exported identifiers
- **Test criteria**: coverage of 90% or more for `internal/twamp` and `stats`, 70% or more overall as a guideline. `-race` required
- **Commits**: Conventional Commits (`feat:`, `fix:`, `docs:` …), in English. Green CI required per PR
- **Branches**: `main` is protected, feature branches + PRs. Breaking changes are allowed during v0.x but must be noted in the CHANGELOG
- **Versioning**: SemVer. The gRPC API keeps `gotwamp.v1`; breaking changes go into a `v2` package (detected with `buf breaking`)
- **ADR**: design decisions (IPC method, timestamping strategy, Light reflector sequence scheme, etc.) are recorded in `docs/adr/`
- **Security**: run govulncheck in CI. Because the reflector by default has no ACL and port 862 open, document the exposure surface (amplification factor, REFWAIT, limits)

---

## 10. Risks and Mitigations

| Risk | Impact | Mitigation |
|--------|------|------|
| Send-timing jitter caused by the Go runtime (GC / scheduler) | Errors of tens to hundreds of µs in measured values | Obtain send/receive times with kernel timestamps and use them in calculations. Make the hot path allocation-free, and document `GOGC` guidelines and the systemd `CPUSchedulingPolicy` option |
| TX timestamps arrive asynchronously (MSG_ERRQUEUE) | T1 in the packet differs from the actual send time | Put the user-space time just before sending in the packet, and use the kernel TX time obtained afterwards for RTT calculation. Error Estimate declares the error of the user-space method |
| Bind permission for port 862 (<1024) | Fails when run as non-root | Grant `CAP_NET_BIND_SERVICE` via systemd / the container. Also allow an alternative port via configuration |
| NOS images have limited TWAMP support, or it does not work in the container version (cEOS found to be unsupported, SR Linux feature flags unconfirmed, no primary sources for vJunos) | Light / Full NOS interop tests cannot be performed | Pin supported releases based on research results. Make gotwamp-to-gotwamp + perfSONAR the main targets, and run NOS tests "if available" |
| One-way delay (T2−T1, etc.) depends on clock synchronization | Presenting incorrect values | Determine validity from the S bits at both ends and the adjtimex sync state; when invalid, make it explicit in the UI (the value is shown but flagged). Document that IPDV does not require synchronization |
| NTP timestamp 2036 era problem, leap seconds, clock steps | Broken time conversion, negative RTT | Era-aware conversion and unit tests. Count negative RTTs and display them separately |
| Dynamic Receiver Port allocation in Full mode does not work well with firewalls / NAT | Interop failure | Port range setting on the reflector side, explicit specification on the sender side, NAT relaxation (source validation off) option |
| Cardinality explosion of Prometheus metrics | TSDB load | Limit labels to `session`, `target`, `mode`, and user-defined labels (with an upper bound). Per-packet data goes to JSONL |
| Differences in TWAMP interpretation between vendors (padding truncation, Sender Address required/forbidden, Light size validation, etc.) | Cannot communicate with some devices | Implement the pitfalls in research/vendors.md as configuration overrides (compatibility knobs), with RFC-compliant defaults |
| Permission management for the Unix domain socket | Any user can operate the daemon | Socket is 0660 with a dedicated group, created via systemd `RuntimeDirectory` |
| Vendor devices do not have an RFC 5357 Control-Client (Cisco is reflector only) | Hard to verify gotwampd's Server/Reflector with external clients | Use perfSONAR twping and the Junos client as Full verification peers, and verify Cisco on the sender side (gotwamp → Cisco reflector) and with the PM Light sender |
| Conditions for obtaining free images (Cisco requires entitlement, Arista/Juniper require an account) | Automated interop in CI is limited | Make perfSONAR + gotwamp-to-gotwamp required in CI, and run NOS tests optionally in nightly / manually |
| Known bugs in vendor reflectors (NX-OS Multiplier=0, XE RSP3 Hop Limit 64, ACX 10-second Start-Ack delay) | Misinterpreted measurements or failed runs | Set compatibility knobs (SPEC §11.2) to the lenient side by default, and log a warning when detected |
| Sending malformed packets to third-party equipment | Device reboots (CVE-2025-20154, etc.) | Limit fuzz and error-path tests to gotwamp-to-gotwamp; send only normal traffic in interop tests |
| Constraints of the free SR Linux container (presence of feature flags, 1000pps limit, weekly restarts) | Light / Full NOS interop cannot be performed, or long-running tests are interrupted | Check features right after startup to decide whether to skip, keep the interop rate at 10pps or below, and run long-running tests gotwamp-to-gotwamp |
| Receiver Port constraints of vendor Servers (SR Linux / Junos Evolved require 862 or 49152–65535, IOS-XR 57343 or below) | The default requested port is refused | Set the default range of the source port and requested Receiver Port to 49152–57343 (SPEC CMP-21) |

---

## 11. Open Questions / To Be Confirmed

| # | Item | Current tentative choice | Decision timing |
|---|------|--------------|----------|
| 1 | Go module path / GitHub owner | Finalized as `github.com/Akkanbe/gotwamp` (2026-10-04). SPEC.md / PLAN.md / research/ are not included in the public repository and are excluded via .gitignore | Decided |
| 2 | Default padding (Unauth) | 27 octets (symmetric: sender 41 B / reflector 41 B) | M1 |
| 3 | Light reflector sequence number scheme | Per-flow counter (default) + "copy Sender Sequence" option | M2 (ADR) |
| 4 | Whether to use multiple sessions per control connection in Full mode | Default 1 session / connection; multiple sessions later | M3 |
| 5 | Daemon-independent CLI mode | None initially (daemon required) | M7 |
| 6 | Result persistence | In-memory ring buffer only | M5 or later |
| 7 | Remote exposure of gRPC (TCP + mTLS) | UDS only initially | M7 |
| 8 | Final list of NOS interop targets | perfSONAR (required), vJunos-router and SR Linux (high), SONiC-VS / VyOS (optional). cEOS is out of scope | Decided (confirming that features are enabled in the SR Linux container remains) |

---

## 12. References

- RFC 5357 A Two-Way Active Measurement Protocol (TWAMP)
- RFC 4656 A One-way Active Measurement Protocol (OWAMP)
- RFC 5618 Mixed Security Mode for TWAMP
- RFC 5938 Individual Session Control Feature for TWAMP
- RFC 6038 TWAMP Reflect Octets and Symmetrical Size Features
- RFC 7717 IKEv2-Derived Shared Secret Key for OWAMP/TWAMP
- RFC 7750 Differentiated Service Code Point and Explicit Congestion Notification Monitoring in TWAMP
- RFC 8186 Support of the IEEE 1588 Timestamp Format in TWAMP
- RFC 8545 Well-Known Port Assignments for OWAMP and TWAMP
- RFC 8762 Simple Two-Way Active Measurement Protocol (STAMP), RFC 8972 STAMP Optional Extensions
- RFC 3393 IP Packet Delay Variation Metric, RFC 5481 Packet Delay Variation Applicability Statement, RFC 4737 Packet Reordering Metrics
- perfSONAR owamp/twamp (https://github.com/perfsonar/owamp)
- Research notes: docs/research/rfc-twamp-protocol.md (RFC review), oss-implementations.md (OSS), vendor-cisco.md, vendor-juniper.md, vendor-others.md (vendors)
