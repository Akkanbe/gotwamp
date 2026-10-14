# 0011. Implement authenticated modes as separate "plaintext message" and "crypto layer" parts, and keep keys in pfs-format files

- Status: Accepted
- Date: 2026-10-05
- Related: RFC 4656 §3.1–§4, §6.10, RFC 5357 §3–§4, RFC 5618, SPEC §5.2–§5.4, §13, ADR 0010, PLAN §6 M7-1

## Context

TWAMP's Authenticated / Encrypted / Mixed modes protect both TWAMP-Control and TWAMP-Test.

- Control messages: Encrypted with one AES-CBC stream per direction, with an HMAC-SHA1 covering everything since the previous HMAC.
- Test packets: A key derived from the SID protects the first 16 octets (authentication) or 32 / 96 octets (encryption).

v0.x implemented only Unauthenticated. Even so, the message types in the wire package and the send/receive path in control were reserved so that this layer could be inserted later (SPEC §13).

Before implementing, the following had to be decided.

- How to separate crypto processing from message encoding.
- How to interpret places where the RFC text is ambiguous. Two points: what the first HMAC covers, and how much of a sender packet the reflector decrypts.
- Where and in what format to store shared keys (KeyID -> passphrase).
- How to verify correctness. The RFCs provide no test vectors.

## Decision

- **Layer separation**:
  - The message types in `internal/twamp/wire` only encode and decode plaintext. The HMAC field is always 0 and sits in the last 16 octets of each message.
  - Control protection is delegated to the `ControlSecurity` interface (`Seal` / `Open` / `Verify`).
    - `Unauthenticated` is a no-op layer.
    - `ControlCrypto` holds the per-direction CBC chaining and HMAC stream.
  - The control client / server only swap the layer after the handshake. Subsequent message handling is shared.
- **Test packets**:
  - The authenticated layouts (`AuthSenderPacket` / `AuthReflectorPacket`) and `TestCrypto` live in wire.
  - The key schedule is built once per session. CBC is driven by our own code, so per-packet allocations are 0.
  - In Mixed mode, test packets stay unauthenticated.
- **Ambiguities in the RFC**: Follow the reference implementation (perfSONAR owamp).
  - What the first HMAC covers:
    - client -> server: Only octets 0–95 of Request-TW-Session.
    - server -> client: The Start-Time block of Server-Start, and octets 0–31 of Accept-Session.
  - How much of a sender packet the reflector decrypts in Encrypted mode: RFC 5357 §4.2 reads as 96 octets, but we use 32 octets.
- **Negotiation**:
  - The client picks among the modes it accepts in the order Encrypted > Authenticated > Mixed > Unauthenticated (same as twping).
  - In authenticated modes, if the Count in the Server Greeting is below 1024, the client returns Mode 0 and ends.
  - The server returns Server-Start with Accept 1, disconnects, and logs the reason in these cases:
    - Unknown KeyID
    - The Token's Challenge does not match (wrong passphrase)
    - A Client-IV was reused within the last 4096
  - If a control message HMAC does not match, disconnect (RFC 4656 §6.10) and log at Warn.
  - Test packets with a mismatched HMAC are dropped (`malformed`) and are used neither to refresh REFWAIT nor to pin the VerifyNone source.
- **Key store**:
  - `internal/control/keystore` provides the `Store` interface (identity -> passphrase). It reads perfSONAR's pfs format: one `identity hex-passphrase` per line.
  - The daemon reads `sender.keys_file` and `reflector.control.keys_file` once at startup. It does not start if permissions exceed 0600 (same rule as ADR 0010).
  - Keys are never written in the body of the config file.
- **Verification**:
  - Bit-exact match against values computed independently with Python (hashlib / hmac) and openssl (`test/golden/auth-vectors.txt`).
  - Interoperation with perfSONAR (twampd / twping) in both directions, all three modes, IPv4 / IPv6 (`make interop`).

## Options considered

| Option | Pros | Cons |
|--------|------|------|
| Separate plaintext messages and the crypto layer (chosen) | Message golden tests work unchanged. The crypto layer can be checked against test vectors on its own. The Unauthenticated path stays as before (no measurement regression) | The layer has calling-order conventions (open the header first, defer the HMAC field) |
| Put crypto processing in each message's Encode / Decode | Simple for callers | Message types would hold keys and chaining state. Violates wire's "no I/O, no state" principle and complicates tests |
| Wrap the control connection in TLS instead of TWAMP crypto | Existing libraries do the work | Non-standard; cannot interoperate with other implementations |
| Write keys directly in the YAML config | Only one file | Secrets leak easily when distributing or backing up config files. Cannot share keys with perfSONAR |
| Pass keys through environment variables | Convenient in containers | Hard to express multiple identities. Leaks easily via process listings and the like |

## Consequences

- Interoperates with perfSONAR in all three modes while sharing the key file.
- Crypto correctness is backed by two things: independently computed golden values and cross-implementation interoperation.
- Key updates require a restart. Automatic key reload and RFC 7717 (IKEv2-derived keys) will be revisited when needed.
- Authentication for TWAMP Light is out of scope, because the RFC does not define how keys are distributed (RFC 5357 Appendix I).
