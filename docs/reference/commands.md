# Command reference (on the PC)

Everything you can run or trigger on a monitored PC: the two programs, their subcommands and flags, the Windows service controls, what the control plane can make a PC do, and the checks worth knowing. The admin tool `exmonctl` runs on admin workstations, not on PCs; see [exmonctl](../exmonctl.md).

"Elevated" means an administrator PowerShell (**Run as administrator**), as **your own account** where a YubiKey is involved. Installed programs live in `C:\Program Files\exmon`; run them from there with `& "C:\Program Files\exmon\exmon.exe" …`, or from the folder you installed from.

## `exmon.exe`

### Subcommands

| Command | Elevated | Does |
|---|---|---|
| `exmon.exe` (no subcommand) | no | Runs the monitor in this console until Ctrl+C. Uses flags and the config file; see [Running in a console](#running-in-a-console). |
| `exmon.exe install [flags]` | yes | Copies `exmon.exe` (and `exmon-helper.exe` next to it) to Program Files, installs or updates both services, locks down `%ProgramData%\exmon`, starts exmon. Flags become the service's command line. [Details](../service.md#install). |
| `exmon.exe uninstall` | yes | Stops and removes both services and their event log sources. Leaves files in Program Files and `%ProgramData%\exmon`, and prints their paths. |
| `exmon.exe status` | no | Whether the services are installed and running, their account, start type and command line. |
| `exmon.exe version` | no | Prints the build's version (`dev` for an unversioned build). |
| `exmon.exe --write-default-config [--config <file>]` | for the default path | Writes the starter config to `<file>`, or to `%ProgramData%\exmon\config\exmon.json`. Never overwrites. |
| `exmon.exe enroll new\|sign-local\|import\|show\|remove` | yes | **Test** device certificates; see [below](#enroll-test-certificates). |
| `exmon.exe -h` | no | Lists every flag. |

### `install` flags

`install` accepts every [run flag](#run-flags) below and bakes them into the service's command line (it checks them first, so a typo fails here). One flag is for `install` only:

| Flag | Does |
|---|---|
| `--proxy <url>` | Sets `HTTPS_PROXY`/`HTTP_PROXY` for both services (Azure Monitor, control plane, update downloads, HTTP probes). `--proxy=` removes it. |

The control-plane and update flags are normally given here (and nowhere else), for example the test PC:

```powershell
.\exmon.exe install --control-host <controlHost> --control-cert-subject exmon-test-01 --control-cert-issuer "exmon Test Device CA" --update-hosts <updateAllowedHost> --update-require-authenticode=false
```

and production (`deploy/intune/install.cmd`):

```powershell
.\exmon.exe install --control-host <controlHost> --control-cert-issuer "exmon Issuing CA" --update-hosts <updateAllowedHost> --update-publisher "Contoso Ltd"
```

Running `install` again with different flags **replaces** the command line: give the full set every time. `exmon.exe status` shows the current one.

### Run flags

Flags override the config file and the cloud config. Each has a setting in `exmon.json` ([Configuration](../configuration.md)); flags are for testing and for the install-time settings.

**Config and output**

| Flag | Default | Does |
|---|---|---|
| `--config <file>` | `exmon.json` next to the exe, else `%ProgramData%\exmon\config\exmon.json` | The config file. |
| `--log-path <file>` | `%ProgramData%\exmon\exmon.log` | The log file (`%VAR%` expanded). |
| `--log-max-size-mb <n>` | 50 | Rotate at this size; 0 = never. |
| `--log-max-files <n>` | 5 | Rotated files to keep. |
| `--log-flush-interval-ms <n>` | 1000 | How often the log is flushed to disk. |

**Monitors**

| Flag | Default | Does |
|---|---|---|
| `--wifi-enabled` | true | The [Wi-Fi monitor](../monitors/wifi.md). |
| `--wifi-poll-seconds <n>` | 5 | Seconds between samples. |
| `--wifi-heartbeat-seconds <n>` | 15 | Write a POLL line at least this often; 0 = only on change. |
| `--wifi-debug-poll` | false | Log every poll. |
| `--wifi-rssi-threshold <n>` | 3 | dBm change that counts. |
| `--wifi-rate-change-percent <n>` | 50 | rx/tx swing that counts; 0 ignores rates. |
| `--wifi-error-pct-threshold <n>` | 10 | Retry/FCS % change that counts; 0 ignores them. |
| `--wifi-min-stats-frames <n>` | 500 | Frames needed before retry/FCS % is trusted. |
| `--wifi-neighbors` | false | Also log other access points with the same SSID. |
| `--nic-enabled` | true | The [network adapter monitor](../monitors/nic.md). |
| `--vpn-enabled` | true | [VPN detection](../monitors/vpn.md). |
| `--probe-enabled` | true | [Connectivity probes](../monitors/probes.md). |
| `--probe-interval-seconds <n>` | 300 | Seconds between probe rounds. |
| `--probe-path-trace-minutes <n>` | 30 | Minutes between [path traces](../monitors/path-trace.md); 0 = off. |
| `--power-enabled` | true | Hold events while starting, waking or shutting down ([Power](../power.md)). |

**Incidents and baseline**

| Flag | Default | Does |
|---|---|---|
| `--incident-enabled` | true | The rolling window and [incident](../incidents.md) triggers. |
| `--incident-dir <dir>` | `%ProgramData%\exmon\incidents` | Where incident files go. |
| `--incident-window-minutes <n>` | 10 | History kept for an incident. |
| `--incident-post-seconds <n>` | 30 | Recording after a trigger. |
| `--baseline` | false | [Baseline](../baseline.md) (learning) mode: record every event and metric. |
| `--baseline-path <file>` | `%ProgramData%\exmon\baseline.jsonl` | The baseline file. |
| `--baseline-summary-minutes <n>` | 15 | How often `baseline-summary.txt` is rewritten. |

**Remotes**

| Flag | Default | Does |
|---|---|---|
| `--remotes-enabled` | true | `false` turns every [remote](../remote.md) off. |
| `--remote-simulate-offline` | false | Testing: every remote's sends fail (events pile up in the spool, then go when you restart without it). |

**Control plane and updates** (install-time; a cloud config can't change them)

| Flag | Default | Does |
|---|---|---|
| `--control-host <host>` | none | The Event Grid MQTT host; also turns the [control plane](../control-plane.md) on. |
| `--control-enabled` | false | Turn the control plane on (with `control.host` from the config). |
| `--control-path <path>` | `/mqtt` | The WebSocket path. |
| `--control-cert-subject <cn>` | none | Use the client certificate with this CN (test PCs: `exmon-test-NN`). |
| `--control-cert-issuer <cn>` | `exmon Issuing CA` (starter config) | Use a client certificate issued by this CA. |
| `--update-hosts <a,b>` | none | Hosts [updates](../updates.md) may come from; also turns updates on. |
| `--update-publisher <cn>` | none | The code-signing certificate CN updates must carry. |
| `--update-require-authenticode` | true | `=false` only for unsigned test builds. |

### Running in a console

For testing on your own PC, without installing:

```powershell
.\exmon.exe --config .\exmon.json --log-path .\test.log --probe-interval-seconds 30
```

Ctrl+C stops it cleanly (incidents in progress are saved, remotes get a last send). Differences from the service:

- **Not elevated:** it can't read `%ProgramData%\exmon\config` (admin-only), so it uses `--config` or the built-in defaults, and can't write into a locked-down `%ProgramData%\exmon`: use `--log-path` and a config whose outputs point elsewhere.
- **Elevated:** it only accepts a config file that only administrators can change, like the service.
- It never applies updates, and while the service is running both would connect to the control plane with the same certificate and keep taking over each other's session. Stop the service first (`Stop-Service exmon`) if the console uses the control plane.

Useful test runs:

```powershell
.\exmon.exe --wifi-poll-seconds 2 --wifi-debug-poll --log-path .\test.log
```

```powershell
.\exmon.exe --baseline --baseline-path .\baseline.jsonl --log-path .\test.log
```

```powershell
.\exmon.exe --remote-simulate-offline --log-path .\test.log
```

### `enroll`: test certificates

For **test** PCs only (production certificates come from Intune). All elevated; see [Device certificates](../enrollment.md).

| Command | Flags | Does |
|---|---|---|
| `exmon.exe enroll new` | `--name exmon-test-NN` (required), `--allow-software-key` (no TPM: VMs), `--out <file.csr>` (to sign elsewhere) | Creates the device key in the TPM and a certificate request. |
| `exmon.exe enroll sign-local` | `--days <1-90>` (default 30), `--ca-name <cn>` (default `exmon Test Device CA`) | Signs the request with the YubiKey test CA (PIN + touch) and installs the certificate. |
| `exmon.exe enroll import` | `--cert <file>` | Installs a certificate signed elsewhere (`exmonctl ca sign-csr`). |
| `exmon.exe enroll show` | | The enrolment and the certificate's issuer and validity. |
| `exmon.exe enroll remove` | | Deletes the certificate, the TPM key and the enrolment state. |

## `exmon-helper.exe`

The small LocalSystem service that grants exmon use of the certificate key and applies updates ([The helper service](../service.md#the-helper-service)). `exmon install` sets it up and writes its flags; you don't normally run it yourself.

| Command | Elevated | Does |
|---|---|---|
| `Start-Service exmon-helper` | yes | One pass with its installed settings: grant key access, finish or apply a pending update, then stop by itself. The usual way to "run" it. |
| `exmon-helper.exe run [flags]` | yes | The same pass in a console, printing what it does (testing). Run it from `C:\Program Files\exmon` (it works on the exe next to it) with the same flags as its service command line. |

Flags (copied from exmon's settings by `install`):

| Flag | Default | Does |
|---|---|---|
| `--cert-subject <cn>` | none | The client certificate whose key exmon may use… |
| `--cert-issuer <cn>` | none | …or every certificate from this CA. |
| `--publisher <cn>` | none | Code-signing CN updates must carry. |
| `--require-authenticode` | true | Require a valid code signature on updates. |
| `--health-minutes <n>` | 10 | How long a new version has to report healthy (5–60). |
| `--max-size-mb <n>` | 64 | Largest update accepted (8–256). |

To see its current flags: `sc.exe qc exmon-helper`.

## Windows service controls

| Command | Elevated | Effect |
|---|---|---|
| `Start-Service exmon` / `Stop-Service exmon` / `Restart-Service exmon` | yes | Start, stop (cleanly: saves incidents, says goodbye to the control plane, last remote send) or restart. A restart is how a changed local `exmon.json` takes effect. |
| `Get-Service exmon, exmon-helper` | no | Their state. The helper is normally **Stopped**: it only runs when there is work. |
| `sc.exe qc exmon` | no | Account, start type and command line. |
| `sc.exe sdshow exmon-helper` | no | The helper's permissions (exmon may only start it). |
| `sc.exe config exmon start= disabled` / `start= delayed-auto` | yes | Stop Windows starting it (and restarting it after a crash) / put the normal start type back. |

Windows restarts a crashed exmon after 5 s, 10 s and 30 s. A clean stop marks the device offline in the control plane at once; a crash or power loss does so after about 2 minutes (the broker's last will), unless exmon is back within 30 seconds ([Control plane](../control-plane.md#how-a-device-connects)).

## What the control plane can make a PC do

Nothing runs on a PC from the network except these, each in a document signed with a key built into exmon ([Operations](../operations.md)):

| Sent with | The PC |
|---|---|
| `exmonctl cmd send --command report-status` | publishes its status now |
| `exmonctl cmd send --command trace-now` | runs a path trace to every probe target |
| `exmonctl cmd send --command check-now` | runs a probe round |
| `exmonctl config publish` | tries the new settings, restarts itself in-process, keeps them after 2 minutes healthy or reverts |
| `exmonctl release publish` | downloads the new exe at its spread time and hands it to the helper, which swaps it in and rolls back if it isn't healthy within `healthMinutes` |
| `exmonctl retained clear` | nothing on the PC; only stops later deliveries |

There is no command to run programs, read files or change install-time settings; those need `exmon install` on the PC (Intune).

## Checks

Logs and state (the folders are admin-only, so elevated):

```powershell
Get-Content C:\ProgramData\exmon\exmon.log -Tail 40
```

```powershell
Get-Content C:\ProgramData\exmon\exmon.log -Wait -Tail 5
```

```powershell
Select-String -Path C:\ProgramData\exmon\exmon.log -Pattern 'MONITOR|CONTROL/|UPDATE/|REMOTE/' | Select-Object -Last 20
```

Windows event log (no elevation needed):

```powershell
Get-WinEvent -FilterHashtable @{LogName='Application'; ProviderName='exmon'} -MaxEvents 20 | Format-List TimeCreated, LevelDisplayName, Message
```

```powershell
Get-WinEvent -FilterHashtable @{LogName='Application'; ProviderName='exmon-helper'} -MaxEvents 10 | Format-List TimeCreated, LevelDisplayName, Message
```

The device certificate:

```powershell
Get-ChildItem Cert:\LocalMachine\My | Where-Object { $_.Issuer -match 'exmon' } | Format-List Subject, Issuer, NotAfter, HasPrivateKey
```

The installed version, and the previous or rejected ones after an update:

```powershell
& "C:\Program Files\exmon\exmon.exe" version; Get-ChildItem "C:\Program Files\exmon" | Format-Table Name, LastWriteTime
```

Network paths (any HTTP status code means reachable; a TLS handshake failure means something blocks the host by name):

```powershell
curl.exe -sS -o NUL -w "%{http_code}`n" https://<controlHost>/mqtt
```

```powershell
curl.exe -sS -o NUL -w "%{http_code}`n" -X POST https://<dcr host>.logs.z1.ingest.monitor.azure.com/
```

The cloud config in force (signed JSON; the settings are base64url in `payload`):

```powershell
Get-Content C:\ProgramData\exmon\config\cloud\active.json
```

To drop the cloud config and run on the local `exmon.json` only: delete `config\cloud\active.json` and `Restart-Service exmon` (the next publish brings one back).

## Files

Where everything lives: [Files](files.md).
