# 0012. Implement STAMP only as base-packet compatibility via a Light switch, and only parse and return TLVs

- Status: Accepted
- Date: 2026-10-05
- Related: RFC 8762 §4, RFC 8972 §3–§4, SPEC §6.7, §13, ADR 0003, research/vendor-others.md (SR Linux), PLAN §6 M7-3

## Context

STAMP (RFC 8762) is a re-standardization of TWAMP Light. Its unauthenticated packets have the same shape as TWAMP-Test (RFC 8972 §3). SR Linux and others provide a STAMP sender / reflector instead of TWAMP Light.

The v0.x Light reflector can already reflect a STAMP base packet (44 octets) as "14 octets + padding". However, it differs from STAMP in these points.

- It does not return the SSID (offset 14–15).
- Octets 41–43 of the reply contain the sender's padding.

Implementing RFC 8972 TLVs, STAMP authentication (HMAC-SHA-256), and SSID-based session identification would be a large scope. So we had to decide how much to implement.

## Decision

- **Reflector**: Implemented as the Light reflector switch `reflector.light.stamp` (default false). When enabled, it replies as follows.
  - Copies the SSID (RFC 8972 §3).
  - Places fields at the same positions as TWAMP and sets 41–43 to MBZ.
  - Reply length is max(received length, 44). Anything after the base is returned as is (RFC 8762 §4.6).
  - Stateful / stateless sequence numbering uses the existing Light setting (ADR 0003) unchanged.
- **TLVs**: Parse them, set flags only, and return them.
  - No TLV semantics are implemented. Set U (Unrecognized) on each TLV. On a TLV whose length runs past the packet, set M (Malformed) and stop there (RFC 8972 §4).
  - Treat the bytes after the base as TLVs only when they match the sender flag pattern `100xxxxx`; otherwise return them unchanged as TWAMP padding. This is the same distinction SR Linux makes, and it avoids corrupting a TWAMP Light sender's padding.
- **Sender**: The Light test parameters `stamp` / `ssid` send STAMP Session-Sender packets.
  - The packet is the 44-octet base plus the SSID. Padding is a single Extra Padding TLV (with U set).
  - Replies are read as TWAMP. If the reply's SSID is neither our own nor 0 (a reflector without SSID support), it is counted as `foreign`, a reply belonging to another session.
- **Out of scope**:
  - STAMP authenticated mode
  - TLV processing (Class of Service, Location, and so on)
  - Reflector-side session identification by SSID (gotwampd separates flows by 4-tuple)
  - Combination with DSCP/ECN monitoring (RFC 7750). STAMP treats 41 as MBZ, so this is a config error.

## Options considered

| Option | Pros | Cons |
|--------|------|------|
| Base-packet compatibility only, as a Light switch (chosen) | Reuses the existing Light path (ACL, rate limiting, flows). Replies to TWAMP senders are unchanged. Small implementation, easy to fuzz | TLV features are unavailable |
| Always reply in STAMP format | No configuration needed | Replies with 44 octets to a 41-octet TWAMP Light request, which can break senders that check reply length strictly. Changes existing behavior |
| A dedicated STAMP reflector (separate port or package) | Easier to grow along the STAMP spec | Duplicates ACL, rate limiting, and so on. Cannot accept both on the same port 862 |
| Implement all RFC 8972 TLVs | Uses STAMP's full feature set | Large scope. Cannot be verified against an interop peer (real SR Linux hardware) |

## Consequences

- Measurement is expected to work in both directions: from an SR Linux STAMP sender to gotwampd, and from gotwamp to an SR Linux STAMP reflector (not yet verified on real hardware; see docs/ops/interop.md).
- When TLV-based features are needed (such as per-class DSCP checks), extend `MarkSTAMPTLVs` into per-TLV processing.
- STAMP authentication uses a different scheme from TWAMP authentication (ADR 0011) (no encryption, HMAC-SHA-256), so adding it will be decided separately.
