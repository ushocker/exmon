# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

`exmon` is a Windows-only Go program that monitors Wi-Fi state by calling the native Windows WLAN API (`wlanapi.dll`) directly through `golang.org/x/sys/windows`. There is no cgo; all Win32 calls go through lazily loaded `windows.LazyProc`s.

**Status:** early prototype pieced together from examples, some of them in other languages. The current structure and call order are exploratory, not deliberate design, so restructure freely. The goal is a long-running monitor that logs Wi-Fi events to a file from an asynchronous loop. The owner is new to Go, so explain Go-specific idioms when introducing them.

## Commands

```bash
go build ./...      # build (must target windows; GOOS=windows if cross-compiling)
go run .            # run the monitor (Ctrl+C to stop the event listener)
go vet ./...
go test ./...       # no tests exist yet
```

## Architecture

- `main.go` only calls `wlanapi.Query()`.
- `wlanapi/wlanapi.go` holds everything else:
  - **DLL bindings**: the `procWlan*` vars at the top. Some are declared but not used yet (`WlanReasonCodeToString`, `WlanGetNetworkBssList`).
  - **Struct mirrors**: Go structs such as `wlanInterfaceInfo`, `wlanConnectionAttributes` and `wlanNotificationData` copy the exact memory layout of the matching Win32 structs (named in a comment on each one). Field order, sizes and fixed-length arrays (`[256]uint16` WCHAR buffers, `[1]` placeholders for variable-length C arrays read with `unsafe.Slice`) must match the C definitions, or reads will be wrong.
  - **Notification pipeline**: `WlanRegisterNotification` calls `notificationCallback` (made with `windows.NewCallback`) on a thread owned by the WLAN service. The callback must never block. It copies what it needs into a `WifiEvent` and does a non-blocking send to the buffered `events` channel. If the channel is full, the event is counted in `dropped`. `eventsListener` drains the channel and prints events until it receives SIGINT. `describe` maps (source, code) pairs to text; events with no mapping are silently ignored.
  - **Current-connection query**: `WlanEnumInterfaces` followed by `WlanQueryInterface(wlan_intf_opcode_current_connection)` prints SSID, BSSID, signal and Rx/Tx rates.

## Conventions and gotchas

- Readability comes first. Wrap each raw `proc.Call(...)` in a small, named Go function with typed parameters that returns `(result, error)`. Callers should never see `uintptr`/`unsafe.Pointer` argument lists.

- Win32 WLAN functions return a Win32 error code directly (0 means success). Check the first return value of `proc.Call`, wrap it as `windows.Errno(r)`, and ignore the `err` return.
- Memory that Windows allocates (interface lists, connection attributes) must be released with `WlanFreeMemory`. The session handle is closed with `WlanCloseHandle`.
- `Query()` runs `eventsListener` first, and that call blocks until Ctrl+C. The interface/connection dump runs only after the listener stops.
- Constant values (opcodes, notification sources, ACM/MSM codes) come from the Windows SDK headers `wlanapi.h` and `windot11.h`. Add new codes there, and add a case to `describe` so they get printed.
