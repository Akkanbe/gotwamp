> AI research notes (2026-10-03). Extracted from Cisco's official documentation (IOS-XE / IOS-XR / NX-OS / XRd / XRv9000 / containerlab). UNVERIFIED items are marked as such.

# Cisco TWAMP / TWAMP Light implementation survey (for gotwamp design + interop test plan)

Scope: IOS-XE, IOS-XR (incl. XRd/XRv9000), NX-OS (incl. N9Kv), plus lab-image availability. ~60 pages fetched (list at the end). Facts carry the URL they came from; anything not confirmed from a primary source is marked **UNVERIFIED**. Note: Cisco Community threads are behind Cloudflare (HTTP 403 for WebFetch/curl/r.jina.ai; web.archive.org is blocked by the tool), so community content is only cited from search snippets and marked UNVERIFIED. Web search budget was exhausted near the end, so a few secondary items (NX-OS command-reference ranges, XR "source address filtering" CLI) could not be chased further.

---

## 0. Executive summary (what matters for gotwamp)

1. **No Cisco OS ships a classic RFC 5357 Control-Client/Session-Sender.** All three families are *responder-only* for RFC 5357 (Server + Session-Reflector co-located). The only Cisco "senders" are TWAMP-*Light* senders inside Segment-Routing Performance Measurement (IOS-XR PM; IOS-XE PM 17.4.1 link-delay, 17.14.1a IP-endpoint). So gotwamp's sender will be tested against Cisco reflectors, and gotwamp's reflector only against Cisco PM TWAMP Light senders.
2. **Control port default 862, test port = whatever the client requests** (1-65535 on XR; IOS-XE/NX-OS show examples accept ports 7, 9, 7147, 7222, 11232...). XR docs warn: "If the port number is greater than the value 57343, then the Twamp server control session is not created on the router" (NCS540/560) — test Receiver Port >57343 explicitly.
3. **Timers** (identical names/values on IOS, IOS-XE, IOS-XR, N7k): control inactivity `timer inactivity` 1-6000 s default 900 (SERVWAIT analogue); test-session `timeout` 1-604800 s default 900 (REFWAIT analogue). XR TWAMP Light `timeout` 60-86400 s.
4. **Modes:** IOS-XE: unauthenticated only (explicit). IOS-XR/NX-OS: only "Unauthenticated" ever appears in outputs; authenticated/encrypted acceptance UNVERIFIED (assume unauth only; test Modes=1 first, then 2/4 to see Server-Greeting/Accept behaviour).
5. **DSCP is NOT reflected from the incoming test packet** on the IOS-XE platforms that document it (reflected packet carries the DSCP negotiated in the control phase; TWAMP Light reflects with best-effort DSCP). XR tracks a per-session DSCP ("D" column) — behaviour for mismatched incoming DSCP UNVERIFIED. Design gotwamp's sender to *not* assume DSCP echo; make the reflector's policy configurable (copy vs. negotiated vs. BE).
6. **Timestamps:** XR hardware timestamps are on by default (PTP needed; IPv4 only), XE ASR900/920 RSP2 use HW (16.6.1+), RSP3 software; ASR1k/ISR4k/CSR/C8000V/XRd/N9Kv: software (XRd/N9Kv inferred, UNVERIFIED). Expect NTP-format (Z=0) timestamps; an XR caveat says timestamps may not match the NTP system clock.
7. **Known interop bugs to tolerate in gotwamp:** NX-OS reflected Error Estimate *Multiplier = 0* (CSCwa79808, RFC-illegal; sender must not reject); NX-OS Accept-Session code 4 ("permanent resource limitations") when responder was configured in the wrong order (CSCwk45686); NX-OS 10.2(4) IPv6 reflection broken (CSCwm30659); IOS-XR 5.3.4 reflected S-bit cleared (CSCve09295, UNVERIFIED snippet); IPv6 reflected hop-limit 64 (not 255) on XE RSP3; IOS/XE/XR crash/reload on crafted TWAMP-Control packets (CVE-2025-20154 — do not fuzz production boxes).
8. **Lab images:** XRv9000 release notes list TWAMP as supported; XRd has *no* published feature matrix (ipsla/TWAMP presence on XRd Control-Plane/vRouter is UNVERIFIED, but XRd docs point to Cisco 8000 guides, which include `ipsla server twamp`, `ipsla responder twamp`, `ipsla responder twamp-light` with "Hardware Timestamp ... not supported"); CSR1000v is explicitly listed for the IP SLA TWAMP responder; C8000V is explicitly listed for PM TWAMP Light sender/responder (17.4.1/17.14.1a) and is "CSR1000V + ISRv in a single image" (so `ip sla responder twamp` expected, UNVERIFIED); classic IOS (IOSv) has had the responder since 15.2(2)S/15.2(3)T and CML lists IP SLA as supported on IOSv; N9Kv feature lists never mention IP SLA/TWAMP (UNVERIFIED).

---

## A. IOS-XE

### Roles
- Server + Session-Reflector only: "For IP SLAs TWAMP Responder v1.0, the TWAMP server and the session-reflector must be configured on the same Cisco device." / "TWAMP client and session sender is not supported." — https://www.cisco.com/c/en/us/td/docs/routers/ios/config/17-x/ip-addressing/b-ip-addressing/m_sla_twamp.html (generic IOS XE 17.x guide: ASR1k/ISR4k/CSR/Cat8k family)
- Purpose statement (interop statement): "measuring the round-trip performance between an IP SLAs TWAMP responder and a non-Cisco TWAMP control device in your network" (same URL). "A TWAMP responder interoperates with the control client and session sender on another device that supports TWAMP" — https://www.cisco.com/c/en/us/td/docs/routers/ncs4200/configuration/guide/ipsla/16-12-1/b-sla-xe-16-12-1-ncs4200/ip_slas_twamp_responder_v1_0.html
- TWAMP Light **reflector** (`ip sla responder twamp-light test-session`): only on ASR 900 RSP2 / ASR 920 / NCS 520 / NCS 4200 from **17.5.1**; the generic ASR1k/ISR4k/CSR/Cat8k guide says "TWAMP Light mode is not supported." — https://www.cisco.com/c/en/us/td/docs/routers/asr920/configuration/guide/ipsla/17-1-1/b-ipsla-xe-17-1-asr920/b-ipsla-xe-17-1-asr920_chapter_011.html ; https://www.cisco.com/c/en/us/td/docs/ios-xml/ios/ipsla/configuration/xe-17/sla-xe-17-book/sla-twamp.html
- TWAMP Light **sender** exists only in Performance Measurement:
  - 17.4.1 "PM for link delay uses the light version of Two-Way Active Measurement Protocol (TWAMP) over IP and UDP defined in Appendix I of RFC 5357" (hardware timestamping; Catalyst 8000V release notes mention it) — https://www.cisco.com/c/en/us/td/docs/routers/ios/config/17-x/ip-routing/b-ip-routing/m-sr-performance-measurement.html ; https://www.cisco.com/c/en/us/td/docs/routers/C8000V/Release-Notes/c8000v-releasenotes-17-4.html
  - 17.14.1a "IP Endpoint Delay Measurement and Liveness Monitoring": "TWAMP Light support for both sender and responder"; platforms: Catalyst 8500, Catalyst 8200, ASR 1000, **Catalyst 8000V** — https://www.cisco.com/c/en/us/td/docs/ios-xml/ios/seg_routing/configuration/xe-17/segrt-xe-17-book/m-ip-endpoint-delay-measurement-and-liveness-monitoring.html
- There is **no** `ip sla ... udp-jitter ... twamp` or `ip sla twamp sender` operation (searched IP SLA guides/command reference; none found — negative, UNVERIFIED only in the sense that absence is hard to prove).

