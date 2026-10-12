# Log format and event kinds

## Line format

```
2026-10-03 00:45:33.912  PROBE/http                   [M365] 200 dns=0.5ms connect=51.0ms ...
└───── local time ─────┘  └── kind (padded to 28) ──┘ └──────────── detail ────────────┘
```

- **Time** is local time with milliseconds, taken when the event happened rather than when it reached the disk.
- **Kind** says what the line is. Kinds with a `/` are grouped by area (`NIC/up`, `PROBE/down`, …).
- **Detail** is free text. Names of adapters, probes and tunnels are in `[brackets]`, and values are `key=value`.
- **`[held]`** at the end marks a line logged while exmon was starting, waking from sleep or shutting down, before the network was ready. Held lines don't fire triggers and aren't forwarded. See [Startup, sleep and shutdown](../power.md).

Lines end with CRLF (Windows style). The same format is used inside incident files. The baseline file and remote records use JSON instead, with the fields `time`, `source`, `kind`, `subject`, `detail`, `metrics`, `severity` and `held`.

The log rotates at `log.maxSizeMB`: `exmon.log` → `exmon.1.log` → `exmon.2.log` …, keeping `log.maxFiles` old files.

## Event kinds

The **source** column is the monitor that produced the event. It isn't shown in the text log, but it is in JSON output and is what remote selectors and triggers use.

### exmon itself (source `exmon`)

| Kind | Meaning |
|---|---|
| `MONITOR` | Startup and shutdown notes: log path (`exmon started` in a console, `exmon service started` as the service), which config file was used, enabled features, `exmon stopped`. Monitors also write `MONITOR` lines when they start or stop. |
| `DEVICE` | Once at startup: computer, user, domain, model, Windows version. As the service, `user` is whoever is logged on at the console (empty if nobody is). |
| `WARNING` | A config file was skipped because non-administrators can change it (service or elevated runs only). The same warning goes to the Windows event log when running as the service. |
| `ERROR` | Something failed but exmon carried on. |

### Wi-Fi (source `wifi`)

See [Wi-Fi monitor](../monitors/wifi.md).

| Kind | Meaning |
|---|---|
| `POLL` | Wi-Fi state, written on change or heartbeat (`[heartbeat]` at the end). |
| `STATS` | 802.11 frame counters since the previous POLL, or a note about the driver's counters. |
| `NEIGHBORS` | Other access points with the same network name (when `wifi.neighbors` is on). |
| `ACM/<event>` | Connection-level events from Windows: `connection_start`, `connection_complete`, `connection_attempt_fail`, `disconnecting`, `disconnected`, `scan_complete`, `scan_fail`, `network_available`, `network_not_available`, `profile_change`, … |
| `MSM/<event>` | Radio-level events: `associating`, `associated`, `authenticating`, `connected`, `roaming_start`, `roaming_end`, `radio_state_change`, `link_degraded`, `link_improved`, `disconnected`, … |

### Network adapters (source `nic`)

| Kind | Meaning |
|---|---|
| `NIC/status` | Adapter summary at startup and every heartbeat. |
| `NIC/up`, `NIC/down` | An adapter came up / went down or was removed. |
| `NIC/changed` | Speed, IP, gateway or DNS changed. |
| `NIC/errors` | Error/discard counters grew. |

### VPN (source `vpn`)

| Kind | Meaning |
|---|---|
| `VPN/status` | Tunnels and VPN client processes, at start and every heartbeat. |
| `VPN/connected`, `VPN/disconnected` | A tunnel came up / went down. |
| `VPN/changed` | Full/split, IP or server changed. |

### Probes (source `probe`)

| Kind | Meaning |
|---|---|
| `PROBE/dns`, `PROBE/gateway`, `PROBE/endpoint`, `PROBE/http` | Result of each probe, every round. |
| `PROBE/down`, `PROBE/up` | A probe changed state. |

### Path trace (source `pathtrace`)

| Kind | Meaning |
|---|---|
| `PATHTRACE/hop` | One hop of a trace. |
| `PATHTRACE/done` | End of a trace, and why it ran. |
| `INCIDENT/hold`, `INCIDENT/release` | Keeps an incident open while its trace runs. |

### Incidents (source `incident`)

| Kind | Meaning |
|---|---|
| `INCIDENT/triggered` | A trigger rule fired. |
| `INCIDENT/saved` | The incident file was written. |
| `INCIDENT/save_failed` | The incident file couldn't be written. |

### Remote queue (source `remote`)

These are never forwarded remotely. Each detail starts with `remote=<name>`, the remote it is about.

| Kind | Meaning |
|---|---|
| `REMOTE/resuming` | Sending events left from a previous run. |
| `REMOTE/send_failed` | A send failed. Shows the attempt number and next retry. |
| `REMOTE/recovered` | Sending works again. |
| `REMOTE/dropped` | The target permanently rejected a batch (`count=N error=...`); it was dropped so later events can go through. |
| `REMOTE/spool_overflow` | Old unsent events were dropped because the spool was full. |
| `REMOTE/spool_error` | The spool couldn't be read or written. |

### Power (source `power`)

Never held. See [Startup, sleep and shutdown](../power.md).

| Kind | Meaning |
|---|---|
| `POWER/starting` | exmon started; monitor events are held until the network is ready. |
| `POWER/suspend` | The machine is going to sleep or hibernate; events are held. |
| `POWER/resume` | The machine woke up (notified, or detected by a clock jump); events are held until the network is ready. |
| `POWER/ready` | The network is ready (default route + connection test); events are live again. Says how long it took and how many events were held. |
| `POWER/not_ready` | No working network within `power.maxWaitSeconds`; events are live again anyway. |
| `POWER/shutdown` | exmon (or Windows) is stopping; events are held from here on. |

### Metrics (`METRICS`)

Measurement events. They never appear in the text log, but are recorded in [baseline mode](../baseline.md) and checked by triggers. See [Metrics](metrics.md).
