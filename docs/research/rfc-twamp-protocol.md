> AI research notes (2026-10-03). Extracted from the RFC texts, the IANA registries, the errata and the source of the perfSONAR reference implementation. For UNVERIFIED items see §13. The primary input to the specification (SPEC.md).

# TWAMP Protocol Family — Research Report for a Go Implementation Specification

## 0. Sources, method, notation

Everything below was taken from the RFC plain-text files downloaded from rfc-editor.org (RFC 5357, 4656, 5618, 5938, 6038, 7717, 7718, 7750, 8186, 8545, 8762, 8972, 9533, 2681, 3393, 7679, 7680, 5481, 6673, 4737, 3550, 5905, 8877), the IANA registries (twamp-parameters, stamp-tlv-types), the RFC Editor errata pages for every TWAMP-family RFC, and, for three points the RFC prose leaves ambiguous, the Internet2/perfSONAR reference implementation source (github.com/perfsonar/owamp, files owamp/owamp/owamp/{protocol.c,endpoint.c,io.c,sapi.c,capi.c,owampP.h}). Nothing in the octet tables comes from memory. Items I could not confirm from those texts are tagged **UNVERIFIED** and collected in §13.

Conventions: offsets are 0-based octet offsets within the UDP payload or the TCP message; all multi-octet integers are unsigned, network byte order (RFC 5357 §2, RFC 4656 §2). "Block" = 16 octets (AES block). "MBZ" = Must Be Zero: "MUST be set to zero by senders and MUST be ignored by receivers" (RFC 5357 §2, RFC 4656 §3.1). Citations are `RFC n §x`. Errata are cited as `EID n` (status in §12).

Important corrections you must apply over the raw RFC 5357 text (all **Verified** errata):

* Authenticated/encrypted Session-Reflector packet minimum is **112 octets, not 104** (EID 5045); the Session-Sender must therefore pad **≥ 64 octets, not 56**, in auth/enc modes to get symmetric sizes (EID 5046; RFC 6038 EIDs 6408, 6409, 6410, 5549).
* The reflector's Sequence Number is maintained **per test session** (EID 1590).
* The Command Number registry holds **256** values, not 16 (EID 1593).
* The auth/enc test-packet figures show the **plaintext** layout (EID 1589).

---

## 1. Architecture and roles

Roles (RFC 4656 §1.2, as modified by RFC 5357 §1.2):

| Role | Definition |
|---|---|
| Control-Client | Initiates requests for test sessions, triggers start and (in TWAMP, exclusively) stop. |
| Server | Manages sessions, configures per-session state in endpoints. TWAMP: cannot return results. |
| Session-Sender | Sending endpoint of TWAMP-Test. |
| Session-Reflector | Replaces OWAMP's Session-Receiver: "has the capability to create and send a measurement packet when it receives a measurement packet… does not collect any packet information." |
| Fetch-Client | Does not exist in TWAMP (RFC 5357 §1.2, §3.9). |

Two-host deployment: the host that initiates the TCP connection is Control-Client + Session-Sender; the host that accepts is Server + Session-Reflector (RFC 5357 §3.1). TWAMP Full = TWAMP-Control (TCP) + TWAMP-Test (UDP); RFC 8545 §4: "the implementation of both TWAMP-Control and TWAMP-Test is REQUIRED for Standards Track TWAMP". TWAMP Light = TWAMP-Test with an unspecified control mechanism (RFC 5357 Appendix I; RFC 8545 §4, Appendix A).

---

## 2. TWAMP-Control

### 2.1 Transport and general rules

* Control-Client opens TCP to well-known port **862** (RFC 5357 §3.1; IANA table in RFC 8545 §7: `twamp-control 862/tcp`). OWAMP uses 861.
* "The type of each OWAMP-Control message can be found after reading the first 16 octets. The length of each OWAMP-Control message can be computed upon reading its fixed-size part. No message is shorter than 16 octets." (RFC 4656 §3). Practical rule: read 16 octets, dispatch on octet 0 (client→server commands) or on the expected server reply; read the remainder.
* State expunging: "if the full control message is not received within some number of minutes after it is expected, the TCP connection… SHOULD be dropped. In absence of other considerations, 30 minutes seems like a reasonable upper bound." (RFC 4656 §3).
* DSCP on the control connection: Control-Client MAY set a DSCP for all packets of a connection; Server SHOULD use the DSCP of the Control-Client's TCP SYN in all subsequent packets (RFC 5357 §3.1).
* The two extension mechanisms are the Modes field and the Command Number (RFC 5357 §2).

### 2.2 Server Greeting (64 octets) — RFC 4656 §3.1

| Offset | Len | Field | Notes |
|---|---|---|---|
| 0 | 12 | Unused | zero on send; ignore on receipt |
| 12 | 4 | Modes | bitwise OR of modes the server is willing to support; see §2.3. `Modes == 0` ⇒ server does not wish to talk and MAY close; client SHOULD close. |
| 16 | 16 | Challenge | random; server "never issues the same challenge twice" (RFC 4656 §6.10) |
| 32 | 16 | Salt | MUST be generated pseudo-randomly |
| 48 | 4 | Count | PBKDF2 iteration count. MUST be a power of 2, MUST be ≥ 1024 (RFC 4656 §3.1). RFC 5357 §6: implementations SHOULD limit the maximum accepted Count, default maximum SHOULD be 32768; a client receiving Count > its max SHOULD close. (Reference implementation default: 2048.) |
| 52 | 12 | MBZ | |

### 2.3 Modes bitmask (IANA "TWAMP-Modes", indexed by bit position since RFC 7717 §7)

| Bit | Value | Meaning | Reference |
|---|---|---|---|
| 0 | 1 | Unauthenticated | RFC 4656 §3.1 |
| 1 | 2 | Authenticated | RFC 4656 §3.1 |
| 2 | 4 | Encrypted | RFC 4656 §3.1 |
| 3 | 8 | Unauthenticated TEST, Encrypted CONTROL (mixed) | RFC 5618 §3.1 |
| 4 | 16 | Individual Session Control | RFC 5938 §3.1 |
| 5 | 32 | Reflect Octets Capability | RFC 6038 §7.2 |
| 6 | 64 | Symmetrical Size Sender Test Packet Format | RFC 6038 §7.2 |
| 7 | 128 | IKEv2Derived Mode Capability | RFC 7717 §7 |
| 8 | 256 | DSCP and ECN Monitoring Capability | RFC 7750 §3 |
| 9 | 512 | PTPv2 Timestamp Capability | RFC 8186 §3 |
| 10–31 | — | Unassigned (IANA, "IETF Review") | |

Rules: Server advertises the OR of what it supports. Client's Set-Up-Response Mode: exactly one security bit among {0,1,2,3} ("one or zero bits MUST be set within last three bits", RFC 4656 §3.1; RFC 5938 §3.1 extends to "bits 0, 1, 2, or 3"), optionally plus feature bits 4–9 that the server offered (RFC 6038 §4.1: "the Control-Client MAY set multiple Mode field bits"). Zero security bits ⇒ client will not continue; both SHOULD close (RFC 4656 §3.1). Receivers MUST ignore bits they do not understand (RFC 4656 §3.1: "A client MUST ignore the values in the first 29 bits"; RFC 5938 §3.7, RFC 6038 §4.4 reaffirm MAY ignore higher bits). Bit 3 semantics: control protocol is encrypted (identical to authenticated/encrypted for control), test protocol uses the unauthenticated packet format; when bit 3 is set the client does not also set bits 0–2 (RFC 5618 §3.1 table: Control "Auth == Encrypted (1,2,3)", Test "Unauth (3) / Auth (1) / Encrypted (2)").

### 2.4 Set-Up-Response (164 octets) — RFC 4656 §3.1

| Offset | Len | Field | Notes |
|---|---|---|---|
| 0 | 4 | Mode | chosen mode bits (see §2.3) |
| 4 | 80 | KeyID | UTF-8 string ≤ 80 octets, zero-padded; selects the shared secret. Unused (zero) in unauthenticated mode. RFC 7717: first 8 octets SPIi, next 8 SPIr, rest zero. |
| 84 | 64 | Token | AES-CBC(IV = 0, key = PBKDF2(passphrase)) of plaintext `Challenge(16) ‖ AES Session-key(16) ‖ HMAC Session-key(32)`. Unused in unauthenticated mode. |
| 148 | 16 | Client-IV | random; MUST never repeat for the same secret key; IV for the client→server CBC stream. Unused in unauthenticated mode. |

Not a multiple of 16 octets; always sent in the clear; **not** covered by any HMAC (see §3.3).

### 2.5 Server-Start (48 octets) — RFC 4656 §3.1

| Offset | Len | Field | Notes |
|---|---|---|---|
| 0 | 15 | MBZ | |
| 15 | 1 | Accept | 0 = accepted; non-zero = server unwilling; server MAY and client SHOULD close |
| 16 | 16 | Server-IV | random; IV for the server→client CBC stream; unused in unauthenticated mode |
| 32 | 8 | Start-Time | NTP timestamp of when this server instantiation started; SHOULD be identical for every client/session; SHOULD be all zeros if Accept ≠ 0 |
| 40 | 8 | MBZ | |

