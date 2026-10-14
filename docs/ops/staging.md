# Staging environment (docker compose)

`deploy/staging/` starts gotwamp on one Docker host: a sender and three
reflectors, each running gotwampd in its own container, on a dedicated
IPv4 / IPv6 bridge. You can impair the links with tc / netem and watch the
statistics (RTT, one-way delays, IPDV, loss) follow. Optional profiles add a
perfSONAR twampd, and Prometheus with Grafana.

It is a test bench, not a deployment model: the containers run gotwampd as
root, the API socket is world-writable, and the shared secrets are published
test keys. For production, use the packages or `deploy/docker/` (see the README).

## Requirements

- Docker with Compose v2 (`docker compose`). Your user must be able to run
  `docker`; nothing else needs root.
- `make`, and `jq` for the scenarios.
- The `sch_netem` and `ifb` kernel modules on the Docker host. Ubuntu and
  Debian kernels ship both, and the kernel loads them on first use (also
  from a container).
- Free subnets `172.31.80.0/24` and `fd00:6a77:7374::/64` and host ports
  19862, 19871–19873, 19090 and 13000 (all bound to 127.0.0.1). To change
  them, edit `deploy/staging/docker-compose.yml`.

## Starting and stopping

```sh
make staging-up                                      # sender + reflector1..3
make staging-up PROFILES=observability               # + Prometheus and Grafana
make staging-up PROFILES="observability perfsonar"   # + perfSONAR twampd
make staging-status                                  # containers, gotwampd status, sessions
make staging-down                                    # remove containers, network and volumes
```

`staging-up` builds the image `gotwamp-staging:dev` from the working tree
(`deploy/staging/Dockerfile`: the binaries built as in `deploy/docker/`, on
Debian with iproute2, tcpdump, curl and ping), then waits until every
container is healthy. Run it again after changing the code or the
configuration; it rebuilds the image and recreates the containers. The first
build takes a few minutes. The build pulls the latest `golang:1.26` image:
the Go image sets `GOTOOLCHAIN=local`, so a cached image older than the `go`
line of `go.mod` cannot build. Without make, use
`docker compose -f deploy/staging/docker-compose.yml build --pull` and then
`docker compose -f deploy/staging/docker-compose.yml up -d --wait`.

## Topology

```
 host (127.0.0.1)
   :19862 metrics ─┐   deploy/staging/run/sender/gotwampd.sock (API)
                   │    │
 ┌─────────────────┴────┴──┐
 │ sender                  │  172.31.80.10   fd00:6a77:7374::10
 │ gotwampd (sender role)  │  6 monitoring sessions, SQLite store
 └────────────┬────────────┘
              │ bridge "staging"  172.31.80.0/24, fd00:6a77:7374::/64
   ┌──────────┼──────────────────┬───────────────────┬──────────────┐
   │          │                  │                   │              │
 ┌─┴──────────┴─┐ ┌──────────────┴─┐ ┌───────────────┴─┐ ┌──────────┴───────┐
 │ reflector1   │ │ reflector2     │ │ reflector3      │ │ perfsonar        │
 │ .11 / ::11   │ │ .12 / ::12     │ │ .13 / ::13      │ │ .20 / ::20       │
 │ default      │ │ auth/enc/mixed │ │ RFC 5938 / 6038 │ │ twampd (profile  │
 │              │ │ (keys file)    │ │ + STAMP (Light) │ │ perfsonar)       │
 │ :19871       │ │ :19872         │ │ :19873          │ └──────────────────┘
 └──────────────┘ └────────────────┘ └─────────────────┘
   prometheus .30 (:19090) and grafana .31 (:13000): profile observability
```

