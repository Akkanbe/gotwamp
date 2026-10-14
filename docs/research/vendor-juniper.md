> AI research notes (2026-10-03). Extracted from Juniper's official documentation (Junos OS / Junos OS Evolved / cRPD / vJunos / containerlab). UNVERIFIED items are marked as such.

# Juniper (Junos OS / Junos OS Evolved / cRPD) TWAMP & TWAMP Light — findings for gotwamp design and interop plan

Legend: facts are tagged with the fetched source; full URLs are in the final list. `UNVERIFIED` = not confirmed from a fetched primary page (search snippet, inference, or page not fetchable). Tool note: all Juniper CLI-reference pages were pulled raw (curl) so syntax/defaults below are verbatim; KB articles and GEANT PDFs could not be fetched (JS/bot walls).

## 0. TL;DR for gotwamp

- Junos TWAMP (Junos OS, `[edit services rpm twamp]`) = full RFC 5357 managed TWAMP client+server since 15.1 (server since 9.3), plus TWAMP Light client+server since 21.1R1. Junos OS Evolved uses `[edit services monitoring twamp]`, `server { managed | light }`, **unauthenticated mode only** [OV][TWAMP-STMT].
- Test against a Junos **client**: expect Modes=unauthenticated, Request-TW-Session with Sender Port == Receiver Port (doc samples show 10008/10008, 10010/10010, 10011/10011) [EX-MX][EX-SRX], control TCP keepalives (idle 120 s / 5 s / 6 probes) [TCPKEEP*], client reconnects every `test-interval` (1–255 s, default 1) after any failure, runs forever when `test-count 0` (default) [AUTORESTART][RECONNECT], padding 60–1400 bytes pseudo-random unless `data-fill-with-zeros` [DSIZE][DFILL], IP TTL 64 (255 before 18.2R1) [TTL].
- Test against a Junos **server**: `client-list` (CIDR allow-list) is mandatory on Junos OS ("You must configure at least one client address to enable TWAMP") [CLIST][CFG]; `authentication-mode none` required before 21.3R1 (default `none` since 21.3R1) [CFG]; control TCP 862 (1–65535) [PORT]; light reflector `set services rpm twamp server light [port 862–65535]` (UDP 862 default) [LIGHT].
- Best free lab targets: vJunos-router (vMX-based) is the only free image with primary-source evidence that TWAMP client+server work without service PICs (vMX listed in Feature Explorer for TWAMP Light 21.1R1 [FE-6317]; Juniper's own SRX/TWAMP example "Updated and revalidated using vMX on Junos OS Release 22.2R1" on plain `ge-` interfaces [EX-SRX]); vJunosEvolved (PTX10001-36MR) should expose `services monitoring twamp` (PTX10001-36MR listed from 21.1R1-EVO [FE-6289]); cRPD standalone: TWAMP is only documented inside JCNR ≥24.2 — `UNVERIFIED` for standalone cRPD. Details in §1.

## 1. PRIORITY: free lab images, what is (not) documented, containerlab kinds

| Image | Base platform / notes (source) | TWAMP evidence | Verdict |
|---|---|---|---|
| **vJunos-router** | "built using vMX as a reference Juniper router"; no bandwidth license; lab only; ≤96 interfaces; 100 Mbps aggregate; single RE/FPC; no ISSU, no SR-IOV, no nested-VM use [VJR] | vJunos docs (overview page + KVM deployment PDF + 23.2R1 release notes) contain **no mention of RPM/TWAMP/si-/inline-services** either way [VJR][VJR-PDF]. vMX itself: TWAMP Light (client & server) IPv4 from Junos OS 21.1R1 [FE-6317]; managed TWAMP client+server validated on vMX 22.2R1 with only `ge-0/0/0` + `lo0`, no si-/MS- interface [EX-SRX]; Paragon/Netrounds verified Junos TWAMP client on "vMX, MX204, MX480, MX960", Junos 18.3–20.2 [NETROUNDS] | Managed + Light client/server expected to work (software/PFE-microkernel timestamps). `UNVERIFIED` on vJunos-router itself. Containerlab kind `juniper_vjunosrouter`, vrnetlab build, admin/admin@123, `ge-0/0/X` (eth1=ge-0/0/0), 5–10 min boot [CL-VJR] |
| **vJunos-switch** | "built using EX9214 as a reference Juniper switch"; ≤96 interfaces; 100 Mbps; unsupported: ISSU, hot interface attach/detach, SR-IOV, nested, multicast [VJS] | `twamp` statement documented for "EX9200 Series switches" (and EX4300, QFX10000) [TWAMP-STMT]. No TWAMP mention in vJunos-switch docs [VJS] | CLI likely present; `UNVERIFIED`. Kind `juniper_vjunosswitch`, ~15 min boot [CL-VJS] |
| **vJunosEvolved** | "built using PTX10001-36MR as a reference"; no licenses; 12 ports + 72 channelized; **2 Kpps / 3–5 Mbps aggregate**; no ISSU/SR-IOV; unsupported list has no TWAMP entry [VJE] | Evolved TWAMP: PTX10003 20.3R1, rest of PTX10000 21.1R1 [TWAMP-STMT]; PTX10001-36MR listed from 21.1R1 [FE-6289] and STAMP 23.4R1 [FE-8836] | `services monitoring twamp` CLI expected; whether `pfe-timestamp` works on the virtual PFE is `UNVERIFIED` — plan to test `offload-type none` (RE timestamps). Kind `juniper_vjunosevolved`, defaults to PTX10001-36MR, `et-0/0/X`, 12 data ports, ~15 min boot [CL-VJE] |
| **cRPD** | Standalone "Features Supported on cRPD" page: BGP/OSPF/IS-IS/MPLS/SR/EVPN… **no RPM, no TWAMP, no rmopd** [CRPD-FEAT]; 21.4R1 cRPD release notes: no TWAMP [CRPD-RN-21.4] | JCNR 24.2: "TWAMP Support: JCNR now supports managed and light TWAMP" [JCNR-RN]; configured in the cRPD CLI as `set services rpm twamp ...` with the same Junos statements/defaults; "cRPD timestamps are enabled by default… higher jitter due to latency in the host network stack"; "vRouter timestamping is supported for TWAMP light mode only" [JCNR] | TWAMP code exists in the cRPD build shipped with JCNR ≥24.2; standalone cRPD support `UNVERIFIED` — try `set services rpm twamp ?` on cRPD ≥24.2. Kind `juniper_crpd`, root/clab123, license file needed for some features [CL-CRPD] |
| vMX (licensed) | — | as above [FE-6317][EX-SRX][NETROUNDS] | Kind `juniper_vmx` (kernel 4.17+, ~7 min boot, ≤17 interfaces) [CL-VMX] |
| vSRX | SRX300/320/340/345/550M/1500/4100/4200 and vSRX: "TWAMP for IPv6 is not supported. TWAMP server and TWAMP client authentication are not supported. TWAMP Light is not supported." [OV] | client+server from 18.1R1, needs `host-inbound-traffic system-services` [EX-SRX] | Managed IPv4 only |

## 2. Roles per platform family (official)

- Elements: Control-Client, Server, Session-Sender, Session-Reflector; Junos packages Control-Client+Session-Sender as "TWAMP client" and Server+Session-Reflector as "TWAMP server" [OV]. Client (15.1) "capable of working with a third-party server implementation" [EX-MX][OV].
- MX (Junos OS): client+server; "TWAMP is not supported when you enable Next Gen Services" [OV]. Table 3: managed client/server **IPv4 Y, IPv6 N** on MX Junos OS; Table 4: Light client/server IPv4 and IPv6 Y [OV].
- PTX (Junos OS): client+server; "destination interface si-x/y/z… is not supported on PTX Series routers for TWAMP client configurations" [OV]; PTX1000/5000/10008/10016 Light 21.1R1 [FE-6317].
- ACX (Junos OS): "ACX710 and ACX5448… support both reflection and generation. Other ACX Series routers running Junos OS support only reflection" [OV]. ACX (Evolved, ACX7k): reflection and generation [OV]; ACX7024/7100/7509… [FE-6289][FE-8836].
- EX/QFX: statement platforms "MX Series routers, ACX routers, EX4300 Switches, PTX Series routers, EX9200 Series switches, and QFX10000 Series switches" [TWAMP-STMT]; EX4100/EX4400/EX4650 "fully support RPM and TWAMP" from 24.2R1 incl. IPv4/IPv6 managed and Light [RN-24.2-EX]; QFX5000 (Evolved) [OV].
- SRX/vSRX: see table above [OV][EX-SRX]; SRX also has a transit **TWAMP ALG** (18.2R1) that "extracts the IP addresses and the port details" from control messages, opens pinholes/NAT-translates; "By default, the ALG TWAMP is disabled"; 4-second session-establishment timeout [ALG].
- Light sender/reflector: MX, PTX (Junos OS 21.1R1) [TWAMP-STMT][FE-6317]; Evolved light server from 20.3R1 [LIGHT]; EX4100/4400/4650 24.2R1 [RN-24.2-EX]; not SRX [OV].

## 3. Modes actually supported

- `authentication-mode (authenticated | encrypted | none)` at `[edit services rpm twamp server]` and `[... client control-connection <name>]`; "authenticated — Note: This mode is supported only on TWAMP servers"; "encrypted — supported only on TWAMP servers"; "none — supported on both TWAMP servers and clients" (9.5; client hierarchy 15.1) [AUTH]. The newer syntax tree shows `authentication-mode <authenticated> <control-only-encrypted> <encrypted> <none>` and `authentication-key-chain name { key-id name { secret secret; } }` [TWAMP-STMT]; key "secret… 1 through 8 ASCII characters"; "you cannot commit the 0.0.0.0/allow statement with authentication keys or key chains" [KEYCHAIN]. `control-only-encrypted` semantics: `UNVERIFIED` (no doc page).
- "Starting in Junos OS Release 21.3R1, you no longer need to configure the authentication-mode statement. The default mode is now none" [CFG]. Pre-21.3R1 it was required with no default [LEGACY-12.2][AUTH].
- ACX: "do not support authentication and encryption modes… must be set to none" [CFG]. SRX/vSRX: no authentication [OV]. Junos OS Evolved: "Unauthenticated mode only" [OV]. Feature Explorer "TWAMP Authentication Mode" lists only Evolved ACX7k/PTX10k platforms (21.1R1+) [FE-6289] (contradiction with the Evolved "unauthenticated only" text — treat auth on Evolved as `UNVERIFIED`).
- Practical: a gotwamp Control-Client should always offer/select unauthenticated; which Modes bits a Junos server advertises in Server-Greeting is `UNVERIFIED` — capture it in the lab.

## 4. Control port
- Server `port number` — "TWAMP server listening port. Default: 862 Range: 1 through 65,535" (Junos OS 9.3; Evolved 20.3R1) [PORT]. Evolved managed: "You can specify port 862, or any port from 49152 through 65535" [MANAGED-EVO].
- Client `destination-port` at control-connection: "Port number 7 or from 862 through 65,535. Default: 862"; required at control-connection level for managed, not allowed there for light [DPORT]; Evolved client: 862 or 49152–65535 [CC-EVO].
- `routing-instance-list name { port port; }` (server, 15.1): per-RI listener, up to 100 RIs, port 1–65535 [RIL]. ACX quirk: "TWAMP control connection traffic always arrives on ACX routers with the listening port set as 862… probes that arrive with a different port number are not recognized… dropped" [CFG][OV].
- "Legacy port 2000": not found in any Juniper doc (`UNVERIFIED`/not applicable).

## 5. Test (UDP) port
- Managed: negotiated; Junos OS does not let you set `destination-port` per test-session in managed mode [DPORT]. Doc samples show client Sender port == Reflector port (10008/10008 [EX-MX]; 10010/10010 and probe-results "Reflector port: 10011… sender-port: 10011" [EX-SRX]).
- Light: `set services rpm twamp server light port <n>` — "UDP port that reflects the TWAMP test packets. Range: 862 through 65535. Default: 862" [LIGHT]; Junos OS min config quoted as `server { authentication-mode none; light; port (862 | 878 | 51000); }` [CFG]. Light client must set `destination-port` on **each test-session** ("7 or 862–65535" Junos OS [DPORT]; "862, or 49152 through 65535" Evolved [TS-EVO]). JCNR's "light minimum" example omits it (defaults 862) [JCNR] — discrepancy to test.

## 6. IPv4/IPv6
- Junos OS MX: managed TWAMP IPv6 **not supported** (Table 3) [OV]; Light IPv6 targets from 21.3R1 (`target-address` + `destination-port` both required) [CFG]; link-local targets 21.4R1 with `target-address X local-link <ifl>` and `source-address` [CFG][TARGET][SRCADDR]; Feature Explorer "TWAMP Light IPv6 client and server" = MX240…MX10016, 22.3R1 [FE-6320]. Netrounds (18.3–20.2): "For the communication between TWAMP sender/client and reflector, Junos currently only supports IPv4" [NETROUNDS].
- Evolved: IPv4+IPv6 for client-list, control and test sessions from 21.4R1 (non link-local); link-local light targets 22.3R1; IPv6 PFE timestamps 22.3R1 (before: `offload-type none`) [OV][CFG][CLIST].
- EX4100/4400/4650 (24.2R1): IPv4 and IPv6 incl. link-local for control and test sessions [RN-24.2-EX]. SRX: IPv4 only [OV].

## 7. Max sessions / connections (defaults, ranges)
Junos OS `[edit services rpm twamp server]`:
- `maximum-connections count` — Default 64, Range 1–1000 [MAXCONN] (12.2 doc said 1–2048 [LEGACY-12.2]).
- `maximum-connections-per-client count` — Default 64, Range 1–500 [MAXCONN-CLI].
- `maximum-sessions count` — Default 64, Range 1–2048 [MAXSESS].
- `maximum-sessions-per-connection count` — Default 64, Range 1–1024 [MAXSESS-CONN].
- `client-list`: "Each list can contain up to 64 entries", multiple lists allowed [CLIST].
- Hardware: "1000 maximum probes" for MX si-interface / delegate-probes managed modes; `inline-timestamping` ≤256 test sessions (ACX/Evolved) [OV][LIGHT].
Evolved `server managed`: `client-limit 0–1000` (default 0 = no limit), `control-per-client-limit 0–1000` (default 0), `test-per-client-limit 0–1000` (default 0) [MANAGED-EVO].
JCNR/cRPD table repeats Junos defaults (64/64/64/64) [JCNR].

## 8. Timeouts (SERVWAIT/REFWAIT analogues)
- `server-inactivity-timeout minutes` — "maximum time the TWAMP server has to finish the TWAMP control protocol negotiation… Configure 0 to disable. Default: 15 minutes Range: 1 through 30 minutes" (11.1) [SRVINACT] (≈ SERVWAIT for control setup; note the syntax tree in [SRV] says "seconds" but the statement page says minutes).
- `inactivity-timeout seconds` — "Length of time the session is inactive before it times out. Default: 1800 seconds" (9.3) [INACT]; 12.2 doc: range 0–3600 [LEGACY-12.2] (≈ REFWAIT; absent from the current syntax tree but the statement page still exists).
- `max-connection-duration hours` — "maximum time a connection can exist between a client and the server. Default: 24 Range: 0-120" [MAXDUR]; but Auto-Restart page: "Starting in Junos OS Release 19.1R1, the default value of test-count at the TWAMP client and max-connection-duration at the TWAMP server is 0" and auto-reconnect requires both 0 [AUTORESTART] — conflicting defaults; verify on the box. (No `max-session-duration` statement exists — both URL guesses 404; Evolved has `control-maximum-duration`.)
- TCP keepalives (client and server, 19.1): `tcp-keepcnt 1–50` default 6; `tcp-keepidle 1–600 s` default 120; `tcp-keepintvl 1–600 s` default 5 [TCPKEEPCNT][TCPKEEPIDLE][TCPKEEPINTVL].
- Evolved: `control-inactivity-timeout 0–86400 s` default 900; `control-maximum-duration 0–86400 s` default 0 (no limit) [MANAGED-EVO].
- Client: "At each test-interval, the TWAMP client continues to send the control packets to re-establish connectivity… statistics will be maintained" [RECONNECT]; `request services rpm twamp (start|stop) client [name]` — stop: "the control connection is closed only after the Stop-sessions message is sent" [REQ].

## 9. DSCP/ToS
- Client: `dscp-code-points dscp-bits` per test-session, 6-bit pattern or aliases af11…nc2 [DSCP]; JCNR default `000000` [JCNR]. Reflector DSCP copy/echo behaviour: **not documented in any fetched page — `UNVERIFIED`** (test: send EF, check reflected DSCP).

## 10. Packet size / padding
- `data-size`: "for TWAMP the value is from 60 through 1400. Default: … 60 for TWAMP" (Junos OS) [DSIZE]; Evolved test-session `data-size 0 through 65400, Default 0` [TS-EVO]; JCNR 60–1400 default 60 [JCNR]. `data-fill-with-zeros`: "contents of the test packet are zeros, if the statement is not configured, then the data content is a pseudo-random number" [DFILL].
- Reflector symmetry: Evolved — "In previous releases, we supported symmetric payloads and stateless reflection"; 23.4R1-EVO STAMP "ensures symmetric payload size (in accordance with RFC 6038)" [CFG]. Junos OS reflector padding/truncation rule: `UNVERIFIED`.

## 11. TTL
- `ttl hop-count` (test-session): "Prior to Junos OS Release 18.2R1… For TWAMP clients, the TTL was 255 sent in the IPv4 header. In 18.2R1 and later, you can specify… Range: 1 through 255… Default: 64" [TTL]; Evolved same range/default [TS-EVO]. Reflector handling of the Sender TTL field / reflected IP TTL: `UNVERIFIED`.

## 12. Timestamp source
- Junos OS: "By default for most platforms, timestamps are set in the Packet Forwarding Engine host processor" (microkernel); inline hardware (LU) timestamping requires `si-`/`sp-` interfaces before 25.4R1, or `offload-type inline-timestamp` on supporting MX line cards from 25.4R1; "If si- interfaces are configured on an MX router for TWAMP, then this option is the default" [OV]. "For microkernel-based timestamping in Junos OS, you don't need to configure an si- interface. In this case, the TWAMP connection and sessions are established based on the target address and route" [CFG]. MX Table 2: TWAMP server "Yes (No responder configuration needed)" with PFE microkernel timestamps; MS-MIC/MS-MPC: TWAMP client No, server No; MS-PIC/MS-DPC: server Yes; RE timestamp: No for TWAMP [OV]. Legacy 12.2: MX without MS-DPC "can configure the twamp-server statement on any interface (for example, ge-1/0/1.10)… you do need to include it… to activate the TWAMP reflector" [LEGACY-12.2] (obsolete; modern docs say no responder config needed).
- `offload-type (none | inline-timestamp | pfe-timestamp)`: none = RE, pfe-timestamp = PFE, inline = hardware; Evolved default `pfe-timestamp` (servers since 23.4R1); IPv6 on Evolved needed `none` before 22.3R1; ACX inline: server or client, not both; not on PM50 ports with FEC74; ≤256 sessions [LIGHT][MANAGED-EVO][TS-EVO]. Junos OS `offload-type` added 25.4R1 (server, light, test-session) [SRV][TS].
- si- interface rules: `set chassis fpc X pic Y inline-services bandwidth (1g|10g)`; `set interfaces si-X/Y/Z unit N rpm (twamp-server|twamp-client)` + `family inet address /32`; "You cannot configure the TWAMP server/client on unit 0 of a services interface"; "The TWAMP client is applicable only for si- interfaces" [CFG][EX-MX][RPM-IF][DIF].
- PTP/NTP requirement: not stated in fetched docs; KB32466 snippet says egress=(T2−T1), ingress=(T4−T3) computed from different clocks (`UNVERIFIED`).

## 13. Key config snippets (verbatim from docs)
Junos OS server (MX inline example) [EX-MX]:
```
set chassis fpc 2 pic 1 inline-services bandwidth 1g
set interfaces si-2/1/0 unit 0 family inet
set interfaces si-2/1/0 unit 10 rpm twamp-server
set interfaces si-2/1/0 unit 10 family inet address 10.70.70.1/32
set services rpm twamp server authentication-mode none
set services rpm twamp server port 862
set services rpm twamp server client-list Client1 address 10.60.60.1/32
```
Minimal software server (SRX/vMX-validated) [EX-SRX]: `set services rpm twamp server authentication-mode none` / `set services rpm twamp server client-list client1 address 10.0.12.1/24` (+ `host-inbound-traffic system-services all` on SRX).
Junos OS client [EX-MX]: `set services rpm twamp client control-connection c1 target-address 10.70.70.1`, `... c1 test-count 1`, `... c1 test-interval 1`, `... c1 history-size 500`, `... c1 test-session t1 target-address 10.70.70.1`, `... t1 data-fill-with-zeros`, `... t1 data-size 1400`, `... t1 probe-count 55`, `... t1 probe-interval 1` (optional `destination-interface si-4/1/0.10` with `rpm twamp-client`).
Light (Junos OS/cRPD) [JCNR][LIGHT][CFG]: server `set services rpm twamp server light` (+ `port`); client `set services rpm twamp client control-connection L1 control-type light` / `... L1 test-session t1 target-address 21.21.21.29` (+ `destination-port`, `dscp-code-points`, `data-size`, `ttl`, `source-address`).
Evolved [CFG][SRV-EVO][CC-EVO]: `set services monitoring twamp server managed` (or `light`), optional `client-list`, `port`, `offload-type`, `stateful-sequence`; client `set services monitoring twamp client control-connection c1 control-type (managed|light) target <addr> test-start (auto|manual) test-session t1 target <addr> destination-port <p>`; manual start via `request services monitoring twamp client start`.
Full syntax trees (Junos OS client/server and Evolved) are on [TWAMP-STMT], [CLIENT], [SRV], [SRV-EVO].

## 14. Result fields (show commands)
- `show services rpm twamp client probe-results [control-connection n] [test-session n]` (15.1): Owner, Test, server-address, server-port, Client address, Client port, TWAMP-Server-Status (`Light` | `Connected` | `Not Connected`), Number-of-Retries-with-TWAMP-Server, Reflector address/port, Sender address/Sender-port, Local-link (21.4R1), Routing Instance Name, Probe type, Test size, Destination Interface Name, Probe results (Response received, hardware timestamps if configured, Rtt, Egress/Ingress/Round trip jitter, Egress/Ingress/Round-trip interarrival jitter), Results over current/last/all tests (Probes sent/received, Loss percentage, Test completed, Measurement = round-trip time, positive/negative round-trip/egress/ingress jitter, egress/ingress time; Samples/Minimum/Maximum/Average/Peak to peak/Stddev/Sum) [SH-PR]. Sample: "Rtt: 718 usec, Ingress time: 134 usec, Egress time: 584 usec…" [EX-SRX].
- `show services rpm twamp client history-results [brief|detail] [since t]` (last 50 probes) [SH-HIST]; `show services rpm twamp client session` → Connection Name, Session Name, Sender address/port, Reflector address/port [SH-CS].
- Server: `show services rpm twamp server connection [id]` → Connection ID, Client address/port, Server address/port, Session count, Auth mode; "Because TWAMP light servers are stateless, information about them is not included" [SH-SC]. `show services rpm twamp server session [id]` → Session ID, Connection ID, Sender address/port, Reflector address/port [SH-SS]; newer sample adds "Session state … Auth mode" ("Active Unauthenticated") [EX-SRX].
- Evolved: `show services monitoring twamp server control-info` (Control identifier, Client/Server address:port, Active tests, Control status sleeping|testing, Auth mode) [SH-EVO-CI]; `server test-info` (Test status active|idle) [SH-EVO-STI]; `client test-info` [SH-EVO-CTI]; `client probe-results [status PASS|FAIL] [source-address] [target]` incl. "Egress loss inferred" when `stateful-sequence` [SH-EVO-PR]; `client history-results control-connection <n>` [SH-EVO-HIST].
- SNMP: jnxTwampClientTestSessionsTable; traps via `traps` (control-connection-closed, test-iteration-done, rtt/jitter/std-dev exceeded, probe-failure, test-completion/failure) [TRAPS][RN-22.4-MX].

## 15. Interop caveats / PRs / third-party
- Client allow-list: Junos OS server only starts with a `client-list`; behaviour toward a non-listed client (TCP accept-then-close vs. refuse) is not documented — `UNVERIFIED`, add to test plan. Evolved: `client-list` optional (min config `server { (managed | light); }`) and the server "listens in on all routing instances" [CFG][SRV-EVO]. Forbidden client-list/source addresses: 0.0.0.0, 127/8, 224/4, 255.255.255.255 (+IPv6 list) [TWAMP-STMT][SRCADDR].
- Multiple sessions per connection are supported (`maximum-sessions-per-connection` default 64) [MAXSESS-CONN]; Junos client configs show several `test-session`s under one control-connection [RECONNECT].
- PR1556829: "The ACX5448 or ACX710 router as the TWAMP server delays the start session acknowledgment by 10 seconds" (resolved 21.2R1) [RN-21.2-ACX] → gotwamp sender must tolerate slow Start-Ack.
- PR1650997: rmopd crash deactivating all TWAMP client control connections while walking jnxTwampClientTestSessionsTable; PR1685952: "Probes received counter is not correct when set moving-average-size > history-size" (both resolved 22.4R1) [RN-22.4-MX].
- `UNVERIFIED` (GEANT/perfSONAR slides, PDFs behind bot wall; from search snippets): twping failed against Junos MX80 until perfSONAR zeroed the unused IPv6 bytes of the Sender/Receiver Address fields for IPv4 in Request-TW-Session; Junos does not run the TWAMP server on fxp0 (management) — use a data interface. Recommendation: gotwamp sender must zero bytes 4–15 of both address fields; gotwamp reflector should be lenient.
- `UNVERIFIED` KB titles (supportportal is JS-only): KB36524 "How to configure Twamp server on ACX with MX client" (ACX = reflector only); KB32466 "Some TWAMP OIDs are missing" (egress/ingress OIDs need synchronized clocks).
- "We recommend that you do not configure the RPM client and a TWAMP server on the same device" [OV]. Light servers invisible in server show commands [LIGHT]. Standards page: RFC 5357 incl. Appendix I supported on Junos OS and Evolved; RFC 8762 STAMP Evolved only; RFC 2925, 2544 [STD].
- SRX in path: TWAMP ALG disabled by default; "requires that the sender address, receiver address, and the header destination IP from the same interface"; failover requires re-initiating control connections [ALG].
- Thresholds (Junos OS): rtt, max-rtt, egress/ingress-time, jitter-egress/ingress/rtt, std-dev-*, successive-loss, total-loss (microseconds) [TWAMP-STMT]; Evolved thresholds: successive-loss/total-loss 0–15 default 1, threshold-type microseconds|average, 0–60,000,000 µs, control-failure on|off default on [THRESH-EVO].
- Range discrepancies to expect on real boxes: history-size statement says 0–255 default 50 [HIST] while official examples use `history-size 500` [EX-MX][RECONNECT] and JCNR says 0–512 [JCNR]; moving-average-size 0–255 [MAVG] vs 0–512 (Evolved/JCNR) [TS-EVO][JCNR]; probe-count TWAMP 1–4,294,967,290 (Junos OS) [PCOUNT] vs 1–255 and < history-size (Evolved) [TS-EVO]; test-interval 1–255 (Junos OS) [TINT] vs 1–86400 (Evolved) [CC-EVO].

## 16. Junos OS Evolved differences (summary)
Hierarchy `[edit services monitoring twamp]`; server `{ managed {...} light {...} }`; "Unauthenticated mode only"; IPv4/IPv6 control+test; stats/history, session status, generation/reception/reflection, syslog+SNMP only [OV]. Client: `control-type`, `test-start (auto|manual)`, `target` (not target-address), per-test-session history-size/moving-average-size/ttl/thresholds/offload-type [CC-EVO][TS-EVO]. Extras not in Junos OS: STAMP (RFC 8762) with `stateful-sequence` (light cache 1000–1,000,000 entries, default 10,000 per summary) and "Egress loss inferred" (23.4R1), static route tracking (24.4R1), SR-MPLS/SRv6 `source-routing` (24.4R1-EVO / 25.4R1 Junos OS) [OV][CFG][LIGHT]. ACX Evolved: CFM+TWAMP coexistence only from 23.4R1 [OV].

## 17. Suggested interop matrix for gotwamp (derived)
1. gotwamp reflector ← vJunos-router client (managed, IPv4, data-size 60/1400, zeros vs random, test-count 0 auto-restart, keepalives, Stop-Sessions on `request ... stop`). 2. gotwamp sender → vJunos-router server (client-list allow/deny, auth none, port 862 and non-862, several sessions per connection, >64 sessions to hit limits, server-inactivity-timeout 15 min, inactivity-timeout 1800 s, SERVWAIT tests). 3. Light both ways on UDP 862 and 49152+ (vJunos-router 21.1R1+, Evolved). 4. vJunosEvolved with `offload-type none` and `stateful-sequence` (check STAMP-style symmetric padding and sequence handling). 5. cRPD ≥24.2 if `services rpm twamp` is present. 6. Negative tests: non-zero address padding, IPv6 to Junos OS managed (expect failure), DSCP echo, TTL echo, Receiver Port != Sender Port.

## 18. URLs fetched (what each confirmed)
- [OV] https://www.juniper.net/documentation/us/en/software/junos/flow-monitoring/topics/concept/twamp-overview.html — roles, platform table, Evolved limits (unauthenticated only), MX timestamp tables, 1000-probe limit, Next Gen Services, ACX/SRX caveats.
- [CFG] https://www.juniper.net/documentation/us/en/software/junos/flow-monitoring/topics/task/rpm-twamp.html — minimum server/light configs, auth default none 21.3R1, client-list rules, si-/sp- requirements, unit 0 restriction, release history table.
- [TWAMP-STMT] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/twamp-edit-services.html — full Junos OS and Evolved syntax trees, platforms, release info.
- [SRV] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/server-edit-services.html — Junos OS server syntax; light 21.1R1; offload-type 25.4R1.
- [SRV-EVO] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/server-edit-services-evo.html — Evolved server syntax, ranges, "listens in on all routing instances".
- [MANAGED-EVO] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/managed-server-twamp-edit-services-evo.html — Evolved managed defaults (900 s, 0 = unlimited, port 862/49152–65535).
- [LIGHT] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/light-server-twamp-edit-services-evo.html — light statement (Junos OS 21.1R1 / Evolved 20.3R1), UDP port 862–65535, stateless, offload-type, stateful-sequence.
- [CLIENT] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/client-edit-services.html — client syntax trees, release history.
- [CC] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/control-connection-edit-services.html — Junos OS control-connection syntax.
- [CC-EVO] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/control-connection-edit-services-evo.html — control-type managed|light semantics, destination-port range, routing-instance note, test-start, test-interval 1–86400.
- [TS] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/test-session-edit-services.html — Junos OS test-session syntax; destination-port 21.1R1.
- [TS-EVO] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/test-session-edit-services-evo.html — Evolved test-session options/defaults (data-size 0–65400, probe-count 1–255, ttl 64, offload-type).
- [AUTH] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/authentication-mode-edit-services.html — modes; authenticated/encrypted server-only.
- [KEYCHAIN] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/authentication-key-chain-edit-services-rmp-twamp.html — key chain, 1–8 char secret, 0.0.0.0/allow restriction.
- [CLIST] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/client-list-edit-services.html — CIDR allow-list, 64 entries, required, IPv6 21.4R1-EVO.
- [PORT] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/port-edit-services-rpm-twamp.html — 862 default, 1–65535.
- [MAXCONN] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/maximum-connections-edit-services.html — 64 / 1–1000.
- [MAXCONN-CLI] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/maximum-connections-per-client-edit-services.html — 64 / 1–500.
- [MAXSESS] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/maximum-sessions-edit-services.html — 64 / 1–2048.
- [MAXSESS-CONN] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/maximum-sessions-per-connection-edit-services.html — 64 / 1–1024.
- [MAXDUR] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/maximum-connection-duration-edit-services.html — hours, 24 / 0–120.
- [SRVINACT] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/server-inactivity-timeout-edit-services.html — 15 min / 1–30, 0 disables.
- [INACT] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/inactivity-timeout-edit-services-rpm.html — session inactivity 1800 s.
- [RIL] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/routing-instance-list-edit-services-rpm-twamp.html — per-RI ports, 100 RIs.
- [TCPKEEPCNT] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/tcp-keepcnt.html ; [TCPKEEPIDLE] …/tcp-keepidle.html ; [TCPKEEPINTVL] …/tcp-keepintvl.html — 6 / 120 s / 5 s (19.1).
- [DPORT] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/destination-port-edit-services-rpm.html — 7 or 862–65535, managed vs light placement.
- [DIF] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/destination-interface-edit-services.html — si- timestamping interface rules.
- [DSIZE] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/data-size-edit-services.html — TWAMP 60–1400 default 60.
- [DFILL] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/data-fill-with-zeros-edit-services.html — zeros vs pseudo-random.
- [DSCP] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/dscp-code-point-edit-services.html — DSCP 6-bit/aliases.
- [PCOUNT] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/probe-count-edit-services.html — TWAMP 1–4,294,967,290.
- [PINT] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/probe-interval-edit-services.html — 1–255 s.
- [TINT] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/test-interval-edit-services.html — TWAMP 1–255 default 1.
- [TCOUNT] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/test-count.html — 0–4294967290 default 0.
- [HIST] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/history-size-edit-services.html — 0–255 default 50.
- [MAVG] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/moving-average-size-edit-services.html — 0–255.
- [PERSIST] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/persistent-results.html — 19.1.
- [SRCADDR] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/source-address-twamp.html — 21.4R1, forbidden addresses.
- [TARGET] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/target-edit-services-qfx-series.html — target-address, IPv6 light 21.3R1 (MX, PTX1000/3000/5000), local-link 21.4R1.
- [TTL] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/probe-ttl-edit-services.html — TTL 64 default, 255 pre-18.2R1.
- [THRESH-EVO] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/thresholds-edit-services-evo.html — Evolved thresholds.
- [TRAPS] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/traps-edit-services.html — trap list, light clients test-session traps only.
- [RPM-IF] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/rpm-edit-interfaces.html — `rpm twamp-client|twamp-server` on unit; client si- only.
- [EX-MX] https://www.juniper.net/documentation/us/en/software/junos/flow-monitoring/topics/example/configuring-twamp.html — MX si- example, sample show outputs.
- [EX-SRX] https://www.juniper.net/documentation/us/en/software/junos/flow-monitoring/network-mgmt/topics/example/configuring-twamp-srx.html — SRX 18.1R1 example "revalidated using vMX on 22.2R1", probe-results sample.
- [EX-LAG] https://www.juniper.net/documentation//us/en/software/junos/flow-monitoring/topics/example/example-twamp-test-lag-member-config.html — Evolved 24.4R1 light + source-routing example, Evolved show commands.
- [RECONNECT] https://www.juniper.net/documentation/us/en/software/junos/flow-monitoring/topics/task/twamp-client-server-configuring.html — reconnect behaviour, keepalive example, multi-session client config, retry counters.
- [AUTORESTART] https://www.juniper.net/documentation/us/en/software/junos/flow-monitoring/topics/concept/twamp_auto-restart.html — 19.1R1 auto-restart; test-count 0 / max-connection-duration 0.
- [STD] https://www.juniper.net/documentation/us/en/software/junos/standards/topics/concept/rpm.html — RFC 5357 (+Appendix I), RFC 8762 (Evolved), RFC 2925/2544.
- [SH-PR] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/command/show-services-twamp-client-probe-results.html — all output fields.
- [SH-HIST] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/command/show-services-rpm-twamp-client-history-results.html — history fields/sample.
- [SH-CS] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/command/show-services-rpm-twamp-client-session-command.html — client session fields.
- [SH-SC] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/command/show-services-rpm-twamp-server-connection-command.html — server connection fields; light excluded.
- [SH-SS] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/command/show-services-rpm-twamp-server-session-command.html — server session fields.
- [REQ] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/command/request-services-rpm-twamp.html — start/stop semantics.
- [SH-EVO-CI] https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/command/show-services-monitoring-twamp-server-control-info.html ; [SH-EVO-STI] …/show-services-monitoring-twamp-server-test-info.html ; [SH-EVO-CTI] …/show-services-monitoring-twamp-client-test-info.html ; [SH-EVO-PR] …/show-services-monitoring-twamp-client-probe-results.html ; [SH-EVO-HIST] …/show-services-monitoring-twamp-client-history-results.html — Evolved show commands and fields.
- [ALG] https://www.juniper.net/documentation/us/en/software/junos/alg/topics/topic-map/security-twamp-alg.html — SRX TWAMP ALG (18.2R1, disabled by default, 4 s timeout).
- [JCNR] https://www.juniper.net/documentation/us/en/software/cloud-native-router24.4/cloud-native-router-user/topics/concept/jcnr_twamp.html — cRPD-CLI TWAMP configs/default tables, vRouter vs cRPD timestamps.
- [JCNR-RN] https://www.juniper.net/documentation/us/en/software/cloud-native-router24.2/release-notes/24.2/jcnr-24.2-release-notes/new-features/jcnr-24.2-new-features.html — TWAMP added in JCNR 24.2.
- [CRPD-FEAT] https://www.juniper.net/documentation/us/en/software/crpd/crpd-deployment/topics/concept/crpd-supported-features.html — no RPM/TWAMP in standalone cRPD feature list.
- [CRPD-RN-21.4] https://www.juniper.net/documentation/us/en/software/junos/release-notes/21.4/junos-release-notes-21.4r1/topics/concept/crpd-release-notes.html — no TWAMP mention.
- [VJR] https://www.juniper.net/documentation/us/en/software/vjunos-router/vjunos-router-kvm/topics/vjunos-router-overview-understanding.html — vMX-based, limits, no TWAMP mention. [VJR-PDF] https://www.juniper.net/documentation/us/en/software/vjunos-router/vjunos-router-kvm/vjunos-router-kvm.pdf (+ vJunosEvolved/vJunos-switch/cRPD PDFs) — downloaded; text not extractable locally (CID fonts) → content `UNVERIFIED`.
- [VJS] https://www.juniper.net/documentation/us/en/software/vJunos/vjunos-switch-deployment-guide-for-kvm/vJunos-switch-kvm/topics/vJunos-switch-overview-understanding.html — EX9214-based, limits.
- [VJE] https://www.juniper.net/documentation/us/en/software/vJunosEvolved/vjunos-evolved-kvm/topics/vjunos-evolved-understand.html — PTX10001-36MR-based, 2 Kpps limit, unsupported list.
- [CL-VJR] https://containerlab.dev/manual/kinds/vr-vjunosrouter/ ; [CL-VJS] https://containerlab.dev/manual/kinds/vr-vjunosswitch/ ; [CL-VJE] https://containerlab.dev/manual/kinds/vr-vjunosevolved/ ; [CL-CRPD] https://containerlab.dev/manual/kinds/crpd/ ; [CL-VMX] https://containerlab.dev/manual/kinds/vr-vmx/ — kind names, credentials, interface naming, boot times.
- [FE-6317] https://apps.juniper.net/feature-explorer/feature/6317 — TWAMP Light IPv4: MX family + vMX + PTX1000/5000/10008/10016 from 21.1R1.
- [FE-6320] https://apps.juniper.net/feature-explorer/feature/6320 — TWAMP Light IPv6 (MX, 22.3R1).
- [FE-6289] https://apps.juniper.net/feature-explorer/feature/6289 — "TWAMP Authentication Mode" (Evolved ACX7k/PTX10k platforms incl. PTX10001-36MR 21.1R1).
- [FE-8836] https://apps.juniper.net/feature-explorer/feature/8836 — STAMP platforms (Evolved 23.4R1+).
- [FE-2509] https://apps.juniper.net/feature-explorer/feature/2509 — "RPM and TWAMP" entry (EX4100–4650 24.2R1, SRX1500–4300 22.1R1+, MX/PTX recent releases).
- [FE-390] https://apps.juniper.net/feature-explorer/feature/390?fn=OAM%3A+TWAMP — sub-feature list only (product table 404).
- [RN-21.2-ACX] https://www.juniper.net/documentation/us/en/software/junos/release-notes/21.2/junos-release-notes-21.2r1/topics/resolved-issues/acx-resolved-issues-21.2r1.html — PR1556829.
- [RN-22.4-MX] https://www.juniper.net/documentation/us/en/software/junos/release-notes/22.4/junos-release-notes-22.4r1/topics/open-issues/resolved-issues/mx-resolved-issues-cover.html — PR1650997, PR1685952.
- [RN-24.2-EX] https://www.juniper.net/documentation/us/en/software/junos/release-notes/24.2/junos-release-notes-24.2r1/topics/new-features/feature-descriptions/support-existing-features-12.html — EX4100/4400/4650 RPM+TWAMP incl. IPv6/Light.
- [LEGACY-12.2] https://www.juniper.net/documentation/en_US/junos12.2/topics/task/configuration/rpm-twamp.html — 12.2 server-only era: sp- `twamp-server`, "any interface" on MX without MS-DPC, inactivity-timeout 1800 (0–3600), mandatory port/auth.
- [NETROUNDS] https://app.netrounds.com/static/3.1/support/task-types/refl/twamp-junos.html — Paragon Active Assurance: Junos TWAMP client verified on vMX/MX204/MX480/MX960, Junos 18.3–20.2; "Junos currently only supports IPv4" sender↔reflector.
- [PS-66] https://github.com/perfsonar/owamp/issues/66 — twping "Invalid reflected sender sequence number" (vendor unspecified; not Juniper-specific).
- Not fetchable (`UNVERIFIED`, from search snippets only): KB36524, KB32466 (supportportal JS), GEANT "TWAMP measurements with perfSONAR" / "Zero-Footprint Monitoring" PDFs (bot wall), Juniper community thread (redirects to HPE community).