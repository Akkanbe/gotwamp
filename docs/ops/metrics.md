# Metrics reference (Prometheus)

Metrics that gotwampd exposes on `daemon.metrics.listen` (default `127.0.0.1:9862`). This list is derived from the implementation in `internal/export/prom`. For the specification, see SPEC §9.6.

## Endpoints

| Path | Content |
|------|------|
| `/metrics` | The gotwamp metrics below, plus the standard Go runtime and process metrics (`go_*`, `process_*`) |
| `/healthz` | `200` if the daemon is running and the API responds, otherwise `503` |

Example scrape configuration:

```yaml
scrape_configs:
  - job_name: gotwamp
    scrape_interval: 15s
    static_configs:
      - targets: ["probe-1.example.net:9862", "probe-2.example.net:9862"]
```

If you open `metrics.listen` on an external interface, use a firewall to restrict access to the scrapers (there is no authentication).

## Labels

- Sender metrics (`gotwamp_sender_*`) are per monitoring session. Their labels are `session` (session name), `target`, `mode` (`full` / `light`), and each session's `labels` (up to 8). A user label that a session does not define is set to the empty string (all sessions share one label set, the union of all sessions' label names).
- **Ad-hoc measurements (`gotwamp ping`) are not included in the metrics.**
- Cardinality control: per-packet values go only into histograms, and labeled gauges are updated once per run or window.

## sender (monitoring sessions)

### Packet counts

| Name | Type | Extra labels | Description |
|------|----|-----------|------|
| `gotwamp_sender_packets_sent_total` | counter | | Test packets sent |
| `gotwamp_sender_packets_received_total` | counter | | Reflected packets accepted (unique by Sender Sequence Number) |
| `gotwamp_sender_packets_lost_total` | counter | | Packets with no response before the timeout |
| `gotwamp_sender_packets_duplicate_total` | counter | `direction` = `forward` / `reverse` | Duplicate responses. If the Reflector Sequence Number is the same, the duplicate occurred on the reverse path (`reverse`); if it differs, on the forward path (`forward`) |
| `gotwamp_sender_packets_reordered_total` | counter | | Reordering (RFC 4737) |
| `gotwamp_sender_packets_stray_total` | counter | | Responses not used for measurement: the sum of too-short responses (malformed), responses to packets that were not sent (stray), and responses after the timeout (late). JSON Lines reports `stray` and `late` separately |

### Anomaly counts

These indicate clock or path anomalies. They should stay at `0`.

