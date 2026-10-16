# Embedding gotwamp

Import `github.com/Akkanbe/gotwamp/sender` and
`github.com/Akkanbe/gotwamp/reflector` to run TWAMP inside another Go program.
The root `gotwamp` package provides shared measurement, timestamping, security,
credential and error types. No daemon, gRPC connection, configuration file,
scheduler, storage or exporter is needed. Runtime UDP timestamping requires
Linux; other systems can compile and validate configuration, but opening UDP
sockets returns an error matching `errors.ErrUnsupported`.

The library is part of the same v0.x module as the binaries. Public API changes
may occur between minor versions and are recorded in the changelog. Pin a
version or commit when embedding it.

## Light sender

```go
target := netip.MustParseAddrPort("127.0.0.1:862")
cfg := sender.DefaultConfig(target) // 100 packets; default padding is 27 bytes
cfg.Timestamping = gotwamp.TimestampAuto
if err := cfg.Validate(); err != nil {
    return err
}
summary, err := sender.RunLight(ctx, cfg, sender.WithHooks(sender.Hooks{
    Summary: func(s sender.Summary) {
        // Consume bounded events quickly, for example update local counters.
    },
}))
```

`Config{Count: 0}` runs continuously and emits one summary per `Window` until
cancellation. This zero value does not select the default packet count.
`Config.WithDefaults` fills timing defaults without network activity;
`Config.Validate` applies the same defaults while checking a copy. Zero padding
means zero padding, not the normal 27-byte padding.

For explicit acquisition, use `sender.New`, inspect `LocalAddr`, call `Run` once
and then `Close`. `RunLight` owns acquisition and cleanup for a complete run.
Cancellation returns the context error and an aborted summary. Pending packets
are retained as `Pending`, not declared lost. `Close` is idempotent and can
interrupt a running socket; wait for `Run` to return before consuming its final
state. Do not call `Run` concurrently on the same sender.

Hooks execute synchronously in the run goroutine and must return quickly. A
blocked hook delays packet reception and cancellation. Consumers that can block
must supply their own bounded queue and overflow policy. A clock callback is
optional; without `WithClock`, the sender advertises unknown clock quality and
one-way delay is invalid.

## Full sender

```go
cfg := sender.DefaultConfig(netip.AddrPort{}) // Target is negotiated
full := sender.FullConfig{
    Server: netip.MustParseAddrPort("127.0.0.1:862"),
    Modes: gotwamp.ModeEncrypted,
    KeyID: "measurement",
    Passphrase: sharedSecret, // supplied by the embedding application
}
if err := sender.ValidateFull(cfg, full); err != nil {
    return err
}
summary, err := sender.RunFull(ctx, cfg, full)
```

Full runs own one TCP control connection and one UDP sender socket. Continuous
windows keep that connection and session. Modes can select unauthenticated,
authenticated, encrypted or mixed mode; zero accepts unauthenticated only. When
several are allowed, negotiation chooses the strongest offered mode in the
existing order: encrypted, authenticated, mixed, unauthenticated. The caller
controls the allowed set; authentication does not silently fall back outside it.
Mixed mode encrypts control but leaves test packets unauthenticated.

`FullConfig.Validate` validates control settings and credentials;
`sender.ValidateFull` additionally checks test and socket settings. Both are pure
and use the same checks as `RunFull` before dialing. An optional `Accepted`
callback receives `gotwamp.SID` and the negotiated reflector endpoint; `Started`
marks successful Start-Ack. These callbacks also must return quickly.

After cancellation the sender attempts Stop-Sessions for at most two additional
seconds, then releases its resources. A control failure is a
`*gotwamp.ControlError`, usable with `errors.As`; inspect its `Stage` and `Accept`,
and use `errors.Is` with sentinels such as `gotwamp.ErrRejected`,
`gotwamp.ErrTimeout` or `gotwamp.ErrControlLost`. Results from a lost control
connection are marked `Invalid`.

## Responders

```go
light, err := reflector.ListenLight(reflector.Config{
    Listeners: []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:0")},
    ACL: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")},
    MaxFlows: 4096,
    RateLimitPPS: 100,
}, reflector.Deps{})
if err != nil {
    return err
}
defer light.Close()
return light.Serve(ctx)
```

