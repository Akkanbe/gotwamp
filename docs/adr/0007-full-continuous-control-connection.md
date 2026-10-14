# 0007. TWAMP Full continuous sessions keep measuring over a single control connection

- Status: Accepted
- Date: 2026-10-04

## Context

Monitoring sessions are either periodic (count packets every frequency) or continuous (continuous sending, aggregated per window) (SPEC §9.3).

SPEC §9.3 says "in Full, the control connection is re-established for each run". For periodic, runs map one-to-one to control connections. However, the SPEC does not define whether a continuous "run" means a single window or the entire sending period that lasts until a failure.

## Decision

- A continuous run is "the entire sending period that lasts until a failure". In TWAMP Full, one test session is started on a single control connection (Count 0), and aggregation is split per window.
- If the control connection drops, or a control or socket operation fails, the run ends. The scheduler resumes on a new control connection after `RetryDelay` (default 5s).
- Periodic is unchanged: a control connection is opened for each run and closed with Stop-Sessions.

## Options considered

| Option | Pros | Cons |
|--------|------|------|
| Continue on a single control connection (chosen) | Sending does not stop at window boundaries (true to the meaning of continuous measurement). Control exchanges and Server-side session allocation do not happen per window | Keeps a long-lived control connection. Events such as a Server restart cause missing data until the loss of the control connection is detected and a reconnect happens |
| Re-establish the control connection per window | Closest to the SPEC wording. Server-side state is fresh for each window | Each window boundary incurs the round trips from Greeting to Start-Ack and a gap for port allocation. It also consumes more of the Server's session limit and SERVWAIT |

## Consequences

- Even for continuous Full sessions, there is no measurement gap between windows.
- The control connection is kept alive by continuously sending test packets, so it is not closed by REFWAIT or similar timers. If the Server closes the control connection, it is recorded as a run failure and retried.
- If re-establishing per window becomes necessary, it can be switched entirely within the daemon's Runner.
