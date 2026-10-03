# Path trace

A path trace is a traceroute. It lists each router between this machine and a target, with latency and packet loss per hop. That shows *where* a problem is: your Wi-Fi, the office router, the ISP, or the far end.

## When traces run

- **On a timer:** every `probe.pathTrace.intervalMinutes` (default 30; 0 turns it off), plus once at startup, giving you a baseline path.
- **When an incident triggers** (`probe.pathTrace.onIncident`, default true). The incident is held open until the trace finishes, so the hops end up **inside the incident file**. See [Incidents](../incidents.md#path-traces-inside-incidents).

If an incident fires while a trace is already running, it uses that trace instead of starting another.

## What is traced

The `probe.endpoints`, or the `probe.dnsNames` if no endpoints are configured. All targets are traced at the same time.

## How it works

For each target, exmon sends pings with TTL 1, 2, 3 … up to `maxHops` (default 30, at most 64) **all at once**, three per hop, with a one-second timeout. Each router whose TTL runs out answers, which identifies the hop. The trace stops at the first hop where the target itself answers. Answering hops get a reverse-DNS name when one exists.

Because every hop is probed in parallel, a full trace usually takes 1–3 seconds. A classic `tracert` takes 30–60 seconds per target. No administrator rights are needed.

## Lines

```
PATHTRACE/hop   [M365Portal]  1 10.0.0.1 min/avg/max=1.0ms/2.0ms/3.0ms loss=0%
PATHTRACE/hop   [M365Portal]  3 162.238.116.1 (162-238-116-1.lightspeed.stlsmo.sbcglobal.net) min/avg/max=4.0ms/4.0ms/4.0ms loss=0%
PATHTRACE/hop   [M365Portal] 10 51.10.37.2 (be26.rwa02.chi10.ntwk.msn.net) min/avg/max=19.0ms/19.0ms/19.0ms loss=67%
PATHTRACE/hop   [M365Portal] 11 * loss=100%
PATHTRACE/hop   [M365Portal] 19 150.171.74.13 min/avg/max=11.0ms/12.0ms/14.0ms loss=0%
PATHTRACE/done  [M365Portal] target=150.171.74.13 hops=19 reached=true (incident id=incident-20261003-004533.912-sev3-gw-check)
```

`*` means that hop did not answer. Many backbone routers (Microsoft's, for example) never answer pings, so a run of `*` hops followed by a reached target is normal. Loss **at a hop** only matters if it continues to the hops after it. A router that is slow to answer pings can show loss while forwarding traffic perfectly well.

The text in brackets at the end says why the trace ran: `interval`, or `incident id=…`.

## Limits

IPv4 only. Ping times have 1 ms resolution (a Windows limit), so a fast LAN hop shows `0.0ms`.
