# Interoperability tests

This document describes how to run the tests that verify gotwamp communicates correctly with other TWAMP implementations, and which implementations are covered. For the planning context and the list of cases, see PLAN §8.

## Targets

| Target | How to obtain | Direction | Mode | Status |
|------|----------|------|--------|------|
| perfSONAR `twampd` / `twping` 5.2.6 | Docker `perfsonar/testpoint` (free, no account needed) | gotwamp → twampd, twping → gotwampd | Full only (perfSONAR does not support TWAMP Light) | **Automated** (`make interop`, nightly) |
| gotwamp ⇄ gotwamp | — | Both directions | Full / Light | `go test ./test/component/...` (loopback), `make test-component` (netns + netem, requires root) |
| Juniper vJunos-router | juniper.net (free download) + vrnetlab | Both directions | Full / Light | containerlab topology and configuration prepared (not run yet) |
| Nokia SR Linux | `ghcr.io/nokia/srlinux` | gotwamp → SR Linux (Full server / STAMP reflector), STAMP sender → gotwampd (Light) | Full / Light (STAMP) | containerlab topology and configuration prepared (not run yet). Has limitations described below |
| VyOS (rolling) | Free | gotwamp → VyOS (Full server based on perfSONAR twampd) | Full | Optional. Can be skipped because it is the same implementation as perfSONAR |
| Cisco XRd / C8000V and others | Vendor account required | — | — | Planned only for environments where the images are available |

## perfSONAR interoperability tests

### Setup

- `test/interop/perfsonar/compose.yaml` starts the perfSONAR testpoint image with **host network** and runs only `twampd` in the foreground (supervisord and the other services are not started).
  - Control port: 862/tcp (can be changed with `PERFSONAR_TWAMPD_LISTEN`)
  - Test ports: 18760–19960/udp (`testports` in `conf/twamp-server.conf`)
  - Authentication modes: all of authenticated / encrypted / mixed / open (`-a AEMO`). Open mode is enabled by `allow_open_mode=on` in `conf/twamp-server.limits`; the shared secrets for the other modes are in `conf/twamp-server-test-only.pfs` (identity `gotwamp`; **TEST ONLY: dummy passphrases, never use in production**; mounted into the container under the name `twamp-server.pfs` that twampd reads). The gotwamp-side tests read the same file with `keystore.LoadPFS`
- Because of the host network, gotwamp and twampd on the host communicate over loopback (`127.0.0.1` / `::1`), and `twping` inside the container also reaches gotwampd on the host over loopback. IPv4 and IPv6 can both be tested with the same procedure.

### Running

```sh
make interop                 # start container → Go tests → stop container
make interop INTEROP_KEEP=1  # keep the container after the run (for investigation)
make interop INTEROP_CLI=1   # also run the CLI script (run.sh)
```

Requirements: Docker (Compose v2), Go. Root is not required (twampd inside the container binds 862). The first run takes time to pull the image (about 1.8GB).

To run the container manually:

```sh
docker compose -f test/interop/perfsonar/compose.yaml up -d --wait
go test -tags interop -count=1 -v ./test/interop/...
test/interop/perfsonar/run.sh          # CLI version (see below)
docker compose -f test/interop/perfsonar/compose.yaml down
```

### Test contents

`test/interop/perfsonar/perfsonar_test.go` (`//go:build interop`) uses gotwamp's internal packages directly, so protocol interoperability can be verified without waiting for the release binaries. The tests are skipped if the container is not running.

