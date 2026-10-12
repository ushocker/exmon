# Incidents

An **incident** is a snapshot of everything exmon logged around a problem. When a trigger rule fires, exmon:

1. copies the **rolling window**: the last `windowMinutes` (default 10, at most 60) of log lines from *all* monitors;
2. keeps recording for `postTriggerSeconds` (default 30, at most 600), plus however long a path trace takes (see below);
3. writes it all to a compressed file such as `incident-20261003-104715.253-sev1-weak-signal.log.gz` in `incident.dir`;
4. logs `INCIDENT/triggered` and `INCIDENT/saved` lines. These can be forwarded to the [remote target](remote.md).

Incidents are on by default but **nothing fires until you add triggers**.

## Trigger rules

Triggers watch [metrics](reference/metrics.md), the numbers monitors report on every sample, even when the log line itself was suppressed because nothing changed.

```json
"incident": {
  "triggers": [
    { "name": "weak-signal",      "source": "wifi",  "metric": "rssi_dbm",         "op": "<",  "threshold": -75, "forSamples": 3, "severity": 3 },
    { "name": "very-weak-signal", "source": "wifi",  "metric": "rssi_dbm",         "op": "<",  "threshold": -82, "forSamples": 3, "severity": 1 },
    { "name": "gateway-loss",     "source": "probe", "metric": "gateway_loss_pct", "op": ">=", "threshold": 50,                   "severity": 1 },
    { "name": "endpoint-down",    "source": "probe", "metric": "reachable",        "op": "<",  "threshold": 1,                    "severity": 2 },
    { "name": "vpn-dropped",      "source": "vpn",   "metric": "vpn_up",           "op": "<",  "threshold": 1,                    "severity": 2 }
  ]
}
```

| Field | Required | Meaning |
|---|---|---|
| `name` | yes | Unique. Used in the file name. |
| `source` | yes | The monitor: `wifi`, `nic`, `vpn`, `services` or `probe`. |
| `metric` | yes | A metric name from [Metrics](reference/metrics.md). |
| `op` | yes | `<`, `<=`, `>` or `>=`. |
| `threshold` | yes | The number to compare with. |
| `subject` | no | Only this subject, e.g. one adapter name or one probe name. Leave it out to match any. |
| `forSamples` | no | How many samples **in a row** must breach before firing (default 1). Use 2–3 to ignore one-off blips. |
| `severity` | no | 1 (most serious) to 5 (least). Default 3. |

Each rule fires at most once per `cooldownSeconds` (default 300), so a long outage gives one incident, not one per poll.

Triggers ignore **held** metrics (from while the machine is starting, waking from sleep or shutting down, before the network is ready), and forget partial `forSamples` counts at each `POWER/*` line. See [Startup, sleep and shutdown](power.md).

**Choosing thresholds:** run [baseline mode](baseline.md) for a while. Its summary suggests starting thresholds from what is normal on that machine.

## Severity

Severity lets you tier incidents. **1 is the most serious** (like Sev1/P1) and 5 the least; the default is 3. You might use:

| Severity | For example |
|---|---|
| 1 | Gateway unreachable, very weak signal: users are effectively offline |
| 2 | A business endpoint down, VPN dropped, repeated disconnects |
| 3 | Weak signal, adapter errors |
| 4–5 | Slow responses, high retry rates: worth knowing, not urgent |

Severity appears:

- in the file name (`-sev1-`);
- in the file's `# severity:` header line;
- as `sev=1` in `INCIDENT/*` lines;
- as `"severity": 1` on events sent to the remote target, where you can [filter on it](remote.md#filtering-by-severity).

Two rules on the same metric with different thresholds and severities, like `weak-signal` and `very-weak-signal` above, give you a tiered alert. Each rule has its own cooldown, so both fire if the signal keeps falling.

## Keeping incident files

Files are kept **per severity**, so a flood of minor incidents can't push out the rare serious ones:

```json
"maxIncidents": 100,
"maxIncidentsBySeverity": { "1": 500, "2": 300, "3": 100, "4": 50, "5": 20 }
```

When a file is saved, exmon deletes the oldest files **of that severity** beyond its limit. Severities not listed use `maxIncidents`. A limit of 0 means unlimited. Files from older versions without `-sevN-` in their name count as severity 3.

## The incident file

A gzip-compressed text file. Open it with 7-Zip, or from PowerShell or bash with `tar -xzf <file>` or `gzip -dc <file>`.

```
# exmon incident incident-20261003-004533.912-sev3-gw-check
# severity:  3 (1 = most serious)
# rule:      gw-check (sev3 probe gateway_rtt_ms > 50, forSamples 1)
# subject:   gateway
# value:     73.5
# triggered: 2026-10-03 00:45:33.912
# captured:  10m0s before, until 2026-10-03 00:46:05.157
# device:    computer="PC01" user="jsmith" domain="corp.example.com" model="Latitude 7440" os="Windows 11 Enterprise 24H2 (build 26100)"
# lines:     138

2026-10-03 00:35:34.001  POLL                         [Intel(R) Wi-Fi 7 BE200] ssid="Corp" ...
...
2026-10-03 00:45:33.912  INCIDENT/triggered           id=incident-20261003-004533.912-sev3-gw-check sev=3 rule=gw-check ...
2026-10-03 00:45:35.932  PATHTRACE/hop                [M365Portal]  1 10.0.0.1 ...
```

The body uses the same line format as the main log, and covers every monitor, not just the one that triggered. A Wi-Fi drop that caused a probe failure shows up together.

## Path traces inside incidents

With `probe.pathTrace.onIncident` on (the default), a trigger starts a [path trace](monitors/path-trace.md). The tracer tells the incident to wait (`INCIDENT/hold`), traces, logs the hops, then lets it go (`INCIDENT/release`). The incident is saved once both `postTriggerSeconds` has passed and the hold is released, but never later than `maxHoldSeconds` (default 120) after the hold began.

The hold/release mechanism is generic. Any future monitor can use it to add evidence to an incident.

## Lines

| Kind | Meaning |
|---|---|
| `INCIDENT/triggered` | A rule fired: `id=… sev=… rule=… source=… subject=… metric=… value=… op=… threshold=…` |
| `INCIDENT/hold`, `INCIDENT/release` | Evidence is being added (path trace). Never forwarded remotely. |
| `INCIDENT/saved` | `id=… sev=… rule=… path=… lines=…` |
| `INCIDENT/save_failed` | The file couldn't be written, e.g. disk full or no permission. |

On Ctrl+C, incidents still recording are saved straight away with what they have.