```go
full, err := reflector.ListenFull(reflector.FullConfig{
    Listeners: []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:0")},
    ACL: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")},
    Modes: gotwamp.ModeEncrypted,
    Credentials: gotwamp.Credentials{"measurement": sharedSecret},
    MaxConnections: 16,
    MaxSessions: 64,
}, reflector.Deps{})
if err != nil {
    return err
}
defer full.Close()
return full.Serve(ctx)
```

Full includes TWAMP-Control and the UDP Session-Reflector. UDP test ports are
allocated on accepted session requests, subject to the configured port range,
source checks, limits and timeouts. Credentials can be supplied through
`gotwamp.CredentialStore`; the application owns loading and rotation. A custom
store must be safe for concurrent calls. Do not mutate a `Credentials` map while
it is in use.

Both listeners apply defaults and validate before binding. A partial bind
failure closes listeners already acquired. `Addrs` reports actual addresses,
including selected ports when binding port zero. `Serve` is one-shot; normal
context cancellation or `Close` returns nil. `Close` is idempotent, works before
or during `Serve`, wakes it and joins owned workers. Full shutdown also joins
control connections and UDP sessions. `Status`, `Flows` and `Sessions` return
snapshots suitable for concurrent observation.

Review defaults before exposing a responder: empty ACLs allow all sources;
default listeners bind port 862 on IPv4 and IPv6 wildcard addresses. Light's
zero rate limit is unlimited. Flow state is bounded even with copy-sender
sequence mode; copying a sequence does not disable flow statistics. UDP read
errors other than a closed socket retain the existing logged retry policy.
Full control temporary accept errors retry; a permanent listener error stops
the service, releases its other listeners and sessions, and is returned by Serve. These behaviors are not a
health check for reachability from another host.

## Interpreting results

- Corrected RTT is `(T4-T1)-(T3-T2)`. `PacketResult.Result.Metrics.RTTRaw` is
  `T4-T1`; it includes reflector processing. Raw RTT has no aggregate distribution.
- A distribution's `Count == 0` means unavailable; its other fields are
  undefined. `Samples` describes percentile sampling and can be below `Count`.
- `LossRatio` uses `Lost/(Sent-Pending)`. Check the denominator; zero with no
  decided packets does not establish zero loss. `SendErrors` are local failures,
  excluded from `Sent`. Aborted and invalid runs retain explicit flags.
- One-way values may be present even when `OWDValid` is false. Both clocks must
  be synchronized with known estimates and nonnegative one-way delays. Inspect
  clock-error unknown and unsupported-format flags too.
- Directional loss estimates require `LossSplitValid`. A copy-sender reflector
  cannot generally establish forward/reverse loss independently.
- Jitter fields have different meanings: IPDV uses consecutive sender sequences
  that were both received, PDV uses distance from minimum RTT, and RFC3550 is an
  arrival-order smoothing estimator. Select and label the intended measure.
- Wire and kernel timestamps use CLOCK_REALTIME. Clock steps can invalidate
  delays; negative corrected RTT samples are counted separately and excluded.
  Actual timestamping mode is reported, including fallback from hardware/kernel.

## Capability and interoperability boundaries

Full TCP negotiation and test traffic support IPv4/IPv6 and all four security
modes. Independent perfSONAR interoperability tests cover both directions and
security modes (`docs/ops/interop.md`). Light has unit, fuzz and loopback tests;
independent vendor Light interoperability is not established by those tests.
RFC 5938 Individual Session Control and RFC 6038 size/padding extensions have
implementation tests; external-peer coverage is separate. RFC 7750 DSCP/ECN
monitoring currently applies to Light only. NTP timestamps are supported; PTPv2
timestamp decoding is not. STAMP support is limited to the documented base
packet/SSID behavior, not the complete authenticated/TLV protocol.

Hardware code requires a supported NIC, privileges and PHC synchronization to
CLOCK_REALTIME. Unit/loopback tests cannot verify physical timestamp accuracy.
Binding an explicit local source can identify the relevant interface; daemon
hardware-interface policy remains private. Portable UDP backends and a public
low-level control/codec API are outside this embedding API.

`test/library` is an independent consumer module. `make test-library` checks
typed imports and ensures sender/reflector dependencies exclude daemon,
configuration, storage, gRPC/protobuf and exporters. Linux public-API loopback
tests exercise Full and Light, including Full security modes.
