# Architecture

This page is for anyone changing exmon's code. The code is plain Go with no cgo; every Windows call goes through `golang.org/x/sys/windows`.

## The big picture

**Monitors** (called *sources* in the code) don't write to the log directly. They publish **events** to a **bus**, which hands a copy of every event to each **consumer**:

```
 sources                      event.Bus                    consumers
 ───────                      ─────────                    ─────────
 wifi   ─┐                              ┌─► eventlog.FileSink    text log (always on)
 nic    ─┤   event.Logger               ├─► incident.Engine      rolling window + triggers → incident-*.log.gz
 vpn    ─┼── (bus.For(name)) ──► Bus ───┼─► probe.Tracer         path traces (interval + on incident)
 probe  ─┘                              ├─► remote.Queue         on-disk spool → Sender
                                        ├─► eventlog.FileSink    baseline.jsonl        (baseline mode)
                                        └─► baseline.Summarizer  baseline-summary.txt  (baseline mode)
```

Adding a monitor never touches the consumers, and adding a consumer never touches the monitors.

## Events

`internal/event`:

```go
type Event struct {
    Time     time.Time
    Source   string             // "wifi", "nic", "incident", ...
    Kind     string             // "POLL", "NIC/up", "METRICS", ...
    Subject  string             // which adapter / probe / tunnel
    Detail   string
    Metrics  map[string]float64 // only for Kind == "METRICS"
    Severity int                // incident events only; 1 = most serious
}
```

A source gets an `event.Logger` from `bus.For("<source>")`, which has three methods:

- `Log(kind, detail)`: a log line.
- `Metrics(subject, map)`: a `METRICS` event. It is never written to the text log; triggers and the baseline use it.
- `DeclareMetrics(map[name]Direction)`: says which way is bad for each metric (`LowIsBad` / `HighIsBad`), for baseline suggestions.

The bus stamps the time when the event is published, not when it is written to disk.

## The bus

`Subscribe(name, buffer, mode)` returns a channel. `Blocking` subscribers never lose events: `Publish` waits for room. `DropWhenFull` subscribers count drops instead. Every built-in consumer uses `Blocking`.

**The one rule:** a consumer must never call `Publish` from the goroutine that reads its own subscription. If its channel is full, `Publish` waits for that goroutine to read, which it never will, so the program deadlocks. Use `bus.PublishAsync`, or publish from a separate worker goroutine. The incident engine, tracer and remote queue all follow this.

## Shutdown order

`main.go`:

1. Ctrl+C (console) or a service Stop/Shutdown request cancels a `context.Context`; each source's `Run` returns.
2. `engine.Flush()` saves incidents still recording, while the bus can still deliver their `INCIDENT/saved` events.
3. `bus.Close()` closes every subscription channel.
4. Each consumer drains its channel and stops. The log file goes last, so the final lines are written.

## Packages

