# gotwamp

gotwamp is an implementation of TWAMP (Two-Way Active Measurement Protocol, [RFC 5357](https://www.rfc-editor.org/rfc/rfc5357)) in Go.
It measures round-trip and one-way delay, delay variation, loss, duplication and reordering between two hosts,
and reflects the test packets of other TWAMP implementations.

> [!NOTE]
> gotwamp is under development (v0.x). Configuration keys, the gRPC API and the output formats may still change
> between minor versions; breaking changes are listed in [CHANGELOG.md](CHANGELOG.md).
> Interoperability with perfSONAR (twampd / twping 5.2.6) is checked in both directions every night
> ([docs/ops/interop.md](docs/ops/interop.md)).

## Features

- **Both roles, both flavors**: Session-Sender and Session-Reflector, for TWAMP Full (negotiated over
  TWAMP-Control) and TWAMP Light.
- **Daemon and CLI**: the daemon `gotwampd` runs the reflector and the monitoring sessions; the CLI `gotwamp`
  talks to it over gRPC on a Unix domain socket, or from another host over TCP with mutual TLS (`--server`).
- **Monitoring**: periodic or continuous sessions, with results exported as Prometheus metrics and JSON Lines,
  and kept in memory or in SQLite.
- **Security modes**: Unauthenticated, and for TWAMP Full Authenticated / Encrypted / Mixed
  (RFC 4656 / 5357 / 5618). Keys files are compatible with perfSONAR's pfs format.
- **Extensions**: Individual Session Control (RFC 5938), Reflect Octets / Symmetrical Size (RFC 6038),
  DSCP / ECN monitoring (RFC 7750, Light), and a STAMP base-packet compatible mode (RFC 8762, SSID of RFC 8972;
  Light).
- **Accurate timestamps**: kernel timestamps (SO_TIMESTAMPING, the transmit time T1' included) by default, and
  hardware timestamps on NICs that support them (with the PHC synchronized to CLOCK_REALTIME).
- **Interoperability knobs** for the quirks of vendor implementations (Cisco, Juniper, Nokia, perfSONAR);
  see [docs/SPEC.md](docs/SPEC.md) §11.
- **Platforms**: Linux on amd64 and arm64. Static binaries (`CGO_ENABLED=0`).

## Installation

### go install

Go 1.26 or later:

```sh
go install github.com/Akkanbe/gotwamp/cmd/gotwampd@latest
go install github.com/Akkanbe/gotwamp/cmd/gotwamp@latest
sudo setcap cap_net_bind_service=+ep "$(go env GOPATH)/bin/gotwampd"   # to bind port 862
```

### deb / rpm packages (systemd)

The release deb and rpm packages (linux amd64 / arm64) contain `gotwampd`, `gotwamp`, a systemd unit and an
example configuration. Installing them creates the user `gotwamp`, but does not enable the service.

```sh
sudo dpkg -i gotwamp_<version>_amd64.deb        # or: sudo rpm -i gotwamp-<version>-1.x86_64.rpm
sudoedit /etc/gotwamp/gotwampd.yaml              # review the configuration (docs/ops/config.md)
sudo systemctl enable --now gotwampd
sudo usermod -aG gotwamp "$USER"                 # users of the CLI need the API socket (log in again)
gotwamp status
```

The unit (`deploy/systemd/gotwampd.service`) runs as the user `gotwamp` with `CAP_NET_BIND_SERVICE` only, for
port 862, and is hardened with `ProtectSystem=strict` and more. `systemctl reload gotwampd` reloads the
configuration.

### Docker

`deploy/docker/Dockerfile` builds a distroless (nonroot) image with both binaries.

```sh
make docker IMAGE=gotwamp:dev
docker run -d --name gotwampd -p 862:862/tcp -p 862:862/udp -p 9862:9862 gotwamp:dev
docker exec gotwampd gotwamp status
docker exec gotwampd gotwamp ping --light 192.0.2.10
```

Replace `/etc/gotwamp/gotwampd.yaml` in the image (`deploy/docker/gotwampd.yaml`) to change the configuration.
The image's HEALTHCHECK checks that the API answers with `gotwamp status`
(`docker inspect -f '{{.State.Health.Status}}' gotwampd`). For `--network host`, see
[docs/ops/troubleshooting.md](docs/ops/troubleshooting.md).

### From source

```sh
make build   # bin/gotwampd and bin/gotwamp, static binaries
sudo setcap cap_net_bind_service=+ep bin/gotwampd
```

## Quick start

Without a configuration file (by default `/etc/gotwamp/gotwampd.yaml`), `gotwampd` starts with the reflector role
only: TWAMP-Control on 862/tcp and TWAMP Light on 862/udp.
The commands below run as root for brevity; the packages run `gotwampd` under systemd as the user `gotwamp`
instead. The CLI reaches the daemon through `/run/gotwamp/gotwampd.sock` (`--socket` to change it).

On the reflector host:

```sh
sudo gotwampd
```

On the sender host, enable the sender role and measure:

```sh
cat > gotwampd.yaml <<'EOF'
sender:
  enabled: true
EOF
gotwamp config validate -f gotwampd.yaml          # checks a configuration without a daemon
sudo gotwampd --config gotwampd.yaml &

sudo gotwamp ping 192.0.2.10                      # TWAMP Full (TWAMP-Control on 862/tcp)
sudo gotwamp ping --light 192.0.2.10 -c 10 -D ef  # TWAMP Light (862/udp)
```

```
--- 192.0.2.10 TWAMP statistics ---
100 sent, 1 lost (1.000%), 0 dup (fwd 0 / rev 0), 0 reordered
rtt      min/median/mean/max = 8.12/8.29/8.31/9.12 ms, p95 8.61 ms, stddev 0.10 ms
forward  min/median/max = 4.01/4.05/4.52 ms   (clocks synchronized, max error 0.21 ms)
reverse  min/median/max = 4.10/4.22/4.61 ms
...
```

Ctrl-C during `ping` stops the measurement and prints the summary so far (exit status 130); a second Ctrl-C
exits at once.

### Try it on one host: the staging environment

`deploy/staging/` runs a sender and three reflectors, each `gotwampd` in its own container, with docker compose.
Impair the links with tc / netem (delay, jitter, loss) and watch the statistics follow; optional profiles add
Prometheus with Grafana, and a perfSONAR twampd. See [docs/ops/staging.md](docs/ops/staging.md).

```sh
make staging-up                                   # sender + reflector1..3
make staging-netem ARGS="reflector2 delay 10ms 2ms"
make staging-status
make staging-down
```

### More commands

```sh
gotwamp ping --security encrypted --key-id alice 192.0.2.10   # security mode (key in gotwampd's sender.keys_file)
gotwamp ping --symmetrical --reflect-octets 0xbeef --padding-to-reflect 8 -s 64 192.0.2.10   # RFC 6038, when offered
gotwamp ping --light --stamp --ssid 7 192.0.2.10               # as a STAMP (RFC 8762) sender
gotwamp status                                    # state of the daemon

gotwamp session list | show NAME | add | update | remove | start | stop
gotwamp stats NAME --last 10                      # summaries of the latest runs or windows
gotwamp watch [NAME ...] [--packets]              # stream of results
gotwamp reflector status | sessions
gotwamp config reload                             # the same as SIGHUP

gotwamp --server 192.0.2.20:9443 --tls-ca ca.pem --tls-cert me.pem --tls-key me-key.pem status   # remote daemon
```

Every command takes `--json` (`--output json`) and `--output jsonl`. Exit statuses: 0 success, 1 all packets lost
or a general error, 2 bad arguments or configuration, 3 cannot reach the daemon, 4 protocol or control error,
130 interrupted.

## Configuration

`gotwampd` reads one YAML file. Monitoring sessions inherit what they leave out from `sender.defaults`:

```yaml
sender:
  enabled: true
  defaults:
    mode: full
    count: 100
    interval: 100ms
  sessions:
    - name: tokyo-osaka
      target: 192.0.2.10
      dscp: ef
      schedule: {frequency: 1m}
      labels: {site: tokyo}
    - name: osaka-light
      target: 198.51.100.7
      mode: light
      schedule: {type: continuous, window: 10s}

daemon:
  metrics:
    listen: 127.0.0.1:9862            # Prometheus /metrics and /healthz

export:
  jsonl:
    enabled: true
    path: /var/lib/gotwamp/results.jsonl
```

`SIGHUP` (or `gotwamp config reload`) applies changed sessions without restarting the others. Every key, its
default and its validation are in [docs/ops/config.md](docs/ops/config.md).

## Documentation

- [docs/README.md](docs/README.md): index of all documents and the glossary of terms
- [docs/ops/config.md](docs/ops/config.md): configuration reference (every key and its default)
- [docs/ops/metrics.md](docs/ops/metrics.md): Prometheus metrics and example alerts; an example Grafana dashboard
  is [docs/ops/grafana-dashboard.json](docs/ops/grafana-dashboard.json)
- [docs/ops/troubleshooting.md](docs/ops/troubleshooting.md): the exposure of a reflector (amplification, limits,
  ACLs), privileges, timestamping, and how to isolate common failures
- [docs/ops/staging.md](docs/ops/staging.md): the docker compose staging environment (a sender, three reflectors,
  delay / jitter / loss experiments with tc / netem)
- [docs/ops/interop.md](docs/ops/interop.md): interoperability tests (perfSONAR automated; SR Linux and
  vJunos-router in a containerlab lab)
- [docs/ops/staging.md](docs/ops/staging.md): a docker compose test bench (a sender, three reflectors, impaired links)
- [docs/SPEC.md](docs/SPEC.md): the implementation specification; [docs/PLAN.md](docs/PLAN.md): the
  implementation plan; [docs/research/](docs/research/): research on the RFCs, other implementations and vendors
- [docs/adr/](docs/adr/): Architecture Decision Records. New ADRs start from `docs/adr/0000-template.md`
- [CHANGELOG.md](CHANGELOG.md): notable changes

## Development

Requirements: Go 1.26 or later and GNU make. Tests with `-race` need a C compiler (cgo).

```sh
make tools   # install the development tools into GOBIN (once)
make build   # bin/gotwampd, bin/gotwamp (static, CGO_ENABLED=0)
make test    # go test -race ./...
make test-component  # netns + veth + tc netem component tests (needs sudo, -tags integration)
make lint    # gofumpt, go vet, golangci-lint, declscope, buf lint
make proto   # regenerate the Go code from api/proto
make vuln    # govulncheck
make docker  # container image (deploy/docker/Dockerfile)
make release-snapshot  # tar.gz / deb / rpm into dist/ with goreleaser (skipped without goreleaser)
make interop # interoperability tests with perfSONAR (needs Docker, docs/ops/interop.md)
make soak    # soak test: sessions, exporters and a loopback reflector, checked for leaks
make staging-up      # docker compose staging environment (docs/ops/staging.md)
```

`make help` lists the targets. The version string comes from `git describe` through `-ldflags`
(`./bin/gotwamp version`, `./bin/gotwampd --version`).

### Development tools

`make tools` installs these with `go install`, at the versions pinned at the top of the `Makefile`.

| Tool | Purpose |
|------|---------|
| `buf`, `protoc-gen-go`, `protoc-gen-go-grpc` | gRPC / protobuf code generation, lint and breaking-change detection |
| `gofumpt` | formatting |
| `golangci-lint` | static analysis |
| `govulncheck` | vulnerability checks |
| `declscope` | declaration scope checks (see below) |

### Repository layout

```
gotwamp/
├── cmd/
│   ├── gotwampd/            # daemon entry point
│   └── gotwamp/             # CLI entry point (cobra)
├── api/proto/gotwamp/v1/    # gRPC / protobuf definitions (package gotwamp.v1, managed with buf)
├── internal/
│   ├── api/gen/gotwamp/v1/  # code generated by buf generate (Go package gotwampv1, committed)
│   ├── twamp/               # protocol core: wire, ntptime, errest (standard library only, no I/O)
│   ├── stats/               # measurement statistics (standard library only)
│   ├── control/             # TWAMP-Control Control-Client / Server state machines
│   ├── sender/              # Session-Sender
│   ├── reflector/           # Session-Reflector (Full) and Light reflector
│   ├── tstamp/, clock/      # timestamping and the clock synchronization state (Linux)
│   ├── schedule/, store/    # scheduler of the monitoring sessions, result store (memory, SQLite)
│   ├── export/              # Prometheus / JSON Lines
│   ├── config/              # configuration file
│   ├── daemon/              # wiring of gotwampd, gRPC server
│   └── cli/                 # gotwamp subcommands
├── test/
│   ├── golden/              # fixed byte sequences taken from the RFC figures
│   ├── component/           # loopback, netns + netem component tests (netns ones behind the integration tag)
│   ├── soak/                # long-running leak test (build tag soak)
│   └── interop/perfsonar/   # interoperability tests with perfSONAR (build tag interop, Docker)
├── deploy/
│   ├── systemd/             # gotwampd.service
│   ├── docker/              # Dockerfile and the container configuration
│   ├── config/              # example configuration installed by deb / rpm
│   ├── nfpm/                # deb / rpm install scripts
│   ├── staging/             # docker compose staging: a sender, three reflectors, netem scenarios
│   └── containerlab/        # interoperability lab (perfSONAR / SR Linux / vJunos-router)
├── docs/
│   ├── SPEC.md, PLAN.md     # specification and plan
│   ├── research/            # research notes
│   ├── adr/                 # Architecture Decision Records
│   └── ops/                 # operations guide (configuration, metrics, troubleshooting, interop)
└── .github/workflows/       # CI (build / test / lint / proto / vulncheck) and the nightly interop tests
```

Dependencies point one way, from the outside in:

```
cmd → internal/{daemon,cli} → internal/{schedule,store,export,config,api/gen}
    → internal/{control,sender,reflector} → internal/{tstamp,clock,stats}
    → internal/twamp/{wire,ntptime,errest}
```

`internal/twamp` and `internal/stats` depend on the standard library only, and not on `net`, `time.Now` or
randomness (randomness is injected as an `io.Reader`).

### gRPC API

The definitions are in `api/proto/gotwamp/v1/`.

| File | Contents |
|------|----------|
| `daemon.proto` | `DaemonService` (GetStatus, ReloadConfig) |
| `sender.proto` | `SenderService` (RunTest, StopTest, CRUD / Start / Stop of monitoring sessions, GetSessionStats, WatchResults) |
| `reflector.proto` | `ReflectorService` (GetReflectorStatus, ListReflectorSessions) |
| `types.proto` | shared types such as `TestSpec`, `PacketResult`, `RunSummary`, `TestEvent` |

- The managed mode of `buf.gen.yaml` (`go_package_prefix`) writes the generated code to
  `internal/api/gen/gotwamp/v1` (Go package `gotwampv1`), which is committed. After changing a proto, run
  `make proto` and commit the generated code with it; CI checks that it is up to date.
- Unset fields inherit the daemon's defaults (`sender.defaults`). Scalars for which 0 means something (padding,
  dscp, count, ...) are `optional`, so that "unset" and "0" differ ([ADR 0005](docs/adr/0005-grpc-api-field-presence.md)).
- Fields of `gotwamp.v1` are reserved, never removed. CI runs `buf breaking` on pull requests.

### Lint policy

`make lint` runs the following, in order; CI runs the same.

1. `gofumpt` formatting check (`make fmt` formats)
2. `go vet`
3. `golangci-lint` (the standard set + `errcheck`, `gosec`, `revive`; configured in `.golangci.yml`)
4. `declscope shrink ./...`, then `declscope ./...`
5. `buf lint` and the `buf format` check

The generated code (`internal/api/gen`) is excluded from every Go linter.

#### declscope

[declscope](https://github.com/mpyw/declscope) is a linter that gives Go declarations the pseudo-scopes "private
to the file" and "shared within the package", and checks them. gotwamp requires it ([ADR 0004](docs/adr/0004-declscope.md)).

- The principle is **one file = one namespace**. A package-level declaration is private to its file by default;
  only the declarations other files use are marked with `//declscope:package // <reason>`.
- Diagnostics are resolved by structure, splitting or merging files, not by a flood of directives.
- `declscope shrink ./...` runs first. It reports exported identifiers under `internal/` that no other package
  uses; unexporting them can reveal new boundary violations, so it is settled before the analyzer
  (`declscope ./...`).
- The configuration keeps the defaults (boundary on, surplus strict, unused strict, naming rule off);
  `.declscope.yaml` only omits the generated code with `filter.omit`.

### Conventions

- Everything is in English: code, identifiers, comments, commit messages, test names and documents.
- Commits follow [Conventional Commits](https://www.conventionalcommits.org/).
- The code builds with `CGO_ENABLED=0`. Linux-only code carries `//go:build linux`.
- Exported identifiers have doc comments, and errors are wrapped with `fmt.Errorf("...: %w", err)`.
- Time-dependent logic is tested with `testing/synctest`, and decoders have fuzz tests.

## Status

gotwamp is at v0.x. TWAMP Full / Light measurement, the reflector, monitoring sessions, the Prometheus / JSON
Lines / SQLite outputs, the Authenticated / Encrypted / Mixed modes (TWAMP Full), the RFC 5938 / RFC 6038
extensions, the STAMP compatible mode, hardware timestamping and remote management over TCP + mTLS work.
Open items are listed under TODO in [CHANGELOG.md](CHANGELOG.md).

## License

[Apache License 2.0](LICENSE)
