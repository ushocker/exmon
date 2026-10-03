# Config security review

> **Status:** covers the **current setup**, where the config is a local file and exmon runs in a console or as the Windows Service.
>
> - **Fixed:** F1 (service installs and elevated console runs), F2 (output containment, link refusal, rotation safety) and F3 (upper limits). Each section below records what was proposed and how it was implemented.
> - **Open:** nothing for the current setup.
> - Concerns that only exist if a cloud control plane is built are in [Cloud config security](cloud-config-security.md), which doesn't apply today.

## Threat model

- **Who writes the config:** a **local administrator**. The service only uses a config file that only administrators can change (`service.Trust` in `internal/service/install.go`), and `exmon.exe install` locks down `%ProgramData%\exmon\config`.
- **Who we defend against:** a **standard local user** who wants to make exmon do something on their behalf, ideally with more privilege than they have.
- **What the service can touch:** it runs as **LocalService** with its own SID (`NT SERVICE\exmon`). Its token still contains `Everyone`, `Users` and `Authenticated Users`, so it can write anywhere a standard user can, plus its own data folder. That is far less than SYSTEM, but not nothing.

Because only an administrator can change the config the service uses, most config-driven issues below are **mistake and defence-in-depth** issues rather than attacks. The exception is the F1 gap, which is still a route for a standard user.

## Findings

### F1. A standard user can plant the config → fixed

> **Implemented:** the elevated-console gap below was closed by `consoleTrust` in `main.go` (tested in `main_test.go`).

**Background.** `C:\ProgramData` lets any user create files and folders (`BUILTIN\Users:(CI)(WD,AD)`, plus `CREATOR OWNER:(F)`). Without protection, any user could create `C:\ProgramData\exmon\config\exmon.json`, or drop an `exmon.json` next to a user-writable `exmon.exe`.

