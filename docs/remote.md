# Remote queue

The remote queue forwards selected events, typically incidents and outages, to a central place, so problems on many machines can be seen in one spot.

> **Status:** the real remote destination is not built yet (`remote.endpoint` is reserved for it). Until then a stand-in writes everything that *would* be sent to a local file, `remote.outbox` (default `%ProgramData%\exmon\remote-outbox.jsonl`). The queue, retry and redelivery machinery is complete and works the same with the stand-in.

## Nothing is lost

Network problems are exactly when sending fails, so every selected event is **written to disk first** (the spool, `remote.spoolDir`) and sent from there:

- **Network down:** events wait on disk. exmon retries with [backoff](#retries-and-backoff).
- **PC restarted** with events still waiting: on the next start exmon logs `REMOTE/resuming pending=N` and sends them first.
- **Crash in the middle of writing:** the half-written line is discarded at the next start; everything before it is kept.

Delivery is **at least once**. If exmon stops right after a send succeeds but before it records that, the same batch is sent again next time. Each record has a unique `id` so the receiver can discard duplicates.

The spool is capped at `maxSpoolMB` (default 100). If a long outage fills it, the oldest unsent events are deleted, and a `REMOTE/spool_overflow dropped=N` line records the loss.

## What a record looks like

One JSON object per event:

```json
{"id":"21a135f9e6cfa53e","host":"PC01","event":{"time":"2026-10-03T00:45:33.9127135-05:00","source":"incident","kind":"INCIDENT/triggered","subject":"gateway","detail":"id=incident-20261003-004533.912-sev1-gw-check sev=1 rule=gw-check ...","severity":1}}
```

`host` is the computer name. `event` has the same fields as a log line, plus `severity` on incident events.

## Choosing what to send

`remote.include` is a list of **selectors**. An event is sent if it matches any of them.

```json
"include": [
  { "source": "wifi", "kinds": ["ACM/*", "MSM/*"] },
  { "kinds": ["INCIDENT/*"], "severityAtLeast": 3 },
  { "kinds": ["PROBE/down", "PROBE/up", "NIC/up", "NIC/down", "VPN/connected", "VPN/disconnected", "VPN/changed"] }
]
```

| Field | Meaning |
|---|---|
| `source` | Only events from this monitor (`wifi`, `nic`, `vpn`, `probe`, `incident`, `pathtrace`, `exmon`). Leave out for any. |
| `kinds` | Event kinds; `*` matches anything within one part, so `ACM/*` matches every `ACM/...` kind. Leave out for any. |
| `severityAtLeast` | Only events with a severity of this or more serious. See below. |

The default is `[{ "kinds": ["INCIDENT/*"] }]`: incidents only. See [event kinds](reference/log-format.md#event-kinds) for everything you can select.

These are never sent, whatever the selectors say: metrics, **held** events (from while the machine was starting, waking or shutting down; see [Startup, sleep and shutdown](power.md)), `REMOTE/*` lines (the queue's own status), and `INCIDENT/hold` / `INCIDENT/release`. `POWER/*` lines are never held, so they can be selected; `POWER/not_ready` is a useful one.

### Filtering by severity

`"severityAtLeast": 2` sends Sev1 and Sev2 incidents and drops Sev3–5 (1 is the most serious). Only incident events carry a severity, so a selector with `severityAtLeast` never matches anything else. Put non-incident kinds in a separate selector, as in the example above.

## Retries and backoff

After a failed send, exmon waits and tries again. The wait starts at `retryInitialSeconds` (default 5) and doubles after each failure (5s, 10s, 20s, 40s …) up to `retryMaxMinutes` (default 15). It then keeps retrying every `retryMaxMinutes` indefinitely. Each wait varies by ±10%, so many PCs that lost the network together don't all retry at the same moment. New events don't cut a wait short; they are simply queued.

Lines:

| Kind | Meaning |
|---|---|
| `REMOTE/send_failed` | `attempt=3 retry_in=20s error="..."` |
| `REMOTE/recovered` | Sending works again after failed attempts. |
| `REMOTE/resuming` | Events left over from the previous run are being sent. |
| `REMOTE/spool_overflow` | The spool was full and old events were dropped. |
| `REMOTE/spool_error` | The spool couldn't be read or written. |

On Ctrl+C the queue makes one last 5-second attempt; anything unsent stays on disk for next time.

## Testing without a network

`--remote-simulate-offline` makes every send fail. To see the whole cycle:

1. Run `exmon.exe --remote-simulate-offline --remote-retry-max-minutes 1` with a trigger that fires. Watch `REMOTE/send_failed` retry times grow, and files appear in the spool folder.
2. Press Ctrl+C, then run `exmon.exe` normally. You should see `REMOTE/resuming pending=N`, and the events appear in the outbox file.

## The spool on disk

`spoolDir` holds numbered segment files (`00000001.jsonl`, `00000002.jsonl` …, a new one every `segmentKB`) and `cursor.json`, which marks the first unsent byte. Fully sent segments are deleted. Don't edit these files while exmon is running.
