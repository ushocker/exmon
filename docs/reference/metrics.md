# Metrics

Monitors report **metrics**: numbers measured on every sample, even when nothing changed and no log line was written. [Triggers](../incidents.md#trigger-rules) compare them with thresholds, and [baseline mode](../baseline.md) records them.

Each metric belongs to a **source** (the monitor) and a **subject** (which thing was measured, such as an adapter, a probe or a tunnel). In a trigger, `source` and `metric` are required; `subject` is optional.

The **bad** column shows which direction is a problem. Baseline mode uses it to suggest thresholds: ↓ low is bad, ↑ high is bad, – no suggestion.

## Wi-Fi

Source `wifi`, subject = adapter description (e.g. `Intel(R) Wi-Fi 7 BE200`). Reported every `wifi.pollSeconds` for each connected adapter.

| Metric | Bad | Unit | Meaning |
|---|---|---|---|
| `rssi_dbm` | ↓ | dBm | Signal strength (-50 excellent, -70 fair, -80 poor). Only when the access point is in the scan list. |
| `signal_quality` | ↓ | 0–100 | Windows' signal quality. |
| `rx_kbps`, `tx_kbps` | ↓ | kbps | Current link rates. |
| `retry_pct` | ↑ | % | Frames needing a retry since the previous poll. Only when ≥ `minStatsFrames` frames were sent and the driver's counters are believable. |
| `fcs_pct` | ↑ | % | Received frames with a bad checksum, under the same conditions. |
| `handshake_fail` | ↑ | count | Four-way handshake failures since the previous poll. |
| `recent_disconnects` | ↑ | count | Disconnects within the last `wifi.disconnectWindowMinutes` (default 30). It starts from 0 when exmon starts. The old name `disconnects_30m` is rejected. |

## Network adapters

Source `nic`, subject = adapter name (e.g. `Ethernet`, `Wi-Fi`). Reported every `nic.pollSeconds` for each up adapter. Counters are growth since the previous poll.

| Metric | Bad | Unit | Meaning |
|---|---|---|---|
| `link_speed_mbps` | ↓ | Mbps | Negotiated link speed. |
| `rx_bps`, `tx_bps` | – | bits/s | Throughput. |
| `in_errors`, `out_errors` | ↑ | count | Packets with errors. |
| `in_discards`, `out_discards` | ↑ | count | Packets dropped (often buffer exhaustion). |

## VPN

Source `vpn`, subject = tunnel name. Reported every `vpn.pollSeconds` for each detected tunnel.

| Metric | Bad | Unit | Meaning |
|---|---|---|---|
| `vpn_up` | ↓ | 0/1 | Tunnel is up. |
| `vpn_server_rtt_ms` | ↑ | ms | Ping to a built-in VPN's server, when it answers. |
| `in_errors`, `out_errors` | ↑ | count | Tunnel adapter errors since the previous check. |

## Services

Source `services`, subject = the service name from `services.watch`. Reported every `services.pollSeconds` for each watched service.

| Metric | Bad | Unit | Meaning |
|---|---|---|---|
| `running` | ↓ | 0/1 | The service is running. |
| `as_expected` | ↓ | 0/1 | The service is in its `expect` state (`running` by default; always 1 for `expect: any`). A trigger on `as_expected < 1` catches any watched service that stops or disappears. |

## Probes

Source `probe`. Reported every `probe.intervalSeconds`.

| Metric | Subject | Bad | Unit | Meaning |
|---|---|---|---|---|
| `dns_ms` | `dns:<name>` | ↑ | ms | Uncached lookup time. Missing when the lookup fails. |
| `gateway_rtt_ms` | `gateway` | ↑ | ms | Average gateway ping. Missing when no ping returns. |
| `gateway_loss_pct` | `gateway` | ↑ | % | Gateway ping loss. |
| `tcp_connect_ms` | endpoint name | ↑ | ms | TCP connect time. Missing when the connect fails. |
| `rtt_avg_ms` | endpoint name | ↑ | ms | Average ping. Missing when no ping returns. |
| `loss_pct` | endpoint name | ↑ | % | Ping loss. |
| `reachable` | endpoint name | ↓ | 0/1 | TCP connect succeeded. |
| `http_total_ms` | test name | ↑ | ms | Whole request. |
| `http_ttfb_ms` | test name | ↑ | ms | Time to first byte. |
| `http_tls_ms` | test name | ↑ | ms | TLS handshake (https only). |
| `http_ok` | test name | ↓ | 0/1 | Status below 400. |

A metric that is **missing** in a sample doesn't count for or against a trigger. So to catch a failure, trigger on the 0/1 metrics (`reachable < 1`, `http_ok < 1`) or on loss, not only on timings.

## Notes for trigger writers

- Ping times have 1 ms resolution, so fast LAN hops read 0.
- Combine `forSamples` with the poll interval to set how long a condition must last. For example, `forSamples: 3` on a 5-second Wi-Fi poll means about 15 seconds.
- `subject` must match exactly, e.g. `"subject": "Wi-Fi"` for the nic adapter named Wi-Fi.
