# 0008. Limit what a config reload applies to monitoring sessions, sender.defaults, and the log level

- Status: Accepted
- Date: 2026-10-04

## Context

A config reload via SIGHUP or `ReloadConfig` must not interrupt unchanged monitoring sessions (FR-15, a completion criterion of PLAN M5).

However, many settings involve socket binds (the API UDS, metrics, the reflector listener), files (JSON Lines), or process-wide behavior (timestamping, clock, realtime, limits). Swapping these at runtime interrupts connections and measurements.

## Decision

- On reload, first validate the whole new config file. If it is invalid, change nothing and return an error.
- Only the following three are applied immediately.
  - **Monitoring sessions**: Match by name and rebuild only the ones that were added, removed, or changed. Unchanged sessions are left alone. Running runs continue. A session that was stopped is rebuilt and stays stopped.
  - **`sender.defaults`**: Becomes the default for subsequent ad hoc measurements and for sessions created through the API. Its effect on existing sessions is handled above as a session change.
  - **`daemon.log.level`**
- For all other settings (`daemon.api`, `daemon.metrics`, `daemon.log.format`, `daemon.timestamping`, `daemon.clock`, `daemon.realtime`, `daemon.limits`, `sender.enabled`, `reflector`, `export`), a detected change produces the warning "restart gotwampd to apply changes of ...". The running values are kept as is.
- Sessions created through the API are not subject to reload. (If one collides by name with a session from the config file, return an error and change nothing.)
- The reload result (added, removed, updated, unchanged, warnings) is returned in `ReloadConfigResponse` and logged.

## Options considered

| Option | Pros | Cons |
|--------|------|------|
| Apply a limited scope (chosen) | Updates monitoring sessions, the most frequently changed item, without stopping measurements or sockets. Behavior is predictable | Changes to the reflector or exporters require a restart |
| Apply everything (rebind as needed) | No restart needed | Replacing listeners drops reflection and control connections. Rolling back on failure becomes complex |
| Reload only validates | Simple to implement | Every session addition needs a restart, which stops measurements |

## Consequences

- Adding or changing monitoring sessions needs only SIGHUP. Measurements of unrelated sessions are not interrupted.
- The warnings keep operators from missing changes that require a restart.