In authenticated/encrypted/mixed control: octets 0–31 are sent in the clear; octets 32–47 (one block) are encrypted with the Server-IV stream and are the first bytes covered by the server's HMAC stream (RFC 4656 §3.1 "Start-Time is encrypted as described in Section 3.4 unless Accept is non-zero"; RFC 5357 §3.2; confirmed in reference `_OWPWriteServerStart`: `_OWPSendHMACAdd(&buf[32],1)` then `_OWPEncryptBlocks(&buf[32],1)`). No HMAC field in this message.

### 2.6 Request-TW-Session (112 octets) — RFC 4656 §3.5 layout, RFC 5357 §3.5 semantics, RFC 6038 §4.2 fields

| Offset | Len | Field | TWAMP value / notes |
|---|---|---|---|
| 0 | 1 | Command Number | **5** (RFC 5357 §3.5). Server MUST interpret as a two-way session request. |
| 1 | 1 | MBZ (high nibble) / IPVN (low nibble) | IPVN = 4 or 6 |
| 2 | 1 | Conf-Sender | **MUST be 0**; non-zero ⇒ server MUST answer Accept = 3 |
| 3 | 1 | Conf-Receiver | **MUST be 0**; same rule |
| 4 | 4 | Number of Schedule Slots | **MUST be 0** |
| 8 | 4 | Number of Packets | **MUST be 0** |
| 12 | 2 | Sender Port | UDP port from which the Session-Sender sends and on which it receives reflected packets |
| 14 | 2 | Receiver Port | UDP port on which the Session-Reflector is asked to receive, and from which it sends |
| 16 | 16 | Sender Address | IPv4: 4 octets at 16–19, octets 20–31 MUST be zero / ignored; IPv6: 16 octets. MAY be 0 ⇒ the TWAMP-Control addresses MUST be used for test packets. |
| 32 | 16 | Receiver Address | same encoding as Sender Address |
| 48 | 16 | SID | **MUST be 0** in the request; server generates the SID |
| 64 | 4 | Padding Length | octets of Packet Padding the Session-Sender appends to every test packet |
| 68 | 8 | Start Time | NTP timestamp; session starts no earlier than this and never before Start-Sessions |
| 76 | 8 | Timeout | NTP-format interval (seconds.fraction). TWAMP meaning: how long after Stop-Sessions the reflector keeps reflecting in-flight packets; MUST NOT reflect packets arriving later. |
| 84 | 4 | Type-P Descriptor | Only DSCP capability in TWAMP: first two bits 00, next six bits = DSCP (RFC 4656 §3.5), i.e. octet 84 = DSCP value (0–63), octets 85–87 zero; all-zero = best effort. Reflector MUST use the same DSCP. (OWAMP's `01`+PHB-ID form is not used in TWAMP.) |
| 88 | 2 | Octets to be reflected (RFC 6038) / MBZ otherwise | echoed in Accept-Session "Reflected octets" |
| 90 | 2 | Length of padding to reflect (RFC 6038) / MBZ otherwise | octets of the test-packet padding the reflector must return verbatim |
| 92 | 4 | MBZ | |
| 96 | 16 | HMAC | covers octets 0–95 (see §3.3); zero in unauthenticated mode |

There are no schedule-slot records and only one HMAC (RFC 5357 §3.5). (The RFC 6038 §4.2 figure says "66 octets not shown" between offset 8 and Padding Length; by the RFC 4656 layout that gap is 56 octets — **UNVERIFIED** figure annotation; the RFC 4656 offsets above are authoritative.)

Server validation rules: unexpected Command Number ⇒ MUST respond Accept-Session with Accept = 3 (RFC 5357 §3.5); Conf-* non-zero ⇒ Accept = 3; Receiver Port unavailable ⇒ MAY suggest an alternate port in Accept-Session with Accept = 0 (client accepts it or sends a new request); any other rejection ⇒ Port MUST be 0 and MUST NOT suggest an alternate (RFC 5357 §3.5 as corrected by EID 1587, restated in RFC 8545 §5.1). RFC 6038 §4.3: if padding arithmetic cannot yield equal-length packets, Accept MUST be 3. Rejecting a request SHOULD NOT close the TCP connection; client MAY close (RFC 4656 §3.5).

### 2.7 Accept-Session (48 octets) — RFC 4656 §3.5, RFC 6038 §4.3

| Offset | Len | Field | Notes |
|---|---|---|---|
| 0 | 1 | Accept | 0 = session accepted |
| 1 | 1 | MBZ | |
| 2 | 2 | Port | Accept = 0: the port where the reflector expects to receive (repeats requested Receiver Port or an alternate). Accept ≠ 0: MUST be 0. |
| 4 | 16 | SID | server-generated; SHOULD be `IPv4 addr(4) ‖ 8-octet timestamp ‖ 4 random octets`; use an IPv4 address even for IPv6 sessions if any exists, else last 4 octets of an IPv6 address MAY be used; MUST be unpredictable if used for access control (RFC 4656 §3.5) |
| 20 | 2 | Reflected octets (RFC 6038) / MBZ | copy of request's "Octets to be reflected" |
| 22 | 2 | Server octets (RFC 6038) / MBZ | non-zero ⇒ the Session-Sender MUST place these 2 octets at the start of the Packet Padding (to be reflected) field; MUST be zero if the server wants nothing returned |
| 24 | 8 | MBZ | |
| 32 | 16 | HMAC | |

### 2.8 Start-Sessions (32) and Start-Ack (32) — RFC 4656 §3.7

Start-Sessions: `0: 2 | 1–15: MBZ | 16–31: HMAC`. Start-Ack: `0: Accept | 1–15: MBZ | 16–31: HMAC`. Server MUST respond with Start-Ack, SHOULD send it as quickly as possible; Accept ≠ 0 ⇒ server MAY, client SHOULD close. Sessions start immediately after the Start-Ack (sender side: after it sees Start-Ack) or at their Start Time, whichever is later. Start-Sessions applies to all accepted, not-yet-started sessions.

### 2.9 Stop-Sessions (32, TWAMP form) — RFC 5357 §3.8

| Offset | Len | Field | Notes |
|---|---|---|---|
| 0 | 1 | Command Number | **3** |
| 1 | 1 | Accept | 0 = normal (possibly premature) completion; non-zero ⇒ results of all sessions SHOULD be considered invalid |
| 2 | 2 | MBZ | |
| 4 | 4 | Number of Sessions | MUST equal the number of sessions started and not yet stopped; mismatch ⇒ message invalid, connection SHOULD be closed, results invalid |
| 8 | 8 | MBZ | |
| 16 | 16 | HMAC | |

Differences from OWAMP: only the Control-Client may send it; it MUST NOT carry session description records or skip ranges; applies to all started sessions. There is no acknowledgement to Stop-Sessions in TWAMP (contrast RFC 5938's Stop-N-Ack). On receipt the reflector MUST discard packets arriving after now + Timeout and MUST reflect those arriving within it (RFC 5357 §3.5, §3.8, §4.2). If Stop-Sessions is never transmitted (e.g., TCP failure) results MAY be considered invalid.

### 2.10 Fetch-Session — not used in TWAMP (RFC 5357 §3.4, §3.9); command 4 is Reserved in the TWAMP registry.

### 2.11 Individual Session Control (RFC 5938, mode bit 4 = 16)

When negotiated, commands 2 and 3 MUST NOT be used; Start-N-Sessions/Stop-N-Sessions and their acks are used instead. Multiple Start-N-Sessions MAY be outstanding and may be processed in any order. Server/reflector MUST manage sessions by SID. Server MAY split its acknowledgement into several acks grouping SIDs by Accept code (example in §3.3: `Accept=0, N=3, SIDs 1,2,4` and `Accept=5, N=1, SID 3`). SERVWAIT is suspended while ANY session is in progress (§3.6). REFWAIT SHOULD be enforced while any session is in progress (§4.2).

| Message | Cmd | Offset 0 | 1 | 2–3 | 4–11 | 12–15 | 16 + 16·k | last 16 |
|---|---|---|---|---|---|---|---|---|
| Start-N-Sessions | 7 | 7 | MBZ (1–11) | | | Number of Sessions (≥1) | SID k (16 octets each) | HMAC |
| Start-N-Ack | 8 | 8 | Accept | MBZ | MBZ (8) | Number of Sessions (≥1) | SID k | HMAC |
| Stop-N-Sessions | 9 | 9 | MBZ (1–11) | | | Number of Sessions (≥1) | SID k | HMAC |
| Stop-N-Ack | 10 | 10 | Accept | MBZ | MBZ (8) | Number of Sessions (≥1) | SID k | HMAC |

Total length = 32 + 16·N. The count of SIDs MUST equal Number of Sessions (else the Start-N-Sessions MUST NOT be affirmed with Accept 0). After Stop-N-Sessions for a SID, the reflector MUST ignore (and MUST NOT answer) that session's packets arriving after now + Timeout.

### 2.12 Accept field values (RFC 4656 §3.3; RFC 5357 §3.3 adopts unchanged)

| Value | Meaning |
|---|---|
| 0 | OK |
| 1 | Failure, reason unspecified (catch-all) |
| 2 | Internal error |
| 3 | Some aspect of request is not supported |
| 4 | Cannot perform request due to permanent resource limitations |
| 5 | Cannot perform request due to temporary resource limitations |

All other values reserved; senders MAY use 1 for every failure; receivers MUST interpret any other value as 1. There is **no IANA registry** for Accept values (the IANA TWAMP page has only Command Numbers and Modes). RFC 7717 §5.3 has the server SHOULD set Accept = **6** in Server-Start when the IKEv2 SA for the SPIs cannot be found — a value outside the RFC 4656 list that non-7717 receivers will interpret as 1 (**flag in spec**).

### 2.13 TWAMP-Control Command Numbers (IANA, "IETF Review", 0–255 per EID 1593)

| Value | Command | Ref |
|---|---|---|
| 0 | Reserved | RFC 5357 |
| 1 | Forbidden (OWAMP Request-Session) | RFC 5357 |
| 2 | Start-Sessions | RFC 4656 §3.7 |
| 3 | Stop-Sessions | RFC 4656 §3.8 / RFC 5357 §3.8 |
| 4 | Reserved (OWAMP Fetch-Session) | RFC 5357 |
| 5 | Request-TW-Session | RFC 5357 §3.5 |
| 6 | Experimentation | RFC 5357 §8.3 |
| 7 | Start-N-Sessions | RFC 5938 §3.2 |
| 8 | Start-N-Ack | RFC 5938 §3.3 |
| 9 | Stop-N-Sessions | RFC 5938 §3.4 |
| 10 | Stop-N-Ack | RFC 5938 §3.5 |
| 11 | Request-TW-Micro-Sessions | RFC 9533 §4.1 |
| 12–255 | Unassigned | |

### 2.14 Timers

| Timer / field | Owner | Default | Rule |
|---|---|---|---|
| SERVWAIT | Server | 900 s (SHALL), MAY be configurable | Server MAY close a control connection with no packets for SERVWAIT; monitoring SHALL be suspended after Start-Sessions and SHALL resume after Stop-Sessions, or when REFWAIT expires on ALL sessions of that connection (RFC 5357 §3.1; RFC 5938 §3.6 for per-SID mode). Implementations supporting SERVWAIT SHOULD also implement REFWAIT. |
| REFWAIT | Session-Reflector | 900 s (SHALL), MAY be configurable | Reflector MAY discontinue a started session with no packets for REFWAIT (RFC 5357 §4.2). |
| Timeout (Request-TW-Session) | Reflector | per request | grace window after Stop-Sessions (§2.9). |
| Start Time | both | — | start at max(Start-Ack time, Start Time). |
| Partial-message timeout | both | ~30 min upper bound | drop TCP if a full message is not received (RFC 4656 §3). |
| Count maximum | client (and server policy) | 32768 (SHOULD) | client SHOULD close if greeting Count exceeds its max (RFC 5357 §6). |

### 2.15 Message sizes (octets)

Server Greeting 64 · Set-Up-Response 164 · Server-Start 48 · Request-TW-Session 112 · Accept-Session 48 · Start-Sessions 32 · Start-Ack 32 · Stop-Sessions 32 · Start-N-Sessions / Start-N-Ack / Stop-N-Sessions / Stop-N-Ack 32 + 16·N. All encrypted messages are block multiples; Set-Up-Response is the only non-multiple and is always cleartext.

---

## 3. Control-protocol security (modes 2, 4, 8 and RFC 7717)

### 3.1 Key hierarchy (RFC 4656 §3.1, §6.10; RFC 5357 §3.1)

1. Shared secret = passphrase, selected by KeyID. Encoding: RFC 5198 Appendix B (NVT ASCII), MUST NOT contain CR/LF, control characters SHOULD be avoided (RFC 5357 §3.1). Both sides keep the same KeyID→secret mapping.
2. `K_pf = PBKDF2(PRF = HMAC-SHA1, password = passphrase, salt = Salt(16), c = Count, dkLen = ?)`. **The RFC does not state dkLen** (UNVERIFIED in RFC text). The reference implementation uses `dkLen = 16` (`io.c`: `I2pbkdf2(I2HMACSha1, 20, pf, pf_len, salt, 16, count, sizeof(dk)=16, dk)`) and the token is encrypted with AES-128, so **16 octets is the de facto standard**; use it.
3. Client generates random AES Session-key (16) and HMAC Session-key (32) ("SHOULD be derived independently"), and Client-IV (16, unique per session under the same secret).
4. `Token = AES-128-CBC_{K_pf, IV=0}( Challenge ‖ AES Session-key ‖ HMAC Session-key )` (64 octets = 4 blocks).
5. Server decrypts Token with its own `K_pf`, checks the recovered Challenge equals the one it sent; failure ⇒ Server-Start with Accept ≠ 0 (reference: Accept 1 "failure").
6. Time is deliberately not used as salt (RFC 4656 §6.9).

### 3.2 Control stream encryption (RFC 4656 §3.4)

"In authenticated or encrypted mode (which are identical as far as OWAMP-Control is concerned…), all further communications are encrypted with the AES Session-key (using CBC mode) and authenticated with HMAC Session-key. The client encrypts everything it sends… using stream encryption with Client-IV as the IV. Correspondingly, the server encrypts its side… using Server-IV as the IV. The IVs themselves are transmitted in cleartext. Encryption starts with the block immediately following the block containing the IV. The two streams… are encrypted independently, each with its own IV, but using the same key." Mixed mode (bit 3) applies this to control as well (RFC 5618 §3.1; RFC 7717 §4.1).

Concretely: one persistent AES-CBC encryptor per direction, chaining state carried across messages (in Go, a single `cipher.BlockMode` instance per direction gives this). Server stream begins with Server-Start octets 32–47; client stream begins with the first octet of the first command (Request-TW-Session). HMAC fields are encrypted along with everything else in the control stream ("in authenticated and encrypted modes, the HMAC in TWAMP-Control packets is encrypted", RFC 5357 §3.2). "When encrypting, authentication happens before encryption… When decrypting, first one decrypts, then one checks the HMAC" (RFC 4656 §3.2). Message length is known after decrypting the first block (RFC 4656 §6.10).

### 3.3 HMAC coverage

HMAC = HMAC-SHA1 truncated to 128 bits (first 16 octets of the 20-octet digest), keyed with the 32-octet HMAC Session-key (RFC 4656 §3.2). "Each HMAC sent covers everything sent in a given direction between the previous HMAC (but not including it) and up to the beginning of the new HMAC." (RFC 4656 §3.2; RFC 5357 §3.2). HMAC MUST be checked as early as possible; a message whose HMAC fails MUST be discarded and the control connection MUST be dropped (RFC 4656 §6.10). In unauthenticated mode HMAC fields are MBZ.

Where the RFC prose is ambiguous about the very first HMAC in each direction, the reference implementation does this (verified in `protocol.c`):

* Server→client: the first HMAC (in the first Accept-Session) covers **only the Start-Time block of Server-Start (octets 32–47)** plus Accept-Session octets 0–31. Server-Start octets 0–31 (MBZ, Accept, Server-IV) are neither HMAC'd nor encrypted. This matches RFC 5357 §3.2 ("the HMAC in the first Accept-Session message also covers the Server-Start message and includes the Start-Time field").
* Client→server: the first HMAC (in Request-TW-Session) covers **only Request-TW-Session octets 0–95**. The Set-Up-Response is **not** fed into the HMAC (`_OWPWriteSetupResponse` adds nothing to the HMAC context). Treat this as the interoperability baseline; note it as an RFC ambiguity in the spec.
* Thereafter every message's HMAC covers that message's plaintext from octet 0 to the HMAC field; for RFC 5938 messages the digest covers header + all SID blocks.

### 3.4 Test-session keys (RFC 5357 §4.2.1; RFC 4656 §4.1.2)

* TWAMP-Test AES Session-key = `AES-ECB_{key = SID}(TWAMP-Control AES Session-key)` — single-block ECB, SID is the key, control key is the plaintext. 16 octets.
* TWAMP-Test HMAC Session-key = `AES-CBC_{key = SID, IV = 0}(TWAMP-Control HMAC Session-key)` — two-block CBC. 32 octets.
* "the key schedule for each TWAMP-Test session MUST be set up at most once per session, not once per packet" (RFC 5357 §4.2.1). The same AES key is used for both directions (reference implementation only keeps separate encrypt/decrypt key schedules of the same bytes).
* The SID also seeds OWAMP's AES-counter-mode PRNG for Poisson schedules; TWAMP has no schedule, so this PRNG is not needed (RFC 5357 §3.6).

### 3.5 Mixed mode (RFC 5618, value 8)
Control: encrypted exactly as in authenticated/encrypted modes (Set-Up-Response carries KeyID/Token/Client-IV). Test: unauthenticated packet format, no crypto. Session-Sender and Session-Reflector MUST support it when negotiated (§4.1.2, §4.2). Motivation: strong protection for low-rate control while keeping accurate/efficient test packets.

### 3.6 IKEv2-derived shared secret (RFC 7717, bit 7 = 128)
Set together with a security bit. Shared secret = `prf(SK_d, "IPPM")` from the IKEv2 SA (not the child SA); keep using it across IKE rekey/delete until the session ends. KeyID = SPIi(8) ‖ SPIr(8) ‖ zeros; server MUST take SPIs from the first 16 octets and ignore the rest; unknown SA ⇒ MUST log, SHOULD answer Server-Start Accept = 6. Everything else (PBKDF2 with Salt/Count, token, IVs) is unchanged; the derived secret simply replaces the passphrase.

### 3.7 Other security requirements
* Server never repeats a Challenge; random IVs required (RFC 4656 §6.10).
* Count DoS: see §2.2 (RFC 5357 §6).
* Servers MUST have mechanisms limiting network capacity and memory use, enabled with conservative defaults (RFC 4656 §6.5).
* Third-party DoS: OWAMP servers MUST by default decline requests whose Receiver Address is not the control peer or the server itself (RFC 4656 §6.2); for TWAMP the analogous protection is that reflected packets go only to the Sender Address/Port, which by default should equal the control-connection peer (**interpretation**, RFC 5357 does not restate the rule). Address spoofing is not permitted.
* Modes = 0 means the server rejects the client (§2.2). Downgrade attacks on Modes negotiation are acknowledged (RFC 4656 §6.7).

---

## 4. TWAMP-Test

### 4.1 Transport and IP-layer rules
* UDP; Session-Sender uses Sender Port for both directions, Session-Reflector uses Receiver Port for both (RFC 5357 §3.5).
* Session-Sender SHOULD set IPv4 TTL / IPv6 Hop Limit to 255 (RFC 4656 §4.1.2); Session-Reflector SHOULD set 255 on reflected packets (RFC 5357 §4.2.1).
* Reflected packets MUST carry the same DSCP as the Type-P descriptor / received packet (RFC 5357 §3.5); RFC 7750 refines: transmit with the provisioned DSCP; if unknown (TWAMP Light) MAY copy the received DSCP or MAY use CS0; ECN SHOULD be Not-ECT unless provisioned.
* Well-known UDP port 862 (RFC 8545): optional default Receiver Port for TWAMP-Test/TWAMP Light; a Control-Client MAY request it and a Server not supporting it answers with an alternate port and Accept 0; Dynamic-range ports avoid conflicts. See §6.

### 4.2 Timestamp and Error Estimate formats

NTP 64-bit timestamp (RFC 4656 §4.1.2, RFC 5357 §4.2.1; RFC 8877 §4.2.1): octets 0–3 integer seconds since 0h 1 January 1900 UTC (RFC 5905 §6: prime epoch of era 0), octets 4–7 fraction in units of 2^-32 s. Unix conversion: `ntp_sec = unix_sec + 2208988800` (RFC 5905 code constant `JAN_1970 2208988800UL`), `frac = round(nsec · 2^32 / 10^9)`; wraps in 2036 (RFC 8877). Affected by leap seconds.

PTPv2 truncated timestamp (RFC 8186; format per RFC 8877 §4.3): octets 0–3 seconds, octets 4–7 nanoseconds (0…10^9−1); epoch 1 January 1970 00:00:00 **TAI**; not affected by leap seconds; wraps in 2106. Converting from UTC requires the current TAI−UTC offset (37 s since 2017-01-01 — **UNVERIFIED at build time**, take from the system leap-second table).

Error Estimate (16 bits, RFC 4656 §4.1.2): `S(1) | Z(1) | Scale(6) | Multiplier(8)`; i.e. `S = v & 0x8000`, `Z = v & 0x4000`, `Scale = (v >> 8) & 0x3F`, `Multiplier = v & 0xFF`; error = `Multiplier · 2^-32 · 2^Scale` seconds. S SHOULD be set iff the clock is synchronized to UTC via an external source (GPS, NTP synced to an external source…); Z was MBZ in RFC 4656 and is redefined by RFC 8186 §2.3 as **Timestamp format: 0 = NTP 64-bit, 1 = PTPv2 truncated**. Multiplier MUST NOT be 0; a packet with Multiplier 0 SHOULD be considered corrupt and discarded. (Reference implementation encodes "unknown/invalid error" as Multiplier 0xFF, Scale 0x3F.) RFC 8186 §2.3: when Z is used, Sender/Receive Error Estimates SHOULD NOT be ignored and MUST be used when computing delay metrics.

RFC 8186 negotiation (bit 9 = 512): Server sets the bit if the reflector can timestamp in PTPv2; Control-Client sets the bit in Set-Up-Response to make the reflector use PTPv2 (EID 8662, Held for Document Update: the client may also leave it 0 if its sender cannot/does not want PTP); if 0 the reflector MUST use NTP, and if it cannot the Server MUST close the connection. The Session-Sender's own format is a local decision signalled per packet by Z. Endpoints supporting the extension MUST be able to interpret both formats. TWAMP Light: configuration (RFC 8186 §2.3.1).

### 4.3 Session-Sender packet formats (RFC 4656 §4.1.2 via RFC 5357 §4.1.2)

Unauthenticated (minimum 14 octets):

| Offset | Len | Field |
|---|---|---|
| 0 | 4 | Sequence Number (starts at 0, +1 per packet, per session) |
| 4 | 8 | Timestamp (best approximation of real departure time — MUST) |
| 12 | 2 | Error Estimate |
| 14 | Padding Length | Packet Padding |

Authenticated / Encrypted plaintext layout (minimum 48 octets):

| Offset | Len | Field | Crypto |
|---|---|---|---|
| 0 | 4 | Sequence Number | block 0: encrypted in both modes |
| 4 | 12 | MBZ | block 0 |
| 16 | 8 | Timestamp | block 1: cleartext in authenticated, encrypted in encrypted mode |
| 24 | 2 | Error Estimate | block 1 |
| 26 | 6 | MBZ | block 1 |
| 32 | 16 | HMAC | never encrypted; covers block 0 (auth) or blocks 0–1 (enc) |
| 48 | Padding Length | Packet Padding | never covered, never encrypted |

### 4.4 Session-Reflector packet formats (RFC 5357 §4.2.1)

Unauthenticated (minimum 41 octets):

| Offset | Len | Field | Source |
|---|---|---|---|
| 0 | 4 | Sequence Number | reflector's own per-session counter from 0 (EID 1590); TWAMP Light without session state MUST copy the sender's sequence number (Appendix I) |
| 4 | 8 | Timestamp | reflector transmit time, taken as late as possible before send (MUST) |
| 12 | 2 | Error Estimate | reflector's; applies to both its timestamps |
| 14 | 2 | MBZ | |
| 16 | 8 | Receive Timestamp | reflector arrival time (MUST be best approximation) |
| 24 | 4 | Sender Sequence Number | copy of received Sequence Number |
| 28 | 8 | Sender Timestamp | exact copy |
| 36 | 2 | Sender Error Estimate | exact copy |
| 38 | 2 | MBZ | |
| 40 | 1 | Sender TTL | TTL/Hop Limit read from the received IP header (SHOULD); if the implementation cannot read it, MUST be 255 |
| 41 | var | Packet Padding | see §4.5 |

Authenticated / Encrypted plaintext layout (minimum **112** octets — EID 5045):

| Offset | Len | Field | Crypto |
|---|---|---|---|
| 0 | 4 | Sequence Number | block 0 |
| 4 | 12 | MBZ | block 0 |
| 16 | 8 | Timestamp | blocks 1–5: clear in auth, encrypted in enc |
| 24 | 2 | Error Estimate | |
| 26 | 6 | MBZ | |
| 32 | 8 | Receive Timestamp | |
| 40 | 8 | MBZ | |
| 48 | 4 | Sender Sequence Number | |
| 52 | 12 | MBZ | |
| 64 | 8 | Sender Timestamp | |
| 72 | 2 | Sender Error Estimate | |
| 74 | 6 | MBZ | |
| 80 | 1 | Sender TTL | |
| 81 | 15 | MBZ | |
| 96 | 16 | HMAC | covers block 0 (auth) or blocks 0–5 = octets 0–95 (enc); never encrypted |
| 112 | var | Packet Padding | never covered/encrypted |

### 4.5 Padding, truncation, sizes

* Padding SHOULD be pseudo-random, generated independently of other PRNGs; implementations MUST offer an all-zeros option (RFC 5357 §4.2.1).
* Padding MUST NOT be covered by the HMAC and MUST NOT be encrypted.
* Because the reflector header is larger (41 vs 14; 112 vs 48), the Session-Sender MAY append padding so both directions have equal IP payload: at least **27** octets unauthenticated, at least **64** authenticated/encrypted (EID 5046 corrects 56). The Session-Reflector SHOULD reduce the sender's padding to equalize lengths when sufficient padding is present; it MAY reuse the sender's padding octets and SHOULD truncate by discarding the highest-numbered octets (RFC 5357 §4.2.1). RFC 6038 makes this exact when its modes are negotiated: truncate exactly 27 / 64 octets (EID 6410).
* Sizes table (RFC 6038 §5.1.3 as corrected by EID 6408): Reflector header 41 / 112; Sender header 14 / 48; truncated padding 27 / 64.
* "Reflector packet must be at least as large as sender's" is not literally stated; the operative rules are the minimum sizes above plus the RFC 6038 Symmetrical Size requirement that reflector length SHALL equal sender length (§5.2.2).

### 4.6 Reflector behaviour (RFC 5357 §4.2, §4.2.1)

Per received packet: (1) take Receive Timestamp; (2) in auth/enc decrypt and verify HMAC (discard on failure — RFC 4656 §4.2 "packets for which authentication fails MUST be discarded"; reference implementation logs and ignores); (3) copy Sender Sequence Number, Sender Timestamp, Sender Error Estimate; (4) read TTL/Hop Limit from the IP header into Sender TTL (else 255); (5) build reply, take Timestamp immediately before transmission; in auth/enc compute HMAC first, then encrypt (MUST); in encrypted mode "fetch the timestamps, generate the HMAC, and encrypt the packet, then send it" (MUST); (6) transmit "as immediately as possible" (MUST respond to every received packet; SHOULD as quickly as possible). No send schedule exists; the reflector never originates packets. Duplicates received are reflected like any packet (each received packet gets a response; the reflector keeps no records). After Stop-Sessions + Timeout, or after REFWAIT, packets MUST be ignored and MUST NOT be answered. Packets arriving before Start-Sessions: not addressed by the RFC (**interpretation**: the session is not "started", so do not reflect).

### 4.7 Test-packet cryptography summary

| Mode | Sender pkt encrypted | Sender pkt HMAC covers | Reflector pkt encrypted | Reflector pkt HMAC covers |
|---|---|---|---|---|
| Unauthenticated | none | none (no HMAC field) | none | none |
| Authenticated | octets 0–15, AES-ECB (single block) | octets 0–15 | octets 0–15, AES-ECB | octets 0–15 |
| Encrypted | octets 0–31, AES-CBC, IV = 0, one chain per packet | octets 0–31 | octets 0–95, AES-CBC, IV = 0, one chain per packet | octets 0–95 |

Sources: RFC 4656 §4.1.2 (sender rules, which RFC 5357 §4.2.1 adopts for Session-Sender packets); RFC 5357 §4.2.1 for the reflector ("in authenticated mode, HMAC covers the first block (16 octets); in encrypted mode, HMAC covers the first six blocks (96 octets)… the HMAC field MUST NOT be encrypted"). "The initialization vector for the CBC encryption is a value with all bits equal to zero… chaining does not span multiple packets." HMAC is computed over plaintext before encryption. RFC 5357 §4.2 bullet 2 says the reflector decrypts "first block (16 octets) for authenticated, 96 octets for encrypted" of the received packet; since a received Session-Sender packet has only 32 encrypted octets per the OWAMP sender rule, this sentence is inconsistent. The reference implementation (`run_reflector`) decrypts 16 (auth) / **32** (enc) octets of received sender packets and encrypts 16 / 96 octets of its replies; the Session-Sender (`run_tw_test`) decrypts 16 / 96 of received reflector packets. Adopt those numbers and flag the RFC sentence.

### 4.8 Reflect Octets and Symmetrical Size (RFC 6038, bits 5 = 32 and 6 = 64)

Reflect Octets, unauthenticated Session-Sender packet: `0–13` standard header; `14 … 14+L−1` "Packet Padding (to be reflected)" where L = Length of padding to reflect; if Accept-Session carried non-zero Server octets, octets 14–15 MUST be those Server octets; then "Additional Packet Padding" of length `Padding Length − L`. Constraints (EID 6409): Padding Length SHALL be > L; with truncation Padding Length SHALL be ≥ 27 + L (unauth) or ≥ 64 + L (auth/enc).

Reflect Octets, unauthenticated Session-Reflector packet (EID 4260 confirms it is the reflector's format): `0–40` standard reflector header; `41 … 41+L−1` Packet Padding (from Session-Sender) = verbatim copy of the L reflected octets (MUST); then Additional Packet Padding (reflector MAY reuse the sender's, MUST truncate highest-numbered octets so total length equals the sender's, i.e. exactly 27 / 64 octets dropped).

Symmetrical Size, unauthenticated Session-Sender packet: `0–3` Seq, `4–11` Timestamp, `12–13` Error Estimate, `14–40` **MBZ (27)**, `41…` Packet Padding; reflector format unchanged, reflector length SHALL equal sender length (§5.1.4, §5.2.2). Authenticated/encrypted: no figure in the RFC; per §5.1.5 + EID 5549 the padding follows **64** MBZ octets, i.e. (inferred, **UNVERIFIED layout**) `0–47` standard auth sender header incl. HMAC, `48–111` MBZ (64), `112…` padding.

Both modes together: sender = header, 27 (or 64) MBZ, L reflected octets, additional padding; reflector = standard header, L reflected octets, additional padding (§5.1.5, §5.2.3).

### 4.9 DSCP and ECN monitoring (RFC 7750, bit 8 = 256)

Session-Sender format unchanged. Reflector constructs `S-DSCP-ECN` = `S-DSCP(6 bits, MSB side) | S-ECN(2 bits)` copied from the received IP header (both MUST).

Unauthenticated reflector packet: as §4.4 but `40` Sender TTL, `41` S-DSCP-ECN, `42–43` MBZ, `44…` Packet Padding (header 44 octets, equal to the STAMP base). Authenticated/encrypted: `80` Sender TTL, `81` S-DSCP-ECN, `82–95` MBZ (14), `96–111` HMAC, `112…` padding (S-DSCP-ECN lies inside the HMAC/encrypted region in encrypted mode). With Symmetrical Size in unauth mode RFC 7750 §2.2.2 says Padding Length SHOULD be ≥ 28 and shows a sender packet with 28 MBZ octets (= 42 octets), which does not equal the 44-octet reflector header — **UNVERIFIED/inconsistent in the RFC; recommend 30 MBZ octets so both directions are 44**. Reflector ignores received ECN congestion marks (does not react). TWAMP Light: configuration (§2.2.3).

### 4.10 Micro-sessions (RFC 9533, command 11) — for completeness only
Adds 2-octet Sender Micro-session ID and Reflector Micro-session ID (LAG member-link identifiers) to test packets: sender unauth at offsets 16–17 and 18–19 (after a 2-octet MBZ following Error Estimate); reflector unauth at 38–39 (Sender Micro-session ID, replacing MBZ) and 42–43 (Reflector Micro-session ID, after Sender TTL and one MBZ octet). Reflector copies the sender's ID and fills its own. Not required for a general implementation.

---

## 5. TWAMP Light (RFC 5357 Appendix I, informative; RFC 8545 §4 and Appendix A)

* Architecture: Server + Control-Client + Session-Sender on the controller; a bare Session-Reflector on the responder; no TWAMP-Control; the reflector is configured by "non-standard means".
* Reflector behaviour = RFC 5357 §4.2 with one exception: "IF the Session-Reflector does not have knowledge of the session state, THEN the Session-Reflector MUST copy the Sequence Number of the received packet to the Sequence Number field of the reflected packet" (stateless mode; identical to STAMP stateless). A stateful Light reflector uses its own per-session counter (EID 3511, Rejected, documents this reading; EID 4429 notes a Server↔Reflector link is still needed for state).
* Security: the non-standard control SHOULD have an authenticated mode, the responder SHOULD be configurable to accept only authenticated control, the control SHOULD be able to activate authenticated/encrypted TWAMP-Test, and in those modes the reflector MUST have some key-generation mechanism (unspecified). RFC 8545 Appendix A explains Light was relegated to an appendix precisely because negotiation and mandatory security are unspecified.
* Practical consequences for a Light reflector: pre-provision (local address/port, optional peer filter, DSCP policy, timestamp format per RFC 8186 §2.3.1, DSCP/ECN monitoring per RFC 7750 §2.2.3, keys for auth/enc), reflect every valid packet, minimum lengths 41/112, support port 862 by default (RFC 8545) but allow any port, treat unknown trailing bytes as padding (this is what keeps STAMP senders working, RFC 8762 §4.6).

---

## 6. Ports (RFC 8545)

| Service | Port | Transport | Note |
|---|---|---|---|
| owamp-control | 861 | tcp | RFC 4656 |
| owamp-test | 861 | udp | OWAMP-Test Receiver Port (reallocated by RFC 8545) |
| twamp-control | 862 | tcp | RFC 5357 |
| twamp-test | 862 | udp | TWAMP-Test Receiver Port (reallocated by RFC 8545); OPTIONAL default for TWAMP-Test/TWAMP Light; STAMP MUST default to it |

Legacy: before 2019 UDP 862 was nominally "twamp-control" and TWAMP Light deployments used arbitrary (often dynamic-range) ports, e.g. per BBF TR-390; a Light reflector may not listen on 862, so senders must allow configurable ports (RFC 8762 §4.6). Security notes (RFC 8545 §6): ACL the port, use non-globally-routable reflector addresses where possible, beware of networks treating the well-known port specially.

---

## 7. STAMP (RFC 8762) and its TLV extensions (RFC 8972) versus TWAMP Light

Wire compatibility (RFC 8972 §3: "Unauthenticated STAMP test packets are compatible on the wire with unauthenticated TWAMP-Test packets"):

| Aspect | TWAMP / TWAMP Light | STAMP |
|---|---|---|
| Unauth sender base | 14 octets + padding | 44 octets: `0–3` Seq, `4–11` Timestamp, `12–13` Error Estimate, `14–43` MBZ (30) (RFC 8972: `14–15` SSID, `16–43` MBZ 28), then optional TLVs |
| Unauth reflector base | 41 octets + padding | 44 octets: same fields as TWAMP at offsets 0–40, `41–43` MBZ (RFC 8972: `14–15` SSID instead of MBZ), then TLVs |
| Authenticated | AES-ECB on block 0 + HMAC-SHA1-128 over block 0 (plus full encryption option) | **No encryption**; HMAC-SHA-256 truncated to 128 bits over octets 0–95; sender base 112 (`26–95` MBZ 70; RFC 8972: `26–27` SSID, `28–95` MBZ 68), reflector base 112 with the same field offsets as TWAMP auth (RFC 8972: SSID at 26–27, MBZ(4) 28–31). Not interoperable with TWAMP auth/enc. |
| Sequence number | reflector's own per session (stateful) or copy (stateless Light) | identical two modes: stateful / stateless (RFC 8762 §4) |
| Symmetry | recommended via padding truncation; mandatory only with RFC 6038 | default: reflector copies everything beyond the base packet so sizes match (§4.3, §4.6) |
| Timestamp format | NTP; PTPv2 via RFC 8186 negotiation / config, signalled by Z | same, Z bit per RFC 8186; default NTP; configured |
| TTL | Sender TTL copied; 255 if unreadable | Session-Sender TTL copied (no fallback text; EID 8518 rejected) |
| Default port | negotiated; 862 optional | MUST default to UDP 862, MUST allow other ports |
| Session identity | 5-tuple / SID in control | 4-tuple; optional 2-octet non-zero SSID (RFC 8972 §3), reflector MUST copy it; reflector MUST discard non-matching sessions |
| Extensions | fixed-field extensions (RFC 6038, 7750, 8186, 9533) | TLVs after the base packet: `Flags(1) | Type(1) | Length(2) | Value`; Flags `U|M|I|R…` (Unrecognized, Malformed, Integrity-failed; bit 3 `C` Conformant added later); presence detected by UDP length > base length; unknown TLVs copied back with U = 1 |
| TLV types (IANA) | — | 1 Extra Padding, 2 Location, 3 Timestamp Information, 4 Class of Service (DSCP1/DSCP2/ECN/RP — EID 8199: ECN copied to ECN field), 5 Direct Measurement, 6 Access Report, 7 Follow-Up Telemetry, 8 HMAC, 9 Destination Node Address (RFC 9503), 10 Return Path (RFC 9503), 11 Micro-session ID (RFC 9534), 12 Reflected Test Packet Control (RFC 10052, not reviewed) |

Interworking rules (RFC 8762 §4.6): a STAMP reflector receiving a packet smaller than its base sends the base 44-octet reply; larger ⇒ copies the remainder; a TWAMP Light reflector sees STAMP TLVs as padding and reflects (possibly truncated) padding. Recommendation for the Go design: make the unauthenticated reflector STAMP-compatible for free by (a) emitting at least 44 octets (fields through offset 43, zero-filled MBZ), (b) copying any payload beyond the sender base as padding when configured for symmetry, (c) copying octets 14–15 of the received packet into octets 14–15 of the reply when "SSID mode" is enabled, (d) never rejecting packets on size alone. Keep auth/enc TWAMP and STAMP-authenticated as separate, explicitly selected modes.

---

## 8. Control-protocol state machines

### 8.1 Control-Client

| State | Event | Action | Next |
|---|---|---|---|
| CLOSED | start | TCP connect to server:862 (optionally set DSCP) | GREETING_WAIT |
| GREETING_WAIT | 64 octets received | if `Modes == 0` → close (SHOULD). If `Count > max` (default 32768) → close (SHOULD). If desired mode ∉ Modes → send Set-Up-Response with Mode 0 and close, or pick another offered mode. Else: derive `K_pf`, build Token, random session keys and Client-IV (secured modes); send Set-Up-Response | SERVER_START_WAIT |
| GREETING_WAIT | TCP error / timeout | close | CLOSED |
| SERVER_START_WAIT | 48 octets received (first 32 clear) | `Accept ≠ 0` → close (SHOULD); results none. Else initialise CBC decrypt stream with Server-IV, decrypt octets 32–47, feed them to the recv-HMAC context; store Start-Time; initialise CBC encrypt stream with Client-IV | IDLE |
| IDLE | app requests session | send Request-TW-Session (HMAC over 0–95) | ACCEPT_WAIT |
| IDLE | ≥ 1 accepted session, app starts | send Start-Sessions (or Start-N-Sessions with SIDs in mode 16) | START_ACK_WAIT |
| IDLE | app done | close TCP | CLOSED |
| ACCEPT_WAIT | 48 octets received | decrypt, verify HMAC (fail → close, MUST). `Accept == 0`: record SID, Port (if Port ≠ requested either accept it or send a new request), Reflected/Server octets; `Accept ≠ 0`: session refused, MAY close or continue | IDLE |
| START_ACK_WAIT | 32 octets received | verify HMAC. `Accept ≠ 0` → close (SHOULD). Else start Session-Sender(s) at max(now, Start Time) | ACTIVE |
| ACTIVE | test traffic | only Stop-Sessions may be sent (RFC 4656 §3.4); keep connection idle | ACTIVE |
| ACTIVE | all sessions finished (sender waited for the last reflections / local timeout) or abort | send Stop-Sessions with Accept 0 (normal) or non-zero (failure), Number of Sessions = started − stopped | IDLE (connection may be reused for new requests — **UNVERIFIED**, RFC silent; many implementations close) or CLOSED |
| ACTIVE (mode 16) | Start-N-Ack / Stop-N-Ack received | verify HMAC; apply Accept per SID group; may send further Start-N-/Stop-N-Sessions at any time | ACTIVE / IDLE |
| any secured | HMAC verification failure | drop connection; results suspect | CLOSED |
| any | TCP failure | results of all sessions MAY be considered invalid | CLOSED |
| any | server silence beyond local wait (30 min guidance) | close | CLOSED |

### 8.2 Server

| State | Event | Action | Next |
|---|---|---|---|
| LISTEN | TCP accept | choose Modes (0 to refuse), random Challenge/Salt, Count; send Server Greeting; if Modes 0 MAY close immediately; start SERVWAIT and partial-message timer | SETUP_WAIT |
| SETUP_WAIT | 164 octets received | `Mode == 0` → close. Security bits ≠ exactly one of {0,1,2,3}, or any set bit not offered → Server-Start Accept ≠ 0 (or close). Secured: look up KeyID (unknown → Accept 1), decrypt Token with `K_pf`, compare Challenge (mismatch → Accept 1), store session keys and Client-IV; send Server-Start (Accept 0, Server-IV, Start-Time block encrypted and HMAC-fed) | REQUEST_WAIT |
| SETUP_WAIT | partial message timeout | close | CLOSED |
| REQUEST_WAIT | first 16 octets of a message | dispatch on octet 0 | — |
| REQUEST_WAIT | cmd 5 | read 96 more; decrypt; verify HMAC (fail → close). Validate: Conf-* ≠ 0 → Accept 3; slots/packets ≠ 0 → Accept 3 (interpretation: "improperly formatted"); IPVN ∉ {4,6} → Accept 3; address policy (default: Sender Address must be the control peer or 0) → Accept 1/3; Receiver Port unavailable → alternate port or Accept 5; resource limits → Accept 4/5; RFC 6038 padding arithmetic → Accept 3. On accept: allocate SID, bind reflector socket, send Accept-Session (Port, SID) | REQUEST_WAIT |
| REQUEST_WAIT | cmd 2 | read 16 more; verify HMAC; if no accepted sessions → Accept 1 (interpretation) else start all sessions (reflector begins answering), send Start-Ack, suspend SERVWAIT, start REFWAIT per session | ACTIVE |
| REQUEST_WAIT | cmd 3 | read 16 more; verify HMAC; Number of Sessions must equal started-not-stopped (0 here) else invalid → close | REQUEST_WAIT / CLOSED |
| REQUEST_WAIT | cmd 7/9 (only if mode 16 negotiated) | read 16·N + 16 more; verify HMAC; per-SID start/stop; send Start-N-Ack / Stop-N-Ack groups | REQUEST_WAIT / ACTIVE |
| REQUEST_WAIT | cmd 0, 1, 4, 6, unknown, or 7–10 without mode 16 | MUST send Accept-Session with Accept 3 (RFC 5357 §3.5); since the message length is unknown the connection cannot be resynchronised → close afterwards (**interpretation**) | CLOSED |
| REQUEST_WAIT | SERVWAIT expiry | MAY close | CLOSED |
| ACTIVE | cmd 3 | read 16 more; verify HMAC; Number of Sessions mismatch → invalid, SHOULD close, results invalid. Else: for each session reflect only packets arriving within Timeout, then release; resume SERVWAIT | REQUEST_WAIT (after Timeout) |
| ACTIVE | any other command | protocol error → close (RFC 4656 §3.4: only Stop-Sessions is valid while active) | CLOSED |
| ACTIVE | REFWAIT expires on every session | discontinue sessions; resume SERVWAIT as if Stop-Sessions were received | REQUEST_WAIT |
| any secured | HMAC failure | close | CLOSED |
| any | TCP close/error | tear down sessions after Timeout (results MAY be invalid) | CLOSED |

Test-session state on the server/reflector: `ACCEPTED` (SID issued, socket bound) → `ACTIVE` (after Start-Sessions/Start-N-Sessions; reflect) → `DRAINING` (after Stop; reflect until Timeout) → `RELEASED`; `ACTIVE → RELEASED` on REFWAIT.

---

## 9. Normative requirements by role (MUST/SHOULD, with citations)

**Control-Client**
* MUST connect to TCP 862 (RFC 5357 §3.1). MAY set DSCP on all control packets (§3.1).
* MUST ignore unknown Modes bits; MUST set exactly one security bit (0–3) and only offered feature bits (RFC 4656 §3.1; RFC 5938 §3.1; RFC 6038 §4.1; RFC 7750 §2.1; RFC 8186 §2.2).
* SHOULD close on Modes = 0; SHOULD close if Count > configured max (default 32768); SHOULD close on Server-Start Accept ≠ 0; SHOULD close on Start-Ack Accept ≠ 0 (RFC 4656 §3.1, §3.7; RFC 5357 §6).
* Passphrase MUST be NVT-ASCII (RFC 5198 App. B), MUST NOT contain newlines (RFC 5357 §3.1). KeyID UTF-8 ≤ 80 octets.
* Session keys MUST have sufficient entropy; Client-IV MUST never repeat under the same secret (RFC 4656 §3.1).
* Request-TW-Session: Conf-Sender = Conf-Receiver = 0, Number of Schedule Slots = 0, Number of Packets = 0, SID = 0 (all MUST, RFC 5357 §3.5); addresses MAY be 0; RFC 6038: Padding Length SHALL exceed Length of padding to reflect, SHALL be ≥ 27 + L / 64 + L with truncation; SHOULD be ≥ 27 / 64 without reflect octets (EID 6409).
* While active, MUST send only Stop-Sessions (RFC 4656 §3.4). Stop-Sessions MUST carry the exact number of started-not-stopped sessions and MUST NOT carry session records (RFC 5357 §3.8).
* MUST verify every HMAC and drop the connection on failure (RFC 4656 §3.2, §6.10). SHOULD treat all results as invalid if its Stop-Sessions Accept ≠ 0 (RFC 5357 §3.8).
* Mode 16: MUST use Start-N/Stop-N commands and MUST NOT use 2/3 (RFC 5938 §3.1); MUST manage sessions by SID (§4.1).
* RFC 8186: SHOULD set the PTPv2 bit to 0 if the server did not offer it; the sender MUST emit NTP when it is 0.

**Server**
* SHOULD listen on 862 (RFC 4656 §2 wording). Modes = OR of supported (MUST semantics); Challenge never repeated; Salt pseudo-random; Count power of 2 ≥ 1024 (RFC 4656 §3.1, §6.10).
* SHOULD mirror the TCP SYN's DSCP (RFC 5357 §3.1). MUST ignore unknown Mode bits (RFC 4656 §3.1).
* Server-Start: Accept semantics; Start-Time SHOULD be the same for all clients; SHOULD be zero if Accept ≠ 0 (RFC 4656 §3.1).
* MUST respond to every Request-TW-Session with Accept-Session (RFC 4656 §3.5; RFC 5357 §3.5). MUST answer Accept 3 for unexpected Command Numbers and for non-zero Conf-* (RFC 5357 §3.5). SHOULD NOT close on rejecting a request (RFC 4656 §3.5). Port MUST be 0 and MUST NOT suggest alternates when rejecting (EID 1587). RFC 6038: MUST send zero Server octets unless it wants octets back; Accept MUST be 3 if symmetric sizes are impossible.
* SID: server-generated, SHOULD follow the RFC 4656 construction, unpredictable.
* MUST respond to Start-Sessions with Start-Ack, SHOULD as quickly as possible (RFC 4656 §3.7). Mode 16: MUST respond with ≥ 1 Start-N-Ack / Stop-N-Ack, N ≥ 1 each (RFC 5938 §3.3, §3.5).
* SERVWAIT default SHALL be 900 s; SHALL suspend/resume as in §2.14; SHOULD implement REFWAIT if SERVWAIT is implemented (RFC 5357 §3.1).
* SHOULD limit Count (default max 32768) (RFC 5357 §6). MUST include resource-use limits with conservative defaults (RFC 4656 §6.5). SHOULD drop connections whose messages do not complete within ~30 min (RFC 4656 §3).
* MUST drop the connection on HMAC failure (RFC 4656 §6.10). RFC 7717: MUST read SPIs from KeyID, MUST log missing SA, SHOULD answer Accept 6. RFC 8186: MUST close if the client requires NTP and the reflector cannot provide it.

**Session-Sender**
* Timestamp MUST be the best approximation of real departure time (RFC 5357 §4.1.1). SHOULD set TTL/Hop Limit 255 and Sender TTL semantics 255 (RFC 4656 §4.1.2; RFC 5357 §4.2.1).
* Sequence numbers start at 0 and increment by 1 per packet per session (RFC 4656 §4.1.2).
* Padding SHOULD be pseudo-random, MUST offer all-zeros; MAY pad ≥ 27 / 64 for symmetry (RFC 5357 §4.1.2, §4.2.1, EID 5046).
* Auth/enc: MUST set up the key schedule once per session; MUST discard received packets failing HMAC (RFC 5357 §4.2.1; RFC 4656 §4.2); SHOULD treat Multiplier 0 as corrupt.
* Uses Sender Port for send and receive (RFC 5357 §3.5). MUST send only from its own address (no spoofing, RFC 4656 §6.2).
* RFC 6038: when Reflect Octets, MUST place Server octets first in the to-be-reflected padding; when Symmetrical Size, SHALL use the 27/64-MBZ format. RFC 8186: SHOULD NOT ignore error estimates when Z is in use.
* RFC 5938: MUST manage sessions by SID.

**Session-Reflector**
* MUST transmit a reply for each received packet, SHOULD as immediately as possible; Receive Timestamp and Timestamp MUST be best approximations (RFC 5357 §4.2, §4.2.1).
* Sequence Number: own counter per session starting at 0 (EID 1590); TWAMP Light without state MUST copy the sender's (Appendix I).
* MUST copy Sender Sequence Number, Sender Timestamp, Sender Error Estimate exactly. SHOULD read TTL/Hop Limit from the IP header; otherwise MUST set 255 (RFC 5357 §4.2).
* MUST use the same DSCP as negotiated/received (RFC 5357 §3.5); RFC 7750 MUSTs for S-DSCP-ECN population and provisioned DSCP; ECN SHOULD be Not-ECT if unknown.
* SHOULD set TTL 255 on replies (RFC 5357 §4.2.1).
* Auth/enc: MUST decrypt and verify before use; MUST compute HMAC before encrypting; in encrypted mode MUST fetch timestamps, HMAC, encrypt, then send; HMAC field MUST NOT be encrypted; padding MUST NOT be covered or encrypted (RFC 5357 §4.2, §4.2.1).
* MUST NOT reflect after Stop-Sessions + Timeout and MUST NOT answer ignored packets (RFC 5357 §3.5, §3.8, §4.2). MAY discontinue after REFWAIT (default SHALL be 900 s).
* Padding: SHOULD truncate to equal lengths; MAY reuse sender padding; SHOULD drop highest-numbered octets; RFC 6038 makes exact truncation (27/64) and verbatim reflection of L octets a MUST; Symmetrical Size reply length SHALL equal the request length.
* RFC 5938: MUST manage by SID; after Stop-N-Sessions MUST ignore that session after Timeout; REFWAIT SHOULD apply while any session is active.
* RFC 8186: endpoints supporting PTPv2 MUST interpret both formats; MUST use NTP when the client's PTPv2 flag is 0.

---

## 10. Metrics

Notation per reflected packet: T1 = Sender Timestamp (sender tx), T2 = Receive Timestamp (reflector rx), T3 = Timestamp (reflector tx), T4 = local receive time at the Session-Sender (host/kernel timestamp on arrival, not carried in the packet).

| Quantity | Formula | Clock requirement | Source |
|---|---|---|---|
| Round-trip delay | `RTT = (T4 − T1) − (T3 − T2)` | sender clock only (self-consistent) | RFC 2681 §2.4 definition; RFC 5357 §1 "processing delays can be accounted for"; RFC 2681 §2.7.3 (Hrefl removed) |
| Raw RTT (no reflector correction) | `T4 − T1` | sender | RFC 2681 |
| Forward (near-end) one-way delay | `T2 − T1` | both clocks synchronized (S bits set) | RFC 7679 §3.4; RFC 8762 §4 naming |
| Reverse (far-end) one-way delay | `T4 − T3` | both synchronized | RFC 7679 §3.4 |
| Reflector processing time | `T3 − T2` | reflector clock only | RFC 5357 §4.2.1 ("the amount of time the packet was in transition in the Session-Reflector") |
| Clock-offset indicator | `((T2 − T1) − (T4 − T3)) / 2` = offset + path-asymmetry/2 | — | derived (not an RFC metric) |

Error budget: RFC 2681 §2.7.4 `2·Rsource + Hinitial + Hfinal + Hrefl`; with TWAMP, Hrefl is bounded by the reflector's Error Estimate (which "also applies to the Receive Timestamp"); one-way delays carry the clock-synchronization error, bounded by the sum of both Error Estimates (RFC 8186 §2.3 says they MUST be used when computing delay metrics).

**Loss** (RFC 7680 §2.4 one-way, RFC 6673 §4.3 round-trip): a sent packet is lost if no reflection arrives within the waiting threshold Tmax after T1; the sample ratio is `lost / sent` (RFC 6673 §6.1, RFC 7680 §4.1). RFC 6673 §4.4: a responder SHOULD answer within 1 s and log otherwise. Direction attribution needs a **stateful** reflector (RFC 8762 §4): with sender sequence S and reflector sequence R, forward (near-end) losses are the S values never assigned an R — estimate `sent − (R_max + 1)` once the stream has drained; reverse (far-end) losses are gaps in R among received replies — `(R_max + 1) − received_unique`. A stateless (copying) reflector only yields round-trip loss. RFC 6673 §7 caveats: the reverse-path sample is conditional on forward success and forward delay variation, so reverse-path metrics are invalid when forward loss or jitter is appreciable; forward reordering misaligns R versus S and MUST be handled.

**Duplicates**: identical S seen twice. Same R ⇒ duplicated on the reverse path; different R ⇒ duplicated on the forward path (reflector saw two copies). For delay, "the first copy to arrive determines the packet's delay" (RFC 7679 §3.5, RFC 2681 §2.5, RFC 3393 §3.6); RFC 4737 §3.6: duplicates MUST be noted and only the first copy analysed further.

**Reordering** (RFC 4737 §3.3–3.4): maintain `NextExp`; on arrival of sequence `s`: `if s >= NextExp { if s > NextExp: sequence discontinuity of size s − NextExp; NextExp = s + 1; in-order } else { reordered }`. Ratio `R = reordered / received` (§4.1.3). Extent `e = i − j` for the smallest arrival index j with `s[j] > s[i]` (§4.2.3). Apply with s = Sender Sequence Number over the arrival order of reflections for round-trip reordering; with a stateful reflector, s = R over arrival order isolates reverse-path reordering, and non-monotonic S→R mapping reveals forward-path reordering.

**Delay variation**
* IPDV (RFC 3393 §2.4; RFC 5481 §4.1): `IPDV(i) = D(i) − D(i−1) = (R_i − R_{i−1}) − (T_i − T_{i−1})`, consecutive packets by sending sequence; undefined if either is lost; takes positive/negative values; mean ≈ 0; sensitive to reordering (RFC 5481 §8.7 — evaluate in sending-sequence order, detect sequence changes).
* PDV (RFC 5481 §4.2): `PDV(i) = D(i) − D_min` over the interval; ≥ 0; quantiles are direct indications; RFC 5481 §8.8 suggests reporting `99.9th percentile − minimum`.
* RFC 3550 §6.4.1 smoothed interarrival jitter: `D(i,j) = (R_j − R_i) − (S_j − S_i)`; `J(i) = J(i−1) + (|D(i−1,i)| − J(i−1)) / 16`, computed continuously in **arrival order** (RFC 5481 §6.6: equals IPDV singletons when there is no reordering). For a TWAMP sender use D = RTT (two-way jitter), or `T2 − T1` / `T4 − T3` for per-direction jitter (direction-specific values are valid even with unsynchronized clocks because offset cancels, RFC 3393 §1.3; frequency skew adds `K·Δt`, RFC 3393 §2.7.1).
* Statistics (RFC 7679 §5): percentile, median, minimum with undefined (lost) values treated as +∞; percentile/median undefined on empty samples.

---

## 11. IANA registries (consolidated)

* TWAMP-Control Command Numbers: §2.13 (IETF Review; 0–255).
* TWAMP-Modes: §2.3 (IETF Review; bit positions 0–9 assigned).
* Accept values: defined only in RFC 4656 §3.3 (no registry); RFC 7717 adds 6 by text.
* Ports: §6.
* OWAMP registries (RFC 7718): OWAMP-Control Command Numbers 1 Request-Session, 2 Start-Sessions, 3 Stop-Sessions, 4 Fetch-Sessions, 5 Request-OW-Micro-Sessions (RFC 9533), 254 Experimentation, 0/255 Reserved; OWAMP-Modes bits 0–2 as TWAMP, 3 Reserved, 4 IKEv2-derived (note: a different bit than TWAMP's 7).
* STAMP TLV registries: §7.

---

## 12. Errata that affect implementation

| EID | RFC | Status | Effect |
|---|---|---|---|
| 5045 | 5357 §4.2.1 | Verified | auth/enc min reflector length 112 (not 104) |
| 5046 | 5357 §4.1.2 | Verified | sender pads ≥ 64 (not 56) in auth/enc |
| 1590 | 5357 §4.2.1 | Verified | reflector sequence numbers are per test session |
| 1593 | 5357 §8 | Verified | command registry has 256 values |
| 1589 | 5357 §4.2.1 | Verified | auth/enc figure is the plaintext layout |
| 1587 | 5357 §3.5 | Verified | "the Server uses the Accept field… to the Control-Client" (role wording) |
| 1591 | 5357 §6 | Held | Count max is 2^32 − 1 |
| 1594 | 5357 §3.8 | Held | "single-block HMAC" terminology |
| 4429 / 3511 | 5357 App. I | Held / Rejected | Light figure arrow; stateful Light reflectors use own sequence numbers |
| 6408, 6409, 6410, 5549 | 6038 | Verified | all 104→112 and 56→64 corrections |
| 4260 | 6038 §5.2.1 | Verified | the Reflect-Octets figure in §5.2.1 is the Session-Reflector's format |
| 4258, 4259 | 6038 | Held | cross-reference typos |
| 2478 | 5938 | Verified | "next section" typo |
| 8662 | 8186 §2.2 | Held | client may set PTPv2 flag 0 if its sender does not support PTP |
| 8199, 8339, 8591, 8592 | 8972 | Verified | CoS TLV ECN copy; Follow-Up TLV length wording; Timestamp Information wording |
| 8518 | 8762 | Rejected | (TTL fallback 255 not added to STAMP) |
| — | 4656, 5618, 7750, 8545, 7717 | — | no errata |

---

## 13. UNVERIFIED items and RFC ambiguities (decide explicitly in the spec)

1. PBKDF2 output length is not in RFC 4656; reference implementation and AES-128 imply 16 octets.
2. Exact coverage of the first HMAC in each direction (RFC 4656 §3.2 wording is "everything sent"); reference implementation: client's first HMAC covers only Request-TW-Session 0–95; server's covers Server-Start 32–47 + Accept-Session 0–31.
3. RFC 5357 §4.2 "96 octets for encrypted" when decrypting received sender packets conflicts with the OWAMP sender rule (32); reference implementation uses 32.
4. RFC 6038 §4.2 "66 octets not shown" annotation (should be 56 by RFC 4656 offsets).
5. RFC 6038 Symmetrical Size authenticated/encrypted sender layout has no figure; 64 MBZ after the HMAC block is inferred.
6. RFC 7750 unauthenticated symmetric sizing (28 MBZ ⇒ 42 vs 44-octet reflector header).
7. RFC 7717 Accept = 6 is outside the RFC 4656 value list; no registry.
8. Whether a control connection may be reused for new Request-TW-Session after Stop-Sessions (RFC silent; OWAMP implies the connection persists).
9. Server behaviour after answering Accept 3 to an unknown command (length unknown ⇒ closing is the only safe option).
10. Reflecting packets that arrive before Start-Sessions (not addressed).
11. Current TAI−UTC offset for PTPv2 conversions.
12. RFC 4656 lost-record convention "Scale = 64" cannot be encoded in 6 bits (OWAMP-only, irrelevant to TWAMP).
13. RFC 9503/9534/10052 STAMP TLVs listed from IANA only; texts not reviewed.

---

## 14. Implementation guidance for Go (not from RFC text; standard practice)

* TTL/Hop Limit of received test packets: enable `IP_RECVTTL` / `IPV6_RECVHOPLIMIT` and read the control message (`golang.org/x/net/ipv4|ipv6` `PacketConn.SetControlMessage(FlagTTL|FlagHopLimit)`); DSCP/ECN for RFC 7750: `IP_RECVTOS` / `IPV6_RECVTCLASS` (`FlagTOS`/`FlagTrafficClass`); set TTL 255 with `SetTTL`/`SetHopLimit`; set DSCP with `SetTOS(dscp<<2 | ecn)` / `SetTrafficClass`.
* Timestamps: take T4 (and T2) from kernel receive timestamps (`SO_TIMESTAMPNS`/`SO_TIMESTAMPING` via control messages) when available, else `time.Now()` immediately after `ReadFrom`; take T1/T3 as late as possible before `WriteTo`; fill Error Estimate from the clock discipline state (e.g., `adjtimex` status / chrony tracking) with S set only when externally synchronized; use a monotonic-safe path for RTT (`T4 − T1` from the same wall clock is required because T1 is carried in the packet).
* Crypto: `crypto/aes` + `cipher.NewCBCEncrypter/Decrypter` (keep one BlockMode per control direction for stream chaining; for test packets create per-packet IV = zero), ECB = `Block.Encrypt` of one block; `crypto/pbkdf2` (Go ≥ 1.24) or `golang.org/x/crypto/pbkdf2` with `sha1.New`, 16-octet output; `crypto/hmac` + `sha1`, truncate to 16; `crypto/rand` for Challenge, Salt, keys, IVs, SID random part, padding.
* Framing: on the server read 16 octets, dispatch on octet 0, read the remainder (96 for cmd 5, 16 for cmds 2/3, `16·N + 16` for cmds 7/9 after decrypting the first block to learn N); decrypt before HMAC check; abort on any HMAC error.
* Sockets: one UDP socket per session bound to Sender Port (client) / Receiver Port (reflector), connected or filtered by 5-tuple; a Light reflector may serve many senders on one socket keyed by (src addr, src port) when stateful.

Scratch copies of all source texts remain in `/tmp/claude-1000/-home-yoichis-project-gotwamp/fc257f91-cc6f-4949-9c61-3dc478077325/scratchpad/rfc/` (RFCs, `errata/`, IANA files) and `.../scratchpad/owamp-src/` (reference implementation), should the spec author want to quote further.