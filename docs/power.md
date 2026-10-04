# Startup, sleep and shutdown

When a computer starts, wakes from sleep or hibernation, or shuts down, its network goes away and comes back. Wi-Fi disconnects and reconnects, adapters go down and up, probes fail for a few seconds, VPNs drop. None of that is a real problem, but without care exmon would report it as one: incidents would fire and outages would be forwarded to the remote target.

To prevent that, exmon **holds back** monitor events at those moments until the network is known to work.

## What "held" means

While events are held, they are still written to the log, so you can see what reconnecting looked like, but they are marked and treated as non-alarms:

```
23:52:24.602  POWER/starting   exmon starting: holding events until the network is ready (up to 3m0s)
23:52:24.643  POLL             [Intel(R) Wi-Fi 7 BE200] state=disconnected [held]
23:52:25.227  PROBE/gateway    [10.0.0.1 via Wi-Fi] ping min/avg/max=0.0ms/0.0ms/0.0ms loss=0% [held]
23:52:29.735  POWER/ready      network ready 5s after startup (via Wi-Fi, connection test passed); 10 events were held
```

Held events:

- are written to the log with `[held]` at the end (`"held": true` in the baseline JSON);
- **don't fire incident triggers**, and triggers forget any partial "N samples in a row" counts at each `POWER/*` line;
- are **never forwarded** to the remote target;
- are **left out of baseline statistics**.

exmon's own `MONITOR`, `DEVICE` and `POWER/*` lines are never held.

When events go live again, the probe monitor re-checks its targets and reports anything that is still down as a normal, live `PROBE/down`. A real outage that started during the wake-up is therefore still reported. Conditions that persist, such as a weak signal or a VPN that stayed down, are caught by the triggers on the next live samples.

## When events are held

| Moment | How exmon knows | Line |
|---|---|---|
| **Startup** | exmon starts holding before any monitor runs, whether started at boot (service) or by hand (console). | `POWER/starting` |
| **Going to sleep or hibernating** | Windows notifies exmon just before suspending. | `POWER/suspend` |
| **Waking up** | Windows notifies exmon on resume. | `POWER/resume` |
| **Waking up, missed notification** | A timer notices the clock jumped forward, e.g. a 5-second tick arriving 40 minutes later. This covers systems that don't send the notification, and Windows "Fast Startup", which hibernates services instead of stopping them. | `POWER/resume clock jumped …` |
| **Shutdown, restart, log-off, or stopping exmon** | The service asks Windows for early **pre-shutdown** notice, while the network is still up. In a console, Windows shutdown, log-off and closing the window are handled like Ctrl+C. | `POWER/shutdown` |

## When events go live again

After startup or waking, exmon checks every few seconds (`checkSeconds`, default 5; the first check is immediate):

1. **Some network adapter is up and has a default route**, Wi-Fi or wired. A docked laptop or a desktop without Wi-Fi works too.
2. **The connection test passes:** fetching `http://www.msftconnecttest.com/connecttest.txt` returns `Microsoft Connect Test`. This is the same test Windows uses to decide whether it has internet access. A captive portal ("sign in to the hotel Wi-Fi") answers with its own page, so the test correctly fails there.
3. **Optional settle time** (`settleSeconds`, default 0): the network must stay ready this long before going live, e.g. to let a VPN reconnect.

Then it logs `POWER/ready` with how long it took and how many events were held.

**If the network isn't ready within `maxWaitSeconds` (default 180, i.e. 3 minutes), exmon goes live anyway** and logs `POWER/not_ready` with the last check result. Failing to get a working network within 3 minutes of waking is itself worth knowing, and from then on any ongoing outage is reported normally. Consider adding `POWER/not_ready` to your [remote selectors](remote.md#choosing-what-to-send).

## Configuration

```json
"power": {
  "enabled": true,
  "testURL": "http://www.msftconnecttest.com/connecttest.txt",
  "expect": "Microsoft Connect Test",
  "maxWaitSeconds": 180,
  "settleSeconds": 0,
  "checkSeconds": 5
}
```

| Key | Default | Meaning |
|---|---|---|
| `enabled` | `true` | Turn the whole feature off with `false` (or `--power-enabled=false`). |
| `testURL` | Microsoft's connect test | Page to fetch. Use `""` to only require a default route (no test request). |
| `expect` | `Microsoft Connect Test` | Text the page must contain. |
| `maxWaitSeconds` | `180` | Go live anyway after this long (10–3600). |
| `settleSeconds` | `0` | Extra time the network must stay ready (0–600). |
| `checkSeconds` | `5` | How often readiness is checked while waiting (1–60). |

**Networks that block the test page.** If the test page can only be reached through an authenticating proxy, or is blocked, every startup and wake ends in `POWER/not_ready` after the full wait. You can:
- point `testURL` at an internal page that is reachable without a proxy, with `expect` set to text it contains; or
- set `testURL` to `""`, so a default route is enough.

## Limits

- **Modern Standby** laptops (the "phone-like" sleep on many newer machines) may stay connected while the screen is off, and don't always send sleep and wake notifications. The clock-jump check catches long sleeps. During short or connected standby periods, events stay live, which is usually correct, since the network really is up.
- The suspend notification is sent just before sleep, so the last few events before it are live.