**Done (service installs):**
- `exmon.exe install` copies the program to `C:\Program Files\exmon` and replaces the permissions on it, `%ProgramData%\exmon` and `%ProgramData%\exmon\config`. They become owned by Administrators, protected from inheritance, with a read-only `OWNER RIGHTS` entry (see [Running as a Windows Service](../service.md#security)).
- The service only loads a config that, together with its folder, only administrators can change. An untrusted `exmon.json` next to the exe is skipped with a warning; an untrusted machine-wide config stops the service from starting.

**Gap: elevated console runs.** When an administrator runs `exmon.exe` in an **elevated** console, `main.go` loads the config with `config.Load`, which has no trust check. On a machine where the service was never installed, `%ProgramData%\exmon` still has the default permissions, so a config a standard user planted there would run with administrator rights. Combined with F2, that becomes a file rename/delete as admin.

**Proposed fix:** in `main.go`'s console path, if `winsys.IsElevated()`, load with `config.LoadWith(os.Args[1:], service.Trust)`, exactly as the service does.
- A non-elevated console is unchanged, because it runs with the user's own rights anyway.
- Elevated runs then refuse an untrusted config with the same message as the service. The fix for an administrator is to run `exmon.exe install` once (which locks the folders), or to keep their config somewhere admin-only.
- **Size:** small (about 10 lines, plus a test on the elevated/non-elevated choice).

### F2. Output paths can rename, delete or write files → fixed (defence-in-depth)

> **Implemented:**
> - containment: `config.Options.CheckOutputs`, called at service startup, plus the `outputRoots` setting;
> - link refusal: the `internal/safepath` package;
> - rotation safety: `eventlog.StartFileSink` refuses a log path that isn't a regular file, with tests in `internal/config/outputs_test.go`, `internal/safepath` and `internal/eventlog/sink_test.go`.
>
> **One change from the proposal:** the link check runs once at service startup, on every configured output path, rather than inside each file-opening function. Those functions don't know which folders are allowed. After startup, only administrators and the service can create anything inside the checked folders (the data folder is locked down, and `outputRoots` are chosen by an admin), so a later link can only be created by someone already trusted.

**The behaviour:**
- **Rotation:** `log.path` rotation (`FileSink.rotateIfNeeded` in `internal/eventlog/sink.go`) deletes `<name>.<maxFiles><ext>` and renames `<path>` → `<name>.1<ext>`. Pointing `log.path` at an existing file with a small `maxSizeMB` moves that file aside and deletes older copies.
- **Appends:** `log.path` and `remote.outbox` (`OutboxSender` in `internal/remote/sender.go`) append to any path, creating folders as needed.
- **Pattern-limited deletes:** `remote.spoolDir` deletes or truncates numeric `*.jsonl` files, and `incident.dir` deletes `incident-*.log.gz` beyond the limits. Both are restricted by file name.
- **Links:** none of these check for junctions or symbolic links; they follow them.

**Residual risk:** only an administrator can set these paths for the service, so this is about mistakes, for example `log.path` set to an existing file someone cares about. The service can only affect places LocalService can write. The bigger value of the fix is defence-in-depth: if a config is ever wrong, or a folder's permissions are later loosened, exmon still can't be steered outside its own area.

**Proposed fixes:**

1. **Output containment (service mode).** At startup, resolve `log.path`, `incident.dir`, `remote.spoolDir`, `remote.outbox` and `baseline.path` to absolute, cleaned paths, and require each to be inside `%ProgramData%\exmon` or a folder listed in a new `outputRoots` setting (e.g. `["D:\\Logs\\exmon"]`). Anything else stops startup with a message such as:

   ```
   log.path D:\Data\important.txt is outside the allowed output folders (C:\ProgramData\exmon, D:\Logs\exmon); add its folder to outputRoots
   ```

   It sits next to `config.Options.Validate`, as a service-mode check (console mode writes with the user's own rights, so containment isn't needed there).
2. **Refuse junctions and symbolic links.** A shared helper, e.g. `safepath.Check(root, path)`, `os.Lstat`s every component from the allowed root down to the target, and refuses any that is a symlink or junction (Go reports junctions as `os.ModeIrregular`). It is called before anything is created or opened, from:
   - `eventlog.StartFileSink`;
   - `incident.Start`;
   - `remote.OpenSpool`;
   - `remote.OutboxSender`.

   Inside `%ProgramData%\exmon` no standard user can create links any more, so this mainly protects `outputRoots` folders and future changes.
3. **Rotation safety.** `eventlog.StartFileSink` refuses to start if `log.path` already exists and isn't a regular file. Add a test proving rotation only touches `<base>.N<ext>` names in the same folder as `log.path`.

**Size:** medium (one small helper package, four call sites, one config field, tests).

### F3. No upper limits on numbers → fixed

> **Implemented:** ranges in each section's `Validate()` (new for `log`, `nic` and `vpn`) using the shared `internal/limits` package, as in the table below (plus `log.flushIntervalMs` 100–60000, `wifi.pollSeconds` 1–3600, and `baseline.summaryMinutes` 1–1440 and `maxFiles` 1–50). Tested in `internal/config/limits_test.go`. The user-facing list is in [Configuration](../configuration.md#allowed-ranges).

**The behaviour:** validation only checks minimums, and the `log`, `nic` and `vpn` sections have no validation. A typo can make exmon unusable:

| Value | Effect if very large |
|---|---|
| `log.maxFiles` | Each rotation loops that many renames, and the writer appears to hang. |
| `probe.pathTrace.maxHops` | One goroutine and three pings per hop per endpoint: memory and CPU exhaustion. |
| `probe.pingCount`, `vpn.pingCount`, `probe.timeoutSeconds` | Probe rounds that never finish. |
| `incident.windowMinutes`, `postTriggerSeconds`, `maxHoldSeconds` | Memory grows with every held event. |
| huge `remote.maxSpoolMB`, tiny `segmentKB` | Unbounded disk use or file counts. |
| number of `endpoints` / `httpTests` / `triggers` | Unbounded work per round. |

**Proposed fix:** hard ranges in each package's `Validate()`, with messages that state the range (e.g. `log.maxFiles 5000 is out of range 1-50`).
- `log`, `nic` and `vpn` gain a `Validate()`, called from `config.Options.Validate` in `internal/config/config.go`.
- Every current default is inside its range, so existing valid configs keep working.

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
| `baseline.maxSizeMB` | 1–1024 |

**Size:** small (range checks and table-driven tests).

### Probe targets: not a finding in the current setup

`probe.endpoints`, `httpTests`, `dnsNames` and path traces make the machine connect to, request, resolve and ping whatever the config lists. With a config written by a local administrator, choosing what to probe is the feature working as intended, so there is nothing to fix today. The fleet-scale version of this risk, one bad config pushed to every machine, is covered in [Cloud config security](cloud-config-security.md#c2-traffic-abuse-at-fleet-scale).

## Roadmap

All local, independent of any cloud work. All three are done:

1. **F1 gap:** elevated console uses the trust check.
2. **F3:** upper limits.
3. **F2:** output containment, link refusal, rotation safety.

## Already sound

- No config-driven command execution, shell, scripts or regexes. Event-kind patterns use `path.Match` and are validated.
- TLS certificate verification is always on; no setting disables it.
- JSON decoding is strictly typed, and the whole config is validated before startup.
- `--write-default-config` never overwrites a file (`O_EXCL`).
- Incident pruning and spool cleanup only touch files that match their own naming pattern.
- No secrets are stored in the config.
- **Service mode** (`internal/service`, `internal/winsys`):
  - the program is in admin-only Program Files;
  - exmon's folders are locked down, and a read-only `OWNER RIGHTS` entry stops file owners from loosening permissions;
  - only admin-only config files are used;
  - it runs as LocalService with its own SID rather than SYSTEM.
