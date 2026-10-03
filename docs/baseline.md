# Baseline mode

Baseline mode helps you choose trigger thresholds from evidence instead of guesses. It records **every measurement** to a file (including the per-poll metrics the normal log never shows), and keeps a running summary with percentiles and suggested triggers.

Turn it on with `--baseline`, or `"baseline": { "enabled": true }`. It runs alongside everything else, and is off by default.

```bash
exmon.exe --baseline
```

Leave it running through a normal working day or week, including busy periods, then read the summary.

## Files

Both files go in the same folder (default `%ProgramData%\exmon\`).

### `baseline.jsonl`: everything

One JSON object per event, including a `METRICS` event for every sample from every monitor:

```json
{"time":"2026-10-03T00:43:43.097-05:00","source":"wifi","kind":"METRICS","subject":"Intel(R) Wi-Fi 7 BE200","metrics":{"rssi_dbm":-52,"rx_kbps":1201000,"signal_quality":84,"tx_kbps":1201000,"recent_disconnects":0}}
```

This loads easily into Excel or Power BI (JSON lines), pandas (`pd.read_json(path, lines=True)`) or `jq`. It rotates like the main log (`maxSizeMB`, `maxFiles`).

### `baseline-summary.txt`: the summary

Rewritten every `summaryMinutes` (default 15) and on exit:

```
exmon baseline summary
covering 2026-10-03 09:00:00 to 2026-10-03 17:00:00 (8h0m0s)

METRICS
source  subject     metric          count  min   p5    p50   p95   p99   max
probe   gateway     gateway_rtt_ms  96     0     0     1     3     9     41
wifi    Intel(R)... rssi_dbm        5760   -71   -66   -55   -50   -49   -47
...

EVENTS
source    kind                count
wifi      ACM/disconnected    2
wifi      POLL                842
...

SUGGESTED TRIGGERS (a starting point from the observed tail; paste into "incident"."triggers")
  { "name": "wifi-rssi_dbm-low", "source": "wifi", "metric": "rssi_dbm", "op": "<", "threshold": -66, "forSamples": 3, "severity": 3 },
  { "name": "probe-gateway_rtt_ms-high", "source": "probe", "metric": "gateway_rtt_ms", "op": ">", "threshold": 9, "forSamples": 3, "severity": 3 }
```

- **p5 / p50 / p95 / p99** are percentiles. p95 = 9 means 95% of samples were 9 or less.
- **EVENTS** counts how often each kind of line occurred, e.g. how many disconnects happened.
- **Suggested triggers**: for metrics where low is bad (signal, rates, `reachable`), the suggestion is "below the 5th percentile"; where high is bad (latency, loss, errors), "above the 99th percentile". So by design they fire on roughly the worst 1–5% of what this machine normally sees.

## Using the suggestions

Treat them as a starting point:

- **Widen the margin.** A suggestion of `rssi_dbm < -66` fires on ordinary bad moments. Something like -75 catches real problems.
- **Ignore suggestions of `> 0`** for counters that were always 0 during the baseline (`in_errors`, `handshake_fail`). They would fire on the first error. Decide whether that is what you want.
- **Ignore `gateway_rtt_ms > 0` on a fast LAN.** Ping has 1 ms resolution, so the baseline is all zeros.
- **Set `severity`** to fit the impact (see [Incidents](incidents.md#severity)).
- **Keep `forSamples` at 2–3** for noisy metrics.

Metrics without a "bad direction" (such as `rx_bps`) appear in the table but get no suggestion. Percentiles are based on a random sample of up to 10,000 values per metric, so memory stays small even if baseline mode runs for weeks.
