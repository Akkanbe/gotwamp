# 0006. Use a StopTest RPC to stop an ad hoc measurement midway

- Status: Accepted
- Date: 2026-10-04
- Related: ADR 0001

## Context

`gotwamp ping` receives results through the RunTest server stream. SPEC §10.4 requires that, when interrupted by SIGINT, it "shows the summary up to the point of interruption" and exits with code 130.

However, when a gRPC client cancels an RPC, the stream closes immediately. The aborted summary that the daemon builds after the interruption never reaches the client. With server streaming, the client also has no way to tell the daemon to stop.

## Decision

- Add `SenderService.StopTest(run_id)`. The run_id is included in every RunTest event.
- The daemon registers in-progress ad hoc measurements by run_id. StopTest cancels only the measurement's context.
  - The stream itself is not closed. The daemon sends the aborted summary and then finishes normally. No error event is emitted.
  - In TWAMP Full, it sends Stop-Sessions as usual before finishing.
  - It returns NotFound for a finished or unknown run_id.
- The CLI handles SIGINT in two stages.
  - First: call StopTest, read the stream to the end, show the summary, and exit with code 130.
  - Second: cancel the RPC, print a simple aggregate from what has been received, and exit with 130.
  - Commands other than ping exit immediately on the first SIGINT.

## Options considered

| Option | Pros | Cons |
|--------|------|------|
| StopTest RPC (chosen) | Keeps the shape of RunTest (server streaming) unchanged. Other clients can also stop the run | Adds one more RPC. Anyone who knows the run_id can stop it (within the scope protected by UDS permissions) |
| Make RunTest a bidirectional stream | Everything fits in a single RPC | Makes the API more complex. Every client must manage a send side for an upstream direction that is rarely used |
| CLI aggregates locally | No daemon changes needed | Sent counts and loss decisions (timeout) diverge from the daemon. The result is not the SPEC summary |

## Consequences

- Even with Ctrl-C, a summary in the same format as a normal finish (marked aborted) is shown.
- Even if the daemon does not respond, a second Ctrl-C always exits.
