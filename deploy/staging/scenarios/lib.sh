# Shared helpers of the staging scenarios; sourced, not run.
# Needs docker compose and jq on the host.

staging="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
compose=(docker compose -f "$staging/docker-compose.yml")
netem="$staging/netem.sh"
metrics_url=${METRICS_URL:-http://127.0.0.1:19862/metrics}
failures=0

# twping ARGS...: gotwamp ping in the sender; prints the summary as one JSON
# line, with the durations in milliseconds.
twping() {
	"${compose[@]}" exec -T sender gotwamp ping -q --output jsonl "$@" |
		jq -c 'select(.summary) | .summary
			| def ms: if type == "string" then rtrimstr("s") | tonumber * 1000 else . end;
			{sent, received, lost, loss_ratio, reordered, reordered_reverse, dup_forward, dup_reverse, loss_split_valid,
			 lost_fwd: (.lost_forward_est | tonumber), lost_rev: (.lost_reverse_est | tonumber),
			 rtt_median: (.rtt.median | ms), rtt_p95: (.rtt.p95 | ms), rtt_stddev: (.rtt.stddev | ms),
			 fwd_median: (.fwd_delay.median // "0s" | ms), rev_median: (.rev_delay.median // "0s" | ms),
			 ipdv_mean: (.jitter.ipdv_rtt_mean | ms), ipdv_fwd_mean: (.jitter.ipdv_fwd_mean // "0s" | ms),
			 ipdv_rev_mean: (.jitter.ipdv_rev_mean // "0s" | ms), jitter_rfc3550: (.jitter.jitter_rfc3550 | ms)}'
}

# field JSON NAME: a value of a ping summary.
field() { jq -r ".$2" <<<"$1"; }

# report LABEL JSON: one line of the main figures.
report() {
	jq -r --arg l "$1" '"\($l): sent \(.sent) lost \(.lost) (fwd \(.lost_fwd) / rev \(.lost_rev)\(if .loss_split_valid then "" else ", no split" end)) reordered \(.reordered) dup \(.dup_forward)/\(.dup_reverse) | rtt median \(.rtt_median * 100 | round / 100) ms p95 \(.rtt_p95 * 100 | round / 100) ms | fwd \(.fwd_median * 100 | round / 100) rev \(.rev_median * 100 | round / 100) ms | ipdv mean \(.ipdv_mean * 100 | round / 100) ms (fwd \(.ipdv_fwd_mean * 100 | round / 100) rev \(.ipdv_rev_mean * 100 | round / 100)) rfc3550 \(.jitter_rfc3550 * 100 | round / 100) ms"' <<<"$2"
}

# check DESCRIPTION LOW VALUE HIGH: LOW <= VALUE <= HIGH, counted as a failure otherwise.
check() {
	if awk -v lo="$2" -v v="$3" -v hi="$4" 'BEGIN { exit !(lo <= v && v <= hi) }'; then
		echo "  ok   $1: $3 in [$2, $4]"
	else
		echo "  FAIL $1: $3 not in [$2, $4]"
		failures=$((failures + 1))
	fi
}

# metric NAME LABELS: a value from the sender's /metrics, e.g.
#   metric gotwamp_sender_last_run_rtt_seconds 'session="r2-light",stat="median"'
metric() {
	local body
	body=$(curl -fsS "$metrics_url")
	awk -v n="$1" -v l="$2" '
		!found && index($0, n "{") == 1 {
			ok = 1; m = split(l, want, ",")
			for (i = 1; i <= m; i++) if (index($0, want[i]) == 0) ok = 0
			if (ok) { print $NF; found = 1 }
		}' <<<"$body"
}

# wait_window: until the continuous sessions (10s windows) have ended a
# window that started after now.
wait_window() {
	echo "  (waiting 22s for two monitoring windows)"
	sleep 22
}

finish() {
	if ((failures > 0)); then
		echo "$failures check(s) FAILED"
		exit 1
	fi
	echo "all checks passed"
}
