# 0009. Do not send ad hoc measurement results to the result store or exporters

- Status: Accepted
- Date: 2026-10-04

## Context

gotwampd has two kinds of measurements: monitoring sessions (defined in the config file or through the API and run repeatedly) and ad hoc measurements via `gotwamp ping` (RunTest).

SPEC §9.6 states that "ad hoc measurements are not included in metrics". It says nothing explicit about the result store (§9.4) or JSON Lines (§9.7).

## Decision

- Ad hoc measurement results are not sent to the result store (GetSessionStats / WatchResults), Prometheus, or JSON Lines. Only the RunTest stream receives them.
- The start and end of each run are logged (info) with the run_id and target.
- Monitoring session results go to every sink (result store, Prometheus, JSON Lines). Runs of Full sessions carry the SID.

## Options considered

| Option | Pros | Cons |
|--------|------|------|
| Stream only (chosen) | Temporary investigations do not add metric labels or time series. Consumers of JSON Lines need not distinguish steady-state monitoring from ad hoc runs | Ad hoc results cannot be queried later (save them with the CLI's `--output json`) |
| Also write to JSON Lines | Investigation results remain in a file | Consumers must handle an (empty) session name to tell them apart. Operators have a hard time telling when and by whom a measurement was run |
| Send to everything | Consistent | Pollutes Prometheus cardinality and time series (contradicts SPEC §9.6) |

## Consequences

- Metrics and JSON Lines become stable series that contain only monitoring sessions.
- To keep an ad hoc measurement as a record, save the output of `gotwamp ping --output jsonl`, or define it as a monitoring session.