| Service | IPv4 / IPv6 | Configuration | Host port (127.0.0.1) |
|---------|-------------|---------------|-----------------------|
| `sender` | 172.31.80.10 / fd00:6a77:7374::10 | `config/sender.yaml`: sender role, the monitoring sessions below, `store.backend: sqlite` (volume `sender-state`) | 19862 (metrics) |
| `reflector1` | 172.31.80.11 / ::11 | `config/reflector1.yaml`: Full (unauthenticated) and Light, IPv4 and IPv6, with no TWAMP extension offered (`extensions: []`; the default offers all three) | 19871 (metrics) |
| `reflector2` | 172.31.80.12 / ::12 | `config/reflector2.yaml`: as reflector1, plus authenticated, encrypted and mixed modes with the keys file | 19872 (metrics) |
| `reflector3` | 172.31.80.13 / ::13 | `config/reflector3.yaml`: as reflector1, plus Individual Session Control (RFC 5938), Reflect Octets and Symmetrical Size (RFC 6038), and STAMP on the Light listener (RFC 8762 / 8972) | 19873 (metrics) |
| `perfsonar` (profile `perfsonar`) | 172.31.80.20 / ::20 | perfSONAR `twampd`, all modes (`-a AEMO`), the configuration of `make interop` | — |
| `prometheus` (profile `observability`) | 172.31.80.30 / ::30 | `observability/prometheus.yml`: scrapes the four gotwampd every 5 s | 19090 |
| `grafana` (profile `observability`) | 172.31.80.31 / ::31 | Anonymous Viewer; the Prometheus data source and `docs/ops/grafana-dashboard.json` are provisioned | 13000 |

The services reach each other by name (`reflector1`, ...). Each name resolves
to both addresses, and `-4` / `-6` (`ipv` in the configuration) picks one.

### Monitoring sessions of the sender

| Session | Target | Mode | Settings | Schedule |
|---------|--------|------|----------|----------|
| `r1-full` | reflector1, IPv4 | Full | 100 packets, 20 ms apart | every 10 s (jitter 2 s) |
| `r1-light-v6` | reflector1, IPv6 | Light | 100 ms interval | continuous, 10 s windows |
| `r2-full-enc` | reflector2, IPv4 | Full | `security: encrypted`, key `staging`, padding 64 | every 10 s |
| `r2-light` | reflector2, IPv4 | Light | 100 ms interval | continuous, 10 s windows |
| `r3-stamp` | reflector3, IPv4 | Light | STAMP, SSID 3 | continuous, 10 s windows |
| `r3-full-ext` | reflector3, IPv6 | Full | ISC, Symmetrical Size, Reflect Octets `0xbeef`, 16 octets of padding to reflect, padding 64 | every 10 s |

Each session carries the labels `reflector` and `family`, which the metrics
also have.

### Shared secrets

`deploy/staging/keys/staging-test-only.pfs` holds TEST ONLY secrets. It is
mounted into the sender and reflector2, and the entrypoint copies it to
`/run/gotwamp-keys/keys.pfs` with mode 0600, because gotwampd refuses a keys
file that others can read.

| KeyID | Passphrase | Used by |
|-------|------------|---------|
| `staging` | `gotwamp-staging` | sender ↔ reflector2 |
| `gotwamp` | `gotwamp-interop` | sender → perfsonar (the identity of `make interop`) |

## Using the CLI

The CLI is in every container and talks to the local gotwampd:

```sh
C="docker compose -f deploy/staging/docker-compose.yml"
$C exec sender gotwamp status
$C exec sender gotwamp session list
$C exec sender gotwamp stats r2-light --last 5
$C exec sender gotwamp watch                       # results as they arrive
$C exec reflector2 gotwamp status                  # a reflector's view
```

The sender's socket is also bind-mounted to `deploy/staging/run/sender/` with
mode 0666, so a CLI built on the host can use it:

```sh
make build
bin/gotwamp --socket deploy/staging/run/sender/gotwampd.sock session list
```

### Changing the configuration

The configuration files are bind-mounted as single files. Edit them in place:
an editor or `sed -i` that writes a new file and renames it leaves the
container on the old file, and a reload then re-reads the old content
without an error. `cat new.yaml > config/sender.yaml` keeps the file. If in
doubt, compare `md5sum config/sender.yaml` with
`$C exec sender md5sum /etc/gotwamp/gotwampd.yaml`, or restart the container.
Then apply the changes:

