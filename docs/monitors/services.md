# Windows services

The `services` monitor checks a list of Windows services every `services.pollSeconds` (default 30): whether each is installed and in the state it should be in (usually running), how it starts, and **the version of the program it runs**. Use it for the agents and services your PCs depend on: security and management agents, VPN clients, your own line-of-business services.

It does nothing until `services.watch` lists at least one service.

```json
"services": {
  "enabled": true,
  "pollSeconds": 30,
  "heartbeatMinutes": 60,
  "watch": [
    { "name": "Dnscache" },
    { "name": "MpsSvc" },
    { "name": "wuauserv", "expect": "any" },
    { "name": "ContosoAgent" }
  ]
}
```

| Setting | Default | Meaning |
|---|---|---|
| `enabled` | `true` | Run the monitor (when `watch` isn't empty). |
| `pollSeconds` | 30 | How often each service is checked (5–3600; at least 10 from a cloud config). |
| `heartbeatMinutes` | 60 | A `SERVICE/status` line for every service at start and then this often (0–1440; 0 = only at start). Changes are logged as they happen regardless. |
| `watch` | none | Up to 50 services. |
| `watch[].name` | | The service's **name**, not its display name: `Spooler`, not "Print Spooler". Not case-sensitive. |
| `watch[].expect` | `running` | The state it should be in: `running`, `stopped`, or `any` (report it, but never treat it as down: for services that start on demand, like Windows Update). |

To find a service's name:

```powershell
Get-Service | Where-Object DisplayName -like '*defender*' | Format-Table Name, DisplayName, Status, StartType
```

## Which program, which version

For each service exmon finds the program it runs and reads that file's version resource (what Explorer shows on the **Details** tab):

- **The exe:** the running process's own path when exmon may read it, otherwise the path in the service's configuration (`sc qc <name>`). As LocalService, exmon usually can't open processes running as SYSTEM, so for those the configured path is used; it is the same file unless something unusual is going on.
- **svchost services:** many Windows services are a DLL inside `svchost.exe`. The svchost version says little, so exmon also reports the service's own DLL (its `ServiceDll`), e.g. `dnsrslvr.dll` for Dnscache.

Reported fields: `version` (the numeric file version, e.g. `10.0.26100.8972`), `product_version` when it differs, and `company`. The DLL's are prefixed `dll_`. A program without a version resource shows `version=-`.

The version is re-read only when the file's size or time changes, so checking costs a few service-manager calls and a file check per service.

## Lines

| Kind | When | Example |
|---|---|---|
| `SERVICE/status` | Start, then every `heartbeatMinutes` | `[Spooler] running "Print Spooler" start=auto pid=5384 exe="C:\WINDOWS\System32\spoolsv.exe" version=10.0.26100.8972 product_version=10.0.26100.8875 company="Microsoft Corporation"` |
| `SERVICE/down` | It isn't in the expected state (at start, or when it changes) | `[ContosoAgent] stopped (expected running) exit=1067` |
| `SERVICE/up` | It is back in the expected state | `[ContosoAgent] running again` |
| `SERVICE/changed` | Its state or start type changed | `[ContosoAgent] running -> stopped exit=1067`, `[ContosoAgent] start type auto -> disabled` |
| `SERVICE/version_changed` | The program or its version changed (an upgrade) | `[ContosoAgent] agent.exe 4.2.0.0 -> agent.exe 4.3.1.0` |
| `SERVICE/error` | The service couldn't be queried (usually access denied) | `[Secured] Access is denied.` |

States: `running`, `stopped`, `start_pending`, `stop_pending`, `paused`, `pause_pending`, `continue_pending`, `not_installed`, `unknown` (the query failed). Start types: `auto`, `delayed_auto`, `manual`, `disabled`, `boot`, `system`. `exit=` is the Windows exit code the service last stopped with (1067 = "the process terminated unexpectedly"); `exit=service-specific N` is the service's own code.

`SERVICE/down` is logged once when the service leaves its expected state, not at every check; `SERVICE/up` when it returns. A service that is `not_installed` counts as down unless `expect` is `any`.

## Metrics

Per service (subject = the service name). See [Metrics](../reference/metrics.md#services):

- `running`: 1 when running, else 0.
- `as_expected`: 1 when in the expected state, else 0. The starter config's trigger `service-down` (`as_expected < 1` for 2 checks, severity 2) records an incident for any watched service that stops.

## Sending it to Log Analytics

The starter config forwards `SERVICE/status`, `/down`, `/up`, `/changed`, `/version_changed` and `/error` to the `law` remote, so every PC reports each service's state and version at least hourly. For example, which versions of an agent the fleet runs:

```kusto
ExmonEvents_CL
| where Kind == "SERVICE/status" and TimeGenerated > ago(1d) and Detail startswith "[ContosoAgent]"
| summarize arg_max(TimeGenerated, Detail) by Host
| extend Version = extract(@" version=(\S+)", 1, Detail), State = extract(@"\] (\S+)", 1, Detail)
| summarize Devices = count() by Version, State
```

PCs where it is down right now:

```kusto
ExmonEvents_CL
| where Kind in ("SERVICE/down", "SERVICE/up") and TimeGenerated > ago(7d)
| extend Service = extract(@"^\[([^\]]+)\]", 1, Detail)
| summarize arg_max(TimeGenerated, Kind, Detail) by Host, Service
| where Kind == "SERVICE/down"
```

The list can be changed fleet-wide with a [cloud config](../operations.md#changing-settings-cloud-configs) (`services` is cloud-settable); remember a published list replaces the local one.

## Permissions

exmon asks only for the right to read a service's status and configuration, which ordinary users and the LocalService account have on almost every service. A service whose permissions deny even that shows `unknown` with `SERVICE/error … Access is denied.`; check it with `sc.exe sdshow <name>`.
