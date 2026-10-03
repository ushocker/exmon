# Getting started

## Requirements

- Windows 10 or 11 (64-bit).
- Go 1.27 or later to build it. The finished `exmon.exe` needs nothing else.
- No administrator rights are needed for monitoring, pings or path traces. Writing to `%ProgramData%` needs rights to that folder; the defaults put everything under `%ProgramData%\exmon`.

## Build

```bash
go build -o exmon.exe .
```

Other useful commands:

```bash
go vet ./...
go test ./...
```

## Run

```bash
exmon.exe
```

exmon runs in the console until you press **Ctrl+C**. On Ctrl+C it stops the monitors, saves any incident that is still recording, flushes the log and exits. (Running as a Windows Service is planned but not built yet.)

Every log line is also echoed to the console, so you can watch it work.

While developing you can use `go run .` instead of building. One catch: `go run` builds the exe in a temporary folder, so an `exmon.json` placed next to it is never found. Pass `--config .\exmon.json` instead.

## Create a config file

exmon runs fine with no config file at all (it uses built-in defaults), but you will want one to set probe targets and triggers. Write a starter file:

```bash
exmon.exe --write-default-config
```

This writes a copy of `exmon.example.json` (built into the exe) to `%ProgramData%\exmon\config\exmon.json`. It never overwrites an existing file. To write it somewhere else:

```bash
exmon.exe --write-default-config --config D:\monitor\exmon.json
```

**Edit it before use.** The starter config includes example probe targets from the original script, such as `your-app.azurefd.net` and `yourtenant.sharepoint.com`. Left as they are, they will report `PROBE/down` and fire the `endpoint-down` trigger. Replace them with your real endpoints and URLs, or delete them.

### Where exmon looks for its config

When you do not pass `--config`, exmon uses the first file that exists:

1. `exmon.json` in the same folder as `exmon.exe` (handy for a portable copy).
2. `%ProgramData%\exmon\config\exmon.json` (machine-wide).

If neither exists it runs on built-in defaults. The second line of the log always says which file it loaded, or where it looked:

```
MONITOR    config: C:\ProgramData\exmon\config\exmon.json
```

If you pass `--config <path>`, that file must exist.

Command-line flags override the config file, which overrides the built-in defaults. See [Configuration](configuration.md).

## Read the log

The default log is `%ProgramData%\exmon\exmon.log`. Each line looks like this:

```
2026-10-03 00:45:33.912  PROBE/http                   [M365] 200 dns=0.5ms connect=51.0ms tls=110.6ms ttfb=632.7ms total=636.8ms bytes=14247
```

That is the time, the event kind, and the detail. A typical start-up:

```
MONITOR       exmon started, logging to C:\ProgramData\exmon\exmon.log (Ctrl+C to stop)
MONITOR       config: C:\ProgramData\exmon\config\exmon.json
DEVICE        computer="PC01" user="jsmith" domain="corp.example.com" model="Latitude 7440" os="Windows 11 Enterprise 24H2 (build 26100)"
NIC/status    [Wi-Fi] Intel(R) Wi-Fi 7 BE200 type=wifi speed=1201Mbps ip=10.0.0.32 gw=10.0.0.1 dns=10.0.0.53 mac=...
VPN/status    connected=no tunnels=0 up=0 clients=none
MONITOR       wifi: started
POLL          [Intel(R) Wi-Fi 7 BE200] ssid="Corp" bssid=7A:45:58:94:F7:93 rssi=-52dBm ch=153 (5GHz) phy=802.11ax auth=WPA3-Personal cipher=CCMP ...
PROBE/dns     [www.microsoft.com] 19.5ms -> 23.54.42.39
PROBE/gateway [10.0.0.1 via Wi-Fi] ping min/avg/max=0.0ms/0.2ms/1.0ms loss=0%
```

Most monitors only write when something **changes**, plus a periodic status ("heartbeat") line, so a quiet log means a healthy network, not a stalled program. See [Log format and event kinds](reference/log-format.md) for every line type.

## Next steps

- Add probe targets for the services your users depend on: [Connectivity probes](monitors/probes.md).
- Run [baseline mode](baseline.md) for a day to learn what normal looks like.
- Add triggers so problems are captured automatically: [Incidents](incidents.md).
