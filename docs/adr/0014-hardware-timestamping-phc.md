# 0014. Use hardware timestamps only when explicitly requested, and assume the PHC is synchronized to CLOCK_REALTIME

- Status: Accepted
- Date: 2026-10-05
- Related: SPEC §8.1, §8.2, §8.4, §13, ADR 0002, PLAN §6 M7-4, [docs/ops/troubleshooting.md](../ops/troubleshooting.md) §3 (hardware timestamping)

## Context

ADR 0002 decided to use kernel software timestamps (SO_TIMESTAMPING) by default and fall back to user space when they are unavailable.

NIC hardware timestamps improve the accuracy of T2 / T4 (and the sender's T1') by the amount of interrupt and driver latency. However, there are caveats.

- NIC timestamps come from the PTP hardware clock (PHC). T1 / T3, on the other hand, are taken from the system clock (CLOCK_REALTIME), so any offset between the two shifts the delay by that amount. PTP deployments often run the PHC on TAI (37 seconds ahead of UTC).
- Enabling NIC timestamps (SIOCSHWTSTAMP) is a NIC-wide setting and requires `CAP_NET_ADMIN`. If ptp4l runs on the same NIC, it also interacts with ptp4l's settings.
- Capabilities vary by NIC. Some timestamp only PTP packets (the receive filter is PTP-only), and some can capture only one transmit timestamp at a time.

## Decision

- **When to use**: Only when `daemon.timestamping: hardware` is set explicitly. `auto` stays kernel -> userspace as before and does not try hardware, so NIC-wide settings are never changed silently.
- **Assumption**: The PHC is synchronized to CLOCK_REALTIME (`phc2sys -s CLOCK_REALTIME -c <if> -O 0`).
  - v0.x does not correct the offset between the PHC and CLOCK_REALTIME. Measuring and correcting it with PTP_SYS_OFFSET is a future extension.
- **Safeguards**: When the assumption breaks, fall back to software timestamps.
  - Receive: If the NIC timestamp and the kernel software timestamp of the same datagram differ by more than ±1ms, use the software timestamp (`RxMeta.HardwareRejected`).
  - Transmit: An inconsistent T1' is discarded by the sender's sanity check (SPEC §8.2) and counted in `tx_timestamp_invalid`.
  - As a result, timestamps from a TAI PHC or an unsynchronized PHC are never used silently.
- **NIC configuration**:
  - Check capabilities with ETHTOOL_GET_TS_INFO. The receive filter `all` is required. Transmit is required only when the sender uses it.
  - If capabilities are insufficient, change the setting with SIOCSHWTSTAMP **only in the widening direction**. Transmit modes such as ptp4l's one-step are preserved.
  - Settings are not restored on exit (other sockets or PTP may be using them).
  - For deployments that do not grant privileges, document a procedure to enable them in advance with `hwstamp_ctl`.
- **Fallback**:
  - Per socket: If the NIC cannot be used, fall back to kernel and log the reason once at Warn on startup.
  - Per packet: If there is no NIC timestamp, use the software timestamp.
  - Sockets bound to a wildcard address use the NIC of `daemon.hardware_interface` (for a Light sender, if empty, the interface of the route to the destination).
- **Number of transmit timestamps**: Do not use SOF_TIMESTAMPING_OPT_TX_SWHW; keep one timestamp per packet. This keeps the sender's T1' matching (one per id) unchanged.
- **Error Estimate**: The method error is `daemon.clock.hardware_error` (default 1µs). The reflector takes T3 in user space, so even with hardware it uses the larger of this and `userspace_error`.

## Options considered

| Option | Pros | Cons |
|--------|------|------|
| Use only when explicit, assume PHC sync, fall back to software timestamps on drift (chosen) | Never changes NIC settings silently. Does not emit wrong values when the assumption breaks. Small implementation | On hosts running the PHC on TAI (ptp4l), it is effectively kernel |
| Measure and correct the PHC vs CLOCK_REALTIME offset with PTP_SYS_OFFSET | Works as is on ptp4l hosts | Handling correction frequency and accuracy gets complex. No real hardware to verify on |
| Also try hardware under `auto` | Better accuracy without configuration | Silently changes NIC-wide settings and may interfere with PTP operation. Uses the hardware without checking the assumption (PHC sync) |
| Use only NIC timestamps (no software fallback) | Simple | Cannot measure packets the NIC does not timestamp (such as those arriving on another interface). Cannot notice a drifting PHC |

## Consequences

- On hosts with a supported NIC and phc2sys, T2 / T4 / T1' get NIC accuracy.
- When the assumption breaks, receive automatically falls back to software timestamps, and transmit problems are detectable through `tx_timestamp_invalid`. Exposing the `HardwareRejected` count as a metric is future work.
- Not yet verified on a real NIC (the development environment is virtio). The manual verification procedure is in [troubleshooting.md](../ops/troubleshooting.md) §3.
