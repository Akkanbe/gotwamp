# 0015. Expose curated embedding APIs over shared engines

- Status: Accepted
- Date: 2026-10-16
- Related: SPEC section 4, docs/LIBRARY.md

## Context

The internal engines already implement Light and Full. Go's internal import
boundary prevents independent applications from using them. Full responder
composition belongs to the daemon, and operational results refer to internal
value types. Publishing every engine extension point would expose test socket
injection and cryptographic session machinery.

## Decision

Expose sender/ and reflector/ packages with deliberately selected entry points
and aliases to canonical configurations, results and service implementations.
Keep one internal runtime, including private transport seams used by the daemon
and tests. Move shared value definitions to the dependency-free root package;
internal codecs and accumulation use aliases to those definitions. Public
consumers can name every required type without internal imports or DTO copying.

The internal reflector owns composed Full control and UDP service lifetime;
both daemon and public API use it. Configuration validation precedes acquisition.
Responder Close wakes Serve and joins workers, including Full connections and
sessions. Sender hooks remain synchronous with an explicit nonblocking contract.

## Options considered

| Option | Benefit | Cost |
|---|---|---|
| Curated entry points and canonical type aliases (chosen) | One implementation and one representation; private daemon/test seams survive | Public aliases must be treated as contracts during internal changes |
| Publish internal packages wholesale | Few import changes | Exposes control/crypto and transport implementation details permanently |
| Separate public DTOs and conversion layer | Independent representation | Duplicate fields, conversion maintenance and drift risks |

## Consequences

No daemon dependency is introduced into measurement engines. Wire codecs,
socket interfaces, injected dialing/listening, control state machines and
statistics accumulation remain private. Public API evolution follows the module's
v0.x compatibility policy. An independent consumer module verifies the exported
type graph and dependency boundary. Linux remains the runtime platform.
