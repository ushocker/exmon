# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

`exmon` is a Windows-only Go program that runs long-lived monitors ("sources") and writes their events to one rotating log file through an async queue. Sources: Wi-Fi (`wlanapi.dll`), network adapters, VPN detection and connectivity probes (iphlpapi/rasapi32), all through `golang.org/x/sys/windows`. There is no cgo.

It was ported from the C# WlanWatch in `reference/csharp/`; the NIC/VPN/probe detections come from the PowerShell script in `reference/Powershell/` (its Azure Log Analytics/credential parts were deliberately not ported). Leave `reference/` untouched. Console mode only for now; running as a Windows Service (`ServiceInstaller.cs`) is a later pass. The owner is new to Go, so explain Go-specific idioms when introducing them.

User and developer documentation lives in `docs/` (start at `docs/README.md`). When you add or change a feature, setting, log kind or metric, update the matching page: `docs/configuration.md`, `docs/monitors/*`, `docs/reference/metrics.md`, `docs/reference/log-format.md`.

## Commands

```bash
go build ./...      # build (must target windows; GOOS=windows if cross-compiling)
go vet ./...
go test ./...
go run . --wifi-poll-seconds 2 --wifi-debug-poll --log-path .\test.log   # run; Ctrl+C to stop
go run . --baseline --baseline-path .\baseline.jsonl   # learning mode: record all metrics + summary
go run . --remote-simulate-offline                     # test remote retries/redelivery without a network
go run . --probe-interval-seconds 30                   # faster probe rounds while testing (default 300)
go run . --write-default-config                        # write exmon.example.json (embedded in the exe) to %ProgramData%\exmon\config\exmon.json
go run . -h         # list flags
```

## Architecture

Sources publish to a fan-out bus; every consumer gets its own copy of each event.

```
 sources ──► event.Logger (bus.For(name)) ──► event.Bus ──┬─► eventlog.FileSink   text log (always on)
 (wifi, nic, vpn, probe)                                  ├─► incident.Engine     rolling window + triggers → incident-*.log.gz
                                                          ├─► probe.Tracer        path traces on interval + on INCIDENT/triggered
                                                          ├─► remote.Queue        on-disk spool → Sender (outbox stand-in; real target TODO)
                                                          ├─► eventlog.FileSink   baseline.jsonl (baseline mode only)
                                                          └─► baseline.Summarizer baseline-summary.txt (baseline mode only)
```

