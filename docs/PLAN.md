# gotwamp Implementation Plan

| Item | Value |
|------|------|
| Version | 0.2 |
| Created | 2026-10-03 |
| Updated | 2026-10-14 |
| Target specification | [docs/SPEC.md](SPEC.md) v0.4 |
| Research notes | [docs/research/](research/) (RFC review / OSS implementation survey / vendor implementation survey) |

Version history:

| Version | Date | Summary of changes |
|----|------|----------|
| 0.1 | 2026-10-03 | Initial version (draft) |
| 0.2 | 2026-10-14 | Records where the project stands after M0–M7 and the publication on GitHub. §6: the status of each milestone (dates, each DoD clause met or not, and why), the status of each M7 item, and a new plan for the next phase (N1–N7, each with a DoD). §11: the open questions updated (publication, persistence, remote management and others). §3 and §5: the deliverables and the repository layout match the actual tree (`deploy/staging`, `internal/store/sqlite`, `docs/README.md`, …). §1.3 and §2 #10: notes on what has changed since the interview (M7 scope, documentation now in English) |

Status as of 2026-10-14 in one paragraph: M0–M7 were implemented on 2026-10-04 and 2026-10-05, except OTLP export and `gotwamp ping --direct`
(M7). The code was published on GitHub on 2026-10-14 (`github.com/Akkanbe/gotwamp`, one initial commit). Every check so far ran on a single
development host (a QEMU VM with 4 vCPUs, no root, no PTP-capable NIC): no GitHub Actions run has been recorded yet, and v0.1.0 has not
been released. The DoD clauses that were not met are listed in §6 with their reasons; the next phase (§6, "Next phase") closes them.

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

Update (v0.2 of this plan): M7 has since implemented the authenticated modes (A / E / M), hardware timestamping, the STAMP base packet
(with the RFC 8972 SSID) and a SQLite result store with retention (ADR 0011–0014). STAMP TLV processing, OWAMP, OSes other than Linux and a
GUI remain out of scope.

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
| 10 | Language | **Documentation in Japanese; code / comments / commits in English** → since 2026-10-14, **everything in English**, documentation included (CLAUDE.md) | Keep it readable when published as OSS. All documents were translated for the publication on GitHub |
| 11 | Dependency policy | **Well-established libraries are allowed** (cobra, grpc/protobuf, prometheus/client_golang, x/sys, yaml.v3) | Protocol implementation (encoding, cryptography) is written with the standard library only, keeping dependencies out of the inner layers |
| 12 | License | **Apache-2.0** | Go module path is `github.com/Akkanbe/gotwamp` (finalized 2026-10-04. Public repository) |

---

## 3. Deliverables

Status as of 2026-10-14 (v0.2 of this plan). "Not yet" marks the parts that are planned in the next phase (§6).

| Deliverable | Description |
|--------|------|
| `gotwampd` | Resident daemon. Sender role (Control-Client / Session-Sender / scheduler / result store / exporter), reflector role (Server / Session-Reflector / Light reflector, STAMP mode), gRPC API over the UDS and optionally TCP + mTLS, Prometheus HTTP (`/metrics`, `/healthz`), JSON Lines writer, result store in memory or SQLite |
| `gotwamp` | CLI. `ping` (ad hoc measurement), `session list\|show\|add\|update\|remove\|start\|stop`, `stats`, `watch`, `status`, `reflector status\|sessions`, `config validate\|reload`, `version`; `--server` / `--tls-*` for a remote daemon |
| `api/proto/gotwamp/v1/*.proto` | gRPC API definitions (`daemon`, `sender`, `reflector`, `types`); the generated code is in `internal/api/gen/gotwamp/v1` |
| `internal/twamp` and other libraries | Protocol implementation. Structured so it can later become a public API under `pkg/` |
| `deploy/systemd/gotwampd.service` | systemd unit (CAP_NET_BIND_SERVICE, dedicated user, hardening). Not yet started under a real systemd |
| `deploy/config/gotwampd.yaml` | Example configuration installed by the packages |
| `deploy/docker/` | distroless-based container image (`Dockerfile`, `gotwampd.yaml`; `make docker`). Not yet published to a registry |
| `.goreleaser.yaml`, `deploy/nfpm/` | linux/amd64 and arm64 tar.gz, deb and rpm (with the systemd unit), checksums. Built only as a local snapshot (`make release-snapshot`) so far; no release workflow yet |
| `deploy/containerlab/` | Interop topology (`gotwamp-interop.clab.yml`: perfSONAR / SR Linux / vJunos-router), the lab image, and NOS configurations (`configs/srl.cli`, `configs/vjunos.set`). Not yet deployed |
| `deploy/staging/` | docker compose test bench on one host: a sender, three reflectors (default, authenticated modes, extensions + STAMP), tc / netem impairment, scenarios, optional perfSONAR and Prometheus + Grafana (`make staging-*`, docs/ops/staging.md) |
| `test/` | `golden/` (byte sequences from the RFC figures), `component/` (netns + netem, tshark), `soak/`, `interop/perfsonar/` (perfSONAR in Docker, `make interop`) |
| `docs/` | Documentation index and glossary (README.md), specification (SPEC.md), this plan, research notes (research/), ADRs 0001–0014 (adr/), operations guide (ops/: config, metrics with an example Grafana dashboard, troubleshooting, interop, staging) |
| `README.md`, `CHANGELOG.md`, `LICENSE` | Project overview and quick start, the change log (Keep a Changelog; everything is still under "Unreleased"), Apache-2.0 |
| CI | GitHub Actions: `ci.yml` (build for amd64 / arm64, unit tests with race and coverage, component, short soak, lint including declscope, proto, govulncheck; also nightly) and `interop.yml` (perfSONAR, nightly). No run result recorded yet. Release on a tag: not yet |

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

