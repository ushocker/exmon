# Connectivity probes

The `probe` monitor actively tests the network every `probe.intervalSeconds` (default 300, i.e. 5 minutes). Each round runs all probes at the same time, each with its own timeout (`timeoutSeconds`, default 10), so one slow target never delays the others. The first round runs at startup.

Every probe writes a line each round. These lines double as the status output, so you always have the latest numbers.

## DNS

Each name in `dnsNames` (default `www.microsoft.com`) is resolved with a real query to the configured DNS servers. It **bypasses the Windows DNS cache**, so the time is the network's time, not a cached answer.

```
PROBE/dns  [www.microsoft.com] 19.5ms -> 23.54.42.39
PROBE/dns  [intranet.corp] FAILED after 2003.1ms: DnsQuery failed: ...
```

Metric (subject `dns:<name>`): `dns_ms`.

## Default gateway

exmon finds the route Windows would use for internet traffic and pings its gateway `pingCount` times (default 4).

```
PROBE/gateway  [10.0.0.1 via Wi-Fi] ping min/avg/max=1.0ms/1.5ms/3.0ms loss=0%
```

If the default route has no gateway (common with a full-tunnel VPN), the line says so and nothing is pinged.

Metrics (subject `gateway`): `gateway_rtt_ms`, `gateway_loss_pct`.

## Endpoints

Each entry in `endpoints` is tested with a **TCP connect** to its port (proving the service is reachable, firewalls included) and **ICMP pings** (latency and loss):

```json
"endpoints": [
  { "name": "SQLServer",  "target": "your-sql.database.windows.net", "port": 1433 },
  { "name": "M365Portal", "target": "portal.office.com",             "port": 443 }
]
```

```
PROBE/endpoint  [M365Portal] portal.office.com:443 (150.171.74.13) tcp 25.0ms ping min/avg/max=10.0ms/11.0ms/12.0ms loss=0%
```

Many cloud services don't answer ping. A 100% ping loss with a successful TCP connect is normal for them. Use `reachable` and `tcp_connect_ms` for those.

Metrics (subject = endpoint name): `tcp_connect_ms`, `rtt_avg_ms`, `loss_pct`, `reachable` (1 when the TCP connect succeeded).

## HTTP tests

Each entry in `httpTests` is fetched with a GET on a **fresh connection**, and every phase is timed:

```json
"httpTests": [
  { "name": "M365", "url": "https://outlook.office365.com" },
  { "name": "ERP",  "url": "https://your-erp.azurewebsites.net/health" }
]
```

```
PROBE/http  [M365] 200 dns=0.5ms connect=51.0ms tls=110.6ms ttfb=632.7ms total=636.8ms bytes=14247 redirected=https://outlook.office365.com/mail/ server="Microsoft-HTTPAPI/2.0"
```

| Field | Meaning |
|---|---|
| `dns` | Name lookup. This one may be served from the Windows cache. |
| `connect` | TCP connection. |
| `tls` | TLS handshake. |
| `ttfb` | Time to first byte, from the start of the request: how long the server took to answer. |
| `total` | Whole request including the body (read up to 10 MB). |
| `redirected` | Final URL if the request was redirected. |
| `cdn` | Azure Front Door, Azure CDN, Cloudflare or CloudFront, recognised from response headers. |

HTTP/2 is used when the server offers it, as a browser would. The timeout is three times `timeoutSeconds`. A 4xx status still counts as "up" (the service answered) but sets `http_ok` to 0. Only connection failures and 5xx count as down.

Metrics (subject = test name): `http_total_ms`, `http_ttfb_ms`, `http_tls_ms`, `http_ok`.

## Down and up

When a probe's state flips, an extra line is written:

```
PROBE/down  [M365] [M365] https://outlook.office365.com FAILED after 188.5ms: ...
PROBE/up    [M365] [M365] 200 dns=...
```

A probe that is down on its very first round also reports `PROBE/down`. These lines make good [remote](../remote.md) events.

## Path trace

Traces to the same endpoints are covered on their own page: [Path trace](path-trace.md).
