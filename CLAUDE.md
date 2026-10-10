# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

`exmon` is a Windows-only Go program that runs long-lived monitors ("sources") and writes their events to one rotating log file through an async queue. Sources: Wi-Fi (`wlanapi.dll`), network adapters, VPN detection and connectivity probes (iphlpapi/rasapi32), all through `golang.org/x/sys/windows`. There is no cgo.

It was ported from the C# WlanWatch in `reference/csharp/`; the NIC/VPN/probe detections come from the PowerShell script in `reference/Powershell/` (its Azure Log Analytics/credential parts were deliberately not ported). Leave `reference/` untouched. It runs in a console or as a Windows Service (`exmon.exe install`; LocalService with the `NT SERVICE\exmon` SID; a second small LocalSystem service, `exmon-helper` (`cmd/exmon-helper`, `internal/helper`), grants exmon use of the TPM client-cert key and applies updates; exmon may only START it; see `docs/service.md`). The owner is new to Go, so explain Go-specific idioms when introducing them.

User and developer documentation lives in `docs/` (start at `docs/README.md`). Azure/Intune deployment lives in `deploy/` (Bicep in `deploy/bicep`, runbook in `deploy/automation`, scripts in `deploy/scripts`, Intune install/detect in `deploy/intune`); docs in `docs/deploy/`.

Dependencies: `golang.org/x/sys` and `github.com/eclipse/paho.golang` (MQTT; brings `gorilla/websocket`, `golang.org/x/net`). All pure Go; still no cgo.

## Keeping docs in sync (required)

Whenever a change affects behaviour a user or developer would read about, update the docs **in the same change**, before reporting the task as done. Don't wait to be asked. A change isn't finished until the docs match the code.

| If you change… | Update |
|---|---|
| a config setting, default, flag or allowed range | `docs/configuration.md` (incl. `#allowed-ranges`), `exmon.example.json` |
| a monitor/source's behaviour | `docs/monitors/<name>.md` (wifi, nic, vpn, probes, path-trace) |
| a metric name, subject or direction | `docs/reference/metrics.md` and the **Metrics** list in this file |
| a log kind or line/jsonl format | `docs/reference/log-format.md` |
| files written, paths, retention | `docs/reference/files.md` |
| incident triggers, severity, holds | `docs/incidents.md` |
| remote queue/spool/delivery | `docs/remote.md` |
| baseline mode | `docs/baseline.md` |
| service install/permissions/trust | `docs/service.md` |
| package layout, bus, consumers | `docs/architecture.md` and the **Architecture** section in this file |
| commands or first-run steps | `docs/getting-started.md`, `docs/reference/commands.md` (every subcommand and flag on the PC) and **Commands** in this file |
| Bicep, runbook, scripts in `deploy/` | `docs/deploy/azure.md` and `cloud-shell.md` (and `intune.md` / `production.md` / `test-install.md` if steps change) |
| control plane, signed documents, updates, enrolment, exmonctl | `docs/control-plane.md`, `docs/reference/signed-documents.md`, `docs/updates.md`, `docs/enrollment.md`, `docs/exmonctl.md`, `docs/operations.md` |
| a new page | link it from `docs/README.md` |

Pure refactors, test-only changes and internal renames with no visible effect need no doc update. If unsure, update. When you finish, say which doc pages you changed (or that none were needed and why). Leave `docs/design/*` as historical design notes unless asked.

## Commands

