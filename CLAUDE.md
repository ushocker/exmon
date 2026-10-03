# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

`exmon` is a Windows-only Go program that runs long-lived monitors ("sources") and writes their events to one rotating log file through an async queue. Wi-Fi is the first source. It calls the native WLAN API (`wlanapi.dll`) through `golang.org/x/sys/windows`. There is no cgo.

It was ported from the C# WlanWatch in `reference/csharp/` (leave that folder untouched). Console mode only for now; running as a Windows Service (`ServiceInstaller.cs`) is a later pass. The owner is new to Go, so explain Go-specific idioms when introducing them.

## Commands

```bash
go build ./...      # build (must target windows; GOOS=windows if cross-compiling)
go vet ./...
go test ./...
go run . --wifi-poll-seconds 2 --wifi-debug-poll --log-path .\test.log   # run; Ctrl+C to stop
go run . --baseline --baseline-path .\baseline.jsonl   # learning mode: record all metrics + summary
go run . --remote-simulate-offline                     # test remote retries/redelivery without a network
go run . -h         # list flags
```

## Architecture

Sources publish to a fan-out bus; every consumer gets its own copy of each event.

```
 sources ──► event.Logger (bus.For(name)) ──► event.Bus ──┬─► eventlog.FileSink   text log (always on)
                                                          ├─► incident.Engine     rolling window + triggers → incident-*.log.gz
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
internal/config       Options composes each package's Config; defaults < exmon.json (next to exe, or --config) < flags
internal/wlan         Win32 layer — the ONLY package using unsafe/uintptr; returns plain Go values
internal/wifi         Wi-Fi Source: notifications + poll ticker in one select loop; start/restart with backoff
```

- **Lines vs metrics**: `Log(kind, detail)` is a log line. `Metrics(subject, map)` is a `METRICS` event that never goes to the text log; triggers and the baseline use it. Sources declare a `Direction` (LowIsBad/HighIsBad) per metric so the baseline can suggest thresholds.
- **Wi-Fi metrics** (every poll, per connected adapter): `rssi_dbm`, `signal_quality`, `rx_kbps`, `tx_kbps`, and per-poll deltas `retry_pct`, `fcs_pct`, `handshake_fail`.
- **Bus rule**: a consumer must never call `Publish` from the goroutine that reads its own subscription (deadlock when its channel is full). Use `PublishAsync` there, or publish from another goroutine.
- **Remote**: delivery is at-least-once; each `Record` has a random `id` for de-duplication. Spool survives restarts; `REMOTE/*` events are local-only. `--remote-simulate-offline` makes sends fail for testing.

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
- Win32 WLAN functions return a Win32 error code directly (0 = success). Use `check(fn, r)` in `wlan/errors.go`; ignore `proc.Call`'s `err` return.
- Memory Windows allocates must be freed with `freeMemory` (WlanFreeMemory) before returning; copy anything you keep.
- Native callbacks must never block or call `Log` directly; they go through a buffered channel.
- Constant values come from the Windows SDK headers `wlanapi.h` and `windot11.h`. The C# offsets for `WLAN_MSM_NOTIFICATION_DATA` (reason code) and `WLAN_PHY_RADIO_STATE` (sw/hw order) were wrong; the Go structs follow the SDK.
