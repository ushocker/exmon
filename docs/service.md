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

`install` does the following:

1. Copies `exmon.exe` to `C:\Program Files\exmon\exmon.exe`. Program Files can only be changed by administrators, so the service's program can't be swapped by a standard user.
2. Creates the `exmon` service:
   - **start:** automatic (delayed start);
   - **account:** `LocalService`, with its own identity `NT SERVICE\exmon`;
   - **on failure:** restart after 5s, 10s and 30s, resetting after a day.
3. Locks down `%ProgramData%\exmon` and `%ProgramData%\exmon\config` (see [Security](#security)).
4. Registers `exmon` as an Application event log source.
5. Starts the service.

**Running `install` again updates an existing install.** It stops the service, replaces the program, applies new flags and starts it again. To upgrade, download the new `exmon.exe` and run `exmon.exe install` from it.

## Status and uninstall

```bash
exmon.exe status       # works without elevation
exmon.exe uninstall    # elevated
```

`status` shows whether the service is installed and running, its account, start type and command line.

`uninstall` stops and removes the service and the event log source. It leaves the program in Program Files and all logs, incidents and config in `%ProgramData%\exmon`, and prints both paths so you can delete them yourself.

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

The service only writes its outputs (log, incidents, baseline, remote spool and outbox) inside `%ProgramData%\exmon`, or inside folders you list in `outputRoots`. This keeps a mistaken path from making it rename, delete or write files anywhere else. To use another folder:

1. List it in the config:

   ```json
   "outputRoots": ["D:\\Logs\\exmon"],
   "log": { "path": "D:\\Logs\\exmon\\exmon.log" }
   ```

2. Give the service write access there:

   ```powershell
   icacls D:\Logs\exmon /grant "NT SERVICE\exmon:(OI)(CI)M"
   ```

At startup the service also refuses an output path that goes through a **junction or symbolic link** anywhere below the allowed folder, so a link can't redirect its writes.

If either check fails, the service doesn't start, and the event log names the setting:

```
config: incident.dir D:\Data\incidents is outside the allowed output folders (C:\ProgramData\exmon); add its folder to outputRoots
```

Console mode doesn't apply these checks, because it writes with your own rights.

## What differs under LocalService

LocalService is a low-privilege account. All monitors work under it, with these differences:

- **HTTP probes** don't use your personal proxy settings, and a proxy that needs Windows sign-in rejects them (LocalService has no network identity). Proxies set through `HTTPS_PROXY` / `HTTP_PROXY` environment variables still work.
- **Built-in VPN server addresses** are read from the phonebook file Windows reports for the connection. Per-user phonebooks in other users' profiles may not be readable, in which case the server and its ping time are left out. The tunnel itself is still detected.

## Security

`install` replaces the permissions on exmon's folders, which by default would let any user create files there:

| Folder | SYSTEM, Administrators | `NT SERVICE\exmon` | Users |
|---|---|---|---|
| `C:\Program Files\exmon` | full | read | read |
| `%ProgramData%\exmon` (logs, incidents, spool …) | full | modify | read |
| `%ProgramData%\exmon\config` | full | read | read |

The folders are owned by Administrators and don't inherit permissions from their parents. They also carry an `OWNER RIGHTS: read` entry, so whoever creates a file inside them (even an administrator's own account) can't later loosen its permissions from a non-elevated program.

At startup the service also warns in the event log if its own program file could be changed by non-administrators, for example if someone registered the service by hand from a user-writable folder.

See [Config security review](design/config-security-review.md) for the background. Hardening for the planned control plane and update process is not part of this.
