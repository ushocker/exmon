# exmon documentation

`exmon` is a Windows network monitor. It runs continuously, watches Wi-Fi, network adapters, VPN tunnels and connectivity to the services you care about, and writes everything to one rotating log file. When something crosses a threshold you set, it saves a compressed **incident** file containing the minutes before and after the problem, and it can forward selected events to a remote target through a queue that survives reboots.

It is a single Windows executable written in Go, with no runtime dependencies and no installer.

## What it does

| Area | What you get | Page |
|---|---|---|
| Wi-Fi | Connection, signal (RSSI), channel, PHY, rates, security, roaming and disconnect events from the Windows WLAN service, frame statistics | [Wi-Fi monitor](monitors/wifi.md) |
| Network adapters | Adapters going up/down, IP/gateway/DNS/speed changes, throughput, errors and discards | [Network adapters](monitors/nic.md) |
| VPN | Built-in Windows VPNs, third-party VPN adapters and client processes, full vs split tunnel | [VPN detection](monitors/vpn.md) |
| Connectivity probes | DNS lookup time, gateway ping, TCP + ping to endpoints, timed HTTP requests | [Connectivity probes](monitors/probes.md) |
| Path trace | Traceroute with per-hop latency and loss, on a timer and whenever an incident fires | [Path trace](monitors/path-trace.md) |
| Incidents | Threshold triggers with severities; each incident saved as a `.log.gz` holding the rolling window plus what happened after | [Incidents](incidents.md) |
| Remote queue | Selected events forwarded to a remote target through an on-disk spool with retries and backoff | [Remote queue](remote.md) |
| Baseline mode | Records every measurement and suggests trigger thresholds from what is normal on this machine | [Baseline mode](baseline.md) |

## Where to start

1. [Getting started](getting-started.md): build, run, create a config, and read the log.
2. [Configuration](configuration.md): every setting and command-line flag.
3. [Incidents](incidents.md): turn on alerting for the problems you care about.

## Reference

- [Log format and event kinds](reference/log-format.md): what each log line means.
- [Metrics](reference/metrics.md): every metric that triggers and the baseline can use.
- [Files exmon writes](reference/files.md): where everything goes on disk.
- [Troubleshooting](troubleshooting.md): known driver quirks and common questions.

## For developers

- [Architecture](architecture.md): how the pieces fit together, and how to add a new monitor or consumer.
- `CLAUDE.md` in the repository root has the coding conventions.
- `reference/` holds the original C# (WlanWatch) and PowerShell (AzureNetMonitor) implementations this was ported from. It is kept for reference and is not built.