| Case | Test | Description | Pass criteria |
|--------|--------|------|------|
| I-01 / I-03 | `TestGotwampToTwampd` | gotwamp Full sender (`sender.RunFull`) → twampd. IPv4 / IPv6 × {default, padding 0, padding 1000 + DSCP EF, zero addresses}, 100 packets each at 20ms intervals | 100 received, 0 loss, 0 duplicates, median RTT ≤ 20ms, hop count 0, alternate port returned by twampd is within testports, reply DSCP matches Type-P |
| I-04 (partial) | `TestGotwampRejectsAlternatePort` | Request to twampd with the equivalent of `accept_alternate_port: false` | Ends with an "alternate port rejected" error in the `request` stage |
| I-02 | `TestTwpingToGotwampd` | perfSONAR `twping -M` → gotwampd TWAMP-Control server + Full reflector. IPv4 / IPv6 × {default, `-s 0`, `-s 1000`, `-D 46`, `-Z`} | twping exits normally, SENT 100, LOST 0, 0 duplicates, MEDIAN ≤ 20ms, TTL 255 (0 hops), DSCP / padding of the session on the gotwampd side are as requested |
| I-05 | `TestGotwampToTwampdSecurity` | gotwamp Full sender → twampd in authenticated / encrypted / mixed. IPv4 / IPv6 × padding {27, 1000}. Also a wrong passphrase | 100 received, 0 loss, median RTT ≤ 20ms, hop count 0. A wrong passphrase is rejected in the `setup` stage |
| I-06 | `TestTwpingToGotwampdSecurity`, `TestTwpingWrongPassphrase` | `twping -A {A,E,M} -u gotwamp -k twamp-server.pfs` (the mounted `conf/twamp-server-test-only.pfs`) → gotwampd (reading the same pfs). IPv4 / IPv6 × `-s` {27, 1000}. Also a gotwampd with a different key | SENT 100, LOST 0, TTL 255, the test packet mode on the gotwampd side (unauthenticated for mixed) and padding are as requested. With a different key, twping exits with `Server denied access` and no session is opened |

The authenticated cases (I-05 / I-06) check all of the following against perfSONAR: key derivation (PBKDF2-HMAC-SHA1), Token, AES-CBC and HMAC on the control stream, and key derivation plus ECB / CBC / HMAC for test packets. The golden values for the unit tests (`test/golden/auth-vectors.txt`) are computed independently with Python and openssl.

`test/interop/perfsonar/run.sh` runs I-01 / I-02 / I-05 / I-06 with the **release binaries** (`gotwampd` + `gotwamp ping`) (confirmed passing on both IPv4 and IPv6).
- It creates a temporary configuration from `gotwampd.yaml.in`. For the authenticated cases, it copies `conf/twamp-server-test-only.pfs` into the working directory with mode 0600 and sets it as `sender.keys_file` and `reflector.control.keys_file`.
- It starts gotwampd and runs twping against it (`twping -A A|E|M -u gotwamp -k ...`), and measures twampd with `gotwamp ping --output jsonl [--security ... --key-id gotwamp]`.
- It can be tuned with the environment variables `CASES` (`"I-01 I-02 I-05 I-06"`), `COUNT`, and `GOTWAMPD` / `GOTWAMP` (to use existing binaries).

### Trying authenticated interoperability manually

