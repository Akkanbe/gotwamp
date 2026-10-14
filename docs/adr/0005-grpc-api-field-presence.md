# 0005. Unset gRPC API fields inherit the daemon defaults, and scalars where 0 is meaningful are optional

- Status: Accepted
- Date: 2026-10-04
- Related: ADR 0001

## Context

`TestSpec` and the monitoring session definition (`SessionSpec`) have the same schema as `sender.sessions[]` in the configuration file.
Fields that are not specified inherit from `sender.defaults`. Users can also change `sender.defaults` itself.
If the API cannot distinguish "not specified" from "0 specified", it cannot express values such as `padding: 0` (the default is 27) or
`dscp: 0` (when the default has been changed to `ef` and the user wants to send 0 explicitly).
proto3 scalars do not track field presence by default.

## Decision

- Unset fields inherit the daemon defaults. For enums, `*_UNSPECIFIED = 0` means "inherit the default"
- Fields of type `google.protobuf.Duration` / `Timestamp` / message types have presence, so "unset = inherit" applies to them as is
- Scalars where 0 can be a valid value different from the default are proto3 `optional`: `receiver_port`, `sender_port`, `count`, `padding`, `dscp`, the bools in `CompatOptions`, and `control_dscp`
- Scalars where 0 is invalid and can stand in for the default (`control_port`, `ttl`) are not `optional`
- For enums, values that must be specified explicitly as distinct from "inherit the default" (`ADDRESS_FAMILY_AUTO`, `PADDING_FILL_AUTO`, etc.) are defined as separate values

## Options considered

| Option | Pros | Cons |
|--------|------|------|
| Give presence with `optional` (chosen) | Generated code exposes `HasX()` / pointers, so the intent shows in the type | Generated code uses pointer types, which is somewhat verbose to handle |
| Wrapper types (`google.protobuf.UInt32Value`) | An older proto3 convention | Verbose, and `optional` can replace it |
| Treat 0 as "default" | Simple | Legitimate values such as `padding: 0` cannot be specified |

## Consequences

- The CLI only needs to set the flags the user specified in the request. Default resolution is centralized in the daemon
- Adding or removing `optional` is detected by `buf breaking` as a breaking change, so this policy is kept for the lifetime of v1