| Package | Role |
|---|---|
| `main.go` | Chooses the mode: `install`/`uninstall`/`status` subcommands, service (started by the SCM) or console. Loads the config with the right trust check (`consoleTrust`: admin-only files when elevated or a service) and, for the service, runs `CheckOutputs`. Then `run(ctx, …)` wires consumers and sources and handles shutdown. Embeds `exmon.example.json` for `--write-default-config`. |
| `internal/service` | Install/uninstall/status through the Service Control Manager, folder permissions, the config trust check, and the `svc.Handler` that reports start/running/stopping to Windows and writes to the Application event log. |
| `internal/winsys` | Windows security and session wrapper: set folder permissions, check that a path is admin-only, find the console user, check elevation. A third `unsafe` package. |
| `internal/config` | `Options` composes each package's `Config`. Load order: defaults, then config file, then flags. Finds the config file (`LoadWith` takes a trust check), validates everything, and `CheckOutputs` keeps the service's outputs inside allowed folders. |
| `internal/limits` | `Range`, `Count` and `First`: the upper/lower bounds every section's `Validate()` uses. |
| `internal/safepath` | `Within` (is a path inside a folder) and `CheckNoLinks` (no junctions or symbolic links below a folder). |
| `internal/event` | `Event`, `Logger`, `Bus`. |
| `internal/eventlog` | `FileSink`: one goroutine owns the file (buffered writes, periodic flush, rotation, console echo). Text or JSONL. |
| `internal/window` | The rolling window (single owner, no locking). |
| `internal/incident` | Trigger rules, captures, hold/release, gzip writer, per-severity retention. |
| `internal/remote` | `Spool` (segment files + cursor), `Backoff`, `Queue` (appender + sender goroutines), `Sender` interface and the stand-in `OutboxSender`. |
| `internal/baseline` | Baseline config and the `Summarizer` (reservoir-sampled percentiles, suggestions). |
| `internal/source` | The `Source` interface: `Name()`, `Run(ctx, log)`. |
| `internal/wifi` | Wi-Fi source: notifications and polling in one `select` loop; change detection; frame stats; reconnect with backoff. |
| `internal/nic` | Adapter source: change notifications + counter polling. |
| `internal/vpn` | VPN source: RAS, adapter patterns, processes, routes. |
| `internal/probe` | Probe source (DNS, gateway, endpoints, HTTP) and `Tracer` (path trace consumer). |
| `internal/device` | Machine description for the DEVICE line, incident headers and remote `host`. |
| `internal/wlan` | Windows WLAN API wrapper. One of only two packages allowed to use `unsafe`. |
| `internal/winnet` | Windows networking wrapper: adapters, counters, routes, ICMP ping with TTL, uncached DNS, RAS, interface-change notifications, processes. Also allowed `unsafe`. |

## Windows API layers

`internal/wlan` and `internal/winnet` are the only places that touch raw pointers. Everything they return is a plain Go value, and memory Windows allocates is freed before a function returns.

- Prefer an existing `x/sys/windows` function. Hand-bind only what's missing: `wlan` binds the WLAN API, and `winnet` binds `IcmpSendEcho2` and `RasEnumConnectionsW`.
- Every raw call sits behind a small, named, typed Go function.
- Go structs that mirror Windows structs must match the SDK layout exactly. Tests check sizes and offsets (`wlan_test.go`, `winnet_test.go`). Watch for headers that pack structs (`ras.h` packs to 4 bytes).
- Windows callbacks (Wi-Fi notifications, interface changes) are created once with `windows.NewCallback`. They copy what they need and do a non-blocking channel send; they never block or log directly.

## Adding a monitor

1. Create `internal/<name>` with a `Config` struct, `DefaultConfig()`, and a type with `Name()` and `Run(ctx, log event.Logger) error`. `Run` must return when `ctx` is cancelled.
2. Add the config to `config.Options` and `config.Defaults`. Add flags and an `overrides` entry in `config.Load`. Give the section a `Validate()` that puts **every resource-costing number in a range** (`internal/limits`), call it from `Options.Validate`, and add the ranges to `docs/configuration.md#allowed-ranges` and `internal/config/limits_test.go`. If it writes files, add its path to `Options.outputs()` so the service's containment check covers it.
3. Add one `if opts.X.Enabled { sources = append(...) }` line in `main.go`.
4. Log lines with `log.Log`. To make it triggerable, call `log.DeclareMetrics` once and then `log.Metrics(subject, …)` on each sample. Triggers, the window, incidents, the remote queue and baseline mode then work with it automatically.
5. Document it in `docs/monitors/`, `docs/reference/metrics.md` and `docs/reference/log-format.md`, and add an example to `exmon.example.json`.

## Adding a consumer

Subscribe in `main.go` before the sources start (`bus.Subscribe(name, busBuffer, event.Blocking)`), read until the channel closes, and append its `Wait` function to `stops` so shutdown waits for it. Follow the bus rule above if it publishes.

## Adding evidence to incidents

Publish `INCIDENT/hold id=<incident id>` (the id is in the `INCIDENT/triggered` detail), log your evidence, then publish `INCIDENT/release id=<id>`. The incident stays open until released, or until `maxHoldSeconds`. `probe.Tracer` is the worked example.

## Tests

Run `go test ./...`. Tests that touch Windows APIs run against the real machine (loopback ping, adapter list, routes); network-dependent tests skip themselves when there is no route.