Steps for testing against your own perfSONAR node. Both sides can use the same keys file (format: [config.md, "Keys File"](config.md#keys-file-senderkeys_file--reflectorcontrolkeys_file)).

1. Create a keys file `keys.pfs` (the test file `conf/twamp-server-test-only.pfs` and the staging file `deploy/staging/keys/staging-test-only.pfs` are **TEST ONLY: dummy passphrases, never use in production**; always create new keys for production). On the perfSONAR side, place it in the twampd configuration directory as `twamp-server.pfs`; on the gotwamp side, place it at `/etc/gotwamp/keys.pfs` (0600, owned by gotwamp).
2. **perfSONAR → gotwampd**:
   - Set `reflector.control.keys_file: /etc/gotwamp/keys.pfs` in gotwampd and restart it. If `modes` is omitted, all four modes are offered.
   - Run `twping -A E -u <identity> -k keys.pfs <gotwampd address>` (likewise for `-A A` / `M`).
   - gotwampd logs `control server: client authenticated`, and the SECURITY column of `gotwamp reflector sessions` shows `encrypted`.
3. **gotwamp → twampd**:
   - Start twampd with `-a AEMO` (or `authmode AEMO` in `twamp-server.conf`) and allow the identity in `twamp-server.limits`.
   - Set `sender.keys_file: /etc/gotwamp/keys.pfs` in gotwampd and run `gotwamp ping --security encrypted --key-id <identity> <twampd address>`.
4. **On failure**: if twping reports `Server denied access`, or gotwamp reports `setup: rejected by server: server-start accept=1`, suspect an identity or passphrase mismatch (troubleshooting.md §4).

### CI

`.github/workflows/interop.yml` runs `make interop` nightly (18:47 UTC) and on manual dispatch (`workflow_dispatch`). Enabling `cli` on manual dispatch also runs the CLI script. On failure, the twampd log is printed.

## containerlab lab (perfSONAR / SR Linux / vJunos-router)

The topology from PLAN §8.2 is in `deploy/containerlab/gotwamp-interop.clab.yml`. gotwamp participates as a container and has one point-to-point link to each peer (IPv4 / IPv6).

| Link | gotwamp side | Peer side | Subnet |
|--------|-----------|--------|-----------|
| eth1 ⇄ perfSONAR eth1 | 10.10.1.1 | 10.10.1.2 | 10.10.1.0/24, fd00:10:1::/64 |
| eth2 ⇄ SR Linux e1-1 | 10.10.2.1 | 10.10.2.2 | 10.10.2.0/24, fd00:10:2::/64 |
| eth3 ⇄ vJunos ge-0/0/0 | 10.10.3.1 | 10.10.3.2 | 10.10.3.0/24, fd00:10:3::/64 |

### Start

```sh
make docker IMAGE=gotwamp:dev
docker build -f deploy/containerlab/Dockerfile.lab -t gotwamp-lab:dev deploy/containerlab
sudo containerlab deploy -t deploy/containerlab/gotwamp-interop.clab.yml
# ... tests ...
sudo containerlab destroy -t deploy/containerlab/gotwamp-interop.clab.yml
```

- The gotwamp node uses a lab image (`Dockerfile.lab`) that puts the release image binaries on Debian. This is because containerlab's `exec` needs the `ip` command to assign addresses (the release image is distroless and has no `ip`).
- Images can be overridden with environment variables: `GOTWAMP_LAB_IMAGE`, `PERFSONAR_IMAGE`, `SRL_IMAGE`, `SRL_TYPE`, `VJUNOS_IMAGE`. Remove the nodes (together with their links) for images you cannot obtain.
- Fault injection: `sudo containerlab tools netem set -n clab-gotwamp-interop-gotwamp -i eth1 --delay 10ms --loss 1`

### Peer configuration and tests

| Peer | Configuration file | Tests (run on the gotwamp node) |
|------|-------------|-----------------------------|
| perfSONAR | `test/interop/perfsonar/conf/` (twampd) | `docker exec clab-gotwamp-interop-gotwamp gotwamp ping 10.10.1.2`, `docker exec clab-gotwamp-interop-perfsonar twping -M 10.10.1.1` |
| SR Linux | `configs/srl.cli` (applied as startup-config): TWAMP Full server, STAMP Session-Reflector (UDP 862), OAM-PM IP STAMP sender → gotwampd | `gotwamp ping 10.10.2.2` (Full), `gotwamp ping --light 10.10.2.2` (STAMP reflector). On SR Linux, `info from state oam` |
| vJunos-router | `configs/vjunos.set` (loaded after boot with `load set terminal`; not used as startup-config so that the vrnetlab default login admin / admin@123 is kept): managed TWAMP server (with client-list), Light reflector, managed client (`test-count 0`) and Light client → gotwampd | `gotwamp ping 10.10.3.2` (Full, IPv4 only), `gotwamp ping --light 10.10.3.2`. On vJunos, `show services rpm twamp client probe-results` |

The gotwamp node configuration is `deploy/containerlab/configs/gotwampd.yaml`. Because Junos and SR Linux accept only 862 or 49152–65535 as the Receiver Port, `reflector.control.test_port_range` is set to `49152-57343`.

### Verified / not verified

- containerlab is not available in this development environment, so **the topology as a whole has not been run**. The YAML syntax and the gotwamp configuration (`gotwamp config validate`) have been checked.
- The gotwamp ⇄ perfSONAR segment was verified by reproducing the same addressing with docker networks. Between gotwampd from the lab image (`configs/gotwampd.yaml`) and twampd, both `gotwamp ping` (Full) → twampd and twping → gotwampd had 0 loss on IPv4 and IPv6.
- **SR Linux (ghcr.io/nokia/srlinux 26.7.2) limitation**: when started standalone in docker, the default model `ixr-d3l` and `ixr-d2l` have no `/oam` hierarchy at all (twamp / stamp also do not appear in `info from state system features`), so the TWAMP server / STAMP reflector could not be configured (the container does ship the `twamp_mgr` app and the YANG models). The service provider models (`ixr-x1b`, `sxr-1d-32d`) did not finish booting without containerlab, so the topology defaults to `type: ixr-x1b`, but whether the feature is enabled must be checked on the first deployment. The paths under `/oam` in `configs/srl.cli` are also inferred from the YANG models and not verified.
- There is no primary source on whether RPM TWAMP works on vJunos-router (research/vendor-juniper.md). The statements in `configs/vjunos.set` follow the documentation but are not verified.

### Verification status per device (SPEC §11.3)

| Item | Status | How verified / why not verified |
|------|------|-------------------------|
| Consistency of IPVN and Receiver Address with IPv4-mapped IPv6 (listening on `[::]` and connecting over IPv4) | **Verified** | Real-socket test `TestIPv4MappedControlConnection` (internal/control/server). Session addresses and SIDs are all plain IPv4. Requests whose IPVN differs from the control connection's family are rejected with Accept 3 (`TestRequestFamily`) |
| Reply source address when the Light reflector listens on a wildcard (`0.0.0.0` / `::`) | **Verified** | `TestLoopbackWildcardSource` (internal/reflector, to 127.0.0.1-3). Replies come from the destination address the packet was received on, and flows are also separated by that address |
| perfSONAR: requested port outside `testports` | **Verified** | `make interop` (alternate port accepted) |
| perfSONAR: authentication modes (authenticated / encrypted / mixed) | **Verified** | I-05 / I-06 in `make interop` (both directions, IPv4 / IPv6). Both via the package API and via the release binaries (`INTEROP_CLI=1`, run.sh) |
| SR Linux: feature flag, Full server `timeout-too-large` threshold, DSCP/TTL reflection by the STAMP reflector | Not verified | As described above, the default model has no `/oam`, and the service provider models require containerlab |
| Cisco OSes: Start Time=0 / real time, responses to Modes=2/4, multiple sessions on one connection, Sender Address=0 | Not verified | Images require a vendor account |
| Junos: how requests outside client-list are rejected, reflected DSCP/TTL, Modes in Server Greeting | Not verified | Behavior of RPM TWAMP on vJunos-router is unknown. containerlab topology is prepared |
| SONiC-VS: whether the image includes the twamp container | Not verified | Not started (no owner assigned) |

## Compatibility with STAMP (RFC 8762 / RFC 8972)

gotwamp supports STAMP in unauthenticated mode only, implemented as a switch on the TWAMP Light receive and send paths (M7-3). It has not been tested against a real device (SR Linux) yet; the following is based on the RFCs and the SR Linux documentation (research/vendor-others.md).

| Direction | gotwamp side | Expected behavior |
|------|-----------|--------------|
| SR Linux STAMP sender (OAM-PM IP, 24.7+) → gotwampd | Put the Light reflector in STAMP mode (`reflector.light.stamp: true`) | Replies in STAMP format to the 44-octet base packet. The SSID at offset 14–15 is returned unchanged, and 41–43 are set to MBZ. Anything beyond the base is returned unchanged. This includes padding and TLVs added by SR Linux `pad-size` (default 0, 3–9502) |
| gotwamp STAMP sender (test parameters `stamp: true`, `ssid`; CLI: `gotwamp ping --light --stamp [--ssid N]`) → SR Linux STAMP reflector | 44-octet base + SSID. Padding is sent as one Extra Padding TLV (with the U flag) | The SR Linux reflector listens on port 862 (default) or 64364–64373. A reply whose SSID is neither our own SSID nor 0 (SSID not supported) is counted as `foreign`, i.e. a reply for another session |
| TWAMP Light sender (including other vendors' devices) → gotwampd (STAMP mode) | Accepted as is | The reply places the same fields at the same positions as TWAMP, so a TWAMP sender can read it. However, the reply length is max(received length, 44), which is longer than TWAMP's 41 |

- **TLV handling (RFC 8972 §4)**: TLVs are only parsed; none are implemented. Each TLV is returned with the U (Unrecognized) flag set. A TLV whose length exceeds the packet gets the M (Malformed) flag, and processing stops there.
- **Distinguishing TLVs from TWAMP padding**: the data is treated as TLVs only when the octet right after the base is `100xxxxx` (only the U flag set by the sender); otherwise it is treated as TWAMP Light padding and returned unchanged. The SR Linux reflector uses the same rule. gotwamp's Light sender (when not in STAMP mode) uses zero padding by default, so it is not mistaken for a TLV.
- **Not supported**:
  - STAMP authenticated mode (HMAC-SHA-256, the TLV I flag).
  - Processing of TLVs such as Class of Service.
  - Session identification by SSID on the reflector side (gotwampd separates flows by 4-tuple).
- **Tests**:
  - wire golden files (`stamp-sender.hex` / `stamp-reflector.hex`).
  - Reflector STAMP round trip (`TestLightSTAMP`), fuzz (`FuzzLightSTAMPReply`).
  - Sender and reflector loopback (`TestSTAMPLoopback`).

## Known behavioral differences and caveats

- perfSONAR does not implement TWAMP Light, so Light interoperability cannot be verified by this suite (planned with SR Linux STAMP, Junos, and Cisco PM).
- perfSONAR also does not implement RFC 5938 (Individual Session Control), RFC 6038 (Reflect Octets / Symmetrical Size), or STAMP. twampd does not offer these Modes bits, so even if gotwamp's `extensions` is set, measurement proceeds as plain RFC 5357. These extensions are verified by gotwamp-to-gotwamp integration tests (`TestRunFullExtensions`, `TestSTAMPLoopback`).
- twampd returns an alternate port when the requested Receiver Port is outside the `testports` range. gotwamp accepts it by default (`accept_alternate_port: true`).
- **twping clock and Start Time**:
  - twping (perfSONAR's owamp library) adds the NTP offset the kernel is still slewing (adjtimex `offset`) to its own time. As a result, T1 and the Start Time in the Request can be a few ms ahead of the system clock (5 ms on this host).
  - twping's `-i` is an exponential distribution with the given mean interval, so the first packet is often sent a few ms after the Start Time.
  - Reflectors that discard packets arriving before the Start Time (SR Linux `enforce-test-session-start-time`, older gotwampd) lose the first one or two packets.
  - gotwampd's Full reflector ignores the Start Time by default and reflects all packets after Start-Sessions. The compatibility setting to enforce the Start Time is `reflector.FullConfig.EnforceStartTime` (planned to be wired as the configuration key `reflector.control.enforce_start_time`, default false).
  - Previously, in I-02 / I-06, one to three cases per run occasionally lost one or two packets.
  - The tests judge twping's loss using the reflector's counters. If gotwampd received a packet but did not reflect it, the test fails. Only when gotwampd reflected everything but twping still lost packets (a delay on the twping side) is the case retried once.
- twping picks the Sender Port from 8760–9960 and requests the same number as the Receiver Port. If that port is in use on the gotwampd host, gotwampd returns an alternate port from `test_port_range` (default 20000–29999), and twping accepts it.
- twping's `-D` takes a DSCP value (0–63). gotwampd replies with the Type-P DSCP (RFC 5357 §3.5).
- Because the container uses host network, 862/tcp and 18760–19960/udp must be free on the host.

## Troubleshooting

- `perfSONAR container ... is not running`: use `make interop`, or start the container with compose before running `go test`.
- `docker compose up --wait` does not finish: the healthcheck tests twampd itself with `twping -c 1`. Check `docker logs gotwamp-perfsonar` for twampd errors (port conflicts, etc.).
- Port 862 is in use: change it, for example `PERFSONAR_TWAMPD_LISTEN='[::]:8620' PERFSONAR_TWAMPD_PORT=8620 make interop`.