## 5. Repository Layout

The actual tree as of 2026-10-14 (v0.2 of this plan; v0.1 showed the planned layout).

```
gotwamp/
├── cmd/
│   ├── gotwampd/            # daemon entry point (wiring only)
│   └── gotwamp/             # CLI entry point (cobra)
├── api/
│   └── proto/gotwamp/v1/    # gRPC / protobuf definitions (managed by buf)
│       ├── daemon.proto     # DaemonService
│       ├── sender.proto     # SenderService (RunTest, StopTest, Session*, Stats, Watch)
│       ├── reflector.proto  # ReflectorService
│       └── types.proto      # TestSpec, PacketResult, Summary, etc.
├── internal/
│   ├── twamp/               # protocol core (pure functions, no I/O, standard library only)
│   │   ├── wire/            # encode/decode of Control / Test messages, Modes, Accept, ISC, RFC 6038, STAMP, security layer
│   │   ├── ntptime/         # NTP 64-bit timestamp conversion (era-aware)
│   │   └── errest/          # Error Estimate computation and encoding
│   ├── control/
│   │   ├── client/          # Control-Client state machine (with ISC)
│   │   ├── server/          # Server state machine (limits, ports, authenticated modes, ISC)
│   │   └── keystore/        # keys files (perfSONAR pfs format)
│   ├── sender/              # Session-Sender: send/receive loop, seq matching, timeouts, T1', per-run aggregation
│   ├── reflector/           # Session-Reflector (Full) and Light reflector (flow table, seq, rate limit, STAMP)
│   ├── tstamp/              # socket abstraction: SO_TIMESTAMPING (kernel / hardware) / cmsg (TTL, TOS) / fallback
│   ├── clock/               # sync state and maximum error via adjtimex
│   ├── stats/               # RTT/OWD/IPDV/PDV/loss/reorder/dup, percentile
│   ├── schedule/            # periodic / continuous scheduler for monitoring sessions
│   ├── store/               # result store interface, in-memory ring buffer, fan-out to sinks
│   │   ├── sqlite/          # SQLite result store (ADR 0013; pure-Go modernc.org/sqlite)
│   │   └── storetest/       # contract tests shared by the memory and SQLite stores
│   ├── export/
│   │   ├── prom/            # Prometheus collector
│   │   └── jsonl/           # JSON Lines writer (rotation)
│   ├── config/              # YAML schema, validation, diff application
│   ├── daemon/              # gotwampd service wiring, gRPC server (UDS, TCP + mTLS), signals/reload
│   ├── cli/                 # gotwamp subcommand implementations (gRPC client + display)
│   ├── api/gen/gotwamp/v1/  # generated protobuf / gRPC code
│   ├── buildinfo/           # version / commit / Go version shared by both binaries
│   ├── ulid/                # run IDs
│   └── testpki/             # test certificates for the mTLS tests
├── deploy/
│   ├── systemd/gotwampd.service
│   ├── config/gotwampd.yaml # example configuration for the packages
│   ├── docker/              # Dockerfile, gotwampd.yaml
│   ├── nfpm/                # deb / rpm install scripts
│   ├── containerlab/        # interop lab definition, lab image and NOS configurations
│   └── staging/             # docker compose staging: configs, test-only keys, netem.sh, scenarios/, observability/
├── sender/                 # curated public sender entry points (Light / Full)
├── reflector/              # curated public reflector services (Light / Full)
├── *.go                    # canonical shared public values; no I/O
├── test/
│   ├── library/             # independent embedding consumer module (make test-library)
│   ├── golden/              # fixed byte sequences derived from RFC figures (encode/decode verification)
│   ├── component/           # integration tests using loopback / netns + netem, tshark decoding
│   ├── soak/                # soak test (make soak)
│   └── interop/perfsonar/   # perfSONAR interop tests (compose, Go tests, run.sh, twampd configuration)
├── docs/
│   ├── README.md            # documentation index and glossary
│   ├── SPEC.md              # implementation specification
│   ├── PLAN.md              # this document
│   ├── adr/                 # Architecture Decision Records
│   ├── research/            # RFC / OSS / vendor research
│   └── ops/                 # operations guide (configuration, metrics, troubleshooting, interop, staging)
├── .github/workflows/       # ci.yml, interop.yml
├── Makefile                 # build / test / lint / proto / soak / interop / staging / release-snapshot
├── buf.yaml, buf.gen.yaml
├── .golangci.yml, .declscope.yaml
├── .goreleaser.yaml
├── go.mod, go.sum
├── CHANGELOG.md
├── CLAUDE.md                # development guidelines
├── LICENSE (Apache-2.0)
└── README.md
```