```bash
go build ./...      # build (must target windows; GOOS=windows if cross-compiling)
go vet ./...
go test ./...
go run . --wifi-poll-seconds 2 --wifi-debug-poll --log-path .\test.log   # run; Ctrl+C to stop
go run . --baseline --baseline-path .\baseline.jsonl   # learning mode: record all metrics + summary
go run . --remote-simulate-offline                     # test remote retries/redelivery without a network (all remotes)
go run . --probe-interval-seconds 30                   # faster probe rounds while testing (default 300)
go run . --write-default-config                        # write exmon.example.json (embedded in the exe) to %ProgramData%\exmon\config\exmon.json
go run . -h         # list flags
exmon.exe install [flags]   # elevated: copy to Program Files, install/update + start the service
exmon.exe uninstall         # elevated: remove the service (files kept)
exmon.exe status            # service state (no elevation needed)
go build -o dist/exmon-helper.exe ./cmd/exmon-helper   # the helper service; `exmon install` needs it next to exmon.exe
exmon-helper.exe run [flags]   # elevated: one helper pass in a console (key grant + pending update), for testing
exmon.exe version           # prints the build version (the helper asks the installed exe this)
exmon.exe enroll new|sign-local|import|show|remove   # elevated: TEST device certificate (TPM key + YubiKey test CA); see docs/enrollment.md
go run ./cmd/exmonctl help  # admin tool: YubiKey test CA, signing keys, publish configs/releases/commands, devices; see docs/exmonctl.md
```

## Architecture

Sources publish to a fan-out bus; every consumer gets its own copy of each event.

```
 sources ──► event.Logger (bus.For(name)) ──► event.Bus ──┬─► eventlog.FileSink   text log (always on)
 (wifi, nic, vpn, probe)                                  ├─► incident.Engine     rolling window + triggers → incident-*.log.gz
                                                          ├─► probe.Tracer        path traces on interval + on INCIDENT/triggered
                                                          ├─► remote.Queue × N    one per entry in `remotes`: spool → Sender chosen by `type`
                                                          ├─► eventlog.FileSink   baseline.jsonl (baseline mode only)
                                                          └─► baseline.Summarizer baseline-summary.txt (baseline mode only)
```

