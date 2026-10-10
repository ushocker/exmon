# Files exmon writes

With the defaults, everything lives under `%ProgramData%\exmon\` (usually `C:\ProgramData\exmon\`). Every path can be changed in the [configuration](../configuration.md). As the service, outputs must stay inside `%ProgramData%\exmon` or a folder listed in `outputRoots` (see [Custom output locations](../service.md#custom-output-locations)).

| Path | Written by | Contents |
|---|---|---|
| `config\exmon.json` | you, or `--write-default-config` | The machine-wide config. An `exmon.json` next to `exmon.exe` takes priority over it. |
| `exmon.log`, `exmon.1.log` … | always | The main log ([format](log-format.md)). Rotates at `log.maxSizeMB`. |
| `incidents\incident-<time>-sev<N>-<rule>.log.gz` | incidents | One compressed file per incident ([details](../incidents.md#the-incident-file)). Kept per severity. |
| `remote-spool\<name>\00000001.jsonl` …, `cursor.json` | each remote | Events waiting to be sent to that remote, and how far sending has got ([details](../remote.md#the-spool-on-disk)). Don't edit while running. |
| `config\cloud\active.json`, `last-known-good.json` | control plane | The signed cloud config in use, and the one before it ([details](../control-plane.md#cloud-config)). Admin-only. |
| `state\control.json` | control plane | The newest version of each signed document applied, so old ones can't be replayed. |
| `state\pending-config.json` | control plane | A new cloud config on trial; removed once it has run healthily. |
| `update\incoming\`, `update\verified\`, `update\status\trial.json`, `update\helper-state\` | updater and exmon-helper | A downloaded update, the helper's checked copy, the trial and the helper's records ([details](../updates.md#files)). |
| `state\update-health.json` | updater | The new version's health report to exmon-helper. |
| `state\enrollment.json`, `state\<name>.csr` | `exmon enroll` | Test-device enrolment: key name and certificate request ([details](../enrollment.md)). |
| `remote-outbox.jsonl` | an `outbox` remote (the default one) | Sent events, one JSON record per line. Grows without limit; delete or rotate it yourself. |
| `baseline.jsonl`, `baseline.1.jsonl` … | baseline mode | Every event and metric as JSON lines. |
| `baseline-summary.txt` | baseline mode | Percentiles and suggested triggers. |

When running, exmon doesn't write anywhere else, and doesn't change Windows settings or the registry.

## What `exmon.exe install` adds

| What | Where |
|---|---|
| The program | `C:\Program Files\exmon\exmon.exe` |
| The `exmon-helper` program and service | `C:\Program Files\exmon\exmon-helper.exe`; service `exmon-helper` (LocalSystem, runs briefly when needed; [details](../service.md#the-helper-service)) |
| The `exmon` service | Service Control Manager (registry under `HKLM\SYSTEM\CurrentControlSet\Services\exmon`) |
| Event log source `exmon` | Application log (registry under `HKLM\SYSTEM\CurrentControlSet\Services\EventLog\Application\exmon`) |
| Locked-down permissions | `C:\Program Files\exmon`, `%ProgramData%\exmon`; restricted `config`, `config\cloud`, `state` and `update` folders below it ([details](../service.md#security)) |
| Service proxy (only with `--proxy`) | `Environment` value of the service's registry key |

`exmon.exe uninstall` removes the service and the event log source. It leaves the program and `%ProgramData%\exmon` in place.

## Safe to delete

With exmon stopped, you can delete any of these.

- Deleting `remote-spool\` (or one remote's folder in it) loses any events still waiting to be sent.
- Deleting the config means exmon falls back to the next config location, or to the built-in defaults.