Dependency direction principle (SPEC §4.3): `cmd` → `daemon`/`cli` → (`schedule`, `store`, `export`, `config`) → (`control`, `sender`, `reflector`) → (`tstamp`, `clock`, `stats`) → `twamp` (innermost layer).
Public entry points and canonical value ownership follow ADR 0015; internal engine imports remain private runtime and transport seams, not a second implementation.
The `twamp` package is a pure encoding layer that does not depend even on `net`, and is hardened with fuzz tests and golden tests.

---

## 6. Milestones and Phases

Each milestone is delimited by "something that works". Effort is an estimate for one person working on it full-time (S: up to 2 days, M: 3–5 days, L: 1–2 weeks).

**Status (v0.2 of this plan, 2026-10-14).** M0–M7 were built by several agents working in parallel, so the effort estimates above were not
used as a schedule. The dates below are those of the completion reports. Every check ran on one development host (a QEMU VM with 4 vCPUs and
a virtio NIC, without root or passwordless sudo, shared with other work); Docker was available, containerlab was not. Each milestone below
ends with a **Result** that takes its DoD clause by clause.

| Milestone | Completed | DoD |
|-----------|-----------|-----|
| M0 | 2026-10-04 | Partly met: local yes; no CI run recorded |
| M1 | 2026-10-04 | Partly met: golden vectors yes; fuzzing 12.2 minutes in total but about 1 minute per target; tshark not run |
| M2 | 2026-10-04 | Partly met: loopback yes; netem tests never run; SR Linux STAMP not achieved |
| M3 | 2026-10-04 | Partly met: perfSONAR in both directions passes locally in Docker, not yet in CI; SR Linux / vJunos-router not run; tshark not run |
| M4 | 2026-10-04 | Partly met: fallback tests yes; the 50µs RTT-error measurement on veth + netem not done |
| M5 | 2026-10-04 (longer soak 2026-10-05) | Partly met: reload yes; the 24-hour soak not run (10 minutes and 2 × 30 minutes passed) |
| M6 | 2026-10-04 (follow-ups 2026-10-05) | Not met: no release on a tag, nightly interop never run; v0.1.0 not released |
| M7 | 2026-10-04 to 2026-10-05 | 5 of 7 items done; OTLP export and `--direct` not done (table at the end of M7) |

### M0: Project foundation (S)

- **Tasks**: `go mod init`, directory skeleton, Makefile, `buf` configuration and empty proto, golangci-lint / govulncheck configuration, GitHub Actions (build / test / lint), LICENSE, README, ADR template, CLAUDE.md (development conventions)
- **Deliverables**: an empty repository on which CI is green
- **Definition of Done (DoD)**: `make build test lint proto` succeeds locally and in CI
- **Result (2026-10-04)**: Makefile, golangci-lint / declscope / buf configuration, the `gotwamp.v1` proto with generated code, the GitHub
  Actions workflow, LICENSE, README, CHANGELOG and the first ADRs.
  - Locally: met. `make build test lint` passed on 2026-10-04 and again on 2026-10-14 (lint includes `buf lint`). A separate `make proto`
    run is not recorded; the CI `proto` job checks that the generated code is up to date.
  - In CI: not met. No GitHub Actions run has been recorded (the CI jobs are now in `.github/workflows/ci.yml`; see "Next phase", N2)

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
- **Result (2026-10-04)**: `wire` (every control message and the Unauthenticated test packets), `ntptime`, `errest` and `stats`. Coverage:
  wire 100%, ntptime 100%, errest 95.8%, stats 99.6% (99.7% after the quality gate added two missing tests).
  - Golden vectors: met. The 14 golden files of M1 match in both directions (encode and decode); M7 added more.
  - Fuzzing: partly met. The quality gate ran 12 decoder targets for about 61 seconds each: 12.2 minutes in total, about 22.6 million
    executions, no crash. No decoder target has run for 10 minutes on its own. Only the control server's `FuzzServerConn` has
    (2.67 million executions, no crash).
  - tshark: not met. tshark is not installed on the development host. `TestFullDecodedByTshark` (test/component) exists but has never run,
    and its dissector field names were written from memory

