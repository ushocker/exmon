# exmon documentation

`exmon` is a Windows network monitor. It runs continuously, watches Wi-Fi, network adapters, VPN tunnels, the Windows services you list and connectivity to the services you care about, and writes everything to one rotating log file. When something crosses a threshold you set, it saves a compressed **incident** file containing the minutes before and after the problem, and it can forward selected events to a remote target through a queue that survives reboots.

It is a single Windows executable written in Go, with no runtime dependencies. Run it in a console, or install it as a Windows Service with `exmon.exe install`.

## What it does

| Area | What you get | Page |
|---|---|---|
| Wi-Fi | Connection, signal (RSSI), channel, PHY, rates, security, roaming and disconnect events from the Windows WLAN service, frame statistics | [Wi-Fi monitor](monitors/wifi.md) |
| Network adapters | Adapters going up/down, IP/gateway/DNS/speed changes, throughput, errors and discards | [Network adapters](monitors/nic.md) |
| VPN | Built-in Windows VPNs, third-party VPN adapters and client processes, full vs split tunnel | [VPN detection](monitors/vpn.md) |
| Windows services | A list of services: running or not, start type, and the version of the exe (or svchost DLL) each runs; down/up and version changes | [Windows services](monitors/services.md) |
| Connectivity probes | DNS lookup time, gateway ping, TCP + ping to endpoints, timed HTTP requests | [Connectivity probes](monitors/probes.md) |
| Path trace | Traceroute with per-hop latency and loss, on a timer and whenever an incident fires | [Path trace](monitors/path-trace.md) |
| Incidents | Threshold triggers with severities; each incident saved as a `.log.gz` holding the rolling window plus what happened after | [Incidents](incidents.md) |
| Remote queue | Selected events forwarded to one or more remotes (a local file or Azure Monitor, each with its own selectors and on-disk spool) with retries and backoff | [Remote queue](remote.md) |
| Baseline mode | Records every measurement and suggests trigger thresholds from what is normal on this machine | [Baseline mode](baseline.md) |
| Startup, sleep and shutdown | Holds events while starting, waking or shutting down until the network passes a connection test, so reconnecting isn't reported as a problem | [Startup, sleep and shutdown](power.md) |
| Windows Service | Runs at startup as a low-privilege service with locked-down folders; install, upgrade and uninstall from the exe | [Running as a Windows Service](service.md) |

## Where to start

1. [Getting started](getting-started.md): build, run, create a config, and read the log.
2. [Running as a Windows Service](service.md): install, upgrade, uninstall, event log, permissions.
3. [Configuration](configuration.md): every setting and command-line flag.
4. [Incidents](incidents.md): turn on alerting for the problems you care about.

## Reference

- [Log format and event kinds](reference/log-format.md): what each log line means.
- [Metrics](reference/metrics.md): every metric that triggers and the baseline can use.
- [Files exmon writes](reference/files.md): where everything goes on disk.
- [Sending events to Azure Monitor](azure-monitor.md): the `azureMonitor` remote, the Azure resources it needs and the write-only app.
- [Data usage](data-usage.md): how much the remote queue would send per machine and per fleet (1,000 / 3,000 / 5,000 machines), with and without incidents.
- [Troubleshooting](troubleshooting.md): known driver quirks and common questions.

## Design proposals (not implemented)

- [Control plane](design/control-plane.md): cloud-delivered config, remote updates over MQTT, direct-to-SIEM events. Records the decisions made so far.
- [Config security review](design/config-security-review.md): how config values could be misused in the current setup (local config, console or service), and the proposed fixes.
- [Cloud config security](design/cloud-config-security.md): extra risks and mitigations if the config were ever delivered from the cloud. Not applicable today.

## For developers

- [Architecture](architecture.md): how the pieces fit together, and how to add a new monitor or consumer.
- `CLAUDE.md` in the repository root has the coding conventions.
