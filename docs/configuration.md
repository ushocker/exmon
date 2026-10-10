# Configuration

exmon combines these layers, each overriding the one before:

1. **Built-in defaults**, listed in the tables below.
2. **The config file**, in JSON. Keys you leave out keep their defaults.
3. **The cloud config**, only when the [control plane](control-plane.md) is on and has delivered one. It can't set paths or control-plane settings; see [what it can set](control-plane.md#what-a-cloud-config-cant-set).
4. **Command-line flags**. Only flags you actually type override anything.

See [Getting started](getting-started.md#where-exmon-looks-for-its-config) for how the config file is found. The fullest example is `exmon.example.json` in the repository root, and `exmon.exe --write-default-config` writes the same file out for you to edit.

## Things to know about the file

- **Lists replace, they don't merge.** If you set `"dnsNames": ["intranet.corp"]`, the default `www.microsoft.com` is gone. The same applies to `triggers`, `endpoints`, `httpTests`, `remotes`, `include`, `adapterPatterns`, `processNames` and `virtualPatterns`.
- **Paths can use environment variables** written as `%NAME%`, for example `%ProgramData%`. This applies to `log.path`, `incident.dir`, each remote's `spoolDir` and outbox `settings.path`, and `baseline.path`. In JSON, a backslash must be doubled: `"C:\\Logs\\exmon.log"`. Forward slashes also work.
- **Errors stop startup.** exmon checks the whole configuration before it starts and exits with a message if something can't work, such as an unknown trigger operator, a severity of 7, or a number outside its [allowed range](#allowed-ranges).
- **Elevated runs only trust admin-controlled config.** When exmon runs as administrator (or as the service), it only uses a config file that only administrators can change. See [Troubleshooting](troubleshooting.md#exmon-exits-straight-away-with-a-config-error).

## Service commands

These come first on the command line, before any flags. See [Running as a Windows Service](service.md).

| Command | Does |
|---|---|
| `exmon.exe install [flags…]` | Install or update the service (elevated). The flags are built into the service's command line. |
| `exmon.exe uninstall` | Remove the service (elevated); files are kept. |
| `exmon.exe status` | Show whether the service is installed and running. |

## Command-line flags

Flags can be written with one or two dashes (`-config` or `--config`). An on/off flag is switched on by naming it (`--baseline`). To switch one off, use `=false`, for example `--wifi-enabled=false`. Run `exmon.exe -h` for the full list.

| Flag | Overrides |
|---|---|
| `--config <path>` | which config file to load (it must exist) |
| `--write-default-config` | write the starter config to `--config` or `%ProgramData%\exmon\config\exmon.json`, then exit |
| `--log-path`, `--log-max-size-mb`, `--log-max-files`, `--log-flush-interval-ms` | `log.*` |
| `--wifi-enabled`, `--wifi-poll-seconds`, `--wifi-neighbors`, `--wifi-debug-poll`, `--wifi-rssi-threshold`, `--wifi-rate-change-percent`, `--wifi-error-pct-threshold`, `--wifi-min-stats-frames`, `--wifi-heartbeat-seconds` | `wifi.*` |
| `--nic-enabled` | `nic.enabled` |
| `--power-enabled` | `power.enabled` |
| `--vpn-enabled` | `vpn.enabled` |
| `--services-enabled`, `--services-poll-seconds` | `services.enabled`, `services.pollSeconds` |
| `--probe-enabled`, `--probe-interval-seconds`, `--probe-path-trace-minutes` | `probe.*` |
| `--incident-enabled`, `--incident-dir`, `--incident-window-minutes`, `--incident-post-seconds` | `incident.*` |
| `--control-enabled`, `--control-host`, `--control-path`, `--control-cert-subject`, `--control-cert-issuer` | `control.*` (`--control-host` also turns the control plane on) |
| `--update-hosts`, `--update-publisher`, `--update-require-authenticode` | `control.update.*` (`--update-hosts` also turns updates on); see [Updates](updates.md) |
| `--remotes-enabled=false` | turns every remote in `remotes` off |
| `--remote-simulate-offline` | makes every remote's sends fail (testing) |
| `--baseline`, `--baseline-path`, `--baseline-summary-minutes` | `baseline.*` |

Lists such as triggers, endpoints, `services.watch` and `remotes` can only be set in the config file. The old `--remote-outbox`, `--remote-spool-dir` and `--remote-retry-max-minutes` flags are gone; set those per remote in the file.

## `log`: the main log file

| Key | Default | Meaning |
|---|---|---|
| `path` | `%ProgramData%\exmon\exmon.log` | Log file. |
| `maxSizeMB` | `50` | Rotate when the file reaches this size. 0 turns rotation off. |
| `maxFiles` | `5` | Rotated copies to keep: `exmon.1.log` (newest) … `exmon.5.log`. |
| `flushIntervalMs` | `1000` | Lines are buffered and written to disk at least this often. They are also written as soon as the queue empties. |

## `wifi`: the Wi-Fi monitor

See [Wi-Fi monitor](monitors/wifi.md).

| Key | Default | Meaning |
|---|---|---|
| `enabled` | `true` | |
| `pollSeconds` | `5` | Seconds between samples. |
| `neighbors` | `false` | Also log other access points broadcasting the same network name (roaming candidates). |
| `debugPoll` | `false` | Log every poll, not just changes. |
| `rssiThreshold` | `3` | A signal change of at least this many dBm counts as a change. |
| `rateChangePercent` | `50` | An rx/tx rate swing of at least this % counts as a change. 0 ignores rates. |
| `errorPctThreshold` | `10` | A retry% or FCS% move of at least this many points counts as a change. 0 ignores them. |
| `minStatsFrames` | `500` | Frames needed in an interval before retry%/FCS% are trusted. |
| `heartbeatSeconds` | `15` | Write a POLL line at least this often even when nothing changed. 0 turns it off. |
| `disconnectWindowMinutes` | `30` | How far back the `recent_disconnects` metric counts. |

## `nic`: network adapters

See [Network adapters](monitors/nic.md).

| Key | Default | Meaning |
|---|---|---|
| `enabled` | `true` | |
| `pollSeconds` | `15` | How often traffic/error counters are read. Up/down changes are reported immediately regardless. |
| `heartbeatSeconds` | `60` | `NIC/status` line per adapter at least this often. 0 turns it off. |
| `includeVirtual` | `false` | Also watch virtual adapters (Hyper-V, VPN, Bluetooth …). |
| `virtualPatterns` | Hyper-V, vEthernet, VMware, VirtualBox, Bluetooth, WAN Miniport, Wi-Fi Direct, Kernel Debug, Loopback, TAP-, Wintun, WireGuard, PANGP, Cisco AnyConnect, Zscaler, Fortinet, Juniper, Tailscale | An adapter whose name or description contains any of these (ignoring case) counts as virtual. |

## `vpn`: VPN detection

See [VPN detection](monitors/vpn.md).

| Key | Default | Meaning |
|---|---|---|
| `enabled` | `true` | |
| `pollSeconds` | `30` | How often to check. |
| `heartbeatSeconds` | `60` | `VPN/status` line at least this often. 0 turns it off. |
| `pingCount` | `3` | Pings to a built-in VPN's server on each check. |
| `adapterPatterns` | Cisco AnyConnect, GlobalProtect, PANGP, Palo Alto, Zscaler, Fortinet, FortiClient, WireGuard, OpenVPN, TAP-Windows, SonicWall, Pulse Secure, Juniper, Check Point, Tailscale | Adapter name/description fragments that mark a third-party VPN adapter. |
| `processNames` | vpnui, vpnagent, PanGPA, PanGPS, ZSATunnel, FortiClient, dsNcService, vpnc, tailscaled | VPN client processes to report (without `.exe`). |

## `services`: Windows services

See [Windows services](monitors/services.md).

| Key | Default | Meaning |
|---|---|---|
| `enabled` | `true` | Runs only when `watch` lists something. |
| `pollSeconds` | `30` | How often each service is checked. |
| `heartbeatMinutes` | `60` | `SERVICE/status` for every service at start and then this often. 0 = only at start. |
| `watch` | none | The services: `{ "name": "Spooler", "expect": "running" }`. `expect` is `running` (default), `stopped` or `any`. |

## `probe`: connectivity probes and path trace

See [Connectivity probes](monitors/probes.md) and [Path trace](monitors/path-trace.md).

| Key | Default | Meaning |
|---|---|---|
| `enabled` | `true` | |
| `intervalSeconds` | `300` | One round of probes this often (minimum 10). |
| `timeoutSeconds` | `10` | Per probe. HTTP tests get three times this. |
| `pingCount` | `4` | Pings per endpoint and to the gateway. |
| `dnsNames` | `["www.microsoft.com"]` | Names to resolve and time. |
| `endpoints` | `[]` | `{ "name", "target", "port" }`: TCP connect plus ping. |
| `httpTests` | `[]` | `{ "name", "url" }`: timed HTTP(S) GET. |
| `pathTrace.intervalMinutes` | `30` | Periodic path trace. 0 turns it off. |
| `pathTrace.onIncident` | `true` | Also trace whenever an incident triggers. |
| `pathTrace.maxHops` | `30` | |

## `incident`: rolling window and triggers

See [Incidents](incidents.md).

| Key | Default | Meaning |
|---|---|---|
| `enabled` | `true` | With no triggers configured, nothing ever fires. |
| `dir` | `%ProgramData%\exmon\incidents` | Where incident files are written. |
| `windowMinutes` | `10` | History kept before a trigger. |
| `postTriggerSeconds` | `30` | Keep recording this long after a trigger. |
| `cooldownSeconds` | `300` | The same rule can't fire again within this time. |
| `maxIncidents` | `100` | Files kept **per severity**, unless `maxIncidentsBySeverity` sets that severity. 0 = unlimited. |
| `maxIncidentsBySeverity` | none | For example `{ "1": 500, "2": 300, "3": 100, "4": 50, "5": 20 }`. |
| `maxHoldSeconds` | `120` | Longest a path trace (or other evidence) can keep an incident open. |
| `triggers` | `[]` | Trigger rules. See [Incidents](incidents.md#trigger-rules). |

## `remotes`: where events are forwarded

A list of remotes. Each one picks its own events with `include`, has its own on-disk queue and sends to its own target. See [Remote queue](remote.md).

```json
"remotes": [
  { "name": "outbox", "type": "outbox",
    "settings": { "path": "%ProgramData%\\exmon\\remote-outbox.jsonl" },
    "include": [ { "kinds": ["INCIDENT/*"] } ] }
]
```

| Key | Default | Meaning |
|---|---|---|
| `name` | required | Unique; 1–32 lowercase letters, digits or `-`. Shown in `REMOTE/*` lines and used for the default spool folder. |
| `type` | required | The target type: `outbox` (JSON lines to a local file) or `azureMonitor` ([Log Analytics](azure-monitor.md)). See [target types](remote.md#target-types). |
| `enabled` | `true` | |
| `settings` | required | The type's own settings: for `outbox`, `{ "path": "<file>" }`; for `azureMonitor`, see [its settings](azure-monitor.md#settings). Unknown keys are errors. |
| `spoolDir` | `%ProgramData%\exmon\remote-spool\<name>` | This remote's on-disk queue. Two remotes can't share or nest spool folders. |
| `segmentKB` | `1024` | Start a new spool file at this size. |
| `maxSpoolMB` | `100` | Delete the oldest unsent events beyond this, and log it. |
| `batchSize` | `50` | Events per send. |
| `flushIntervalMs` | `5000` | Check the spool at least this often. |
| `retryInitialSeconds` | `5` | First retry delay after a failed send. |
| `retryMaxMinutes` | `15` | The retry delay doubles up to this, then stays there. |
| `simulateOffline` | `false` | Testing: every send fails. |
| `include` | none | Which events to forward. See [selectors](remote.md#choosing-what-to-send). |

Without a `remotes` key, exmon runs one remote, `outbox`, sending incidents to `%ProgramData%\exmon\remote-outbox.jsonl`. A `remotes` list in the file replaces that default completely; `"remotes": []` forwards nothing. The old single `remote` section is refused at startup; see [Migrating from `remote`](remote.md#migrating-from-remote).

## `baseline`: learning mode

See [Baseline mode](baseline.md).

| Key | Default | Meaning |
|---|---|---|
| `enabled` | `false` | |
| `path` | `%ProgramData%\exmon\baseline.jsonl` | Every event, including metrics, as JSON lines. |
| `summaryMinutes` | `15` | How often `baseline-summary.txt` (next to `path`) is rewritten. |
| `maxSizeMB` | `100` | Rotation size for the baseline file. |
| `maxFiles` | `5` | Rotated baseline files to keep. |

## `power`: startup, sleep and shutdown

See [Startup, sleep and shutdown](power.md).

| Key | Default | Meaning |
|---|---|---|
| `enabled` | `true` | Hold monitor events at startup, after waking and during shutdown until the network is ready. |
| `testURL` | `http://www.msftconnecttest.com/connecttest.txt` | Connection test page; `""` = a default route is enough. |
| `expect` | `Microsoft Connect Test` | Text the test page must contain. |
| `maxWaitSeconds` | `180` | Go live anyway after this long, logging `POWER/not_ready`. |
| `settleSeconds` | `0` | Extra time the network must stay ready before going live. |
| `checkSeconds` | `5` | How often readiness is checked while waiting. |

## `control`: the control plane

Off by default. Connects to the fleet's MQTT broker (Azure Event Grid) with the device's client certificate. Every setting, and what the device does, is in [Control plane](control-plane.md#settings-control). These are install-time settings; a cloud config can't change them (except `chirpMinutes` and `statusMinutes`).

```json
"control": { "enabled": true, "host": "exmon-prod.eastus-1.ts.eventgrid.azure.net",
             "cert": { "issuerCN": "exmon Issuing CA" } }
```

## `outputRoots`: extra output folders (service only)

| Key | Default | Meaning |
|---|---|---|
| `outputRoots` | none | Folders, besides `%ProgramData%\exmon`, where the service may write its log, incidents, baseline and remote spools and outbox files. Full paths; `%VAR%` allowed. See [Custom output locations](service.md#custom-output-locations). |

## Allowed ranges

Every number with a resource cost has a range, so a typo can't make exmon exhaust the machine. A value outside its range stops startup with a message such as `log.maxFiles 5000 is out of range 1-50`. All defaults are inside these ranges.

| Setting | Allowed |
|---|---|
| `log.maxSizeMB` | 0–1024 (0 = no rotation) |
| `log.maxFiles` | 1–50 |
| `log.flushIntervalMs` | 100–60000 |
| `wifi.pollSeconds` | 1–3600 |
| `nic.pollSeconds`, `vpn.pollSeconds` | 5–3600 |
| `vpn.pingCount`, `probe.pingCount` | 1–10 |
| `services.pollSeconds` | 5–3600 |
| `services.heartbeatMinutes` | 0–1440 |
| `services.watch` | at most 50 entries |
| `probe.intervalSeconds` | 10–86400 |
| `probe.timeoutSeconds` | 1–60 |
| `probe.pathTrace.maxHops` | 1–64 |
| `probe.endpoints`, `probe.httpTests` | at most 25 entries each |
| `probe.dnsNames` | at most 10 entries |
| `incident.windowMinutes` | 1–60 |
| `incident.postTriggerSeconds`, `incident.maxHoldSeconds` | 0–600 |
| `incident.triggers` | at most 100 entries |
| `incident.maxIncidents`, each `maxIncidentsBySeverity` value | 0–10000 (0 = unlimited) |
| `remotes` | at most 8 entries |
| each remote's `batchSize` | 1–1000 |
| each remote's `maxSpoolMB` | 1–1024 |
| each remote's `segmentKB` | 64–16384 |
| each remote's `flushIntervalMs` | 100–60000 |
| each remote's `retryInitialSeconds` | 1–3600 |
| each remote's `retryMaxMinutes` | 1–1440 |
| each remote's `include` | at most 50 selectors |
| `baseline.summaryMinutes` | 1–1440 |
| `baseline.maxSizeMB` | 1–1024 |
| `baseline.maxFiles` | 1–50 |
| `control.port` | 1–65535 |
| `control.keepAliveSeconds` | 30–1160 |
| `control.sessionExpiryMinutes` | 1–480 |
| `control.connectTimeoutSeconds` | 5–120 |
| `control.chirpMinutes`, `control.statusMinutes` | 1–1440 |
| `control.maxInboundKB` | 16–1024 |
| `control.update.allowedHosts` | at most 5 entries |
| `control.update.maxSizeMB` | 8–256 |
| `control.update.healthMinutes` | 5–60 |
| `power.maxWaitSeconds` | 10–3600 |
| `power.settleSeconds` | 0–600 |
| `power.checkSeconds` | 1–60 |

Ranges are only checked for features that are enabled, except `remotes`: every remote in the list is checked, even one with `"enabled": false`, so a mistake shows up before you turn it on.
