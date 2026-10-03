# Configuration

exmon combines three layers, each overriding the one before:

1. **Built-in defaults**, listed in the tables below.
2. **The config file**, in JSON. Keys you leave out keep their defaults.
3. **Command-line flags**. Only flags you actually type override anything.

See [Getting started](getting-started.md#where-exmon-looks-for-its-config) for how the config file is found. The fullest example is `exmon.example.json` in the repository root, and `exmon.exe --write-default-config` writes the same file out for you to edit.

## Things to know about the file

- **Lists replace, they don't merge.** If you set `"dnsNames": ["intranet.corp"]`, the default `www.microsoft.com` is gone. The same applies to `triggers`, `endpoints`, `httpTests`, `include`, `adapterPatterns`, `processNames` and `virtualPatterns`.
- **Paths can use environment variables** written as `%NAME%`, for example `%ProgramData%`. This applies to `log.path`, `incident.dir`, `remote.outbox`, `remote.spoolDir` and `baseline.path`. In JSON, a backslash must be doubled: `"C:\\Logs\\exmon.log"`. Forward slashes also work.
- **Errors stop startup.** exmon checks the whole configuration before it starts and exits with a message if something can't work, such as an unknown trigger operator or a severity of 7.

## Command-line flags

Flags can be written with one or two dashes (`-config` or `--config`). An on/off flag is switched on by naming it (`--baseline`). To switch one off, use `=false`, for example `--wifi-enabled=false`. Run `exmon.exe -h` for the full list.

| Flag | Overrides |
|---|---|
| `--config <path>` | which config file to load (it must exist) |
| `--write-default-config` | write the starter config to `--config` or `%ProgramData%\exmon\config\exmon.json`, then exit |
| `--log-path`, `--log-max-size-mb`, `--log-max-files`, `--log-flush-interval-ms` | `log.*` |
| `--wifi-enabled`, `--wifi-poll-seconds`, `--wifi-neighbors`, `--wifi-debug-poll`, `--wifi-rssi-threshold`, `--wifi-rate-change-percent`, `--wifi-error-pct-threshold`, `--wifi-min-stats-frames`, `--wifi-heartbeat-seconds` | `wifi.*` |
| `--nic-enabled` | `nic.enabled` |
| `--vpn-enabled` | `vpn.enabled` |
| `--probe-enabled`, `--probe-interval-seconds`, `--probe-path-trace-minutes` | `probe.*` |
| `--incident-enabled`, `--incident-dir`, `--incident-window-minutes`, `--incident-post-seconds` | `incident.*` |
| `--remote-enabled`, `--remote-outbox`, `--remote-spool-dir`, `--remote-retry-max-minutes`, `--remote-simulate-offline` | `remote.*` |
| `--baseline`, `--baseline-path`, `--baseline-summary-minutes` | `baseline.*` |

Lists such as triggers, endpoints and remote selectors can only be set in the config file.

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

## `remote`: remote queue

See [Remote queue](remote.md).

| Key | Default | Meaning |
|---|---|---|
| `enabled` | `true` | |
| `endpoint` | `""` | Reserved for the real remote target (not implemented yet). |
| `outbox` | `%ProgramData%\exmon\remote-outbox.jsonl` | Where the stand-in sender writes what would be sent. |
| `spoolDir` | `%ProgramData%\exmon\remote-spool` | The on-disk queue. |
| `segmentKB` | `1024` | Start a new spool file at this size. |
| `maxSpoolMB` | `100` | Delete the oldest unsent events beyond this, and log it. |
| `batchSize` | `50` | Events per send. |
| `flushIntervalMs` | `5000` | Check the spool at least this often. |
| `retryInitialSeconds` | `5` | First retry delay after a failed send. |
| `retryMaxMinutes` | `15` | The retry delay doubles up to this, then stays there. |
| `simulateOffline` | `false` | Testing: every send fails. |
| `include` | `[{ "kinds": ["INCIDENT/*"] }]` | Which events to forward. See [selectors](remote.md#choosing-what-to-send). |

## `baseline`: learning mode

See [Baseline mode](baseline.md).

| Key | Default | Meaning |
|---|---|---|
| `enabled` | `false` | |
| `path` | `%ProgramData%\exmon\baseline.jsonl` | Every event, including metrics, as JSON lines. |
| `summaryMinutes` | `15` | How often `baseline-summary.txt` (next to `path`) is rewritten. |
| `maxSizeMB` | `100` | Rotation size for the baseline file. |
| `maxFiles` | `5` | Rotated baseline files to keep. |
