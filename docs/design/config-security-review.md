# Config security review

> **Status: all findings fixed.** This covers the **current setup**, where the config is a local file and exmon runs in a console or as the Windows Service. It records what was found, what was fixed and where, and what risk remains.
>
> Concerns that only exist if a cloud control plane is ever built are in [Cloud config security](cloud-config-security.md), which doesn't apply today.

## Summary

| # | Finding | Severity (before) | Fix | Where |
|---|---|---|---|---|
| F1 | A standard user could plant a config that runs with higher rights | High | The service and elevated consoles only use admin-controlled config. The installer locks down exmon's folders and puts the program in Program Files. | `internal/service`, `internal/winsys`, `main.go` (`consoleTrust`) |
| F2 | Output paths could rename, delete or write files outside exmon's area | High → Low (after F1) | Service outputs are contained to `%ProgramData%\exmon` or `outputRoots`. Junctions and links are refused. The log writer refuses non-regular files. | `config.Options.CheckOutputs`, `internal/safepath`, `eventlog.StartFileSink` |
| F3 | No upper limits on numbers, so a typo could exhaust the machine | Medium | Every resource-costing setting has an allowed range. | `internal/limits`, each section's `Validate()` |

All fixes have tests, and `go test ./...` passes.

## Threat model

- **Who writes the config:** a **local administrator**.
- **Who we defend against:** a **standard local user** who wants exmon to do something for them, ideally with more privilege than they have.
- **What the service can touch:** it runs as **LocalService** with its own SID (`NT SERVICE\exmon`). Its token still contains `Everyone`, `Users` and `Authenticated Users`, so it can write wherever a standard user can, plus its own data folder. That is far less than SYSTEM, but not nothing.

After F1, only an administrator can change the config that elevated runs or the service use. That turns F2 and F3 from attack routes into **mistake and defence-in-depth** issues, which is how they were fixed.

## F1. A standard user could plant the config → fixed

**The issue.** `C:\ProgramData` lets any user create files and folders (`BUILTIN\Users:(CI)(WD,AD)`, plus `CREATOR OWNER:(F)`), so any user could create `C:\ProgramData\exmon\config\exmon.json`, or drop an `exmon.json` next to a user-writable `exmon.exe`. Anything that loaded it with higher rights would act on that user's behalf: the service, or an administrator running exmon in an elevated console.

**What was fixed:**

- **Locked-down install** (`exmon.exe install`, `internal/service/install.go`):
  - copies the program to `C:\Program Files\exmon`;
  - replaces the permissions on that folder, `%ProgramData%\exmon` and `%ProgramData%\exmon\config`. They become owned by Administrators and protected from inheritance, so standard users can only read. They also get a read-only `OWNER RIGHTS` entry, so whoever creates a file inside can't later loosen its permissions (`winsys.ApplyDACL`). Details are in [Running as a Windows Service](../service.md#security).
- **Trusted config only** (`service.Trust`, `config.LoadWith`): a config file is only used if it **and its folder** can be changed by administrators alone. That means:
  - the owner is SYSTEM, Administrators or TrustedInstaller, or is neutralised by a read-only `OWNER RIGHTS` entry;
  - no other account has write rights (`winsys.AdminOnlyWritable`).

  An untrusted `exmon.json` next to the exe is **skipped with a warning**. An untrusted `--config` file or machine-wide config **stops startup**.
- **Applies to the service and elevated consoles** (`consoleTrust` in `main.go`). A normal, non-elevated console keeps loading any config, because it only runs with the user's own rights.
- **Warning if the program itself is replaceable:** at startup the service logs one if its own exe could be changed by non-administrators.

**Tests:**
- `internal/winsys/winsys_test.go`: permission checks on parsed descriptors and on real files, including `OWNER RIGHTS` neutralisation;
- `internal/config/trust_test.go`: skip versus refuse;
- `internal/service/service_test.go`;
- `main_test.go`: `consoleTrust`.

**Residual risk:** a machine where the service was never installed still has the default permissions on `%ProgramData%\exmon`. A standard user can create files there, but an elevated run now refuses them, so a planted config never runs with admin rights.

## F2. Output paths could rename, delete or write files → fixed (defence-in-depth)

**The issue:**
- **Rotation** (`FileSink.rotateIfNeeded`) deletes `<name>.<maxFiles><ext>` and renames `<path>` → `<name>.1<ext>`. Pointing `log.path` at an existing file moved it aside and could delete older copies.
- **Appends:** `log.path` and `remote.outbox` append to any path, creating folders as needed.
- **Pattern-limited deletes:** `remote.spoolDir` and `incident.dir` delete files matching their own naming patterns.
- **Links:** nothing checked for junctions or symbolic links, so a link could redirect all of the above.

**What was fixed:**

- **Output containment** (service mode; `config.Options.CheckOutputs`, called from `runService` in `main.go`):
  - every output in use (`log.path`, `incident.dir`, `remote.spoolDir`, `remote.outbox`, `baseline.path`) must be inside `%ProgramData%\exmon`, or a folder listed in the new `outputRoots` setting;
  - otherwise the service doesn't start, and the event log says which setting is wrong, e.g. `incident.dir D:\Data\incidents is outside the allowed output folders (C:\ProgramData\exmon); add its folder to outputRoots`;
  - console mode doesn't apply this, because it writes with the user's own rights.
- **No junctions or links** (`internal/safepath`):
  - `safepath.Within` compares paths after cleaning them, ignoring case, and doesn't treat a folder whose name merely starts with the allowed name as inside it;
  - `safepath.CheckNoLinks` refuses an output path if any existing part below the allowed folder is a junction or symbolic link.