### M2: TWAMP Light (the first end-to-end working form) (L)

- **Tasks**:
  - `tstamp`: UDP socket abstraction (user-space timestamp version). Setting IP_TTL/IPV6_UNICAST_HOPS=255 and IP_TOS/IPV6_TCLASS, receiving IP_RECVTTL/IPV6_RECVHOPLIMIT and IP_RECVTOS/IPV6_RECVTCLASS
  - `reflector`: Light reflector (multiple listeners, flow table and per-flow sequence, DSCP reflection, TTL reflection, ACL, flow expiry equivalent to REFWAIT)
  - `sender`: Session-Sender loop (send schedule based on absolute time, receive matching, timeout determination, per-run aggregation)
  - `daemon`: minimal `gotwampd` (config loading, reflector role, gRPC `RunTest` streaming, `GetStatus`)
  - `cli`: `gotwamp ping` (streaming display of per-packet lines + final summary, `--json`), `gotwamp status`
- **Deliverables**: `gotwampd` (reflector) ⇄ `gotwamp ping --light` works over loopback and netns
- **DoD**: integration tests that inject delay/loss/duplication/reordering with netem produce the expected statistics. Light measurement works against the SR Linux STAMP session-reflector (if the feature is enabled in the container), and gotwampd's Light reflector replies to packets from the SR Linux STAMP sender
- **Result (2026-10-04)**: `tstamp` (user space), `clock`, the Light reflector (flow table, per-flow sequence, ACL, rate limit, DSCP and TTL
  reflection), the sender loop, and a minimal daemon and CLI. The real binaries ran `gotwamp ping --light` over IPv4 and IPv6 loopback with
  every packet answered.
  - netem tests: not met. They exist (`test/component`, build tag `integration`) but have never run. The development host has no root, no
    passwordless sudo and no unprivileged user namespaces, and no CI run has been recorded.
    - Related, but not this suite: on 2026-10-14 the docker compose staging (§3) ran tc / netem inside its containers, and the statistics
      followed the impairments. Examples: delay 10ms ± 2ms gave a Light RTT of 10.27ms, and 10% loss gave 98 of 1000 lost.
  - SR Linux STAMP: not met. SR Linux v26.7.2 was started alone in Docker:
    - The default models (7220 IXR-D3L and D2L) have no `/oam` tree, so neither the STAMP reflector nor the TWAMP server could be enabled.
    - The service-provider models did not finish booting without containerlab, which is not available on the development host.

### M3: TWAMP Full (control protocol) (L)

- **Tasks**:
  - `control/client`: Control-Client state machine (Greeting → Set-Up-Response → Server-Start → Request-TW-Session → Accept-Session → Start-Sessions → Start-Ack → measurement → Stop-Sessions), per-stage timeouts, Modes evaluation, error formatting
  - `control/server`: Server (TCP 862 listener, per-connection state machine, SERVWAIT, Modes=0 refusal handling, ACL, connection and session count limits, Receiver Port allocation and port range configuration)
  - `reflector`: Full-mode Session-Reflector (UDP socket bound to the session, source validation and NAT relaxation option, REFWAIT, cleanup on Stop-Sessions / disconnect)
  - `sender`: Full-mode run (one control connection per run, pre-binding and announcing the Sender Port, defaults and overrides for Request fields)
  - CLI: make Full the default for `gotwamp ping <host>`, Light with `--light`
