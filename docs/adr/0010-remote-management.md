# 0010. Also expose the gRPC API over TCP + mTLS (remote management)

- Status: Accepted
- Date: 2026-10-04
- Related: ADR 0001 (gRPC over UDS), SPEC §9.5 / §9.10 / §13, PLAN §6 M7-5

## Context

ADR 0001 decided to expose the gotwampd gRPC API only on a Unix domain socket and to authorize via file permissions.
As the number of measurement points grows, users want to check status and manage monitoring sessions without logging in to each host.
On the other hand, the API can start measurements and create or delete sessions, so exposing it on the network must satisfy three points.

- The peer can be authenticated.
- The channel can be protected.
- A misconfiguration does not expose it unprotected.

## Decision

- **Exposure**: Expose the same gRPC server over TCP only when `daemon.api.tcp.listen` is set. The UDS always remains. The default is UDS only.
- **mTLS is mandatory**: Over TCP, accept nothing but TLS 1.3 with mutual authentication.
  - A config missing any of `ca` / `cert` / `key` fails config validation.
  - If a certificate or key cannot be read, startup fails (exit code 2).
  - Plaintext TCP cannot be configured at all.
- **Authorization**: Decided in two stages.
  1. The client certificate is signed by the CA in `ca`.
  2. If `allowed_names` is set, the Common Name or one of the SANs (DNS, IP, URI, email address) is in the list. DNS names are case-insensitive.

  If `allowed_names` is empty, a CA signature alone is enough, and config validation emits a warning. There are no per-RPC permissions (such as read-only). An authorized peer has the same privileges as a member of the UDS group.
- **Handshake**: Runs in a goroutine separate from Accept, with a 10-second deadline. Slow or malicious peers cannot block acceptance of other connections.
  - Refused connections are logged at Warn (`API client refused`) with the peer address and reason.
  - Accepted connections are logged at Info with the peer name.
- **Private key permissions**: `config.Load` checks that `daemon.api.tcp.key` is readable only by its owner (0600 or stricter); otherwise the daemon does not start. The same rule applies to the key files for M7-1 (`sender.keys_file`, `reflector.control.keys_file`).
- **CLI**: `--server host:port` and `--tls-ca` / `--tls-cert` / `--tls-key` (environment variables `GOTWAMP_SERVER` / `GOTWAMP_TLS_*`) switch the target.
  - `--server` requires all three files.
  - TLS flags without `--server` are an argument error.
  - The server certificate is verified against the host name in `--server`, which `--tls-server-name` can override.
- **Certificate renewal**: Applied on restart. `daemon.api` is not subject to reload (ADR 0008).
- **OTLP export**: Deferred for now (TODO). It can be added later as an exporter implementing `store.ResultSink` and does not affect this decision.

## Options considered

| Option | Pros | Cons |
|--------|------|------|
| Expose the same gRPC server on both UDS and TCP with mTLS (chosen) | One implementation and one service definition. UDS operation is unchanged. Only the listener is swapped, as ADR 0001's extension point intended | Cannot have fine-grained permissions, such as making only TCP read-only |
| Run a separate gRPC server for TCP (`grpc.Creds`) | Certificates are available through gRPC peer info | Service registration and shutdown are duplicated. Refusal logs go to grpclog instead of slog |
| TLS only (no client certificate) + token auth | Simple key distribution | Token storage and revocation need a separate design. Tends to become weak when misconfigured |
| Forward the UDS over SSH port forwarding (no change to gotwampd) | No implementation needed | High operational cost. Unsuitable for always-on connections from monitoring systems |

## Consequences

- From outside the measurement point, status, session, watch, and so on work with the CLI as is.
- Plaintext TCP exposure cannot be configured. Configs with loose key permissions do not start.
- Per-RPC permissions, automatic certificate reload, and OTLP export will be revisited later.