```
main.go               load config → subscribe consumers → run sources → Ctrl+C → engine.Flush → bus.Close → wait consumers (log file last)
internal/event        Event, Logger (Log / Metrics / DeclareMetrics), Bus (Subscribe, Publish, PublishAsync, Close)
internal/eventlog     FileSink: one writer goroutine owns the file (bufio, flush ticker, rotation, echo); text or jsonl
internal/window       rolling window of events (single owner, no mutex)
internal/incident     trigger Rules on METRICS events; captures window + post-trigger lines; gzip writer; INCIDENT/* events
internal/remote       Spool (segment files + cursor.json, fsync), Backoff, Queue (appender + sender goroutines), Sender
internal/baseline     baseline config + Summarizer (reservoir percentiles, suggested triggers)
internal/source       Source interface: Name(), Run(ctx, log) — blocks until ctx is cancelled
internal/config       Options composes each package's Config; defaults < config file < flags. Config file: --config, else exmon.json next to the exe, else %ProgramData%\exmon\config\exmon.json (first found wins; logged at startup)
internal/wlan         Win32 WLAN layer (unsafe allowed); returns plain Go values
internal/winnet       Win32 network layer (unsafe allowed): adapters, counters, routes, ICMP ping with TTL, uncached DNS, RAS, interface notifications, processes
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
- **Bus rule**: a consumer must never call `Publish` from the goroutine that reads its own subscription (deadlock when its channel is full). Use `PublishAsync` there, or publish from another goroutine.
- **Remote**: delivery is at-least-once; each `Record` has a random `id` for de-duplication and the computer name as `host`. Spool survives restarts; `REMOTE/*` events are local-only. `--remote-simulate-offline` makes sends fail for testing.

- **wlan**: `types.go` struct mirrors must match the SDK layouts exactly; `wlan_test.go` checks sizes/offsets. Variable-length C arrays use a `[1]` placeholder read with `unsafe.Slice`. The notification callback (`notify.go`, created once with `windows.NewCallback`) copies the payload and does a non-blocking send; drops are counted.
- **wifi**: POLL lines are written only on change (signature, RSSI ≥ threshold, rx/tx swing ≥ %, retry/FCS % move, handshake failure). Baselines and STATS deltas are relative to the last *logged* sample.
  - Retry%/FCS% only count when the interval had ≥ `minStatsFrames` frames (default 500), and are compared with the last *valid* logged value (`RefRetryPct`). `errorPctThreshold: 0` disables them.
  - If a driver reports retries > frames sent in one poll (impossible per 802.11: RetryCount counts frames, not retransmissions), that adapter is marked inconsistent: one warning, then its retry/FCS are ignored for change detection, triggers and metrics. The MediaTek MT7927 driver does this (retries jump in steps of 57, rx always 0). Stale handle (Win32 6/1062) or 3 failed polls in a row → close and reopen.

## Adding a source

1. Create `internal/<name>` with a `Config` struct, `DefaultConfig()`, and a type implementing `source.Source`.
2. Add a field to `config.Options`, its default in `config.Defaults`, and `--<name>-...` flags (plus an `overrides` entry) in `config.Load`.
3. Add one `if opts.X.Enabled { sources = append(...) }` line in `main.go`.
4. To make it triggerable: call `log.DeclareMetrics(...)` once, then `log.Metrics(subject, ...)` each sample. Triggers, the window, the remote queue and the baseline work with it automatically.

## Adding a consumer

Subscribe in `main.go` before sources start (`bus.Subscribe(name, busBuffer, event.Blocking)`), read until the channel closes, and append its `Wait` to `stops`.

## Conventions and gotchas

- Readability first. Wrap each raw `proc.Call(...)` in a small, named Go function with typed parameters that returns `(result, error)`. Callers never see `uintptr`/`unsafe.Pointer`.
- `exmon.example.json` is embedded into the exe (`//go:embed` in `main.go`) as the `--write-default-config` starter; keep it valid (a config test loads it). With `go run`, the exe lives in a go-build temp folder, so "next to the exe" only matters for a built/installed exe.
- Only `internal/wlan` and `internal/winnet` may use `unsafe`/`uintptr`. Prefer a `golang.org/x/sys/windows` wrapper when one exists; hand-bind only what's missing (winnet binds `IcmpSendEcho2` and `RasEnumConnectionsW`).
- Win32 WLAN functions return a Win32 error code directly (0 = success). Use `check(fn, r)` in `wlan/errors.go`; ignore `proc.Call`'s `err` return. Not every API follows this: `IcmpSendEcho2` returns a reply count, and 0 means "see GetLastError" (which may be an IP_STATUS such as a timeout).
- `ras.h` packs its structs to 4 bytes (`RASCONNW` is 1388 bytes on x64); mirror an 8-byte handle as `[8]byte` to get that layout. Layout tests live next to each package.
- Memory Windows allocates must be freed with `freeMemory` (WlanFreeMemory) before returning; copy anything you keep.
- Native callbacks must never block or call `Log` directly; they go through a buffered channel.
- Constant values come from the Windows SDK headers `wlanapi.h` and `windot11.h`. The C# offsets for `WLAN_MSM_NOTIFICATION_DATA` (reason code) and `WLAN_PHY_RADIO_STATE` (sw/hw order) were wrong; the Go structs follow the SDK.