```
main.go               subcommands (install/uninstall/status/enroll) | service (svc.IsWindowsService) | console → supervise(ctx, session, opts): loop of run(ctx, opts, asService, ready, warn, sess) — subscribe consumers → run sources → ctx cancelled (Ctrl+C, service Stop, or sess.restart after a new cloud config) → engine.Flush → bus.Close → wait consumers (log file last); ready() reaches the SCM once. supervise.go: pending cloud config trial (3 starts / 10 min, commit after 2 min healthy, else RevertCloud). controlwire.go: control client deps, fixed commands, config handler (TestCloud → InstallCloud → pending → restart)
internal/service      SCM install/uninstall/status, folder DACLs, config Trust check, svc.Handler (StartPending→Running→StopPending checkpoints), Application event log
internal/event        Event (incl. Held), Logger (Log / Metrics / DeclareMetrics / Held), Bus (Subscribe, Publish, PublishAsync, Close, Hold/Release gate, For vs ForUnheld)
internal/power        hold gate driver: suspend/resume (winsys.WatchPower), clock-jump fallback, readiness = default route + connect test (msftconnecttest), max wait → POWER/not_ready, Shutdown on stop
internal/eventlog     FileSink: one writer goroutine owns the file (bufio, flush ticker, rotation, echo); text or jsonl
internal/window       rolling window of events (single owner, no mutex)
internal/incident     trigger Rules on METRICS events; captures window + post-trigger lines; gzip writer; INCIDENT/* events
internal/remote       per-remote Config + Remotes list, Spool (segment files + cursor.json, fsync), Backoff, Queue (appender + sender goroutines), Sender, Permanent errors, SenderType registry (outbox built in)
internal/remote/azuremonitor  "azureMonitor" target: Logs Ingestion API (gzip, <1 MB chunks, 401/413/429/403/400 handling); registered in internal/remote/senders
internal/secret       Secret string type that prints as [redacted] (fmt, %#v, JSON); Reveal() is the only accessor
internal/entra        client-credentials token source (cached, errors carry status + AADSTS code only)
internal/version      Version, set with -ldflags "-X exmon/internal/version.Version=1.4.0" (default "dev")
internal/baseline     baseline config + Summarizer (reservoir percentiles, suggested triggers)
internal/source       Source interface: Name(), Run(ctx, log) — blocks until ctx is cancelled
internal/config       Options composes each package's Config; defaults < config file < cloud config (only with control on; signed config\cloud\active.json, re-verified each load; cloud.go: allowed keys, stricter cloud ranges checked on the cloud layer over defaults, Install/RevertCloud, Pending) < flags. Config file: --config, else exmon.json next to the exe, else %ProgramData%\exmon\config\exmon.json (first found wins; logged at startup)
internal/wlan         Win32 WLAN layer (unsafe allowed); returns plain Go values
internal/winnet       Win32 network layer (unsafe allowed): adapters, counters, routes, ICMP ping with TTL, uncached DNS, RAS, interface notifications, processes
internal/winsys       Win32 security/session/crypto layer (unsafe allowed): CNG keys as crypto.Signer (TPM/software/smart card, ECDSA + RSA PKCS1/PSS), cert store find/import/remove, ReadSecret (no-echo PIN), ApplyDACL, AdminOnlyWritable (owner + DACL walk; read-only OWNER RIGHTS neutralises the owner), ConsoleUser (WTS), IsElevated
internal/enroll       test-device enrolment: TPM key + CSR, sign with the YubiKey CA (smart-card KSP), import linked to the key, MakeCA
cmd/exmonctl          admin tool (same module, profile exmonctl.json): ca init/sign-csr, keys export/dev-init, config publish (ValidateCloudConfig, retained), release publish, cmd send, retained clear, devices, rollout status, sync run; signs via Key Vault REST, MQTT via autopaho with Entra JWT (OAUTH2-JWT), ARM for clients/runbook; all tokens from `az`
internal/signdoc      signed envelopes {alg ES256, keyId, payload b64url, signature r||s}: Verify (size, key for kind, signature BEFORE parsing, kind/expiry/target), DecodeStrict, Seal; public keys embedded from trust/{config,release}/<keyId>.pem; dev keys only with -tags exmondev
internal/control      control-plane client (source "control"): autopaho MQTT v5 over wss:443 to Event Grid; cert picked per handshake (GetClientCertificate, TPM via winsys), RSA-PSS self-test -> TLS 1.2; topics exmon/v1/device/<CN>/{status,outbox,inbox/#}; signdoc-verified inbox, version state (state/control.json), 5-min apply gap, fixed commands; chirp CONTROL/unreachable every chirpMinutes; Transport interface (fake in tests)
internal/updater      exmon's half of an update (LocalService): percent selection + spread from issuedAt, https download from allowedHosts into update\incoming (size cap, sha256 streaming, redirects checked), early Authenticode, writes request.json {envelope bytes, file} and starts exmon-helper (UPDATE/handed_over); CheckOnStart writes state\update-health.json after 2 min + control connected; returns control.ErrScheduled so the retained manifest reschedules
internal/release      Manifest + version compare/selection + hand-over files (request, trial in update\status, health) shared by updater and helper
internal/helper       exmon-helper logic (SYSTEM): GrantKeys (winsys.GrantKeyRead for NT SERVICE\exmon), apply = re-verify EVERYTHING (own release keys, expiry, replay vs own state, installed `exmon.exe version`, size/sha256/Authenticode/publisher on an admin-only copy), swap, health watch, rollback; own svc handler; imports only signdoc, release, winsys
internal/limits       Range/Count/First helpers for config upper bounds
internal/safepath     Within (case-insensitive containment), CheckNoLinks (refuse junctions/symlinks below a root)
internal/device       computer/user/domain/model/OS, logged as DEVICE at startup, in incident headers, `host` on remote records
internal/wifi         Wi-Fi Source: notifications + poll ticker in one select loop; start/restart with backoff
internal/nic          adapter up/down/changed (event-driven) + counters, NIC/status heartbeat
internal/vpn          RAS VPNs, VPN adapters by pattern, client processes, full vs split tunnel, VPN/status heartbeat
internal/probe        every intervalSeconds: DNS (uncached), gateway ping, endpoint TCP+ping, HTTP with httptrace phases; Tracer = path trace
```

