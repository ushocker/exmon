# Running as a Windows Service

exmon can run as a Windows Service, so it starts with Windows and keeps monitoring whether or not anyone is logged on.

## Install

From an **elevated** prompt (Run as administrator):

```bash
exmon.exe install
```

Flags after `install` are built into the service's command line, for example:

```bash
exmon.exe install --probe-interval-seconds 60 --baseline
```

The flags are checked first, so a typo fails here and not silently inside the service. Most settings belong in the config file instead (see below).

For a managed fleet, `install` also takes the control-plane and update flags (`--control-host`, `--control-cert-issuer`, `--update-hosts`, `--update-publisher` …); see [Intune deployment](deploy/intune.md#5-the-win32-app) and [Updates](updates.md).

One flag is for `install` only and isn't passed to the service: `--proxy <url>` sets `HTTPS_PROXY` and `HTTP_PROXY` for the service, for networks where outbound HTTPS must go through a proxy. `--proxy=` (empty) removes it again.

```bash
exmon.exe install --proxy http://proxy.corp:8080
```

`install` does the following:

1. Copies `exmon.exe`, and `exmon-helper.exe` if it is next to it, to `C:\Program Files\exmon\`. Program Files can only be changed by administrators, so the programs can't be swapped by a standard user.
2. Creates the `exmon` service:
   - **start:** automatic (delayed start);
   - **account:** `LocalService`, with its own identity `NT SERVICE\exmon`;
   - **on failure:** restart after 5s, 10s and 30s, resetting after a day.
3. Locks down `%ProgramData%\exmon` and the folders below it (see [Security](#security)).
4. Creates the `exmon-helper` service (see [The helper service](#the-helper-service)) and runs it once.
5. Registers `exmon` and `exmon-helper` as Application event log sources.
6. Starts the service.

**Running `install` again updates an existing install.** It stops the service, replaces the program, applies new flags and starts it again. To upgrade, download the new `exmon.exe` and run `exmon.exe install` from it.

## Status and uninstall

```bash
exmon.exe status       # works without elevation
exmon.exe uninstall    # elevated
```

`status` shows whether the service is installed and running, its account, start type and command line.

`uninstall` stops and removes both services (`exmon` and `exmon-helper`) and their event log sources. It leaves the program in Program Files and all logs, incidents and config in `%ProgramData%\exmon`, and prints both paths so you can delete them yourself.

The usual Windows tools work too: `Start-Service exmon`, `Stop-Service exmon`, `sc qc exmon`, and the Services console.

## Configuration

The service uses the same config search as the console (see [Getting started](getting-started.md#where-exmon-looks-for-its-config)), but **only accepts a config file that only administrators can change**:

1. `C:\Program Files\exmon\exmon.json`, if present and admin-only. If it fails the check it is **skipped**, with a warning in the event log and the exmon log.
2. `C:\ProgramData\exmon\config\exmon.json`. If it exists but fails the check, **the service refuses to start** and logs an event log error. Running a config someone may have tampered with is worse than not running.

Create the config from an elevated prompt:

```bash
exmon.exe --write-default-config
notepad C:\ProgramData\exmon\config\exmon.json
Restart-Service exmon
```

Files an administrator creates in the config folder pass the check automatically; the folder's permissions are set up for that. Config changes take effect when the service restarts.

## Logs and the event log

- **Output files:** the exmon log, incidents, the baseline and the remote spool go to the usual places under `%ProgramData%\exmon` ([Files](reference/files.md)). Nothing is echoed to a console.
- **Event log:** the Windows **Application** event log (source `exmon`) records:
  - service started, with the config file used;
  - service stopped;
  - warnings, such as a skipped untrusted config;
  - **anything that stops the service from starting**, such as an invalid config or an unwritable log path. If the service won't start, look here first:

    ```powershell
    Get-WinEvent -FilterHashtable @{LogName='Application'; ProviderName='exmon'} -MaxEvents 20 | Format-List TimeCreated, LevelDisplayName, Message
    ```

The service asks Windows for **pre-shutdown** notice, which arrives while the network is still up, so it starts holding events before the network is torn down (see [Startup, sleep and shutdown](power.md)). On Stop or shutdown the service shuts down the same way as Ctrl+C in the console: incidents still recording are saved, the remote queue gets a last send attempt, and the final log lines are written.

## The logged-on user

The service runs as its own account. So that the `DEVICE` line and incident file headers still say who was using the machine, exmon reports the user **logged on at the console**:

- the startup `DEVICE` line shows `user=""` if nobody was logged on yet;
- each incident file's header is filled in with whoever is logged on when the file is written.

## Custom output locations

The service only writes its outputs (log, incidents, baseline, remote spools and outbox files) inside `%ProgramData%\exmon`, or inside folders you list in `outputRoots`. This keeps a mistaken path from making it rename, delete or write files anywhere else. To use another folder, list it in the config:

```json
"outputRoots": ["D:\\Logs\\exmon"],
"log": { "path": "D:\\Logs\\exmon\\exmon.log" }
```

Then give the service write access there:

```powershell
icacls D:\Logs\exmon /grant "NT SERVICE\exmon:(OI)(CI)M"
```

At startup the service also refuses an output path that goes through a **junction or symbolic link** anywhere below the allowed folder, so a link can't redirect its writes.

If either check fails, the service doesn't start, and the event log names the setting:

```
config: incident.dir D:\Data\incidents is outside the allowed output folders (C:\ProgramData\exmon); add its folder to outputRoots
```

Console mode doesn't apply these checks, because it writes with your own rights.

## The helper service

exmon runs as **LocalService**, a low-privilege account. Two jobs need more than that, so they are done by a second, deliberately small program, **`exmon-helper.exe`**, which runs as its own service (`exmon-helper`, LocalSystem):

1. **Letting exmon use its device certificate.** The certificate's private key is created in the TPM by Intune (or `exmon enroll`), and only SYSTEM and Administrators may use it at first. The helper grants `NT SERVICE\exmon` permission to use it (sign with it, never export it). Intune renews the certificate with a new key every year, so the helper does this at every boot, once a day, and whenever exmon reports it can't use the key (`CONTROL/helper_started reason=key_access`).
2. **Applying updates.** exmon can't replace its own program. It downloads an update and asks the helper, which checks everything again itself, swaps the exe, restarts exmon and rolls back if the new version isn't healthy ([Updates](updates.md#what-the-helper-checks)).

The helper runs only when there is work, usually a second or two, so `exmon status` normally shows it stopped. It parses no config file, opens no network connection and reads only two things exmon prepared: a small request file and the downloaded exe, both re-verified. Its settings (which certificate, the code-signing publisher, health time) are baked into its command line by `exmon install`, which only an administrator can change. **Run `install` again after changing `control.cert` or `control.update` settings.**

exmon's only right on the helper service is to **start** it (and see whether it is running). It can't stop, reconfigure or replace it:

| `exmon-helper` service | SYSTEM, Administrators | `NT SERVICE\exmon` | Users |
|---|---|---|---|
| Rights | full | query status, start | query |

Its messages go to the Application event log, source `exmon-helper`:

```powershell
Get-WinEvent -FilterHashtable @{LogName='Application'; ProviderName='exmon-helper'} -MaxEvents 20 | Format-List TimeCreated, LevelDisplayName, Message
```

### What differs under LocalService

LocalService is a low-privilege account. All monitors work under it, with these differences:

- **Proxies:** it has no per-user proxy settings. Set one with `exmon.exe install --proxy <url>`. A proxy that needs Windows sign-in rejects it (LocalService has no network identity).
- **Built-in VPN server addresses** are read from the phonebook file Windows reports for the connection. Per-user phonebooks in other users' profiles may not be readable, in which case the server and its ping time are left out. The tunnel itself is still detected.

## Security

`install` replaces the permissions on exmon's folders, which by default would let any user create files there:

| Folder | Holds | SYSTEM, Administrators | `NT SERVICE\exmon` | Users |
|---|---|---|---|---|
| `C:\Program Files\exmon` | `exmon.exe`, `exmon-helper.exe` | full | read | read |
| `%ProgramData%\exmon` | logs, incidents, spools | full | modify | read |
| `config` | `exmon.json`, which can hold the Azure Monitor secret | full | read | **none** |
| `config\cloud` | the signed cloud config (re-verified on every load) | full | modify | none |
| `state` | control-plane state | full | modify | none |
| `update\incoming` | downloaded updates and the request to the helper | full | modify | none |
| `update\status` | the helper's record of an update on trial | full | read | none |
| `update\verified`, `update\helper-state` | the helper's own copy and records | full | **none** | none |

The folders are owned by Administrators and don't inherit permissions from their parents. They also carry an `OWNER RIGHTS: read` entry, so whoever creates a file inside them (even an administrator's own account) can't later loosen its permissions from a non-elevated program.

Because standard users can't read the config folder, **a console run without elevation can't see the machine-wide config** and uses the built-in defaults (the `config:` line at startup says so). Run elevated, or pass `--config` with a file you can read.

**The helper trusts nothing exmon wrote.** If exmon (or anything running as LocalService) were compromised, it could put any file in `update\incoming`. The helper verifies the manifest's signature with its own built-in keys, checks versions against its own records, and checks the size, SHA-256 and code signature of the exe on a copy in `update\verified`, which exmon can't touch. A forged request is refused and deleted.

At startup the service also warns in the event log if its own program file could be changed by non-administrators, for example if someone registered the service by hand from a user-writable folder.

See [Config security review](design/config-security-review.md) for the background.
