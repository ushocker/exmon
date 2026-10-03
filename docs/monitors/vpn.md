# VPN detection

The `vpn` monitor checks every `vpn.pollSeconds` (default 30) for VPN tunnels in three ways:

1. **Built-in Windows VPN** (Settings → Network → VPN). Active connections are read from Windows Remote Access (RAS). The server address comes from the connection's phonebook file (`rasphone.pbk`), and exmon pings the server each check to measure latency.
2. **Third-party VPN clients.** Adapters whose name or description contains one of `vpn.adapterPatterns`, such as GlobalProtect (`PANGP`), Cisco AnyConnect, Zscaler, Fortinet, WireGuard or Tailscale.
3. **VPN client processes.** Running processes named in `vpn.processNames`, for example `PanGPA` or `tailscaled`. These are reported in the status line. A running client with no tunnel up usually means the VPN is installed but disconnected.

For each tunnel that is up, exmon reports its IP address and whether it is a **full tunnel** or a **split tunnel**. A full tunnel owns a default route (`0.0.0.0/0`), so all traffic goes through the VPN. In a split tunnel, only some networks go through the VPN and the rest goes direct.

## Lines

| Kind | When | Example |
|---|---|---|
| `VPN/status` | First check, then every `heartbeatSeconds` (default 60) | `connected=yes tunnels=1 up=1 clients=tailscaled [Tailscale] adapter "Tailscale Tunnel" up ip=100.88.214.97 tunnel=split` |
| `VPN/connected` | A tunnel came up | `[Work VPN] builtin "WAN Miniport (IKEv2)" up ip=172.16.0.9 tunnel=full server=vpn.example.com` |
| `VPN/disconnected` | A tunnel went down or disappeared | `[Ethernet 3] adapter "PANGP Virtual Ethernet Adapter" down` |
| `VPN/changed` | Full/split, tunnel IP or server changed | `[Work VPN] tunnel split->full` |

`builtin` marks a Windows VPN connection; `adapter` marks one found by adapter name.

## Metrics

Per tunnel (subject = tunnel name). See [Metrics](../reference/metrics.md#vpn):

- `vpn_up`: 1 or 0. A trigger like `vpn_up < 1` catches a VPN that drops.
- `vpn_server_rtt_ms`: built-in VPNs only, when the server answers pings.
- `in_errors`, `out_errors`: growth of the tunnel adapter's error counters.

## Adding your VPN client

If your organisation's client isn't detected, add a fragment of its adapter description to `adapterPatterns` and its process name to `processNames`. To find them:

```powershell
Get-NetAdapter | Select-Object Name, InterfaceDescription
Get-Process | Select-Object ProcessName
```