- **Rotation safety** (`eventlog.StartFileSink`): refuses to start if the log path exists but isn't an ordinary file. Rotation was confirmed to touch only `<base>.N<ext>` in the log's own folder.

**Change from the original proposal:** the link check runs **once at service startup**, on every configured output path, instead of inside each of the four file-opening functions. Those functions don't know which folders are allowed. After startup, only administrators and the service can create anything inside the checked folders (the data folder is locked down by F1, and `outputRoots` are chosen by an admin), so a later link could only come from someone already trusted.

**Tests:**
- `internal/config/outputs_test.go`: containment, `outputRoots`, disabled features, relative roots, a real junction;
- `internal/safepath/safepath_test.go`: case, look-alike prefixes, `..`, other drives, a real junction;
- `internal/eventlog/sink_test.go`: non-regular log path, rotation leaves neighbouring files alone.

**Residual risk:** an administrator can still point outputs at any folder by adding it to `outputRoots`. That is deliberate, and limited to where `NT SERVICE\exmon` has been granted write access.

## F3. No upper limits on numbers → fixed

**The issue:** validation only checked minimums, and `log`, `nic` and `vpn` had no validation. A typo could make exmon unusable:

| Value | Effect if very large |
|---|---|
| `log.maxFiles` | Each rotation loops that many renames, so the writer appears to hang. |
| `probe.pathTrace.maxHops` | One goroutine and three pings per hop per endpoint: memory and CPU exhaustion. |
| `probe.pingCount`, `vpn.pingCount`, `probe.timeoutSeconds` | Probe rounds that never finish. |
| `incident.windowMinutes`, `postTriggerSeconds`, `maxHoldSeconds` | Memory grows with every held event. |
| huge `remote.maxSpoolMB`, tiny `segmentKB` | Unbounded disk use or file counts. |
| number of `endpoints` / `httpTests` / `triggers` | Unbounded work per round. |

**What was fixed:** each section's `Validate()` checks its ranges using the shared `internal/limits` package (`Range`, `Count`, `First`). `log`, `nic` and `vpn` gained a `Validate()`, called from `config.Options.Validate`. Errors name the setting and its range, e.g. `log.maxFiles 5000 is out of range 1-50`. Every default is inside its range, and so is `exmon.example.json`.

| Setting | Allowed range |
|---|---|
| `log.maxSizeMB` | 0–1024 (0 = no rotation) |
| `log.maxFiles` | 1–50 |
| `log.flushIntervalMs` | 100–60000 |
| `wifi.pollSeconds` | 1–3600 |
| `nic.pollSeconds`, `vpn.pollSeconds` | 5–3600 |
| `probe.intervalSeconds` | 10–86400 |
| `probe.timeoutSeconds` | 1–60 |
| `probe.pingCount`, `vpn.pingCount` | 1–10 |
| `probe.pathTrace.maxHops` | 1–64 |
| `probe.endpoints`, `probe.httpTests` | at most 25 each |
| `probe.dnsNames` | at most 10 |
| `incident.windowMinutes` | 1–60 |
| `incident.postTriggerSeconds`, `incident.maxHoldSeconds` | 0–600 |
| `incident.triggers` | at most 100 |
| `incident.maxIncidents`, each `maxIncidentsBySeverity` value | 0–10000 (0 = unlimited) |
| `remote.maxSpoolMB` | 1–1024 |
| `remote.segmentKB` | 64–16384 |
| `remote.batchSize` | 1–1000 |
| `baseline.summaryMinutes` | 1–1440 |
| `baseline.maxSizeMB` | 1–1024 |
| `baseline.maxFiles` | 1–50 |

The user-facing copy of this table is in [Configuration](../configuration.md#allowed-ranges).

**Tests:**
- `internal/config/limits_test.go`: each limit set just out of range through a real config file, and the defaults passing;
- `internal/limits/limits_test.go`.

**Residual risk:** none for the original issue. A value *inside* a range can still be a poor choice, such as `probe.intervalSeconds` 10 with 25 endpoints, but it can no longer exhaust the machine.

## Behaviour changes users will notice

- **The service only uses an admin-controlled config.** Create or edit it from an elevated prompt in `%ProgramData%\exmon\config`, or run `exmon.exe install` to (re)set the folder permissions.
- **An elevated console refuses an untrusted config** with the same message as the service. A normal console is unchanged.
- **Service outputs outside `%ProgramData%\exmon` need `outputRoots`**, plus write access for `NT SERVICE\exmon`. See [Custom output locations](../service.md#custom-output-locations).
- **Out-of-range values stop startup** with a message naming the setting.

## Probe targets: not a finding in the current setup

`probe.endpoints`, `httpTests`, `dnsNames` and path traces make the machine connect to, request, resolve and ping whatever the config lists. With a config written by a local administrator, choosing what to probe is the feature working as intended, and F3 now caps how much of it there can be. The fleet-scale version of this risk is covered in [Cloud config security](cloud-config-security.md#c2-traffic-abuse-at-fleet-scale).

## Already sound

These were sound before the review and remain so:

- No config-driven command execution, shell, scripts or regexes. Event-kind patterns use `path.Match` and are validated.
- TLS certificate verification is always on; no setting disables it.
- JSON decoding is strictly typed, and the whole config is validated before startup.
- `--write-default-config` never overwrites a file (`O_EXCL`).
- Incident pruning and spool cleanup only touch files that match their own naming pattern.
- No secrets are stored in the config.
- The service runs as LocalService with its own SID, not SYSTEM.