| Name | Type | Description |
|------|----|------|
| `gotwamp_sender_invalid_rtt_total` | counter | Responses with a negative RTT. Excluded from statistics. Indicates a clock step (time adjustment) during the run (SPEC §8.1) |
| `gotwamp_sender_negative_owd_total` | counter | Responses with a negative one-way delay. Indicates that the clocks at the two ends disagree |
| `gotwamp_sender_tx_timestamp_missing_total` | counter | Responses for which the kernel transmit timestamp (T1') did not arrive within `tx_timestamp_wait`, so the user-space T1 was used (SPEC §8.2) |
| `gotwamp_sender_tx_timestamp_invalid_total` | counter | Responses whose kernel transmit timestamp was discarded as inconsistent: more than 100µs earlier than the user-space T1, or later than T4 |
| `gotwamp_sender_reflected_mismatch_total` | counter | Responses where, with Reflect Octets (RFC 6038, `padding_to_reflect`), the padding requested for reflection did not come back. Still used for measurement |
| `gotwamp_store_results_written_total` / `gotwamp_store_results_dropped_total` / `gotwamp_store_results_failed_total` | counter | With `store.backend: sqlite`: results written to the DB, results dropped because the writer could not keep up, and results rejected by the DB |
| `gotwamp_sender_unsupported_timestamp_format_total` | counter | Responses with a timestamp in a format that cannot be converted (such as the PTPv2 Z bit). Excluded from delay statistics |

### DSCP / ECN (RFC 7750)

Only sessions with `dscp_ecn_monitoring` enabled have values.

| Name | Type | Description |
|------|----|------|
| `gotwamp_sender_dscp_remarked_total` | counter | Packets that reached the reflector with a DSCP different from the one sent (remarked on the forward path) |
| `gotwamp_sender_ecn_ce_total` | counter | Packets marked ECN CE (Congestion Experienced) on the forward path |
| `gotwamp_sender_last_run_dscp_reflected` | gauge | The most frequent DSCP received by the reflector in the latest run or window. Present only when there is data |

### Delay distribution (histograms)

Bucket boundaries are set by `daemon.metrics.rtt_buckets`.

| Name | Description |
|------|------|
| `gotwamp_sender_rtt_seconds` | RTT = (T4 − T1) − (T3 − T2). Excludes reflector processing time |
| `gotwamp_sender_forward_delay_seconds` | Forward one-way delay T2 − T1. Observed only for valid packets when the clocks at both ends are synchronized |
| `gotwamp_sender_reverse_delay_seconds` | Reverse one-way delay T4 − T3. Same conditions as above |
| `gotwamp_sender_reflector_delay_seconds` | Reflector processing time T3 − T2 |
| `gotwamp_sender_control_setup_seconds` | Time from the TWAMP-Control connection to Start-Ack (`mode="full"` only) |

### Latest run / window (gauges)

| Name | Extra labels | Description |
|------|-----------|------|
| `gotwamp_sender_last_run_rtt_seconds` | `stat` = `min` / `mean` / `median` / `max` / `p95` / `p99` / `stddev` | RTT statistics of the latest run |
| `gotwamp_sender_last_run_forward_delay_seconds` | `stat` | Forward delay statistics of the latest run. Present only when one-way delay is valid |
| `gotwamp_sender_last_run_reverse_delay_seconds` | `stat` | Reverse delay statistics of the latest run. Same conditions as above |
| `gotwamp_sender_last_run_jitter_seconds` | `kind` = `ipdv_mean` / `ipdv_max` / `pdv_p95` / `pdv_p99` / `rfc3550` / `p95_p50` | Delay variation. IPDV is the RTT difference between two consecutive packets (RFC 3393), PDV is the difference from the minimum RTT (RFC 5481), `rfc3550` is the smoothed jitter, and `p95_p50` has the same definition as twping's PDV |
| `gotwamp_sender_last_run_loss_ratio` | | Loss ratio (0–1) |
| `gotwamp_sender_last_run_owd_valid` | | 1 if one-way delay is valid (S bit set at both ends and Error Estimate valid) |
| `gotwamp_sender_last_run_success` | | 1 if control and send/receive completed, 0 on failure |
| `gotwamp_sender_last_run_timestamp_seconds` | | End time of the latest run or window (Unix time) |
| `gotwamp_sender_path_hops` | `direction` = `forward` / `reverse` | Most frequent hop count in the latest run. Forward is sent TTL − Sender TTL; reverse is 255 − received TTL |

### Run counts

| Name | Type | Extra labels | Description |
|------|----|-----------|------|
| `gotwamp_sender_runs_total` | counter | `result` = `success` / `error` / `skipped` | Number of runs or windows. `skipped` counts runs not started because the previous run had not finished |
| `gotwamp_sender_run_errors_total` | counter | `stage` | The stage at which a failed run failed (`connect` / `greeting` / `setup` / `request` / `start` / `test` / `stop` / `internal`) |

## reflector

No session labels.

| Name | Type | Labels | Description |
|------|----|-------|------|
| `gotwamp_reflector_packets_received_total` | counter | `kind` = `full` / `light`, `listener` | Test packets received (including dropped ones) |
| `gotwamp_reflector_packets_reflected_total` | counter | `kind`, `listener` | Packets reflected |
| `gotwamp_reflector_packets_dropped_total` | counter | `kind`, `reason` | Packets dropped. See the table below for `reason` |
| `gotwamp_reflector_control_connections_active` | gauge | | Open TWAMP-Control connections |
| `gotwamp_reflector_control_connections_total` | counter | `result` = `accepted` / `rejected` | Control connections accepted, and control connections rejected (Modes=0) by the ACL or the connection limit |
| `gotwamp_reflector_sessions_active` | gauge | `kind` | Stateful Full sessions / Light flows |
| `gotwamp_reflector_light_flows_overflow_total` | counter | | Packets of new flows handled statelessly because `max_flows` was exceeded |

Values of `reason`:

| Value | Applies to | Meaning |
|----|------|------|
| `acl` | Light | The source is not in `reflector.light.acl` |
| `malformed` | Both | Shorter than 14 octets, or larger than `max_packet_size`. For Full Authenticated / Encrypted sessions, also includes packets shorter than 48 octets and HMAC mismatches (packets with a different key, or tampered packets). For Symmetrical Size (RFC 6038) sessions, also includes packets shorter than 41 octets (112 in authenticated modes) |
| `unknown_session` | Full | The source does not match the session's Sender (`verify_sender`) |
| `rate_limit` | Light | Exceeded the per-source `rate_limit_pps` |
| `not_started` | Full | Arrived before Start-Sessions (or, when Start Time is enforced, before Start Time) |
| `drained` | Full | Arrived after Stop-Sessions + Timeout |

The security mode of Full sessions (authenticated / encrypted / mixed) and the RFC 6038 / RFC 5938 extensions are not metric labels. Check each session's mode in the SECURITY column of `gotwamp reflector sessions`. The reasons for authentication failures on control connections (unknown KeyID, wrong passphrase, reused Client-IV) and HMAC mismatches are logged (troubleshooting.md §4, §5).

## daemon

| Name | Type | Labels | Description |
|------|----|-------|------|
| `gotwamp_clock_synchronized` | gauge | | 1 if the clock is synchronized (reflects the `daemon.clock.sync` override) |
| `gotwamp_clock_max_error_seconds` | gauge | | Maximum clock error |
| `gotwamp_timestamping_mode` | gauge | `mode` = `hardware` / `kernel` / `userspace` | Timestamping method actually in use (value is always 1) |
| `gotwamp_jsonl_dropped_total` | counter | | Records dropped because they could not be written to JSON Lines (when `export.jsonl.enabled`). Includes cases where writing cannot keep up and where writing fails (such as a full disk; the file is reopened every second to resume, and failure and recovery are logged) |
| `gotwamp_build_info` | gauge | `version`, `commit`, `go_version` | Build information (value is always 1) |

## Example alerts

```yaml
groups:
  - name: gotwamp
    rules:
      - alert: TwampLossHigh
        expr: gotwamp_sender_last_run_loss_ratio > 0.01
        for: 5m
        labels: { severity: warning }
        annotations:
          summary: "{{ $labels.session }}: loss {{ $value | humanizePercentage }}"
      - alert: TwampRunFailing
        expr: gotwamp_sender_last_run_success == 0
        for: 10m
        labels: { severity: critical }
        annotations:
          summary: "{{ $labels.session }} ({{ $labels.target }}): runs are failing"
      - alert: TwampRttHigh
        expr: gotwamp_sender_last_run_rtt_seconds{stat="p95"} > 0.05
        for: 10m
        labels: { severity: warning }
      - alert: TwampClockUnsynchronized
        expr: gotwamp_clock_synchronized == 0
        for: 15m
        labels: { severity: info }
        annotations:
          summary: "one-way delays are invalid while the clock is unsynchronized"
      - alert: TwampReflectorDropping
        expr: sum by (instance, reason) (rate(gotwamp_reflector_packets_dropped_total[5m])) > 10
        for: 10m
        labels: { severity: warning }
      - alert: TwampTxTimestampInvalid
        # Hardware / kernel transmit timestamps keep being inconsistent: a sign that the PHC is not synchronized to CLOCK_REALTIME
        expr: rate(gotwamp_sender_tx_timestamp_invalid_total[10m]) > 0
        for: 30m
        labels: { severity: warning }
        annotations:
          summary: "{{ $labels.session }}: transmit timestamps disagree with the system clock (check phc2sys)"
      - alert: TwampReflectedOctetsMismatch
        # Reflect Octets (RFC 6038) was requested, but the peer is not returning the padding
        expr: increase(gotwamp_sender_reflected_mismatch_total[1h]) > 0
        labels: { severity: info }
```

For an example dashboard, see [`grafana-dashboard.json`](grafana-dashboard.json) (import it into Grafana and select a Prometheus data source).