- **Deliverables**: bidirectional interop of perfSONAR `twping` → `gotwampd` reflector and `gotwamp ping` → perfSONAR `twampd`. If possible, bidirectional with vJunos-router's RPM TWAMP client/server as well
- **DoD**: bidirectional interop tests with perfSONAR run automatically in CI (Docker) and succeed. Also succeeds against the SR Linux TWAMP Full server (if enabled) and vJunos-router's RPM TWAMP server/client. Wireshark correctly interprets both control and test traffic
- **Result (2026-10-04)**: the Control-Client and Server state machines, the Full Session-Reflector, the sender's Full run, and Full as the CLI
  default.
  - Tests: real-socket loopback over IPv4 and IPv6 (50 of 50 packets). Robustness tests: `FuzzServerConn` for 10 minutes, slow clients,
    limits.
  - perfSONAR in CI: partly met. The interop suite (built in M6) passes in both directions against perfSONAR testpoint 5.2.6 in Docker,
    run locally with `make interop`:
    - gotwamp → twampd: 8 cases; twping → gotwampd: 10 cases. Each case sends 100 packets with no loss.
    - M7 added the authenticated modes. The last full run with `INTEROP_CLI=1` passed on 2026-10-14.
    - An intermittent loss seen with twping was traced to the reflector enforcing the Start Time. It is off by default since then (SPEC
      CMP-26).
    - The nightly workflow (`interop.yml`) exists, but no run has been recorded.
  - SR Linux / vJunos-router: not met. Neither has been run. SR Linux: see M2. The vJunos-router configuration
    (`deploy/containerlab/configs/vjunos.set`) follows the vendor documentation but is unverified, because containerlab is not available.
  - Wireshark: not met (see M1, tshark)

### M4: Measurement accuracy (M)

- **Tasks**:
  - `tstamp`: implementation of SO_TIMESTAMPING (RX: SCM_TIMESTAMPING, TX: MSG_ERRQUEUE + OPT_ID/OPT_TSONLY), capability detection and automatic fallback, setting `timestamping: auto|kernel|userspace`
  - `clock`: sync determination (S bit) and maximum error (maxerror) via adjtimex, reflected in Error Estimate
  - Improving send-loop accuracy (absolute deadlines, buffer pool, eliminating allocations on the hot path), GC tuning guidelines
  - Validity determination for one-way delay (shown as valid only when S=1 at both ends)
