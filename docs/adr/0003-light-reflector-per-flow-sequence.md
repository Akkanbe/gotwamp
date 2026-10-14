# 0003. The TWAMP Light reflector counts Reflector Sequence Numbers per flow

- Status: Accepted
- Date: 2026-10-03

## Context

TWAMP Light has no control protocol, so the Reflector does not know that a session exists. RFC 5357 Appendix I states that
"a Reflector without session state must copy the Sequence Number of the received packet".
On the other hand, if the Reflector returns its own Sequence Number, the Sender can estimate forward and reverse loss separately
(the stateful reflector of RFC 8762 §4: `lost_forward = sent − (R_max + 1)`, `lost_reverse = (R_max + 1) − received`).
Detecting reordering on the reverse path also requires a sequence on the Reflector side.

A survey of vendor implementations shows that the Light reflectors of Nokia SR OS / SR Linux, Huawei, and others keep per-4-tuple state
(the SR OS inactivity timer defaults to 100s), and Juniper Evolved offers `stateful-sequence` as an option. Stateless implementations also exist.

## Decision

- By default (`light.sequence: per_flow`), the Light reflector counts Reflector Sequence Numbers from 0 for each flow key (source address, source port, destination address, destination port)
- A flow is discarded after `flow_timeout` (default 900s, same as REFWAIT) of inactivity
- New flows beyond the flow limit `max_flows` (default 65536) are treated as stateless (the Sender's value is copied), and `flows_overflow_total` is incremented
- `light.sequence: copy_sender` selects the same behavior as RFC 5357 Appendix I / STAMP stateless
- Per-flow statistics (packets received, packets reflected, last receive time) are available through `ReflectorService.ListReflectorSessions`
- On the Sender side, if the peer reflector's sequence goes backward during a run (the peer's flow expired), the separate forward/reverse loss estimate is disabled (`loss_split_valid=false`). This does not affect duplicate classification

## Options considered

| Option | Pros | Cons |
|--------|------|------|
| Per-flow sequence (default, chosen) | Enables separating forward/reverse loss and detecting reverse-path reordering. Same behavior as major vendors' stateful reflectors | Needs memory for the flow table and expiry handling. Requires protection (limit, ACL) against floods of flows with spoofed sources |
| Always copy the Sender's value | Stateless and simple. Matches the wording of RFC 5357 Appendix I | Only round-trip loss is known |
| Sequence per source address | Smaller table | Multiple sessions from the same host get mixed, and the sequence loses its meaning |

## Consequences

- When measuring between gotwamp instances or against stateful peers, the direction of loss can be estimated
- The flow table has a limit. Reflection continues even when the limit is exceeded (degrading to stateless)
- If the peer Sender assumes a stateless reflector, select `copy_sender` in the configuration
