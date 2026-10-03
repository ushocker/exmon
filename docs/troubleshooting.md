# Troubleshooting

## The log is quiet. Is it working?

Most monitors only write when something changes, plus heartbeat lines:

- Wi-Fi POLL at least every 15 seconds;
- `NIC/status` and `VPN/status` every 60 seconds;
- a probe round every 5 minutes.

If even those are missing, check the first lines of the log:

- `config:` shows which file was loaded;
- the `MONITOR` lines show what is enabled.

To see every Wi-Fi poll for a while, run with `--wifi-debug-poll`.

## My exmon.json isn't being used

- The second log line says which config file was loaded, or where exmon looked.
- With `go run .` the exe is built in a temporary folder, so "next to the exe" never finds your file. Use `--config .\exmon.json`, or build with `go build`.
- An `exmon.json` next to `exmon.exe` takes priority over `%ProgramData%\exmon\config\exmon.json`.

## exmon exits straight away with a config error

The whole configuration is checked at startup. The message names the problem, for example:

- `trigger 2: weak: op "=" must be one of < <= > >=`
- `metric disconnects_30m was renamed to recent_disconnects`: update the trigger.
- `severity 7 must be 1 (most serious) to 5`
- A JSON syntax error with the file name. Common causes are a missing comma, a trailing comma, or a single backslash in a path (use `\\` or `/`).

## Probes report PROBE/down for targets I didn't add

The starter config (`--write-default-config`) contains example targets from the original script, such as `your-app.azurefd.net`. Replace them with your real endpoints, or remove them.

## Endpoint ping shows 100% loss but TCP works

Many cloud services, including Azure and Microsoft 365 front ends, block ping. Trigger on `reachable` and `tcp_connect_ms` for those instead of `loss_pct`.

## Gateway ping shows 0.0ms

Windows reports ping times in whole milliseconds, so anything under 1 ms reads as 0. That's normal on a wired or good Wi-Fi LAN.

## Path trace shows lots of `*` hops

Many routers, especially in large backbones, never answer the probes traceroute relies on. If the trace still ends with `reached=true`, the path is fine. Only worry when loss starts at a hop and continues all the way to the target.

## Retry% is over 100%, or STATS lines look wrong

Some Wi-Fi drivers report impossible frame counters. For example, the MediaTek MT7927 driver adds retries in blocks of 57 and always reports 0 received frames. exmon detects this (retries greater than frames sent in one poll), logs one warning, and stops using that adapter's retry/FCS figures. Raw numbers are still printed for reference. Nothing to fix in exmon; a newer driver may help.

Before that detection kicks in, the first few seconds after start can log a handful of noisy STATS lines.

## No STATS lines at all

The driver doesn't expose 802.11 frame statistics. exmon logs this once and carries on without them.

## recent_disconnects is 0 even though Wi-Fi dropped earlier

It only counts disconnects seen while exmon was running. After a restart it starts from 0 and needs `disconnectWindowMinutes` to cover a full window.

## My VPN isn't detected

Add a fragment of its adapter description to `vpn.adapterPatterns`, and its process name to `vpn.processNames`. See [VPN detection](monitors/vpn.md#adding-your-vpn-client).

## Nothing arrives at the remote target

There is no real remote target yet: events go to `remote-outbox.jsonl`. Also check:

- `remote.include`: the default only forwards `INCIDENT/*`. Selectors with `severityAtLeast` only match incident events.
- `REMOTE/send_failed` lines in the log.
- Files in `remote-spool\`. That folder holding many segments means sending is failing.

## Incident files disappear

Old files are deleted per severity beyond `maxIncidents` / `maxIncidentsBySeverity`. Raise the limit for the severities you want to keep longer, or use 0 for unlimited.