### Modes
- "TWAMP-Control and TWAMP-test stream support only unauthenticated security mode." — ASR920 17.x and ASR900 17.x guides (URLs above / https://www.cisco.com/c/en/us/td/docs/ios-xml/ios/ipsla/configuration/xe-3s/asr903/17-1-1/b-sla-xe-17-1-1-asr900/m-sla-twamp-asr903.html). ASR1k/CSR guide silent → assume unauth only (UNVERIFIED for those).

### Control port
- `port` "(Optional) Configures the port to be used by the TWAMP server to listen for connection and control requests"; default 862; command reference: range 1-65535, default 862 — https://www.cisco.com/c/en/us/td/docs/routers/ios/config/17-x/ip-addressing/b-ip-addressing/m_sla_twamp.html ; https://www.cisco.com/c/en/us/td/docs/ios-xml/ios/ipsla/command/sla-cr-book/sla_i1.html (note: a first fetch of sla_i1 returned a syntax `ip sla responder twamp [ipv4|ipv6] port ... [timeout]` that a stricter re-fetch could not find — treat that syntax as UNVERIFIED/garbled; the config guides show plain `ip sla responder twamp` + `timeout`).

### Test port
- Not configurable on the responder; it is whatever the client asks for in Request-TW-Session. Doc examples show accepted Receiver Ports 7 and 9 (IPv4/IPv6 examples) — https://www.cisco.com/c/en/us/td/docs/routers/ncs4200/configuration/guide/ipsla/16-12-1/b-sla-xe-16-12-1-ncs4200/ip_slas_twamp_responder_v1_0.html
- Port collisions (TWAMP Light platforms): "UDP port configured on IP SLA Permanent Port cannot be configured on TWAMP Light session." / "TWAMP Light Responder and TWAMP Responder cannot be enabled simultaneously on the same UDP port." / "If a TWAMP test session is in progress, a TWAMP-Light session cannot be configured on the same port." / "If a request test session message is received from the TWAMP control client for the same port number that is used by the TWAMP Light test session, then the message will not be accepted." — ASR920 17.x URL above.

### IPv4/IPv6
- "IPv6 is supported for IP SLA TWAMP Responder on the RSP3 module." — ASR920/ASR900/NCS4200 and even the generic 17.x guide repeat this; no explicit IPv6 statement for ASR1k/CSR (UNVERIFIED → test).
- IPv6 caveats (ASR900 RSP3): "IPv6 TWAMP test packets are sent back with a hop limit of 64 instead of the value 255." / "IPv6 TWAMP test packets that are fragmented are not reflected back correctly." — https://www.cisco.com/c/en/us/td/docs/ios-xml/ios/ipsla/configuration/xe-3s/asr903/17-1-1/b-sla-xe-17-1-1-asr900/m-sla-twamp-asr903.html

### Max sessions
- "Upto nine session-senders can be configured for one TWAMP responder." (all XE guides). TWAMP Light: "You can configure a maximum of 100 TWAMP Light sessions as allowed by the Control Plane." (ASR920/NCS4200 17.x). Sessions-per-control-connection limit not documented (connection detail shows "Number of Test Requests - 0:1") — UNVERIFIED.

### Timeouts
- `timer inactivity seconds` (config-twamp-srvr): "maximum time the TWAMP control session can be inactive before the session is ended"; range 1-6000, default 900; history 15.2(2)S, XE 3.6S, 15.2(3)T.
- `timeout seconds` (config-twamp-ref): "inactivity timer for a TWAMP test session"; range 1-604800, default 900. — https://www.cisco.com/c/en/us/td/docs/ios-xml/ios/ipsla/command/sla-cr-book/sla_s2.html

### DSCP/ToS
- TWAMP (RSP2/RSP3 docs): "Custom DSCP values are not supported on TWAMP. The DSCP values used in the control phase are populated in the TWAMP test packets irrespective of whatever may be the DSCP value in the test packets incoming to the reflector." — ASR920 17.x URL above.
- TWAMP Light: "The DSCP value of the incoming packet is not used to mark the reflected packet. The default DSCP best effort value is marked in the reflected packets." — same URL / NCS4200 17.x https://www.cisco.com/c/en/us/td/docs/routers/ncs4200/configuration/guide/ipsla/17-1-1/b-sla-xe-17-1-1-ncs4200/b-sla-xe-17-1-1-ncs4200_chapter_0111.html
- ASR1k/ISR4k/CSR/C8000V DSCP behaviour: not documented → UNVERIFIED (test).

### Packet size / padding
- Show output reports "Pad Length: 128" per session (NCS4200 16.12 IPv6 example). No max stated; fragmented IPv6 not reflected (above). PM padding: "padding-size" 0-8000, >1500 "can cause fragmentation and reassembly which impacts system performance" (17.14 PM doc). RFC 6038 reflect-octets/symmetrical size: never mentioned; `show ip sla twamp standards` lists only RFC 5357 → treat as unsupported (UNVERIFIED negative).

### TTL
- IPv6 hop limit 64 on reflected packets (RSP3) (above). Third-party test (stamp.rs, IOS XE 17.5.1, TWAMP Light): incoming TTL 4 → reflected "Sender TTL" field = 255, padding zero-filled, reflected length = test length — https://www.stamp.rs/interop-twamp-light/ (third party; they claim "ASR920 ... CML deployment", which is inconsistent since ASR920 is not a CML image — treat details as UNVERIFIED).

### Timestamp source
- "Effective Cisco IOS-XE Everest 16.6.1, time stamping for sender (T1, T4) and receiver (T3, T2) is performed by the hardware, instead of the software." (ASR920/ASR900 RSP2); "ASR 920 supports only hardware time stamping."; "Software time stamping is implemented for TWAMP IP SLA packets on the RSP3 module." Caveat: "TWAMP with NTP displays timestamp that does not match with the system clock." — ASR920/ASR900 17.x URLs above. "Time stamping is not supported for TWAMP test packets that ingress or egress through management interfaces ... supported only on routed interfaces and BDI interfaces." ASR1k/ISR4k/CSR/C8000V: software (UNVERIFIED; no HW statement exists).

### Key config snippets (quoted)
```
! RFC 5357 responder (all IOS-XE)
ip sla server twamp
 port 9000               ! optional, default 862
 timer inactivity 300    ! optional, 1-6000, default 900
ip sla responder twamp
 timeout 300             ! optional, 1-604800, default 900
! TWAMP Light reflector (RSP2/ASR920/NCS520/NCS4200, 17.5.1+)
ip sla responder twamp-light test-session 1 local-ip 10.0.0.1 local-port 1234 remote-ip 2.2.2.2 remote-port 3456
! PM link-delay TWAMP-Light (17.4.1): querier
performance-measurement
  protocol twamp-light
    measurement delay
      unauthenticated
        querier-dst-port 6634
  interface GigabitEthernet 0/0/1
    delay-measurement
      next-hop ipv4 10.50.62.1
! PM responder side
performance-measurement
  protocol twamp-light
    measurement delay
      unauthenticated
! PM IP-endpoint (17.14.1a) sender
performance-measurement
 endpoint test-ipv4-1
  source-address ipv4 1.1.1.1
  destination-address ipv4 4.4.4.4
  delay-measurement
   profile test-profile
 delay-profile endpoint name test-profile
  probe
   measurement-mode two-way
   computation-interval 60
   burst-interval 3000
   padding-size 0
   tos-dscp 48
```
(sources: generic 17.x guide; ASR920 17.x; IP routing PM 17.x; SR 17.14 endpoint chapter — URLs above). PM defaults: burst-interval 30-15000 ms default 3000; tos-dscp 0-63 default 48; computation-interval 1-3600 default 30 s; probe interval 30 s (30-3600); burst count 10 (1-30).

### Show commands / fields
- `show ip sla twamp connection requests` → Connection-Id, Client Address, Client Port, Client VRF.
- `show ip sla twamp connection detail` → Connection Id, Client IP Address, Client Port, Client VRF, Mode (Unauthenticated), Connection State (Connected), Control State (Active), "Number of Test Requests - 0:1".
- `show ip sla twamp session` → "IP SLAs Responder TWAMP is: Enabled", Recvr Addr, Recvr Port, Sender Addr, Sender Port, Sender VRF, Session Id (format `100.1.0.2:15833604877498391199:6D496912` = IP:timestamp:random, i.e., the RFC 4656 §3.5 SID construction), Connection Id; IPv6 variant adds Mode: UnAuthorized, DSCP: 0, Pad Length: 128, Number of Packets Received. — https://www.cisco.com/c/en/us/td/docs/routers/ncs4200/configuration/guide/ipsla/16-12-1/b-sla-xe-16-12-1-ncs4200/ip_slas_twamp_responder_v1_0.html
- `show ip sla twamp-light session` (session ID, status, mode, addresses/ports, packet counts); `show ip sla twamp standards`.
- PM: `show performance-measurement summary|interfaces detail|responder interfaces|endpoint ...`.

### Platforms / releases
- Feature table (ASR1k 16.6 book): "IP SLAs TWAMP Responder v1.0 — Cisco IOS XE Release 3.6S"; "TWAMP RFC compliance — Cisco IOS XE Release 3.12S: support was added for RFC 5357" — https://www.cisco.com/c/en/us/td/docs/ios-xml/ios/ipsla/configuration/xe-16-6/sla-xe-16-6-book/sla-twamp.html
- Generic 17.x guide feature table: support "for Cisco ASR 1000 series, Cisco ISR 4000 series, and Cisco CSR 1000v" (the release label in that table reads "Cisco IOS XE 12.2SE", an obvious doc artifact) — https://www.cisco.com/c/en/us/td/docs/routers/ios/config/17-x/ip-addressing/b-ip-addressing/m_sla_twamp.html
- Classic IOS: introduced 15.2(3)T (15M&T guide) and 15.2(2)S (command history) — https://www.cisco.com/c/en/us/td/docs/ios-xml/ios/ipsla/configuration/15-mt/sla-15-mt-book/sla_twamp.html ; sla_s2 URL above.
- Catalyst 9000 (9300 17.15 NM guide; Cat9k IP SLA guide): **no TWAMP chapter/mention** — https://www.cisco.com/c/en/us/td/docs/switches/lan/catalyst9300/software/release/17-15/configuration_guide/nmgmt/b_1715_nmgmt_9300_cg/configuring_service_level_agreements.html ; https://www.cisco.com/c/en/us/td/docs/switches/lan/c9000/lyr3-fwd/ip-slas/ip-slas-configuration-guide/ip-slas.html → assume unsupported on Cat9k.

### Interop caveats
- CVE-2025-20154 "Cisco IOS, IOS XE, and IOS XR Software TWAMP Denial of Service Vulnerability": "out-of-bounds array access when processing specially crafted TWAMP control packets" → reload (IOS/XE) / process crash (XR with debug); vulnerable when `ip sla server twamp` / `ipsla server twamp` configured; bug IDs CSCve33776, CSCwk80897, CSCwm63229; XE 16.6.1-17.2.3 only with debug; XR fixed 24.3.2+; NX-OS not affected; "no workarounds" — https://sec.cloudapps.cisco.com/security/center/content/CiscoSecurityAdvisory/cisco-sa-twamp-kV4FHugn
- Start-Time semantics, unsupported Mode bits handling, number of sessions per connection: **not documented anywhere** → UNVERIFIED; include explicit tests (Start-Time=0 vs. future; Modes=2/4; 2+ Request-TW-Session on one connection; 10th sender).

---

## B. IOS-XR

### Roles
- "This router supports only Session-Server and Session Reflector functionality." (NCS5500 7.7 / NCS540 7.5 / NCS560 7.4 / Cisco 8000 7.3.x) — https://www.cisco.com/c/en/us/td/docs/iosxr/ncs5500/system-monitoring/77x/b-system-monitoring-cg-ncs5500-77x/m-ipsla-twamp-ncs5500.html ; https://www.cisco.com/c/en/us/td/docs/iosxr/cisco8000/system-monitoring/73x/b-system-monitoring-cg-cisco8k-73x/m-ipsla-cisco8k.html
- TWAMP Light reflector: `ipsla responder twamp-light test-session` (6.6.1+, `any` keywords 7.4.1+).
- TWAMP Light **sender** = Performance Measurement only ("Interface delay measurement using RFC 5357 with IP/UDP encap (TWAMP-Light)" is the default PM protocol); IP Endpoint Delay Measurement from 7.4.1 (IPv4/IPv6/MPLS) — https://www.cisco.com/c/en/us/td/docs/routers/asr9000/software/24xx/segment-routing/configuration/guide/b-segment-routing-cg-asr9000-24xx/configure-performance-measurement.html ; https://www.cisco.com/c/en/us/td/docs/iosxr/cisco8000/segment-routing/73x/b-segment-routing-cg-cisco8000-73x/configuring-performance-measurement.html . No `ipsla operation ... type twamp-light` exists (the `show ipsla application` operation list is ICMP ECHO/PATH JITTER/PATH ECHO/UDP JITTER/UDP ECHO/MPLS LSP PING/TRACE — NCS5500 command reference).

### Modes
- Docs describe "unauthenticated, authenticated, and encrypted" for the protocol, but the `brief` legend defines only "M - Mode of authentication U - Unauthenticated" and all outputs show "Mode: Unauthenticated" → authenticated/encrypted acceptance UNVERIFIED (NCS5500 command reference PDF https://www.cisco.com/c/en/us/td/docs/iosxr/ncs5500/system-monitoring/b-ncs5500-system-monitoring-cli-reference/b-ncs5500-system-monitoring-cli-reference_chapter_0111.pdf).

### Control port / timers (command reference, verbatim)
- `server twamp [ port number | timer inactivity value ]` — "Port number. Range is 1 to 65535." "Inactivity timer value in seconds. Range is 1 to 6000." "Default port is 862. Default timer value is 900 seconds." Introduced 6.3.1 (NCS5500) / 5.1.1 (ASR9k). — NCS5500 CLI ref PDF above; ASR9k https://www.cisco.com/c/en/us/td/docs/routers/asr9000/software/system_monitoring/command/reference/b-sysmon-cr-asr9k/b-sysmon-cr-asr9k_chapter_011.pdf
- `responder twamp [ timeout value ]` — "Inactivity timeout period (in seconds). Range is 1 to 604800." "Default timeout is 900 seconds." (ASR9k 5.1.1; NCS5500 6.3.1).
- Restriction: "If the port number is greater than the value 57343, then the Twamp server control session is not created on the router." — https://www.cisco.com/c/en/us/td/docs/iosxr/ncs5xx/system-monitoring/75x/b-system-monitoring-cg-75x-ncs540/m-ipsla-twamp-ncs5xx.html ; https://www.cisco.com/c/en/us/td/docs/iosxr/ncs560/system-monitoring/24xx/b-system-monitoring-cg-24xx-ncs560/implementing-ip-service-level-agreements.html (ambiguous whether it is the server port or the requested receiver port → test both).
- Port recommendation for senders: "TWAMP Sender must not use port values already assigned by IANA ... use port values in the range of the Dynamic Ports ... from 49152 to 65535" — https://www.cisco.com/c/en/us/td/docs/routers/asr9000/software/asr9k-r7-6/system-monitoring/configuration/guide/b-system-monitoring-cg-asr9000-76x/implementing-ip-service-level-agreements.html

### TWAMP Light reflector CLI (command reference, verbatim)
`responder twamp-light test-session test-session-id [ local-ip { local-ip-address | any { ipv4 | ipv6 } } local-port local-port-number remote-ip { remote-ip-address | any { ipv4 | ipv6 } } remote-port { remote-port-number | any } vrf { vrf-name | any | default } | timeout timeout-value ]`
- test-session "Range: 1 - 65535"; local-port "Range: 1 - 65535"; remote-port "Range: 1 - 65535" or `any`; vrf: "any: use this only when local-ip is configured as any; default: use this when the local ip-address belongs to default vrf"; timeout "For TWAMP-light, the range is 60 - 86400". Defaults are contradictory across two entries: `responder` entry says "By default, there is no timeout for TWAMP-light responder." while `responder twamp-light` entry says "Default timeout is 900 seconds." (show output of an un-timed session prints "Session timeout: 0"). History: introduced 6.6.1; `any` option 7.4.1. Usage: "Caution must be taken ... any option ... opens up the specified local-port for packets from any IP address." — NCS5500/ASR9k CLI reference PDFs above.
- IPv6 example: `local-ip 2001:db8::1 local-port 13001 remote-ip 2001:db8::2 remote-port 13002 vrf default` — https://www.cisco.com/c/en/us/td/docs/iosxr/ncs5500/system-monitoring/66x/b-system-monitoring-cg-ncs5500-66x/b-system-monitoring-cg-ncs5500-66x_chapter_0111.html
- Restrictions: VRF must match the interface's VRF; "Once you configure a TWAMP-light test-session on a device, it opens a permanent port, which will remain open until you delete the configuration"; "any two test-sessions cannot have the same local IP address and local port under the same VRF"; "Allows for only one local IP configuration per TWAMP-light test session"; "Allows to configure any number of TWAMP-light test sessions" but "consider the lower layers such as UDP and so on which can handle only 500 PPS"; timeout cannot be configured before the IP parameters. — NCS5500 7.7 / ASR9k 7.6 URLs above.

### IPv4/IPv6
- "TWAMP v4 and v6 are supported." / "TWAMP-Light v4 and v6 are supported." (NCS5500/540/560, ASR9k 7.6+). But ASR9k "TWAMP accuracy enhancement" limitations: "TWAMP reflector does not support IPv6" (hardware-timestamp path) and "TWAMP supports only IPv4 hardware timestamp." — ASR9k 7.6/7.11 and NCS540/560 URLs above.

### Max sessions
- Not stated for RFC 5357 TWAMP (UNVERIFIED). TWAMP Light: unlimited count, 500 PPS aggregate caution (above).

### DSCP
- Per-session DSCP shown ("D - DSCP value", values 24/40/8/0 in the `brief` example) and "DSCP: 0 / Pad Length: 128" per session; no statement on copying incoming DSCP → UNVERIFIED (test).

### Timestamp source
- "hardware time stamping is enabled by default and requires no configuration"; "A pre-requisite for the hardware timestamp feature is PTP"; "TWAMP supports only IPv4 hardware timestamp"; disable with `ipsla hw-timestamp disable` (introduced 6.0.1; recommended on 3rd-gen ASR9k Ethernet LCs); NCS5500: NC57 cards need `hw-module profile npu native-mode-enable` (7.3.1). Cisco 8000 (7.3.x) and NCS5500 6.6.x: "Hardware Timestamp feature which provides greater accuracy is not supported." PTPv2 vs NTP coding via Z-bit noted in NCS5500 7.7 guide. — URLs above.

### Show commands / fields
- `show ipsla twamp status` → "TWAMP Server is enabled / TWAMP Server port : 862 / TWAMP Reflector is enabled".
- `show ipsla twamp connection [detail source-ip | requests]` (Connection ID, Client IP Address, Client Port, Client VRF Name, Mode, Connection State, Control State).
- `show ipsla twamp session [source-ip host-name | brief]` → "***** TWAMP Sessions ***** / ***** TWAMP-LIGHT Sessions *****" with Session status, Recvr Addr, Recvr Port, Sender Addr, Sender Port, Sender VRF Name, Session ID, Mode, Number of Packets Received, Session timeout, Number of Packets Sent (7.4.1); `brief` columns: Receiver Address_Port/VRF Name, M/D, PL, RX/TX, Type (T/TWL), Sender Address_Port.
- `show ipsla twamp standards` → "TWAMP Server IETF RFC5357 / TWAMP Reflector IETF RFC5357". — CLI reference PDFs above.

### PM (TWAMP Light sender/responder) facts
- "By default, PM uses port 862 as the TWAMP-reserved UDP destination port"; `performance-measurement protocol twamp-light measurement delay unauthenticated querier-dst-port <port>`; "Port must match on querier and responder nodes"; defaults burst-interval 3000 ms (30-15000), burst count 10, computation-interval 30 s, DSCP 48; one-way needs PTP both ends; two-way/loopback need PTP-capable HW on the sender (XR physical). 24.3.1 (XRd/8000 notes): "Delay Measurement Using Software Timestamp ... when the existing hardware lacks timestamp support"; "The timestamp-format NTP keyword is introduced in the performance-measurement delay-profile command." "Two-Way Active Measurement Protocol Light Source Address Filtering" (7.11.1) restricts "TWAMP response for querier and responder nodes" by configured IP addresses (exact CLI not retrieved — UNVERIFIED). — https://www.cisco.com/c/en/us/td/docs/routers/virtual-routers/xrd-24xx/release/notes/b-release-notes-xrd-r2431.html ; https://www.cisco.com/c/en/us/td/docs/iosxr/cisco8000/segment-routing/24xx/configuration/guide/b-segment-routing-cg-cisco8000-24xx/configuring-performance-measurement.html ; https://www.cisco.com/c/en/us/td/docs/iosxr/ncs560/segment-routing/711x/b-segment-routing-cg-711x-ncs560/m-configure-performance-measurement-ncs5xx.html

### Interop caveats / bugs
- CVE-2021-34720 (CSCvw32825, CSCvw61840): XR IP SLA/TWAMP "socket creation failures" → packet-memory exhaustion / IP SLA process crash; vulnerable with `ipsla responder` or `ipsla responder twamp`; fixed 6.2.3/6.3.2/6.3.3/7.2.2, 7.3+ not affected — https://www.cisco.com/c/en/us/support/docs/csa/cisco-sa-ipsla-ZA3SRrpP.html
- CVE-2025-20154 (above) — XR fixed 24.3.2+.
- CSCve09295 "TWAMP S bit is set to False" (ASR9k/CRS 5.3.4 with EXFO probe: Error-Estimate S-bit not set in reflected direction, breaking one-way delay) — UNVERIFIED (findbugzero search snippet; page body not retrievable).
- CSCwe59306 PM twamp-light `querier-src-port` issue after ISSU on NCS560; CSCvj47149 YANG for twamp-light — UNVERIFIED snippets.
- Community thread "TWAMP Light interoperability issue Cisco XR 7.3.2 and Nokia SR OS" exists (https://community.cisco.com/t5/network-management/twamp-light-interopérablity-issue-cisco-xr-7-3-2-and-nokia-sr-os/td-p/4668474) — content NOT retrievable (403) → UNVERIFIED; worth reading manually.

### XRd / XRv9000 specifics (priority item)
- XRd lineage: "XR control plane pedigree shared with the likes of Cisco 8000 and data plane capabilities that are derived from the powerful XRv9000" (7.9.1, 25.1.1 notes) — https://www.cisco.com/c/en/us/td/docs/routers/virtual-routers/xrd-79x/release/notes/b-release-notes-xrd-r791.html
- XRd release notes (7.7.1, 7.8.1, 7.9.1, 7.10.1, 7.11.2, 24.1.1, 24.3.1, 24.4.1, 25.1.1, 25.4.2, 26.3.1) contain **no supported/unsupported feature matrix and never mention ipsla or TWAMP**; 7.7.1's feature table points to Cisco 8000 configuration guides for each feature — https://www.cisco.com/c/en/us/td/docs/routers/virtual-routers/xrd-77x/release/notes/b-release-notes-xrd-r771.html (others listed at the end). PM delay-measurement features *are* listed for XRd (24.3.1 software timestamp; 24.4.1 far-end one-way metrics, GRE-tunnel delay with "TWAMP test packets") — https://www.cisco.com/c/en/us/td/docs/routers/virtual-routers/xrd-24xx/release/notes/b-release-notes-xrd-r2441.html → PM TWAMP Light sender/responder on XRd with software timestamps is supported; `ipsla server/responder twamp(-light)` on XRd: **UNVERIFIED** (expected present because Cisco 8000 guide documents them; test on the image).
- Hardware timestamping on XRd: no NPU → software; Cisco 8000 7.3.x says "Hardware Timestamp feature ... is not supported" for TWAMP; 24.3.1 PM software-timestamp feature is described for "when the existing hardware lacks timestamp support" → inference, UNVERIFIED.
- XRv9000 24.2.1 release notes list "Implementing IP Service Level Agreements" (Introduced 6.0.0) and "The Two-Way Active Measurement Protocol (TWAMP)" (Introduced 6.0.1) as supported — https://www.cisco.com/c/en/us/td/docs/routers/virtual-routers/xrv9k-24xx/release/notes/b-release-notes-xrv9000-r2421.html (community snippet: XRv9k IP SLA "only UDP probes are supported, not ICMP" — UNVERIFIED).
- XRd Control Plane: "minimal forwarding capabilities, which is adequate for peering or southbound interfacing"; vRouter: DPDK forwarder; host needs 3 GiB hugepages, 4+ cores — https://www.cisco.com/c/en/us/products/collateral/routers/ios-xrd/ios-xrd-ds.html . Images: "available on CCO (https://software.cisco.com/downloads) ... login with a valid cisco account that has privileges to download Cisco software images", otherwise "contact your Cisco Sales Representative" — https://xrdocs.io/virtual-routing/tutorials/2022-08-22-xrd-images-where-can-one-get-them/ ; containerlab: "available for download only for users who have an active service account" — https://containerlab.dev/manual/kinds/xrd/ (so "free with a cisco.com login" is UNVERIFIED/entitlement-dependent). "Cisco IOS XRd is not supported for production networks on Linux" (all notes).

---

## C. NX-OS (Nexus 9000; N7k for defaults)

### Roles / modes / limits
- Server + reflector only: "TWAMP client and session sender is not supported."; "Up to ten control sessions can be configured and established for one TWAMP responder."; "TWAMP Light mode is not supported." (10.1(x), 10.4(x), 10.5(x), 9.3(x) guides) — https://www.cisco.com/c/en/us/td/docs/dcn/nx-os/nexus9000/101x/configuration/ip-sla/cisco-nexus-9000-series-nx-os-ip-slas-configuration-guide-101x/m-configuring-twamp-responder.html ; https://www.cisco.com/c/en/us/td/docs/dcn/nx-os/nexus9000/105x/configuration/ip-sla/cisco-nexus-9000-series-nx-os-ip-slas-configuration-guide/m-configuring-twamp-responder.html
- Modes: only "Mode: Unauthenticated" shown; no explicit statement → UNVERIFIED.
- TWAMP Light in NX-OS 10.x: still "not supported" in the 10.5(x) chapter; no TWAMP Light feature appears in 10.x release notes.

### CLI (verbatim from 10.5(x) chapter PDF text)
```
feature sla twamp-server
ip sla server twamp
 port 9000                ! "(Optional) ... default port ... is the RFC-specified port [862] and can be reconfigured"
 timer inactivity 300
feature sla responder
ip sla responder twamp
 timeout 300
```
- N9k guides give **no ranges/defaults** for `timer inactivity`/`timeout`; the N7k 8.x guide (same code lineage, introduced NX-OS 8.3(1)) states: timer inactivity default 900, range 1-6000; timeout default 900, range 1-604800 — https://www.cisco.com/c/en/us/td/docs/switches/datacenter/nexus7000/sw/ipslas/config/cisco_nexus7000_ipslas_config_guide_8x/cisco_nexus7000_ipslas_config_guide_8x_chapter_01101.html (N9k values UNVERIFIED). NX-API objects `slaTwampServer`/`slaTwampResponder` only expose `adminState` — https://developer.cisco.com/docs/cisco-nexus-3000-and-9000-series-nx-api-rest-sdk-user-guide-and-api-reference-release-102x/configuring-ip-slas/
- Introduction release on N9k: the 7.x IP SLA book has no TWAMP chapter (IP SLA initial support 7.0(3)I2(1)); 9.3(x) "New and Changed" says "IP SLA — No updates since Cisco NX-OS Release 9.2(x)" → TWAMP responder appeared in a 9.2(x) or 7.0(3)I7-era release (exact release UNVERIFIED) — https://www.cisco.com/c/en/us/td/docs/switches/datacenter/nexus9000/sw/7-x/ip_sla/configuration/guide/b_Cisco_Nexus_9000_Series_NX-OS_IP_SLAs_Configuration_Guide_7x.pdf ; https://www.cisco.com/c/en/us/td/docs/switches/datacenter/nexus9000/sw/93x/ip-sla/configuration/guide/b-nexus-9000-series-nx-os-ip-slas-configuration-guide-93x/b-nexus-9000-series-nx-os-ip-slas-configuration-guide-93x_chapter_01.html

### Show outputs (9.3x/10.x)
- `show ip sla twamp standards` → "TWAMP Server IETF RFC5357 / TWAMP Reflector IETF RFC5357"; `show ip sla twamp session` → Recvr Addr 30.30.30.1, Recvr Port 7147, Sender Addr/Port, Sender VRF, Session Id `30.30.30.1:15918249420668138422:DF55BEE9`, Connection Id; `show ip sla twamp connection detail` → Mode: Unauthenticated, Connection State: Connected, Control State: Active, "Number of Test Requests - 0:1" — https://www.cisco.com/c/en/us/td/docs/switches/datacenter/nexus9000/sw/93x/ip-sla/configuration/guide/b-nexus-9000-series-nx-os-ip-slas-configuration-guide-93x/b-nexus-9000-series-nx-os-ip-slas-configuration-guide-93x_chapter_01101.html

### IPv6 / DSCP / TTL / timestamps / padding
- IPv6 TWAMP exists (bug CSCwm30659 "Twampv6 not working in 10.2(4)"); DSCP/TTL/padding behaviour undocumented → UNVERIFIED; timestamps software (N9Kv: "Cisco proprietary software data plane"). NX-OS 10.6(1)F added IPv6 for UDP-jitter IP SLA (not TWAMP) per release notes snippet.

### Bugs (interop-relevant)
- CSCwa79808 "Multiplier set to zero for TWAMP-TEST UDP packets": "NXOS running platforms set multiplier value at 0 on replying back while running as TWAMP server"; workaround "if the TWAMP client can ignore the Multiplier of zero"; affected 9.3(8), 10.1(1), 10.2(2); fixed 9.3(10), 10.2(3), 10.2(4) — https://www.findbugzero.com/operational-defect-database/vendors/cisco/defects/CSCwa79808
- CSCwk45686 "Twamp not working in a server-N9k setup": client sees "ERROR CODE 4 ... Cannot perform request due to permanent resource limitations" when "ip sla responder twamp" is configured before "ip sla responder"; fixed 10.3(6), 10.4(4), 10.5(2) — https://www.findbugzero.com/operational-defect-database/vendors/cisco/defects/CSCwk45686 ; https://www.cisco.com/c/en/us/td/docs/dcn/nx-os/nexus9000/105x/release-notes/cisco-nexus-9000-nxos-release-notes-1052F.html
- CSCwm30659 "Twampv6 not working in 10.2(4)": "Session will be established but test packets received from client will be dropped by responder"; fixed 10.4(4), 10.5(2) — https://www.findbugzero.com/operational-defect-database/vendors/cisco/defects/CSCwm30659
- NX-OS is not affected by CVE-2025-20154 (advisory above). UDP-jitter IP SLA on N9k needs CoPP/netstack port-range alignment ("Configuring CoPP for IP SLA Packets", "Matching the Netstack Port Range" chapters in the 7.x book) — relevance to TWAMP UNVERIFIED but worth checking if test packets are dropped.

### N9Kv
- Nexus 9000v guides (10.2(x), 10.6(x)) list supported/unsupported features (unsupported: QoS, BFD, ACL, policy maps, SPAN, IGMP snooping...) and **never mention IP SLA or TWAMP**; "uses the same software image that runs the Nexus 9000 hardware platforms", forwarding on "a Cisco proprietary software data plane", ~4 Mbps/700 Mbps — https://www.cisco.com/c/en/us/td/docs/dcn/nx-os/nexus9000/102x/configuration/n9000v/cisco-nexus-9000v-guide-102x/m-cisco-nexus-9000v.html ; https://www.cisco.com/c/en/us/td/docs/dcn/nx-os/nexus9000/106x/configuration/n9000v-9300v-9500v/cisco-nexus-9000v-9300v-9500v-guide-release-106x/m-overview.html → TWAMP on N9Kv UNVERIFIED (CLI likely present; try `feature sla twamp-server`).

---

## D. Sender-Address / Receiver-Port / sessions / Start-Time / Modes — what Cisco documents
- Sender Address: XR TWAMP Light filters on `remote-ip`/`remote-port` (or `any` since 7.4.1) and shows "Sender Addr: any (IPV4)"; XE TWAMP Light requires explicit remote-ip/remote-port (no `any` documented). For RFC 5357 sessions all three OSes display Sender Addr/Port and Recvr Addr/Port from the Request-TW-Session (so gotwamp should populate Sender Address/Port honestly; whether a zero Sender Address is accepted is UNVERIFIED).
- Receiver Port: client-chosen; XR caution about >57343; XE rejects a Request-TW-Session whose port collides with a TWAMP Light session or IP SLA permanent port (exact Accept code UNVERIFIED); NX-OS bug returns Accept=4 in a misconfiguration.
- Sessions: XE 9 session-senders/responder; NX-OS 10 control sessions; XR unspecified. Multiple Request-TW-Session per connection: UNVERIFIED everywhere (only "Number of Test Requests - 0:1" visible).
- Start-Time, Modes bits other than 1, Timeout field in Stop-Sessions, reflect-octets (RFC 6038): **no Cisco documentation found** → UNVERIFIED; must be covered by the interop test plan.
- Explicit interop statements: XE/NX-OS "interoperates with the control client and session sender on another device that supports TWAMP" / "non-Cisco TWAMP control device"; XR "supports all devices deployed in the network"; `show ... twamp standards` = RFC 5357 only.

---

## E. Virtual images for lab (and containerlab kinds)
- containerlab kinds: `cisco_xrd` (Control-Plane only: "containerlab supports only the control-plane flavor of XRd ... vrouter requires PCI interfaces"), `cisco_xrd_vrouter` (KVM wrapper, example `vrnetlab/cisco_xrd-vrouter:25.4.2`, 4 vCPU/8-10 GiB, 3 GiB 1G hugepages, nested virt), `cisco_xrv9k`, `cisco_csr1000v`, `cisco_c8000v` (example `vrnetlab/vr-c8000v:17.11.01a`, data ports Gi2+, ~5 min boot, "successor of Cisco CSR1000v"), `cisco_n9kv` (admin:admin, Ethernet1/1+, 4 vCPU/10 GB or lite 2/6 GB, ~5 min), `cisco_iol` (`vrnetlab/cisco_iol:17.12.01`, from CML refplat ISO, "a version of Cisco IOS/IOS-XE software"), `cisco_cat9kv`, `cisco_c8000`, `cisco_xrv`, `cisco_ftdv` — https://containerlab.dev/manual/kinds/ ; https://containerlab.dev/manual/kinds/xrd/ ; https://containerlab.dev/manual/kinds/cisco_xrd_vrouter/ ; https://containerlab.dev/manual/kinds/vr-c8000v/ ; https://containerlab.dev/manual/kinds/vr-n9kv/ ; https://containerlab.dev/manual/kinds/cisco_iol/
- TWAMP expectations per image: CSR1000v — responder documented (feature table). C8000V — responder expected (CSR1000V+ISRv image; UNVERIFIED), PM TWAMP Light sender/responder documented (17.4.1 link, 17.14.1a endpoint). IOSv — CML: "IP SLA" listed among supported features; classic-IOS responder since 15.2(2)S/15.2(3)T; TWAMP-specific support on IOSv UNVERIFIED — https://developer.cisco.com/docs/modeling-labs/iosv/ . IOL (17.12) — "an implementation of Cisco IOS-XE" (no netconf/restconf, no EEM); TWAMP responder UNVERIFIED — https://developer.cisco.com/docs/modeling-labs/iol/ . XRd CP/vRouter — see B (ipsla TWAMP UNVERIFIED; PM TWAMP Light with software timestamps documented from 24.3.1). XRv9000 — TWAMP listed as supported. N9Kv — UNVERIFIED. Cat9kv — no TWAMP (Cat9k docs have none).

---

## F. Suggested interop test matrix derived from the above (brief)
1. Control: Modes=1 happy path on each reflector; Modes=2/4 → expect Server-Greeting Modes=1 and client-side abort; verify `timer inactivity` closes idle control at 900 s; test Request-TW-Session with Receiver Port 0 vs explicit, ports <1024 (docs show 7/9 accepted), >57343 on XR, colliding with a TWAMP Light session on XE; Start-Time now vs. +N s; 2 sessions on one connection; 10th sender on XE / 11th control session on NX-OS; Stop-Sessions with Timeout.
2. Test: compare incoming vs reflected DSCP (expect negotiated DSCP on XE RSP2/3, BE for TWAMP Light), TTL/hop-limit (expect 255 sender-TTL field; reflected IPv6 hop-limit 64 on RSP3), padding echo length and zero-fill (RFC 5357 vs 6038), Error Estimate parsing with Multiplier=0 (NX-OS) and S-bit=0 (old XR), timestamp format Z=0, fragmentation avoidance.
3. TWAMP Light: XR reflector with `remote-ip any remote-port any vrf any`; XE reflector needs exact remote-ip/remote-port; gotwamp reflector vs XR/XE PM querier on UDP 862 (`querier-dst-port` must match), with and without 7.11.1 source-address filtering.
4. Security: never send malformed control packets to unpatched images (CVE-2025-20154 / CVE-2021-34720).

---

## URLs fetched (one-line confirmation each)
IOS-XE
- https://www.cisco.com/c/en/us/td/docs/routers/ios/config/17-x/ip-addressing/b-ip-addressing/m_sla_twamp.html — generic XE17 responder: restrictions, CLI, default 862, 9 senders, "TWAMP Light mode is not supported", ASR1000/ISR4000/CSR1000v feature row.
- https://www.cisco.com/c/en/us/td/docs/ios-xml/ios/ipsla/configuration/xe-17/sla-xe-17-book/sla-twamp.html — ASR1k XE17: same restrictions; IPv6 only RSP3; TWAMP Light unsupported.
- https://www.cisco.com/c/en/us/td/docs/ios-xml/ios/ipsla/configuration/xe-16-11/sla-xe-16-11-book/sla-twamp.html — ASR1k 16.11: restrictions/CLI.
- https://www.cisco.com/c/en/us/td/docs/ios-xml/ios/ipsla/configuration/xe-16-6/sla-xe-16-6-book/sla-twamp.html — feature table XE 3.6S (v1.0), 3.12S (RFC 5357 compliance).
- https://www.cisco.com/c/en/us/td/docs/routers/asr920/configuration/guide/ipsla/17-1-1/b-ipsla-xe-17-1-asr920/b-ipsla-xe-17-1-asr920_chapter_011.html — TWAMP Light 17.5.1, 100 sessions, port-collision rules, DSCP rules, unauth only, HW timestamps 16.6.1, show commands.
- https://www.cisco.com/c/en/us/td/docs/ios-xml/ios/ipsla/configuration/xe-3s/asr903/17-1-1/b-sla-xe-17-1-1-asr900/m-sla-twamp-asr903.html — IPv6 hop-limit 64, fragmentation, NTP clock mismatch, TWAMP Light.
- https://www.cisco.com/c/en/us/td/docs/routers/ncs4200/configuration/guide/ipsla/16-12-1/b-sla-xe-16-12-1-ncs4200/ip_slas_twamp_responder_v1_0.html — full show outputs incl. IPv6 session (Mode/DSCP/Pad Length), interop statement.
- https://www.cisco.com/c/en/us/td/docs/routers/ncs4200/configuration/guide/ipsla/17-1-1/b-sla-xe-17-1-1-ncs4200/b-sla-xe-17-1-1-ncs4200_chapter_0111.html — TWAMP Light restrictions/DSCP/IPv6 caveats.
- https://www.cisco.com/c/en/us/td/docs/ios-xml/ios/ipsla/configuration/15-mt/sla-15-mt-book/sla_twamp.html — classic IOS 15.2(3)T.
- https://www.cisco.com/c/en/us/td/docs/ios-xml/ios/ipsla/command/sla-cr-book/sla_s2.html — timer inactivity 1-6000/900; timeout 1-604800/900; history 15.2(2)S, XE 3.6S.
- https://www.cisco.com/c/en/us/td/docs/ios-xml/ios/ipsla/command/sla-cr-book/sla_i1.html — port (twamp) 1-65535 default 862 (other syntax from this page unreliable).
- https://www.cisco.com/c/en/us/td/docs/ios-xml/ios/ipsla/command/sla-cr-book/sla_s1.html — show entries not rendered (no data).
- https://www.cisco.com/c/en/us/td/docs/ios-xml/ios/seg_routing/configuration/xe-17/segrt-xe-17-book/m-ip-endpoint-delay-measurement-and-liveness-monitoring.html — 17.14.1a TWAMP Light sender+responder; platforms incl. C8000V; probe defaults; restrictions.
- https://www.cisco.com/c/en/us/td/docs/routers/ios/config/17-x/ip-routing/b-ip-routing/m-sr-performance-measurement.html — 17.4 PM link delay TWAMP Light (RFC 5357 App. I); querier/responder CLI; querier-dst-port.
- https://www.cisco.com/c/en/us/td/docs/routers/C8000V/Release-Notes/c8000v-releasenotes-17-4.html — C8000V = CSR1000V+ISRv; PM TWAMP light 17.4.1; eval licensing.
- https://www.cisco.com/c/en/us/td/docs/switches/lan/catalyst9300/software/release/17-15/configuration_guide/nmgmt/b_1715_nmgmt_9300_cg/configuring_service_level_agreements.html — Cat9300: no TWAMP.
- https://www.cisco.com/c/en/us/td/docs/switches/lan/c9000/lyr3-fwd/ip-slas/ip-slas-configuration-guide/ip-slas.html — Cat9k IP SLA guide: no TWAMP chapter.
- https://sec.cloudapps.cisco.com/security/center/content/CiscoSecurityAdvisory/cisco-sa-twamp-kV4FHugn — CVE-2025-20154 details.
IOS-XR
- https://www.cisco.com/c/en/us/td/docs/iosxr/ncs5500/system-monitoring/77x/b-system-monitoring-cg-ncs5500-77x/m-ipsla-twamp-ncs5500.html — roles, v4/v6, HW TS/PTP, NC57 native mode, TWAMP Light restrictions, brief output.
- https://www.cisco.com/c/en/us/td/docs/iosxr/ncs5500/system-monitoring/b-ncs5500-system-monitoring-cli-reference/b-ncs5500-system-monitoring-cli-reference_chapter_0111.html and .pdf — verbatim command entries (responder twamp, responder twamp-light, server twamp, show ipsla twamp *, hw-timestamp disable).
- https://www.cisco.com/c/en/us/td/docs/routers/asr9000/software/system_monitoring/command/reference/b-sysmon-cr-asr9k/b-sysmon-cr-asr9k_chapter_011.html and .pdf — same entries; history 5.1.1.
- https://www.cisco.com/c/en/us/td/docs/routers/asr9000/software/asr9k-r7-6/system-monitoring/configuration/guide/b-system-monitoring-cg-asr9000-76x/implementing-ip-service-level-agreements.html — HW TS default, "TWAMP reflector does not support IPv6", TWAMP Light v4/v6, 49152-65535 recommendation.
- https://www.cisco.com/c/en/us/td/docs/routers/asr9000/software/711x/system-monitoring/configuration/guide/b-system-monitoring-cg-asr9000-711x/implementing-ip-service-level-agreements.html — same for 7.11.
- https://www.cisco.com/c/en/us/td/docs/iosxr/ncs5xx/system-monitoring/75x/b-system-monitoring-cg-75x-ncs540/m-ipsla-twamp-ncs5xx.html — NCS540: 57343 rule, HW TS IPv4-only, PTP.
- https://www.cisco.com/c/en/us/td/docs/iosxr/ncs560/system-monitoring/74x/b-system-monitoring-cg-74x-ncs560/m-ipsla-twamp-ncs5xx.html — NCS560 7.4: same.
- https://www.cisco.com/c/en/us/td/docs/iosxr/ncs560/system-monitoring/24xx/b-system-monitoring-cg-24xx-ncs560/implementing-ip-service-level-agreements.html — NCS560 24.x: 57343 rule, VRF 7.4.1, DSCP/Pad Length fields.
- https://www.cisco.com/c/en/us/td/docs/iosxr/ncs5500/system-monitoring/66x/b-system-monitoring-cg-ncs5500-66x/b-system-monitoring-cg-ncs5500-66x_chapter_0111.html — TWAMP 6.3.1, TWAMP Light IPv6 example, no HW TS at 6.6.
- https://www.cisco.com/c/en/us/td/docs/iosxr/cisco8000/system-monitoring/73x/b-system-monitoring-cg-cisco8k-73x/m-ipsla-cisco8k.html — Cisco 8000: TWAMP + TWAMP Light, HW timestamp not supported.
- https://www.cisco.com/c/en/us/td/docs/iosxr/cisco8000/segment-routing/73x/b-segment-routing-cg-cisco8000-73x/configuring-performance-measurement.html — PM TWAMP Light, 862 default, probe defaults, endpoint DM 7.4.1.
- https://www.cisco.com/c/en/us/td/docs/routers/asr9000/software/24xx/segment-routing/configuration/guide/b-segment-routing-cg-asr9000-24xx/configure-performance-measurement.html — PM 24.x defaults/modes; source-address filtering section exists (7.11.1).
- https://www.cisco.com/c/en/us/td/docs/iosxr/cisco8000/segment-routing/24xx/configuration/guide/b-segment-routing-cg-cisco8000-24xx/configuring-performance-measurement.html — "restrict TWAMP response for querier and responder nodes"; PTP both ends for one-way far-end.
- https://www.cisco.com/c/en/us/td/docs/iosxr/cisco8000/segment-routing/26xx/configuration/guide/b-segment-routing-cg-cisco8000-26xx/configuring-performance-measurement.html — 862 default; PTP requirement.
- https://www.cisco.com/c/en/us/td/docs/iosxr/ncs560/segment-routing/711x/b-segment-routing-cg-711x-ncs560/m-configure-performance-measurement-ncs5xx.html — source-address filtering heading; querier-dst-port.
- https://www.cisco.com/c/en/us/support/docs/csa/cisco-sa-ipsla-ZA3SRrpP.html — CVE-2021-34720 XR IPSLA/TWAMP DoS.
XRd / XRv9000 / virtual
- https://www.cisco.com/c/en/us/td/docs/routers/virtual-routers/xrd-77x/release/notes/b-release-notes-xrd-r771.html — initial XRd feature list (refs Cisco 8000 guides; no IPSLA/TWAMP).
- https://www.cisco.com/c/en/us/td/docs/routers/virtual-routers/xrd-78x/release/notes/b-release-notes-xrd-r781.html — vRouter GRE/PBR/BFD features; no IPSLA.
- https://www.cisco.com/c/en/us/td/docs/routers/virtual-routers/xrd-79x/release/notes/b-release-notes-xrd-r791.html — lineage statement (8000 CP / XRv9000 DP).
- https://www.cisco.com/c/en/us/td/docs/routers/virtual-routers/xrd-710x/release/notes/b-release-notes-xrd-r7101.html — no relevant features.
- https://www.cisco.com/c/en/us/td/docs/routers/virtual-routers/xrd-711x/release/notes/b-release-notes-xrd-r7112.html — no relevant features.
- https://www.cisco.com/c/en/us/td/docs/routers/virtual-routers/xrd-24xx/release/notes/b-release-notes-xrd-r2411.html — no PM/IPSLA rows.
- https://www.cisco.com/c/en/us/td/docs/routers/virtual-routers/xrd-24xx/release/notes/b-release-notes-xrd-r2431.html — "Delay Measurement Using Software Timestamp", timestamp-format NTP.
- https://www.cisco.com/c/en/us/td/docs/routers/virtual-routers/xrd-24xx/release/notes/b-release-notes-xrd-r2441.html — PM one-way far-end metrics; GRE delay with TWAMP test packets.
- https://www.cisco.com/c/en/us/td/docs/routers/virtual-routers/xrd-25xx/release/notes/b-release-notes-xrd-2511.html — maintenance; lineage statement.
- https://www.cisco.com/c/en/us/td/docs/routers/iosxr/release-notes/25xx/cisco-XRd-routers-release-notes-2542.html — no new features; AWS EKS + lab deployments.
- https://www.cisco.com/c/en/us/td/docs/routers/iosxr/release-notes/26xx/cisco-XRd-routers-release-notes-2631.html — security features only.
- https://www.cisco.com/c/en/us/support/routers/ios-xrd/series.html — XRd doc index (release notes through 26.3.1; AWS setup guide).
- https://www.cisco.com/c/en/us/products/collateral/routers/ios-xrd/ios-xrd-ds.html — CP vs vRouter, requirements, subscription licensing.
- https://xrdocs.io/virtual-routing/tutorials/2022-08-22-xrd-images-where-can-one-get-them/ — CCO download with entitled login.
- https://xrdocs.io/virtual-routing/tutorials/2022-08-23-xrd-with-docker-control-plane-and-vrouter — CP linux interfaces vs vRouter PCI/vfio.
- https://www.cisco.com/c/en/us/td/docs/routers/virtual-routers/xrv9k-24xx/release/notes/b-release-notes-xrv9000-r2421.html — XRv9000 lists IPSLA and TWAMP as supported.
- https://www.cisco.com/c/en/us/products/collateral/routers/ios-xrv-9000-router/datasheet-c78-734034.html — no IP SLA/TWAMP mention.
- https://containerlab.dev/manual/kinds/ ; /xrd/ ; /cisco_xrd_vrouter/ ; /vr-c8000v/ ; /vr-n9kv/ ; /cisco_iol/ — kind names, images, credentials, requirements.
- https://developer.cisco.com/docs/modeling-labs/iosv/ — IOSv: IP SLA supported. https://developer.cisco.com/docs/modeling-labs/iol/ — IOL is IOS-XE, no netconf/EEM.
NX-OS
- https://www.cisco.com/c/en/us/td/docs/dcn/nx-os/nexus9000/101x/configuration/ip-sla/cisco-nexus-9000-series-nx-os-ip-slas-configuration-guide-101x/m-configuring-twamp-responder.html — restrictions (10 control sessions, no TWAMP Light), CLI, show commands.
- https://www.cisco.com/c/en/us/td/docs/dcn/nx-os/nexus9000/105x/configuration/ip-sla/cisco-nexus-9000-series-nx-os-ip-slas-configuration-guide/m-configuring-twamp-responder.html (+ .pdf, text-extracted locally) — same in 10.5(x); no feature history.
- https://www.cisco.com/c/en/us/td/docs/switches/datacenter/nexus9000/sw/93x/ip-sla/configuration/guide/b-nexus-9000-series-nx-os-ip-slas-configuration-guide-93x/b-nexus-9000-series-nx-os-ip-slas-configuration-guide-93x_chapter_01101.html — show outputs, standards RFC5357.
- https://www.cisco.com/c/en/us/td/docs/switches/datacenter/nexus9000/sw/93x/ip-sla/configuration/guide/b-nexus-9000-series-nx-os-ip-slas-configuration-guide-93x/b-nexus-9000-series-nx-os-ip-slas-configuration-guide-93x_chapter_01.html — "No updates since 9.2(x)".
- https://www.cisco.com/c/en/us/td/docs/switches/datacenter/nexus9000/sw/7-x/ip_sla/configuration/guide/b_Cisco_Nexus_9000_Series_NX-OS_IP_SLAs_Configuration_Guide_7x.pdf (text-extracted locally) — no TWAMP chapter in 7.x; IP SLA initial 7.0(3)I2(1).
- https://www.cisco.com/c/en/us/td/docs/switches/datacenter/nexus7000/sw/ipslas/config/cisco_nexus7000_ipslas_config_guide_8x/cisco_nexus7000_ipslas_config_guide_8x_chapter_01101.html — N7k 8.3(1): timer defaults/ranges.
- https://www.cisco.com/c/en/us/td/docs/dcn/nx-os/nexus9000/105x/release-notes/cisco-nexus-9000-nxos-release-notes-1052F.html — CSCwk45686, CSCwm30659 resolved.
- https://www.cisco.com/c/en/us/td/docs/dcn/nx-os/nexus9000/103x/release-notes/cisco-nexus-9000-nxos-release-notes-1036.html — CSCwk45686 resolved.
- https://www.cisco.com/c/en/us/td/docs/dcn/nx-os/nexus9000/102x/release-notes/cisco-nexus-9000-nxos-release-notes-1029.html — no TWAMP mention.
- https://www.findbugzero.com/operational-defect-database/vendors/cisco/defects/CSCwa79808 ; .../CSCwk45686 ; .../CSCwm30659 — bug details (secondary mirror of Cisco bug text).
- https://www.cisco.com/c/en/us/td/docs/dcn/nx-os/nexus9000/102x/configuration/n9000v/cisco-nexus-9000v-guide-102x/m-cisco-nexus-9000v.html ; https://www.cisco.com/c/en/us/td/docs/dcn/nx-os/nexus9000/106x/configuration/n9000v-9300v-9500v/cisco-nexus-9000v-9300v-9500v-guide-release-106x/m-overview.html — N9Kv feature lists (no IP SLA/TWAMP).
- https://developer.cisco.com/docs/cisco-nexus-3000-and-9000-series-nx-api-rest-sdk-user-guide-and-api-reference-release-102x/configuring-ip-slas/ — slaTwampServer/slaTwampResponder adminState only.
Third-party
- https://www.stamp.rs/interop-twamp-light/ — TWAMP Light interop vs IOS XE 17.5.1 (local-port 863), TTL-255 and zero-padding observations.
Not retrievable (403/blocked): community threads td-p/4668474 (XR 7.3.2 vs Nokia TWAMP Light), td-p/5567299 (twamp-light IPv6), td-p/4130552 (IPSLA in XRv9); findbugzero CSCve09295 body; content.cisco.com CSR1000v chapter tag; Cisco 8000 24.x/7.11 System Monitoring IPSLA chapter and N9k 10.4 command reference (URL guesses 404).