- **Lines vs metrics**: `Log(kind, detail)` is a log line. `Metrics(subject, map)` is a `METRICS` event that never goes to the text log; triggers and the baseline use it. Sources declare a `Direction` (LowIsBad/HighIsBad) per metric so the baseline can suggest thresholds.
- **Metrics** (subject in brackets):
  - wifi [adapter]: `rssi_dbm`, `signal_quality`, `rx_kbps`, `tx_kbps`, per-poll `retry_pct`, `fcs_pct`, `handshake_fail`, `recent_disconnects` (window `wifi.disconnectWindowMinutes`, default 30; counts only disconnects seen since exmon started; the old name `disconnects_30m` is rejected at startup)
  - nic [adapter name]: `link_speed_mbps`, `rx_bps`, `tx_bps`, `in_errors`, `out_errors`, `in_discards`, `out_discards` (deltas per poll)
  - vpn [tunnel]: `vpn_up`, `vpn_server_rtt_ms`, `in_errors`, `out_errors`
  - probe [probe name; `gateway`; `dns:<name>`]: `dns_ms`, `gateway_rtt_ms`, `gateway_loss_pct`, `tcp_connect_ms`, `rtt_avg_ms`, `loss_pct`, `reachable`, `http_total_ms`, `http_ttfb_ms`, `http_tls_ms`, `http_ok`
