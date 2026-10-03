# Network adapters

The `nic` monitor watches the machine's network adapters: wired, Wi-Fi and others.

- **Up/down and address changes are reported immediately.** Windows notifies exmon whenever an adapter changes, and after a half-second settle time exmon re-reads the adapter list.
- **Traffic and error counters are read every `nic.pollSeconds`** (default 15).

## What is watched

By default exmon skips:

- loopback and tunnel-type interfaces;
- adapters whose name or description contains any of `virtualPatterns` (Hyper-V, VMware, Bluetooth, VPN clients, Tailscale …). VPN adapters are covered by [VPN detection](vpn.md) instead.

Set `includeVirtual: true` to watch everything.

An adapter that is down and has never been up since exmon started is not reported. That keeps unplugged ports from cluttering the log.

## Lines

| Kind | When | Example |
|---|---|---|
| `NIC/status` | At startup for each adapter that is up, then every `heartbeatSeconds` (default 60) with traffic since the previous status | `[Ethernet] Intel(R) Ethernet ... type=ethernet speed=1000Mbps ip=10.0.0.2 gw=10.0.0.1 dns=10.0.0.53 mac=98:B7:... rx=24.8kbps tx=5.4kbps in_errors=0 out_errors=0 in_discards=0 out_discards=0 (last 60s)` |
| `NIC/up` | An adapter came up (or a new one appeared) | `[Ethernet] Intel(R) Ethernet ... speed=1000Mbps ip=...` |
| `NIC/down` | An adapter went down or was removed | `[Ethernet] Intel(R) Ethernet ...` / `[USB LAN] removed` |
| `NIC/changed` | Link speed, IPv4 address, gateway or DNS servers changed | `[Wi-Fi] speed 1201->573Mbps ip 10.0.0.32->10.0.0.40` |
| `NIC/errors` | Error or discard counters grew since the last poll | `[Ethernet] in_errors=12 out_errors=0 in_discards=3 out_discards=0 (last 15s)` |

A link speed drop on wired Ethernet (e.g. 1000 → 100 Mbps) often points to a bad cable or port. On Wi-Fi it follows the signal.

## Metrics

Per adapter (subject = adapter name), every poll. See [Metrics](../reference/metrics.md#network-adapters):

`link_speed_mbps`, `rx_bps`, `tx_bps`, `in_errors`, `out_errors`, `in_discards`, `out_discards`.

Counter metrics are the growth since the previous poll. If an adapter resets and its counters drop, the new value is used as the growth.