```sh
$C exec sender gotwamp config reload        # or: $C kill -s HUP sender
```

A reload applies the monitoring sessions at once (added, changed, removed;
sessions made with `gotwamp session add` are kept). A file that fails
validation is refused (exit status 2) and the running configuration stays.
Changes to the reflector role need a restart (`$C restart reflectorN`).

### Ad-hoc measurements

`make staging-ping` measures every reflector once (50 packets, 20 ms apart):

| Target | Options |
|--------|---------|
| reflector1 | Full over IPv4; Light over IPv6 |
| reflector2 | Full over IPv4, encrypted with the key `staging`; Light over IPv6 |
| reflector3 | Full over IPv6 with ISC, Symmetrical Size, Reflect Octets and padding to reflect; STAMP Light over IPv4 |

Single measurements:

```sh
$C exec sender gotwamp ping reflector1 -c 200 -i 10ms
$C exec sender gotwamp ping reflector1 -6 --light
$C exec sender gotwamp ping reflector2 --security encrypted --key-id staging
$C exec sender gotwamp ping reflector3 --light --stamp --ssid 7
$C exec sender gotwamp ping reflector3 --isc --symmetrical --reflect-octets 0xbeef --padding-to-reflect 16 -s 64
$C exec sender gotwamp ping perfsonar --security auth --key-id gotwamp      # profile perfsonar
$C exec sender gotwamp ping reflector2 --light -q --output jsonl | jq .summary.rtt
```

With no impairment the containers answer in about 20 µs (RTT median) with no
loss. The containers share the host's clock, so `clock synchronized` holds
and the one-way delays are valid. The reported maximum clock error is the
host kernel's (adjtimex) and grows between NTP updates. That growth is normal.

## Impairing links with netem

`deploy/staging/netem.sh` runs `tc` as root inside a container through
`docker compose exec`, so the host needs no sudo:

```sh
deploy/staging/netem.sh SERVICE [--in|--out] [--peer PEER] NETEM-ARGS...
deploy/staging/netem.sh SERVICE show          # qdiscs, filters, counters
deploy/staging/netem.sh SERVICE clear         # remove both directions
deploy/staging/netem.sh all clear             # every gotwampd container
make staging-netem ARGS="reflector2 delay 10ms 2ms"
```

- `--out` (the default) impairs what SERVICE sends: a netem qdisc at the root
  of `eth0`.
- `--in` impairs what SERVICE receives: the `eth0` ingress is redirected to
  an `ifb0` device, and netem sits on `ifb0`.
- `--peer PEER` limits the impairment to packets to PEER (`--out`) or from
  PEER (`--in`). It uses u32 filters on PEER's IPv4 and IPv6 addresses (a
  `prio` qdisc for `--out`), so other peers are unaffected.
- A new call replaces the impairment of that service and direction. `--in`
  and `--out` can be active at the same time.

### Which direction is which

For a measurement from the sender to reflectorN:

| Path to impair | Command | Shows as |
|----------------|---------|----------|
| Forward (test packets) | `netem.sh reflectorN --in ...` | forward delay / forward loss |
| Reverse (replies) | `netem.sh reflectorN --out ...` | reverse delay / reverse loss |
| Reverse, one reflector, on the sender | `netem.sh sender --in --peer reflectorN ...` | reverse delay / reverse loss |
| Forward, on the sender | `netem.sh sender --out [--peer reflectorN] ...` | **loss and duplication only** |

Delay added on the sender's `--out` does not show in TWAMP. The sender takes
T1 from the kernel's transmit timestamp, which the kernel records when the
packet leaves the qdisc, after netem has held it. The delay is spent before
T1 and drops out of both the RTT and the forward delay. `ping` (ICMP)
does see it. Real networks behave the same way: queueing in the sender's own
qdisc is not path delay. Impair the forward path at the reflector's
ingress instead.

### NETEM-ARGS

These are the arguments of `tc-netem(8)`, and several can be combined in one call:

| Example | Effect | Notes |
|---------|--------|-------|
| `delay 10ms` | fixed delay | |
| `delay 10ms 2ms` | delay with jitter (uniform ±2 ms) | jitter reorders packets sent closer together than the jitter |
| `delay 10ms 2ms distribution normal` | normally distributed jitter | |
| `loss 1%`, `loss 0.3% 25%` | random loss, correlated loss | |
| `delay 10ms reorder 25% 50%` | 25% of the packets are sent at once, ahead of the others | needs a delay |
| `duplicate 1%` | duplication | |
| `corrupt 0.1%` | bit errors | TWAMP drops the corrupt packets: they show as loss |
| `rate 10mbit` | bandwidth limit | netem keeps the order when `rate` is set, even with jitter |

Examples:

```sh
deploy/staging/netem.sh reflector2 delay 10ms 2ms                  # reverse path of reflector2
deploy/staging/netem.sh reflector1 --in delay 20ms                 # forward path of reflector1
deploy/staging/netem.sh sender --in --peer reflector3 loss 5%      # reverse loss from reflector3 only
deploy/staging/netem.sh reflector3 delay 5ms 1ms loss 2% duplicate 2% rate 10mbit
deploy/staging/netem.sh reflector2 show
deploy/staging/netem.sh all clear
```

### What the statistics show

Typical results with 300 to 2,000 packets per measurement. The scenarios
below check these ranges.

| Impairment | RTT median | Forward / reverse median | IPDV mean | Loss |
|------------|------------|--------------------------|-----------|------|
| none | 0.02 ms | 0.00 / 0.02 ms | 0.01 ms | 0 |
| `reflector2 delay 10ms 2ms` | 9.9–10.3 ms | 0.00 / 9.9–10.3 ms | 1.3–1.6 ms (reverse) | 0 |
| `reflector1 --in delay 20ms` | 20.05 ms | 20.03 / 0.02 ms | 0.03 ms | 0 |
| `sender --in --peer reflector2 delay 15ms` | 15.07 ms | 0.00 / 15.07 ms | 0.1 ms | 0 (reflector1 unaffected) |
| `reflector1 --in loss 10%` | — | — | — | about 10%, all estimated forward |
| `reflector1 --in loss 5%` + `--out loss 5%` | — | — | — | about 10%, split about evenly between forward and reverse |
| `reflector3 delay 10ms reorder 25% 50%` | 10.04 ms | — | 0.95 ms | 0, about 18% reordered |

When loss is on the reverse path only, gotwamp reports the loss but leaves
the split empty (`lost_forward_est` = `lost_reverse_est` = 0,
`loss_split_valid: false`, `no split` in the scenario output). Every reply
then carries a Reflector Sequence Number equal to its Sender Sequence Number.
A stateless (`copy_sender`) reflector also returns that pattern, so the
direction cannot be told. Once some loss is forward too, the numbers
differ and the split is reported.

## Scenarios

`make staging-scenarios` (`deploy/staging/scenarios/all.sh`) runs the
following against a running staging, about 13 minutes in all;
`all.sh modes loss` runs only those. Each scenario clears its impairments when it ends and
exits non-zero when a check fails:

| Script | Impairment | Checks |
|--------|------------|--------|
| `delay-jitter.sh` | `reflector2 delay 10ms 2ms` | RTT and reverse median +10 ms, forward ≈ 0, IPDV and reverse IPDV ≈ 1–4 ms, Light and encrypted Full; the `r2-light` session in `/metrics` |
| `forward-delay.sh` | `reflector1 --in delay 20ms`, then `sender --in --peer reflector2 delay 15ms` | forward-only delay over IPv4 and IPv6; reverse-only delay for one peer, the other unaffected |
| `loss.sh` | 10% reverse, 10% forward, 5% + 5% | loss ratio about 10%, the split per direction (and none for reverse-only loss); `r1-full` loss ratio in `/metrics` |
| `combined.sh` | `reflector3 delay 5ms 1ms loss 2% duplicate 2% rate 10mbit`, then `reorder 25% 50%` | STAMP and Full with the extensions still measure; RTT, loss, IPDV, reverse duplicates, reordering |
| `modes.sh` | none | 44 ad-hoc measurements: Full and Light over IPv4 and IPv6 on every reflector; the security modes on reflector2; STAMP, ISC, Symmetrical Size, Reflect Octets and padding to reflect on reflector3; the combinations a reflector is not set up for are refused, and ISC or Symmetrical Size that is not offered falls back to a plain session |
| `netem-matrix.sh` | reflector2 `--in delay 8ms 1ms loss 3% duplicate 2%` with `--out delay 4ms 1ms loss 3% reorder 10%`; sender `--out --peer reflector1 loss 20%` with `--in --peer reflector3 delay 12ms` | each delay, loss estimate, duplicate and reordering on its own direction, in auth, encrypted and Light; per-peer impairments leave the other reflectors untouched |
| `rate-padding.sh` | reflector3 `rate 2mbit`, then `rate 1mbit` | the serialization time of 1,400-octet padding (about 5.9 ms) in Full, Full with Symmetrical Size over IPv6 and STAMP; under overload, the RTT climbs to the timeout and late replies count as lost |
| `monitoring.sh` | three phases of changes on all reflectors | the six monitoring sessions (periodic and continuous) follow each change in Prometheus within 27 s: RTT median, loss ratio, IPDV, the histogram quantile, the loss counter; all back to normal after clearing. Needs the `observability` profile |
| `reload.sh` | none | a session added and one changed by reload; an invalid file refused; SIGHUP back to the original; API sessions kept |
| `outage.sh` | reflector stopped, paused, cut off and killed; sender restarted | how the runs fail (connect, greeting timeout, aborted and invalid) and that every session recovers; the SQLite history survives a sender restart |

Every scenario uses `gotwamp ping -q --output jsonl` in the sender and jq on
the host. `scenarios/lib.sh` has the helpers (`twping`, `check`, `metric`) for
new scenarios.

## Metrics

| Endpoint | Content |
|----------|---------|
| http://127.0.0.1:19862/metrics | sender: `gotwamp_sender_*` per session (labels `session`, `target`, `mode`, `reflector`, `family`) |
| http://127.0.0.1:19871/metrics ... 19873 | reflectors: `gotwamp_reflector_*` |
| http://127.0.0.1:19090 | Prometheus (profile `observability`) |
| http://127.0.0.1:13000 | Grafana, dashboard "gotwamp overview" (profile `observability`) |

The metrics of the latest run or window follow an impairment within one
schedule period (10 s):

```sh
curl -s 127.0.0.1:19862/metrics | grep 'session="r2-light"' | grep -E 'last_run_(rtt|jitter|loss)'
# gotwamp_sender_last_run_rtt_seconds{...,session="r2-light",stat="median",...} 0.0101
# gotwamp_sender_last_run_jitter_seconds{kind="ipdv_mean",...,session="r2-light",...} 0.0013
```

PromQL in Prometheus or Grafana:

```promql
gotwamp_sender_last_run_rtt_seconds{stat="median"}
gotwamp_sender_last_run_reverse_delay_seconds{stat="median"}
gotwamp_sender_last_run_jitter_seconds{kind="ipdv_mean"}
gotwamp_sender_last_run_loss_ratio
sum by (instance) (rate(gotwamp_reflector_packets_reflected_total[1m]))
```

The metrics are described in [metrics.md](metrics.md). The SQLite
store keeps the results for 72 hours, and `gotwamp stats SESSION --since 1h`
reads them back.

## perfSONAR profile

With `PROFILES=perfsonar`, the container `perfsonar` runs the twampd of
`make interop` ([interop.md](interop.md)) with the same configuration, on the
staging bridge. Use `--key-id gotwamp` for the authenticated, encrypted and
mixed modes. twampd answers TWAMP Full only (no Light). Its timestamps
carry an offset of a few milliseconds, so the forward and reverse delays to
it are off (they can be negative) while the RTT is right. Compare RTTs with
it, not one-way delays.

## Cleaning up

```sh
deploy/staging/netem.sh all clear     # impairments only (also gone when a container restarts)
make staging-down                     # containers, network, the volume sender-state (SQLite)
docker image rm gotwamp-staging:dev   # the image
```