- **Deliverables**: accuracy evaluation report (error over loopback / veth + netem, kernel vs userspace comparison)
- **DoD**: median RTT error under 50µs against veth + netem 10ms delay (with kernel timestamps, as a guideline). Tests exist for the fallback path
- **Result (2026-10-04)**:
  - Delivered: SO_TIMESTAMPING on receive and transmit (T1'), `auto` / `kernel` / `userspace` with automatic fallback, and the adjtimex sync
    state (S bit) and maximum error. The packet path has no allocations (0 B/op, about 0.9µs per packet).
  - Fallback tests: met. Failures of each setsockopt are injected, and the loopback tests run both kernel and user space timestamping on
    IPv4 and IPv6.
  - Accuracy report: partly met. It was measured on loopback only (QEMU VM, 4 vCPUs) and kept in the development report; there is no
    accuracy document in docs/.
    - User-space timestamps make the RTT about 12–20µs longer than kernel timestamps. Medians over 5 runs: receive 7.3–17.5µs, transmit
      4.0–5.5µs.
    - The worst cases were 4.3ms on receive and 0.57ms on transmit (garbage collection and scheduling).
  - Median RTT error < 50µs on veth + netem: not measured. It needs root for veth and netem (N2)

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
- **Result (2026-10-04)**:
  - Delivered: configuration with reload by difference, scheduler, result store, Prometheus and JSON Lines exporters, the session RPCs and
    the CLI, and the soak harness (`make soak`) with goroutine leak checks.
  - Reload: met. A 500-packet run carries on through a reload of other sessions (`TestReloadAppliesDifferences`).
  - 24-hour soak: not met. It was planned as a manual run (`make soak SOAK_SESSIONS=100 SOAK_DURATION=24h`) and has not been carried out.
    Shorter runs at 100 sessions × 10pps passed with no loss and no leaks:
    - 10 minutes, on 2026-10-04: 502,930 packets. Goroutines stayed at 222–227 and RSS at 56–64MiB, using 23% of a core. Goroutines and file
      descriptors returned to their starting values after the stop.
    - 2 × 30 minutes, on 2026-10-05: one run with the memory store and one with SQLite. RSS was at most 84MiB, using 26–27% of a core, and
      memory was flat after the result history filled.
    - CI runs a short soak: 20 sessions × 10pps for 60 seconds.
  - Scale (NFR-03): the sender side is met; the reflector side is not shown.
    - Sender: 100 sessions × 10pps use 23–27% of one core.
    - Reflector, 10,000 pps with no loss: 11 of 17 runs (10 seconds each, one of 60 seconds) lost nothing; the others lost 0.37–10.2%.
    - Every loss was a receive-buffer overflow while other users kept the host's CPU busy: `rmem_max` 212,992 gives about 44ms of buffering
      at 10k pps.
    - The reflector itself used about 20% of a core, with no allocations per packet.

### M6: Operability, distribution, interop suite (M)

- **Tasks**:
  - systemd unit (dedicated user, `AmbientCapabilities=CAP_NET_BIND_SERVICE`, hardening), Dockerfile, goreleaser, deb/rpm
  - containerlab topologies (perfSONAR / SR Linux / vJunos-router / optionally XRd, CSR1000v, SONiC, VyOS) and `make interop`
  - Operations documentation (configuration reference, metrics list, example Grafana dashboard, troubleshooting)
  - Release v0.1.0
- **DoD**: pushing a tag produces each artifact, and the interop suite runs nightly
- **Result (2026-10-04, follow-ups 2026-10-05)**:
  - Delivered: the systemd unit, the container image (39.7MB, binds port 862 as nonroot), goreleaser and nfpm, the perfSONAR interop suite
    (`make interop`, `interop.yml`), the operations documents, the containerlab topology, the CI `component` and `soak` jobs, and ADRs
    0006–0009.
  - Artifacts on a tag: not met. goreleaser has run only as a local snapshot, and no workflow runs it on a tag (N1). No container registry
    is configured.
    - The snapshot built tar.gz, deb and rpm for both architectures, plus checksums. The deb installed on Debian 12 and the rpm on
      Rocky Linux 9.
    - The snapshot binaries reported `commit none`.
    - The systemd unit has not been started under a real systemd.
  - Nightly interop: not verified. `interop.yml` is scheduled, but no run has been recorded.
  - Release v0.1.0: not done. There is no tag, and CHANGELOG.md keeps everything under "Unreleased". The steps above (a release workflow and
    green CI) come first (N1, N2)

### M7 (later): Extensions

Priorities are revisited based on usage.

1. Authenticated / Encrypted modes (RFC 4656 key derivation, AES-CBC, HMAC-SHA1, RFC 5618 Mixed), a keys file equivalent to `twampd.pfs`, authenticated interop with perfSONAR
2. RFC 6038 Reflect Octets / Symmetrical Size, RFC 7750 DSCP/ECN monitoring, RFC 5938 Individual Session Control
3. STAMP (RFC 8762) base packet compatible reflector / sender mode
4. Hardware timestamping (SOF_TIMESTAMPING_RX/TX_HARDWARE, PHC)
5. Exposing gRPC over TCP + mTLS (remote management), OTLP export
6. Whether to have a daemon-independent CLI mode (`gotwamp ping --direct`)
7. Result persistence (SQLite, etc.), long-term history

**Result (2026-10-04 to 2026-10-05)**: items 1, 2, 3, 4 and 7 are implemented and wired into the daemon and the CLI (SPEC 0.3 and 0.4). Item 5 is
half done (mTLS yes, OTLP no), and item 6 is not done.

| # | Item | Status | How it was verified | What remains |
|---|------|--------|---------------------|--------------|
| 1 | Authenticated / Encrypted / Mixed, keys file, authenticated interop with perfSONAR | Done (ADR 0011) | Key derivation, AES-CBC and HMAC match vectors made independently with Python / openssl. Loopback tests. perfSONAR in both directions: A / E / M × IPv4 / IPv6 × padding 27 and 1000, and a wrong key refused | TWAMP Light authentication and RFC 7717 are not supported. Whether A / E default to padding 64 is undecided (§11) |
| 2 | RFC 6038, RFC 7750, RFC 5938 | RFC 6038 and RFC 5938: done. RFC 7750: TWAMP Light only | Golden files from the RFC figures, loopback tests (measured lengths), daemon tests, `FuzzServerConn` | perfSONAR does not support these extensions, so they are untested against other implementations (N3). Full-mode RFC 7750 would need Modes bit 8, which the Server does not offer. An ISC Stop-N cannot ask to discard results |
| 3 | STAMP base-packet reflector / sender | Done: base packet and the RFC 8972 SSID (ADR 0012) | Golden files, loopback and daemon tests | TLV processing and STAMP authentication are out of scope. Untested against other implementations, SR Linux included (N3) |
| 4 | Hardware timestamping | Implemented and wired; not verified on hardware (ADR 0014) | A fake ioctl for the code paths. The real ioctl on loopback falls back to kernel timestamps. The development host's virtio NIC has no PHC | Verification on a NIC with a PHC. Timestamps rejected by the PHC guard (`HardwareRejected`) are not yet shown in metrics or logs (N6) |
| 5 | gRPC over TCP + mTLS; OTLP export | mTLS: done (ADR 0010). OTLP: not done | `TestRemoteAPI` and the CLI tests on loopback. The CLI with `--server` and certificates made with openssl. A test between two hosts was not run | OTLP export (N4). The grpc update for GO-2026-6443 (CHANGELOG TODO; N1) |
| 6 | Daemon-independent CLI (`gotwamp ping --direct`) | Not done; undecided | — | Decide by ADR (N5) |
| 7 | Result persistence (SQLite) | Done (ADR 0013) | Contract tests shared with the memory store, the 30-minute soak, `gotwamp stats --since` reading back to the retention. The fixes of Codex review round 4 (lock order, cache bound, recovery after a failed commit) have regression tests | Per-packet records stay in memory only. No test of disk failure or power loss |

Also left from M7: the configuration key `reflector.control.enforce_start_time` (SPEC CMP-26) is not wired; the reflector's default (no
enforcement) applies (N1).

