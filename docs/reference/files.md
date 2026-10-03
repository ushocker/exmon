# Files exmon writes

With the defaults, everything lives under `%ProgramData%\exmon\` (usually `C:\ProgramData\exmon\`). Every path can be changed in the [configuration](../configuration.md). As the service, outputs must stay inside `%ProgramData%\exmon` or a folder listed in `outputRoots` (see [Custom output locations](../service.md#custom-output-locations)).

| Path | Written by | Contents |
|---|---|---|
| `config\exmon.json` | you, or `--write-default-config` | The machine-wide config. An `exmon.json` next to `exmon.exe` takes priority over it. |
| `exmon.log`, `exmon.1.log` … | always | The main log ([format](log-format.md)). Rotates at `log.maxSizeMB`. |
| `incidents\incident-<time>-sev<N>-<rule>.log.gz` | incidents | One compressed file per incident ([details](../incidents.md#the-incident-file)). Kept per severity. |
| `remote-spool\00000001.jsonl` …, `cursor.json` | remote queue | Events waiting to be sent, and how far sending has got ([details](../remote.md#the-spool-on-disk)). Don't edit while running. |
| `remote-outbox.jsonl` | remote queue (stand-in sender) | What would have been sent to the remote target, one JSON record per line. Grows without limit until the real sender exists. |
| `baseline.jsonl`, `baseline.1.jsonl` … | baseline mode | Every event and metric as JSON lines. |
| `baseline-summary.txt` | baseline mode | Percentiles and suggested triggers. |

When running, exmon doesn't write anywhere else, and doesn't change Windows settings or the registry.

## What `exmon.exe install` adds

| What | Where |
|---|---|
| The program | `C:\Program Files\exmon\exmon.exe` |
| The `exmon` service | Service Control Manager (registry under `HKLM\SYSTEM\CurrentControlSet\Services\exmon`) |
| Event log source `exmon` | Application log (registry under `HKLM\SYSTEM\CurrentControlSet\Services\EventLog\Application\exmon`) |
| Locked-down permissions | `C:\Program Files\exmon`, `%ProgramData%\exmon`, `%ProgramData%\exmon\config` ([details](../service.md#security)) |

`exmon.exe uninstall` removes the service and the event log source. It leaves the program and `%ProgramData%\exmon` in place.

## Safe to delete

With exmon stopped, you can delete any of these.

- Deleting `remote-spool\` loses any events still waiting to be sent.
- Deleting the config means exmon falls back to the next config location, or to the built-in defaults.
