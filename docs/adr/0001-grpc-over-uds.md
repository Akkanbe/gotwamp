# 0001. Use gRPC over a Unix domain socket for IPC between the daemon and the CLI

- Status: Accepted
- Date: 2026-10-03

## Context

gotwamp consists of two binaries: the resident daemon `gotwampd` and the CLI `gotwamp`. The CLI does not send or receive packets itself.
Even ad hoc measurements (`gotwamp ping`) are delegated to the daemon. The IPC between the daemon and the CLI must therefore:

- Stream per-packet results to the CLI in real time during a measurement (server streaming)
- Define many operations with types, such as CRUD for monitoring sessions, fetching statistics, and showing status
- Make authorization simple and fail-safe
- Be extensible to remote management (TCP + TLS) in the future

## Decision

- Use gRPC for IPC. In v0.x, listen only on a Unix domain socket (default `/run/gotwamp/gotwampd.sock`). Do not expose it over TCP
- Authorize through UDS file permissions. The socket is `0660` with group `gotwamp`. Users of the CLI are added to that group
- Define the API as protobuf package `gotwamp.v1` in `api/proto/gotwamp/v1/`. Use `buf` for linting, code generation, and breaking-change detection. Commit the generated code to `internal/api/gen`
- Split the services into `DaemonService` (status, reload), `SenderService` (measurements, monitoring sessions), and `ReflectorService` (reflector status). Return measurement results as `stream TestEvent`
- When the `RunTest` RPC is canceled, the daemon aborts the run. In Full mode it sends Stop-Sessions

## Options considered

| Option | Pros | Cons |
|--------|------|------|
| gRPC over UDS (chosen) | Typed API and server streaming. `buf breaking` enforces compatibility mechanically. Can be extended to TCP + mTLS by swapping the listener | Adds dependencies on grpc / protobuf |
| Custom JSON over UDS | Few dependencies | Streaming, error representation, and compatibility management must be built in-house |
| HTTP REST (+ SSE) | Callable with curl | Type definitions and a client must be provided separately. Streaming is cumbersome |
| CLI measures on its own (no daemon dependency) | Easy, no daemon needed | Measurement code runs in two places. Handling of privileges (CAP_NET_BIND_SERVICE) and timestamp settings becomes scattered |

## Consequences

- The CLI becomes a thin gRPC client, and measurement logic is concentrated in the daemon
- The grpc / protobuf dependencies stay in the outer layers (`internal/daemon`, `internal/cli`, generated code) and are not brought into the protocol layer
- Daemon-independent CLI measurement (`gotwamp ping --direct`) and exposing gRPC over TCP + mTLS will be reconsidered later