- **Severity**: each trigger has `severity` 1-5, **1 = most serious** (default 3). It appears as `sev=N` in INCIDENT/* details, `Event.Severity`, `-sevN-` in the incident id/file name and a `# severity:` header line. Retention is per severity (`maxIncidentsBySeverity`, falling back to `maxIncidents`; legacy files without `-sevN-` count as 3), so floods of low-severity incidents never delete Sev1 files. Remote selectors can add `severityAtLeast: N` (matches Sev1..N; events without a severity never match it).
- **Incident holds**: `INCIDENT/triggered` carries `id=<file stem>`. Any component may publish `INCIDENT/hold id=X` / `INCIDENT/release id=X` to keep that capture open for late evidence (capped by `maxHoldSeconds`). The path tracer uses this so hops land in the incident file. Hold/release are never forwarded remotely.
- **Held events**: while `internal/power` holds the gate (startup, sleep/wake, shutdown until the network is ready), events from `bus.For` loggers carry `Held`. They are logged with `[held]` but don't trigger, aren't forwarded and are excluded from baseline stats. exmon/power lines use `bus.ForUnheld`. Any new consumer that alarms or forwards must skip `Held`; fake `event.Logger`s in tests need a `Held() bool` method. The service accepts pre-shutdown; the console treats SIGTERM (Windows shutdown/log-off) like Ctrl+C.
- **Bus rule**: a consumer must never call `Publish` from the goroutine that reads its own subscription (deadlock when its channel is full). Use `PublishAsync` there, or publish from another goroutine.
- **Remote**: `remotes` is a list (max 8); each remote has a `name`, a `type` (sender from the registry in `internal/remote/registry.go`), raw-JSON `settings` decoded strictly by that type, its own selectors, spool (`remote-spool\<name>`) and bus subscription (`remote:<name>`). List items don't inherit defaults from JSON, so `Config.ApplyDefaults` fills them; `Enabled` is `*bool` (nil = on). The old `remote` key is rejected in `loadFile`. Delivery is at-least-once; each `Record` has a random `id` for de-duplication and the computer name as `host`. `remote.Permanent(err)` drops a batch instead of retrying; `REMOTE/*` lines start with `remote=<name>` and are local-only. `--remote-simulate-offline` makes every remote's sends fail.

- **wlan**: `types.go` struct mirrors must match the SDK layouts exactly; `wlan_test.go` checks sizes/offsets. Variable-length C arrays use a `[1]` placeholder read with `unsafe.Slice`. The notification callback (`notify.go`, created once with `windows.NewCallback`) copies the payload and does a non-blocking send; drops are counted.
- **wifi**: POLL lines are written only on change (signature, RSSI ≥ threshold, rx/tx swing ≥ %, retry/FCS % move, handshake failure). Baselines and STATS deltas are relative to the last *logged* sample.
  - Retry%/FCS% only count when the interval had ≥ `minStatsFrames` frames (default 500), and are compared with the last *valid* logged value (`RefRetryPct`). `errorPctThreshold: 0` disables them.
  - If a driver reports retries > frames sent in one poll (impossible per 802.11: RetryCount counts frames, not retransmissions), that adapter is marked inconsistent: one warning, then its retry/FCS are ignored for change detection, triggers and metrics. The MediaTek MT7927 driver does this (retries jump in steps of 57, rx always 0). Stale handle (Win32 6/1062) or 3 failed polls in a row → close and reopen.

## Adding a source

1. Create `internal/<name>` with a `Config` struct, `DefaultConfig()`, and a type implementing `source.Source`.
2. Add a field to `config.Options`, its default in `config.Defaults`, and `--<name>-...` flags (plus an `overrides` entry) in `config.Load`. Give it a `Validate()` with ranges (`internal/limits`) called from `Options.Validate`; if it writes files, add the path to `Options.outputs()` for the service containment check.
3. Add one `if opts.X.Enabled { sources = append(...) }` line in `main.go`.
4. To make it triggerable: call `log.DeclareMetrics(...)` once, then `log.Metrics(subject, ...)` each sample. Triggers, the window, the remote queue and the baseline work with it automatically.

## Adding a consumer

Subscribe in `main.go` before sources start (`bus.Subscribe(name, busBuffer, event.Blocking)`), read until the channel closes, and append its `Wait` to `stops`.

## Conventions and gotchas

- Readability first. Wrap each raw `proc.Call(...)` in a small, named Go function with typed parameters that returns `(result, error)`. Callers never see `uintptr`/`unsafe.Pointer`.
- `exmon.example.json` is embedded into the exe (`//go:embed` in `main.go`) as the `--write-default-config` starter; keep it valid (a config test loads it). With `go run`, the exe lives in a go-build temp folder, so "next to the exe" only matters for a built/installed exe.
- Only `internal/wlan`, `internal/winnet` and `internal/winsys` may use `unsafe`/`uintptr`. Prefer a `golang.org/x/sys/windows` wrapper when one exists; hand-bind only what's missing (winnet binds `IcmpSendEcho2` and `RasEnumConnectionsW`; winsys binds `WTSQuerySessionInformationW`).
- Config trust: the service and an **elevated** console load with `config.LoadWith(args, service.Trust)` (admin-only file and folder); a non-elevated console uses no check (`consoleTrust` in `main.go`). The service also runs `opts.CheckOutputs(service.DataDir())`: outputs must be inside `%ProgramData%\exmon` or `outputRoots`, with no junctions/links (`internal/safepath`). Errors before the log file opens go to the Application event log (source `exmon`). Console echo is off.
- Every resource-costing number has a range, checked in its package's `Validate()` with `internal/limits` (`Range`, `Count`, `First`). When adding a setting, give it a range there and add it to `docs/configuration.md#allowed-ranges` and `internal/config/limits_test.go`.
- Win32 WLAN functions return a Win32 error code directly (0 = success). Use `check(fn, r)` in `wlan/errors.go`; ignore `proc.Call`'s `err` return. Not every API follows this: `IcmpSendEcho2` returns a reply count, and 0 means "see GetLastError" (which may be an IP_STATUS such as a timeout).
- `ras.h` packs its structs to 4 bytes (`RASCONNW` is 1388 bytes on x64); mirror an 8-byte handle as `[8]byte` to get that layout. Layout tests live next to each package.
- Memory Windows allocates must be freed with `freeMemory` (WlanFreeMemory) before returning; copy anything you keep.
- Native callbacks must never block or call `Log` directly; they go through a buffered channel.
- Constant values come from the Windows SDK headers `wlanapi.h` and `windot11.h`. The C# offsets for `WLAN_MSM_NOTIFICATION_DATA` (reason code) and `WLAN_PHY_RADIO_STATE` (sw/hw order) were wrong; the Go structs follow the SDK.