`deploy/staging/run/` (git-ignored) holds the sender's socket directory, and
the bind mount creates it as root. gotwampd removes the socket when it stops,
so after `staging-down` the empty directory can be removed with
`rm -rf deploy/staging/run/sender`.

## Troubleshooting

| Symptom | Cause and fix |
|---------|---------------|
| `Pool overlaps with other one on this address space` | The subnets are taken by another Docker network. Change `networks.staging` and the addresses in `docker-compose.yml` |
| `Error: Specified qdisc kind is unknown` / `Unknown device type` (ifb) | The host kernel lacks `sch_netem` or `ifb`. Load them on the host (`sudo modprobe sch_netem ifb`) |
| `RTNETLINK answers: Operation not permitted` | The container lacks `NET_ADMIN`. Keep `cap_add` in `docker-compose.yml` |
| Delay on the sender's `--out` does not change the TWAMP figures | Expected (see "Which direction is which"). Use `reflectorN --in` |
| `cannot connect to gotwampd at ...: invalid (non-empty) authority` | A CLI from before the fix of relative socket paths. Pass an absolute path, or rebuild |
| `go: go.mod requires go >= ...` while building | The cached `golang` image is older than `go.mod`. `make staging-up` pulls it; without make, add `--pull` to the build |
| A reload reports no change after editing a file | The file was replaced instead of rewritten, and the container still sees the old one. See "Changing the configuration" |
| A container stays `unhealthy` | `docker compose -f deploy/staging/docker-compose.yml logs SERVICE`. Check the configuration with `$C exec SERVICE gotwamp config validate -f /etc/gotwamp/gotwampd.yaml` |

## Test campaign

The test campaign was run on 2026-10-14 with `deploy/staging/scenarios/all.sh`
and the `observability` profile, on one host:
- Linux 6.8, Docker 29.8, Compose 5.5, 4 vCPU.
- gotwampd built from the working tree (`timestamping: kernel`; the
  containers share the host clock).

In all, 233 checks passed in 10 scenarios (about 13 minutes). The figures
are those of the last run. Earlier runs on the same day agreed within the
checked ranges.

