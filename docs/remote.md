# Remote queue

The remote queue forwards selected events, typically incidents and outages, to a central place, so problems on many machines can be seen in one spot.

You can have **several remotes** (up to 8) in the `remotes` list. Each one:

- picks its own events with its own [selectors](#choosing-what-to-send);
- has its own on-disk queue (spool) and retry timer, so a slow or offline target never holds up another;
- sends to its own target, chosen by its [`type`](#target-types).

```json
"remotes": [
  { "name": "outbox", "type": "outbox",
    "settings": { "path": "%ProgramData%\\exmon\\remote-outbox.jsonl" },
    "include": [ { "kinds": ["INCIDENT/*"] } ] }
]
```

Every setting is listed in [Configuration](configuration.md#remotes-where-events-are-forwarded).

## Target types

| `type` | Sends to | `settings` |
|---|---|---|
| `outbox` | A local file, one JSON record per line. Useful to see exactly what would be sent, or for a local collector that reads the file. | `{ "path": "<file>" }` (`%VAR%` allowed) |
| `azureMonitor` | A Log Analytics workspace, through the Azure Monitor Logs Ingestion API. | endpoint, DCR id, stream, and a write-only Entra app; see [Sending events to Azure Monitor](azure-monitor.md) |

New target types are added in code (see [Architecture](architecture.md#adding-a-remote-target-type)); nothing else in the config changes. Unknown keys in `settings` are errors, so a misspelt setting is caught at startup.

## Nothing is lost

Network problems are exactly when sending fails, so every selected event is **written to disk first** (the remote's spool, `spoolDir`) and sent from there:

- **Network down:** events wait on disk. exmon retries with [backoff](#retries-and-backoff).
- **PC restarted** with events still waiting: on the next start exmon logs `REMOTE/resuming remote=<name> pending=N` and sends them first.
- **Crash in the middle of writing:** the half-written line is discarded at the next start; everything before it is kept.

Delivery is **at least once**. If exmon stops right after a send succeeds but before it records that, the same batch is sent again next time. Each record has a unique `id` so the receiver can discard duplicates.

Each spool is capped at `maxSpoolMB` (default 100). If a long outage fills it, the oldest unsent events are deleted, and a `REMOTE/spool_overflow remote=<name> dropped=N` line records the loss.

If the target **rejects** a batch in a way retrying can't fix (for example it doesn't accept the data's format), that batch is dropped with a `REMOTE/dropped` line instead of blocking everything behind it forever.

## What a record looks like

One JSON object per event:

```json
{"id":"21a135f9e6cfa53e","host":"PC01","event":{"time":"2026-10-03T00:45:33.9127135-05:00","source":"incident","kind":"INCIDENT/triggered","subject":"gateway","detail":"id=incident-20261003-004533.912-sev1-gw-check sev=1 rule=gw-check ...","severity":1}}
```

`host` is the computer name. `event` has the same fields as a log line, plus `severity` on incident events. Other target types may reshape this for their destination, but carry the same information.

## Choosing what to send

Each remote's `include` is a list of **selectors**. An event is sent to that remote if it matches any of them.

```json
"include": [
  { "source": "wifi", "kinds": ["ACM/connection_complete", "ACM/connection_attempt_fail", "ACM/disconnected", "ACM/scan_fail",
                                "MSM/connected", "MSM/disconnected", "MSM/roaming_start", "MSM/roaming_end", "MSM/radio_state_change"] },
  { "kinds": ["INCIDENT/*"], "severityAtLeast": 3 },
  { "kinds": ["PROBE/down", "PROBE/up", "NIC/up", "NIC/down", "VPN/connected", "VPN/disconnected", "VPN/changed", "POWER/not_ready"] }
]
```

This is the selection in the starter config (`--write-default-config`). It sends Wi-Fi connects, disconnects, failures and roams, but not the background chatter.

**Avoid `"ACM/*"` for Wi-Fi.** Windows scans for networks about once a minute, so `ACM/*` also matches `ACM/scan_complete`, `ACM/scan_list_refresh` and `ACM/network_available`. On a test machine these were about 99% of everything forwarded: roughly 1,400 events per machine per day, none of them useful. List the kinds you want instead. See [Data usage](data-usage.md) for what each choice costs across a fleet. `MSM/*` is quieter, but it also includes `MSM/associating`, `MSM/authenticating` and similar steps of every connection, plus `MSM/link_degraded` / `MSM/link_improved` on some drivers.

| Field | Meaning |
|---|---|
| `source` | Only events from this monitor (`wifi`, `nic`, `vpn`, `services`, `probe`, `incident`, `pathtrace`, `exmon`). Leave out for any. |
| `kinds` | Event kinds; `*` matches anything within one part, so `ACM/*` matches every `ACM/...` kind. Leave out for any. |
| `severityAtLeast` | Only events with a severity of this or more serious. See below. |

See [event kinds](reference/log-format.md#event-kinds) for everything you can select.

These are never sent, whatever the selectors say: metrics, **held** events (from while the machine was starting, waking or shutting down; see [Startup, sleep and shutdown](power.md)), `REMOTE/*` lines (the queues' own status), and `INCIDENT/hold` / `INCIDENT/release`. `POWER/*` lines are never held, so they can be selected; `POWER/not_ready` is a useful one.

### Filtering by severity

`"severityAtLeast": 2` sends Sev1 and Sev2 incidents and drops Sev3–5 (1 is the most serious). Only incident events carry a severity, so a selector with `severityAtLeast` never matches anything else. Put non-incident kinds in a separate selector, as in the example above.

### Different events to different places

Because each remote has its own selectors, you can split traffic, for example everything useful to a SIEM but only Sev1–2 incidents to a second, more expensive destination:

```json
"remotes": [
  { "name": "siem",  "type": "outbox", "settings": { "path": "D:\\Collect\\siem.jsonl" },
    "include": [ { "kinds": ["INCIDENT/*", "PROBE/down", "PROBE/up", "NIC/down"] } ] },
  { "name": "pager", "type": "outbox", "settings": { "path": "D:\\Collect\\pager.jsonl" },
    "include": [ { "kinds": ["INCIDENT/triggered"], "severityAtLeast": 2 } ] }
]
```

An event that matches several remotes is sent to each of them.

## Retries and backoff

After a failed send, exmon waits and tries again. The wait starts at `retryInitialSeconds` (default 5) and doubles after each failure (5s, 10s, 20s, 40s …) up to `retryMaxMinutes` (default 15). It then keeps retrying every `retryMaxMinutes` indefinitely. Each wait varies by ±10%, so many PCs that lost the network together don't all retry at the same moment. If the target asks for a longer wait (for example an HTTP `Retry-After`), exmon waits at least that long. New events don't cut a wait short; they are simply queued.

Lines (each starts with `remote=<name>`):

| Kind | Meaning |
|---|---|
| `REMOTE/send_failed` | `remote=law attempt=3 retry_in=20s error="..."` |
| `REMOTE/recovered` | Sending works again after failed attempts. |
| `REMOTE/resuming` | Events left over from the previous run are being sent. |
| `REMOTE/dropped` | The target permanently rejected a batch; it was dropped so later events can go through. |
| `REMOTE/spool_overflow` | The spool was full and old events were dropped. |
| `REMOTE/spool_error` | The spool couldn't be read or written. |

On Ctrl+C each queue makes one last 5-second attempt; anything unsent stays on disk for next time.

## Testing without a network

`--remote-simulate-offline` makes every remote's sends fail. To see the whole cycle:

1. Set `"retryMaxMinutes": 1` on a remote, and run `exmon.exe --remote-simulate-offline` with a trigger that fires. Watch `REMOTE/send_failed` retry times grow, and files appear in the spool folder.
2. Press Ctrl+C, then run `exmon.exe` normally. You should see `REMOTE/resuming pending=N`, and the events arrive at the target.

`--remotes-enabled=false` turns every remote off for one run.

## The spool on disk

Each remote's `spoolDir` (default `%ProgramData%\exmon\remote-spool\<name>`) holds numbered segment files (`00000001.jsonl`, `00000002.jsonl` …, a new one every `segmentKB`) and `cursor.json`, which marks the first unsent byte. Fully sent segments are deleted. Don't edit these files while exmon is running. Two remotes can't share or nest spool folders.

## Migrating from `remote`

Older versions had a single `remote` section. exmon now refuses to start with it, rather than silently ignoring it. Move it into a `remotes` list:

| Old | New |
|---|---|
| `"remote": { ... }` | `"remotes": [ { "name": "outbox", "type": "outbox", ... } ]` |
| `remote.outbox` | `"settings": { "path": "..." }` |
| `remote.endpoint` | removed; pick a target `type` instead |
| `remote.spoolDir` (`...\remote-spool`) | `spoolDir`, default `...\remote-spool\<name>` |
| other `remote.*` settings | the same keys on the remote |
| `--remote-enabled=false` | `--remotes-enabled=false` |
| `--remote-outbox`, `--remote-spool-dir`, `--remote-retry-max-minutes` | removed; set them in the file |

Events still waiting in the old `remote-spool` folder (segment files directly inside it) are not picked up by the new per-remote folder. Let the old version send them before upgrading, or point the remote's `spoolDir` at the old folder for one run.
