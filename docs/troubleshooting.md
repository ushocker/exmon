# Troubleshooting

## The service won't start, or stops straight away

Look in the Application event log, source `exmon`. Anything that stops the service starting is written there, because the exmon log may not be open yet:

```powershell
Get-WinEvent -FilterHashtable @{LogName='Application'; ProviderName='exmon'} -MaxEvents 20 | Format-List TimeCreated, LevelDisplayName, Message
```

Common causes:

- **`refusing config …: … can be changed by …`**: the config file, or its folder, can be changed by non-administrators, so the service won't trust it. Recreate it from an elevated prompt inside `%ProgramData%\exmon\config` (that folder's permissions make admin-created files trusted), or run `exmon.exe install` again to reset the folder permissions.
- **`… is outside the allowed output folders`**: a log, incident, baseline or spool path is outside `%ProgramData%\exmon`. Add its folder to `outputRoots` (and, if that folder excludes SYSTEM, `icacls <folder> /grant "SYSTEM:(OI)(CI)M"`), or use the default locations. See [Custom output locations](service.md#custom-output-locations).
- **`… is a junction or link; exmon won't write through it`**: a folder on the way to an output is a junction or symbolic link. Point the setting at the real folder instead.
- **An invalid config**: the same message you would see in the console. Test it with `exmon.exe --config <file>` in a console first.

Windows restarts a failed service three times (after 5s, 10s and 30s), then gives up until the next day or the next manual start.

## HTTP probes fail only when running as a service

The service account (LocalService) doesn't use your personal proxy settings, and a proxy that needs Windows sign-in rejects it (it has no network identity). Set a proxy for the service with `exmon.exe install --proxy http://proxy.corp:8080`, or allow the probe targets directly. See [Differences from a console run](service.md#what-differs-under-localservice).

## `ignored the cloud config` or `reverted to the previous one`

- **`ignored the cloud config: …`** at startup: the stored cloud config (`config\cloud\active.json`) failed its signature check (changed on disk, or signed with a key this build doesn't trust), or breaks a rule. The local config runs instead. Publish a new cloud config, or delete the file.
- **`cloud config version N didn't run healthily … reverted`**: exmon failed to start, or kept restarting, with a new cloud config, so the previous one was put back automatically. Check the config in your repository against the [cloud limits](control-plane.md#what-a-cloud-config-cant-set) and look at the lines just before the revert.

## `CONTROL/unreachable reason=key_access`

The device has its certificate, but the exmon service isn't allowed to use its key yet. exmon starts the helper service to fix that by itself (`CONTROL/helper_started`). If it keeps happening:

- check `exmon.exe status` shows `exmon-helper` installed; if not, put `exmon-helper.exe` next to `exmon.exe` and run `exmon.exe install` again;
- look at the helper's messages: `Get-WinEvent -FilterHashtable @{LogName='Application'; ProviderName='exmon-helper'} -MaxEvents 10`;
- check the helper's certificate settings match exmon's (`install` copies `control.cert` to it; run `install` again after changing them).

## A console run ignores the machine-wide config

`%ProgramData%\exmon\config` is readable only by administrators, because `exmon.json` can hold the Azure Monitor secret. A console run without elevation can't see it, so it uses the built-in defaults; the `config:` line at startup says which file (if any) was loaded. Run the console elevated, or use `--config` with a copy you can read (without real secrets).

## Lines end with `[held]`, or `POWER/not_ready` after every wake

`[held]` lines were logged while exmon was starting, waking from sleep or shutting down, before the network was ready. That's expected; they don't fire triggers or get forwarded. See [Startup, sleep and shutdown](power.md).

If **every** startup or wake ends in `POWER/not_ready` after the full wait, the connection test can't reach `www.msftconnecttest.com`. This is common behind an authenticating proxy, especially for the service account. Point `power.testURL` at an internal page reachable without a proxy (with `power.expect` set to text on it), or set `power.testURL` to `""` so a default route is enough.

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
- `log.maxFiles 5000 is out of range 1-50`: every number has an allowed range (see [Configuration](configuration.md#allowed-ranges)).
- `refusing config …: … can be changed by …`, when running **elevated** (as administrator): an elevated console only uses a config that only administrators can change, the same rule as the service. This stops a config planted by a standard user running with admin rights. Run `exmon.exe install` once to lock down `%ProgramData%\exmon`, or keep the config in a folder only administrators can change.
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

Check:

- The `MONITOR` lines at startup: there is one `remote <name>: ...` line per enabled remote. No line means it is disabled (or `--remotes-enabled=false` was used).
- That remote's `include`: without a `remotes` key, the default remote only forwards `INCIDENT/*` to `remote-outbox.jsonl`. Selectors with `severityAtLeast` only match incident events.
- `REMOTE/send_failed` and `REMOTE/dropped` lines for that remote (`remote=<name>`).
- Files in `remote-spool\<name>\`. Many segments there means sending is failing.
- An `azureMonitor` remote failing with `…: EOF`: the connection to the ingestion endpoint is closed during the TLS handshake. That's a network filter blocking the host name (`*.ingest.monitor.azure.com` is on many telemetry block lists), not Azure. See [the test setup troubleshooting table](deploy/test-install.md#troubleshooting).

## Incident files disappear

Old files are deleted per severity beyond `maxIncidents` / `maxIncidentsBySeverity`. Raise the limit for the severities you want to keep longer, or use 0 for unlimited.