### Next phase (from 2026-10-14)

Ordered by priority. Each item closes DoD clauses that §6 lists as not met.

#### N1: Preparing the v0.1.0 release

- **Tasks**:
  - A release workflow that runs goreleaser on a `v*` tag, with the Go version of `go.mod` (the snapshot was built with Go 1.27.1 and
    reported `commit none`)
  - Decide where the container image is published, and publish it from the same workflow
  - Wire `reflector.control.enforce_start_time` (SPEC CMP-26, Appendix A), or remove the key from SPEC
  - Update `google.golang.org/grpc` to v1.85.0 or later once it is released (GO-2026-6443). Until then, keep the CHANGELOG note that the
    affected path is not called
  - Install the deb on a host with systemd: `systemctl enable --now`, binding port 862 with CAP_NET_BIND_SERVICE only, adjtimex under
    the unit's `SystemCallFilter`
  - Move the CHANGELOG "Unreleased" section to `[0.1.0]`
- **DoD**: a pre-release tag (e.g. `v0.1.0-rc.1`) produces the tar.gz, deb, rpm, checksums and the container image from CI. The binaries
  report the tag's version and commit. The packaged unit starts under systemd and the daemon answers `gotwamp status`

#### N2: Stabilizing CI

- **Tasks**:
  - Run every job of `ci.yml` and `interop.yml` on GitHub runners and fix what fails. These would be the first runs of the netem
    component tests and of `TestFullDecodedByTshark`; fix the tshark field names if they are wrong
  - Measure the M4 accuracy on the runners: veth + netem 10ms with kernel timestamps; record the RTT error (median and p99)
  - Nightly fuzzing: `-fuzztime=10m` for each decoder target
  - Find and fix flaky tests (rerun with `-count`)
  - Tag v0.1.0 once N1 and this item are done
- **DoD**:
  - `ci.yml` passes on `main` 10 times in a row without reruns
  - `interop.yml` passes 7 nights in a row
  - Every decoder target fuzzes 10 minutes without a crash
  - The tshark test passes
  - The M4 measurement is recorded. A median error of 50µs or more is explained, or the guideline is revised
  - v0.1.0 is released

#### N3: Interoperability with real devices

- **Tasks**:
  - Deploy `deploy/containerlab/gotwamp-interop.clab.yml` on a host with containerlab and root
  - SR Linux: check `info from state system features` on the service-provider model (`ixr-x1b`), and verify `configs/srl.cli`
  - vJunos-router: obtain the image and verify `configs/vjunos.set`
  - Run cases I-05, I-06, I-09, I-10 and I-12 (§8.3). Where a peer supports them, test STAMP, RFC 6038 and RFC 5938 too
  - Record the results and the differences in docs/ops/interop.md and SPEC §11.3, and add compatibility knobs where needed
- **DoD**:
  - The SR Linux and vJunos-router clauses of M2 and M3 are met. Where a feature is missing from the container image, that is recorded
    with the evidence
  - The CHANGELOG TODO on real-device interoperability of STAMP, RFC 6038 and RFC 5938 is closed, or narrowed to named devices

#### N4: OTLP export

- **Tasks**:
  - An exporter that implements `store.ResultSink` (ADR 0010), in the outer layers only
  - Configuration keys
  - SPEC: §1 lists OTLP as out of scope and §13 as deferred; update them, §9 and Appendix A
  - An ADR on the mapping of run and window summaries to OTLP
- **DoD**:
  - Run and window summaries reach an OpenTelemetry Collector in an automated test
  - A slow or unreachable collector never holds up a measurement; drops are counted
  - docs/ops/config.md and metrics.md are updated

#### N5: Deciding on `--direct`

