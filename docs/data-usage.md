# Data usage

How much data the [remote queue](remote.md) would send per machine and across a fleet, depending on what `remote.include` selects. Use it to size SIEM ingestion and licensing before choosing selectors.

These are **estimates**. There is no real remote target yet, so they are measured from the `remote-outbox.jsonl` stand-in and the baseline file, not from a live SIEM.

## How the numbers were measured

The measurements come from one desktop over about 18 hours. It was on the starter config: Wi-Fi and Ethernet both connected, one Tailscale tunnel, and probes for 1 DNS name, 1 endpoint and 1 HTTP test every 5 minutes.

- **Record size:** each forwarded event is one JSON record of 140–370 bytes. That's the event (90–320 bytes, depending on kind) plus about 50 bytes of `id`, `host` and wrapping.
- **Counts per day** for the noisy kinds:

| Kind | Per machine per day | Why |
|---|---|---|
| Wi-Fi `ACM/scan_complete`, `scan_list_refresh`, `network_available` | ~2,300 | Windows scans for networks about once a minute |
| Wi-Fi `POLL` / `STATS` | ~5,800 each | heartbeat every 15s, plus changes |
| `NIC/status` | ~2,500 | every 60s per adapter |
| `VPN/status` | ~1,100 | every 60s |
| `PROBE/*` | ~1,150 | 4 lines per round, every 5 minutes |
| `PATHTRACE/*` | ~1,000 | every 30 minutes, one line per hop |
| `METRICS` (all sources) | ~33,000 | every Wi-Fi/adapter/VPN poll and every probe |

Machines with more adapters, probe targets or a shorter poll interval send proportionally more of the periodic kinds. The state-change kinds (connect, disconnect, up, down) depend on how much a machine moves around, not on the config.

## Without incidents

| What is forwarded | Per machine / day | 1,000 machines / day | 1,000 machines / month | 3,000 machines / day | 3,000 machines / month | 5,000 machines / day | 5,000 machines / month |
|---|---|---|---|---|---|---|---|
| **Incidents only** (built-in default), no incidents | ~0 | ~0 | ~0 | ~0 | ~0 | ~0 | ~0 |
| **Starter config**, desk PC (~10 state changes/day) | 2 KB | 2 MB | 60 MB | 6 MB | 180 MB | 10 MB | 300 MB |
| **Starter config**, roaming laptop (~100 state changes/day) | 20 KB | 20 MB | 600 MB | 60 MB | 1.8 GB | 100 MB | 3 GB |
| All Wi-Fi events (`"ACM/*", "MSM/*"`, the old starter config) | 325 KB | 325 MB | 9.8 GB | 975 MB | 29 GB | 1.6 GB | 49 GB |
| Every log line (no selector limits, no metrics) | 4.5 MB | 4.5 GB | 135 GB | 14 GB | 405 GB | 22 GB | 675 GB |
| Everything, including metrics (what baseline mode records) | 13 MB | 13 GB | 390 GB | 39 GB | 1.2 TB | 65 GB | 1.9 TB |

## With incidents

An incident sends two events, `INCIDENT/triggered` and `INCIDENT/saved`, about 0.6 KB together. The incident **file** stays on the machine. The "incl. files" rows show what uploading the `.log.gz` files would add, at about 5 KB each (10 minutes of log lines, gzipped). exmon can't upload incident files today.

| What is forwarded | Per machine / day | 1,000 machines / day | 1,000 machines / month | 3,000 machines / day | 3,000 machines / month | 5,000 machines / day | 5,000 machines / month |
|---|---|---|---|---|---|---|---|
| Incidents only, 5 incidents/day | 3 KB | 3 MB | 90 MB | 9 MB | 270 MB | 15 MB | 450 MB |
| Incidents only, 20 incidents/day | 12 KB | 12 MB | 360 MB | 36 MB | 1.1 GB | 60 MB | 1.8 GB |
| Starter config, roaming laptop + 5 incidents/day | 23 KB | 23 MB | 690 MB | 69 MB | 2.1 GB | 115 MB | 3.5 GB |
| Starter config, roaming laptop + 20 incidents/day | 32 KB | 32 MB | 960 MB | 96 MB | 2.9 GB | 160 MB | 4.8 GB |
| Starter config, roaming laptop + 5 incidents/day incl. files *(not implemented)* | 48 KB | 48 MB | 1.4 GB | 144 MB | 4.3 GB | 240 MB | 7.2 GB |
| Starter config, roaming laptop + 20 incidents/day incl. files *(not implemented)* | 132 KB | 132 MB | 4 GB | 396 MB | 12 GB | 660 MB | 20 GB |

Months are 30 days. Sizes are decimal (1 KB = 1,000 bytes) and uncompressed JSON.

The number of incidents depends almost entirely on your triggers and their `cooldownSeconds`. Five per machine per day is already a lot for well-tuned triggers. Run [baseline mode](baseline.md) first to pick thresholds that don't fire on normal behaviour.

## What this means

- **Selected events cost almost nothing; periodic kinds dominate.** The starter selectors and incidents stay in the single-digit-GB-per-month range even at 5,000 machines. Forwarding anything that repeats on a timer (`POLL`, `STATS`, `*/status`, `PROBE/*` lines, scans, metrics) multiplies usage 100–1,000 times.
- **Never forward `ACM/*`.** See [Choosing what to send](remote.md#choosing-what-to-send).
- **Keep metrics local.** The `METRICS` events are about two-thirds of the "everything" volume. They are never forwarded today (`remote.include` can't select them) and are better summarised on the machine. The baseline summary and incident files exist for this.
- **Compression helps a lot if the target accepts it.** These records gzip to about 15% of their size. A sender that compresses each batch would cut every figure above by roughly 85%. The current stand-in sender doesn't compress.
- **Network overhead is small.** Records are sent in batches of `remote.batchSize` (default 50), so HTTP/TLS overhead per event is a few bytes. A retry after a failed send re-sends the batch, so a machine that is offline for a while catches up later rather than sending more in total.
