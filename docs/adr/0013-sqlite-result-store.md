# 0013. Persist results in SQLite (pure Go driver modernc.org/sqlite), wrapped with an in-memory front and asynchronous writes

- Status: Accepted (implemented and wired into the daemon; payload compression (schema version 2) added on 2026-10-05)
- Date: 2026-10-05
- Related: SPEC §9.4, §13, PLAN §6 M7-7, ADR 0009

## Context

Monitoring session results (run / window summaries and run errors) existed only in the `store.Memory` ring buffer. As a result, restarting gotwampd erased the `gotwamp stats` history.

SPEC §13 separates the store interface from the in-memory implementation so that persistence can be added later. Choosing a persistence method came with these constraints.

- Keep the `CGO_ENABLED=0` static binary and the distroless image (quality criteria in CLAUDE.md).
- Do not stall the measurement hot path on slow disks (ResultSink's "Publish does not block").
- Operators can inspect the contents without special tools.
- Do not change existing read semantics (Recent returns the latest N entries, Range returns a time range).

## Decision

- **Database**: Use SQLite with the pure Go driver `modernc.org/sqlite`.
  - WAL, `synchronous=NORMAL`, a single connection.
  - The schema has a version table and is migrated one version at a time at startup. A DB with a newer version is not opened.
- **Structure**: `sqlite.Store` has a `store.Memory` in front.
  - **Publish**: Delivered immediately to memory and watchers. A dedicated writer goroutine writes to the DB in batches, one transaction each (up to 256 entries).
    - If the queue (default 4096 entries) overflows, the result is counted without being saved and a Warn is logged. Measurement does not stop.
  - **Reads**:
    - Recent / Packets / Sessions / Watch are answered from memory. At startup, the latest `result_history` entries per session are loaded from the DB.
    - Range waits for the writer to catch up, then queries the DB. It reaches back as far as retention.
- **What is stored**: Only run / window summaries and run errors. Per-packet records are not stored (they are voluminous, and summaries are sufficient).
- **Encoding**: Summaries and errors are represented as JSON (with Go field names). Fields added later read as zero values in old rows.
  - **Schema version 2 (2026-10-05)**: Result rows (`results.data`) are stored as JSON with fields equal to the type's zero value omitted, compressed with DEFLATE using a fixed dictionary (the first octet is the format: `0x01`). In soak measurements, one entry went from 4.1KB to about 0.6KB. Version 1 rows (JSON, starting with `{`) are read as is without conversion. A version 1 gotwampd does not open a version 2 DB, treating it as a "newer schema".
  - The formats are described in the `payload_formats` table. Compressed rows cannot be read directly with `sqlite3`; read them with `gotwamp stats` or the API.
  - Session descriptions (SessionInfo) with identical content are deduplicated into one row (table `infos`, kept as JSON and readable with `sqlite3`).
  - Session definitions (`config.Session`) are a type the store does not know, so they are encoded with a codec supplied by the daemon.
  - The description caches (one for the writer, one for readers) hold weak references and do not retain generations that no event references anymore. A newly written description enters the cache only after its transaction commits. Readers do not hold the cache lock while reading the DB (to avoid deadlock with the single-connection writer).
- **Maintenance**:
  - Rows past retention (default 30 days) are deleted at startup and every hour.
  - A Warn is logged when the combined size of the DB and WAL exceeds the limit.
  - By default, a corrupt DB is moved aside to `<path>.corrupt-<timestamp>` and the daemon starts with an empty one. For deployments that prefer to stop, a setting to refuse startup is also provided.
- **Contract tests**: The same contract tests (`internal/store/storetest`) run against both memory and sqlite.

## Options considered

| Option | Pros | Cons |
|--------|------|------|
| SQLite + pure Go driver modernc.org/sqlite (chosen) | Works with `CGO_ENABLED=0`. Queryable with SQL, with time-range indexes. A single file is easy to back up | The binary grows by several MB. Slower than C SQLite (still, writes run at about 5,000 entries/s including compression (`BenchmarkPublish -benchtime 20000x`, 4 vCPU QEMU; 12,000 to 16,000 entries/s before compression), which is well above the 1000 entries/s requirement) |
| SQLite + mattn/go-sqlite3 (cgo) | Fast and widely proven | Requires cgo, contradicting the static binary and cross-build policy |
| Embedded KV store (bbolt, pebble, and so on) | Pure Go and lightweight | Time-range queries and content inspection must be built ourselves |
| Re-read the JSON Lines export | No extra dependencies | No indexes, so startup loading and range queries are slow. Gaps from rotation |
| External DB / TSDB (PostgreSQL, Prometheus remote write, and so on) | Suited to large-scale aggregation | Requires operations outside gotwampd. Overlaps with the existing split where long-term time series are left to Prometheus |

## Consequences

- Session history and time-range queries survive restarts. `gotwamp stats --since` (`since` in `GetSessionStats`) uses the DB Range and reaches back as far as retention. Without `--since`, it returns the latest `--last` entries (default 10) from the in-memory history (latest `result_history` entries).
- Write lag is made visible by counting it as Dropped, without stopping measurement.
- Per-packet persistence, automatic DB VACUUM, and multi-host aggregation are out of scope. They will be revisited when needed.
- Config keys are `store.backend` (`memory` / `sqlite`) and `store.sqlite.path` / `retention` / `max_size` / `on_corrupt` (docs/ops/config.md). `gotwamp status` shows the backend and write counts, and the metrics `gotwamp_store_results_*_total` expose the counters.
- Capacity estimate: about 0.6KB per entry x entries x retention days (docs/ops/config.md).