| Area | Condition | Result |
|------|-----------|--------|
| Modes (`modes.sh`, 44 cases) | Full and Light × IPv4 and IPv6 on every reflector; reflector2: auth, encrypted and mixed; reflector3: ISC, Symmetrical Size, Reflect Octets `0xbeef`, padding to reflect, all at once, STAMP with SSID 65535 | All measured 50/50 without loss |
| Refusals (in `modes.sh`) | Security modes on reflector1 and reflector3; unauthenticated on reflector2; unknown KeyID; Reflect Octets or padding to reflect where not offered; padding shorter than the padding to reflect | All refused with a clear message (exit status 4, or 2 for the KeyID) |
| Fallback (in `modes.sh`) | ISC or Symmetrical Size where not offered | Plain session, 50/50 |
| Delay and jitter | reflector2 `--out delay 10ms 2ms` | Light: RTT median 10.21 ms (reverse 10.21, forward 0.00), IPDV mean 1.31 ms.<br>Encrypted Full: 10.06 ms, IPDV 1.35 ms.<br>`/metrics` r2-light: 10.39 ms, IPDV 1.25 ms |
| One direction | reflector1 `--in delay 20ms`, IPv4 and IPv6 | Forward 20.03 ms, reverse 0.02 ms |
| One direction, one peer | sender `--in --peer reflector2 delay 15ms` | reflector2: reverse 15.07 ms.<br>reflector1: 0.02 ms (untouched) |
| Loss, reverse only | reflector1 `--out loss 10%`, 1,000 packets | Full 10.5%, Light 8.8%. No split (R = S, as a copy_sender reflector would) |
| Loss, forward | reflector1 `--in loss 10%` | 10.5%, all estimated forward |
| Loss, both ways | 5% `--in` + 5% `--out`, 2,000 packets | 10.45%: forward 116, reverse 93 (sum = lost) |
| Everything on both directions | reflector2 `--in delay 8ms 1ms loss 3% duplicate 2%`, `--out delay 4ms 1ms loss 3% reorder 10%`, 2,000 packets | auth: forward 8.05 / reverse 3.97 ms; lost 102 (forward 50 / reverse 52); 40 forward duplicates; 326 reordered.<br>Encrypted and Light v6 alike |
| Per-peer on the sender | `--out --peer reflector1 loss 20%` with `--in --peer reflector3 delay 12ms` | reflector1: 20.7% lost, all forward.<br>reflector3: reverse 12.04 ms.<br>reflector2: untouched |
| Combined on reflector3 | `delay 5ms 1ms loss 2% duplicate 2% rate 10mbit` | STAMP: 5.11 ms, 2.4% lost, 20 reverse duplicates.<br>Full with the extensions over IPv6: 5.19 ms, 1.6% lost, 26 duplicates |
| Reordering | reflector3 `delay 10ms reorder 25% 50%` | 251 of 1,000 reordered, RTT median 10.03 ms |
| Large padding at a rate limit | reflector3 `rate 2mbit`, padding 1,400 at 50 packets/s | RTT median 5.94 ms (computed serialization 5.82 ms); padding 27: 0.38 ms.<br>Symmetrical Size over IPv6: 6.08 ms. STAMP: 6.02 ms. No loss |
| Overload | reflector3 `rate 1mbit`, 1,400 octets at 200 packets/s | RTT from 11.7 ms climbing to 1,999 ms (the 2 s timeout); 25.5% counted lost as late |
| Monitoring sessions in Prometheus | Three phases of changes, read 27 s after each | All six sessions (three periodic, three continuous) followed each change:<br>- 30 ms and 10 ms of delay: RTT median 0.0301 and 0.0101 s.<br>- 10% forward loss: loss ratio 0.05–0.12.<br>- 5 ms ± 2 ms: IPDV 1.4 ms.<br>Also the histogram quantile and the loss counter. All back to about 20 µs and no loss after clearing |
| Reload | Add a session, change an interval, invalid file, SIGHUP back | Added and changed at once (200 packets per window at 50 ms). The invalid file was refused with exit status 2, and the running configuration stayed. SIGHUP restored the original, and the removed session's series left `/metrics`. API sessions were kept |
| Reflector stopped (20 s) | `docker compose stop reflector1` | Full runs failed at connect (DNS name gone, then connection refused). Light lost the windows on the cached address. Both recovered on the next run or window |
| Reflector hung (25 s) | `docker compose pause reflector3` | Full: one run failed at the greeting after `control_timeout` (15 s), and the next was skipped. A run already in progress was kept as an invalid summary (61/100), then failed at Stop-N-Sessions. STAMP lost the windows. Both recovered |
| Link cut (30 s) | reflector2 `loss 100%` both ways | Full: connect i/o timeout. Light: lost windows. Both recovered |
| Reflector killed mid-run | `docker compose kill reflector3` during an ad-hoc Full measurement | Control connection lost (EOF): summary aborted and invalid (196/196 before the kill), exit status 4 |
| Sender restarted | `docker compose restart sender` | Sessions resumed, and `gotwamp stats --since 15m` still returned the history (87 results, SQLite). The API-made session was gone, as specified (definitions are not persisted) |

Notes from the campaign:

- `last_run_success` stays 1 for a Light window that loses every packet,
  because the window completed. Alert on `gotwamp_sender_last_run_loss_ratio`
  for reachability, and on `last_run_success` / `runs_total{result="error"}`
  for runs that failed.
- `gotwamp stats` lists summaries only. Runs that failed without a summary
  (connect, greeting) leave a gap there. The errors are in the log, in
  `gotwamp_sender_runs_total{result="error"}`, and in `gotwamp session show`
  for the latest run.
- Found and fixed during the campaign:
  - the CLI did not accept a relative `--socket` path;
  - the configuration errors said "(1 problems)";
  - `make staging-up` failed with a cached `golang` image older than `go.mod`.
