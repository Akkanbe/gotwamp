# 0002. Prefer SO_TIMESTAMPING (software) for timestamps and fall back to time.Now()

- Status: Accepted
- Date: 2026-10-03

## Context

TWAMP delay is computed from four times: T1 (Sender transmit), T2 (Reflector receive), T3 (Reflector transmit), and T4 (Sender receive).
Taking `time.Now()` in user space mixes delays from Go runtime scheduling and GC into the measured values.
Hardware timestamps, on the other hand, depend on the NIC and on privileges, and are often unavailable in containers and virtual environments.
gotwamp is Linux-only and should run without privileges.

## Decision

- Provide two timestamping implementations, `kernel` and `userspace`, selected by the setting `timestamping: auto | kernel | userspace` (default `auto`). `auto` tries to set the socket options at startup and uses `kernel` on success and `userspace` on failure
- `kernel`: Receive times (T2, T4) come from the `SCM_TIMESTAMPING` cmsg of `SO_TIMESTAMPING` (`SOF_TIMESTAMPING_RX_SOFTWARE | SOF_TIMESTAMPING_SOFTWARE`). If that is unavailable, use `SO_TIMESTAMPNS`
- The transmit time must be written into the packet, so the packet carries `time.Now()` taken just before sending. The Sender additionally obtains the kernel TX time T1' from `MSG_ERRQUEUE` with `SOF_TIMESTAMPING_TX_SOFTWARE | OPT_ID | OPT_TSONLY`, and prefers T1' when computing RTT. If T1' is unavailable or inconsistent, the user-space T1 is used
- `userspace`: Use `time.Now()` right after `recvmsg` and right before sending
- All times use CLOCK_REALTIME (T1 is passed to the peer in the packet, so a monotonic clock cannot be used)
- Record the timestamping mode used in the run summary and metrics. The Error Estimate value also depends on the mode (`kernel` adds 5µs and `userspace` adds a default of 50µs to the synchronization error)
- Design the interface so that hardware timestamping can be added later as a third implementation

## Options considered

| Option | Pros | Cons |
|--------|------|------|
| SO_TIMESTAMPING software + time.Now() fallback (chosen) | No privileges required. Removes user-space jitter on the receive side. Works in any environment | On the transmit side, the time written into the packet is a user-space value |
| time.Now() only | Simple implementation, highly portable | GC and scheduling delays go directly into the RTT |
| Hardware timestamping | Highest precision | Depends on the NIC, driver, and PHC synchronization. Almost never usable in container environments |

## Consequences

- User-space jitter is removed from receive times, so a median RTT error under 50µs in a veth + netem environment becomes a realistic target
- The socket layer (`internal/tstamp`) becomes Linux-only (`//go:build linux`), and cmsg parsing and the fallback paths need tests
- The Reflector's T3 is a user-space value, so its error is included in the Reflector's Error Estimate