- **Tasks**: an ADR that decides whether `gotwamp ping` gets a mode that runs without the daemon. It weighs the convenience for ad hoc use
  against duplicated wiring, the privileges needed for timestamping and port binding, and keeping the CLI a thin gRPC client
- **DoD**:
  - The ADR is accepted and §11 #5 is closed
  - If the answer is yes: `--direct` gives the same output and exit statuses as through the daemon, with tests

#### N6: Hardware timestamping on a real NIC

- **Tasks**:
  - On a NIC with hardware timestamping and a PHC kept in step with CLOCK_REALTIME (phc2sys), follow docs/ops/troubleshooting.md §3
  - Compare RTT and the Error Estimate with kernel timestamping on the same path
  - Show the timestamps rejected by the PHC guard in metrics and logs
- **DoD**:
  - `gotwamp status` and `gotwamp_timestamping_mode` show `hardware` on that NIC
  - The comparison with kernel timestamping is recorded
  - The rejected-timestamp counter is exported
  - ADR 0014's assumptions are confirmed or revised

#### N7: 24-hour soak and load

- **Tasks**:
  - `make soak SOAK_SESSIONS=100 SOAK_DURATION=24h` with the SQLite store, on a host without other load. Record samples, e.g. every
    10 minutes (`-soak.sample=10m -soak.csv=…`)
  - Reflector load: 10,000 pps for 60 seconds with `net.core.rmem_max` raised (e.g. 8MiB)
  - Measure the send-interval accuracy (NFR-02), which the soak harness does not measure yet
- **DoD**:
  - M5's DoD is met: memory, goroutines and file descriptors stop growing once the result history is full, with no loss
  - NFR-03 is met: the reflector handles 10,000 pps for 60 seconds with no loss
  - NFR-02 is measured, and the results are recorded

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

Updated on 2026-10-14 (v0.2 of this plan). Rows 1–8 come from v0.1; rows 9–12 are new.

| # | Item | Current choice / status | Decision timing |
|---|------|--------------|----------|
| 1 | Go module path / GitHub owner, publication | `github.com/Akkanbe/gotwamp`, Apache-2.0. Published on GitHub on 2026-10-14, in one initial commit. SPEC.md, PLAN.md and research/ are now part of the public repository (the .gitignore exclusion was removed), and every document is in English | Decided |
| 2 | Default padding (Unauth) | 27 octets (symmetric: sender 41 B / reflector 41 B; SPEC Appendix A). Whether the authenticated modes (A / E) default to 64, which would make them symmetric too, is still undecided (SPEC §5.9, Appendix A) | Decided for Unauth; A / E before v0.1.0 (N1) |
| 3 | Light reflector sequence number scheme | Per-flow counter (default) + `copy_sender` option (ADR 0003) | Decided |
| 4 | Whether to use multiple sessions per control connection in Full mode | The Control-Client can request several sessions on one connection and supports ISC (RFC 5938). The daemon still runs one session per run; a continuous Full session keeps one control connection (ADR 0007) | Decided for now (one session per run) |
| 5 | Daemon-independent CLI mode | None (daemon required); not implemented in M7 | Undecided (N5) |
| 6 | Result persistence | `store.backend`: `memory` (default) or `sqlite` (ADR 0013), with retention and a size limit. Per-packet records are not persisted | Decided (M7) |
| 7 | Remote exposure of gRPC (TCP + mTLS) | Opt-in `daemon.api.tcp` with mutual TLS 1.3 and `allowed_names` (ADR 0010); the UDS stays the default. Per-RPC authorization and certificate reload are not implemented | Decided (M7) |
| 8 | Final list of NOS interop targets | perfSONAR (required), vJunos-router and SR Linux (high), SONiC-VS / VyOS (optional). cEOS is out of scope. SR Linux v26.7.2 has no `/oam` on its default models (7220 IXR-D3L / D2L) when run alone in Docker; the service-provider models remain to be checked under containerlab | Decided (the SR Linux feature check remains, N3) |
| 9 | OTLP export | Not implemented; SPEC lists it as out of scope (CHANGELOG TODO) | N4 |
| 10 | `reflector.control.enforce_start_time` | The reflector does not enforce the Start Time (SPEC CMP-26), but the configuration key is not wired yet | N1 |
| 11 | Metrics: `DROP_REASON_AUTH`, splitting `packets_stray_total` | HMAC mismatches count as `malformed`; whether to add an `auth` reason, and whether to split the sender's stray counter into `replies_discarded_total{reason}` (a proposal), are undecided (SPEC §9.6) | Before v0.1.0 (N1) |
| 12 | Container image publication | Built with `make docker`; no registry chosen | N1 |

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
