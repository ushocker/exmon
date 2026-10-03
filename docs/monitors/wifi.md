# Wi-Fi monitor

The Wi-Fi monitor talks to the Windows WLAN AutoConfig service (`wlanapi.dll`) directly. It does two things at once:

- **Events as they happen.** Windows notifies exmon of connects, disconnects, roaming, radio on/off, link quality changes and more. Each one becomes a log line immediately.
- **A poll every few seconds** (`wifi.pollSeconds`, default 5) reads the current state of each wireless adapter. It only writes a line when something meaningful changed, and also at least every `heartbeatSeconds`.

## Event lines (`ACM/*` and `MSM/*`)

Windows reports events from two parts of the Wi-Fi stack:

- **ACM** (auto configuration module): connection-level events such as `ACM/connection_start`, `ACM/connection_complete`, `ACM/connection_attempt_fail`, `ACM/disconnecting`, `ACM/disconnected`, `ACM/scan_complete` and `ACM/network_available`.
- **MSM** (media specific module): radio-level events such as `MSM/associating`, `MSM/associated`, `MSM/authenticating`, `MSM/connected`, `MSM/roaming_start`, `MSM/roaming_end`, `MSM/radio_state_change`, `MSM/link_degraded`, `MSM/link_improved` and `MSM/disconnected`.

Connection and MSM events include the profile, network name, access point (BSSID) and the reason with Windows' own description:

```
ACM/disconnected     profile="Corp" ssid="Corp" reason=229377 "The operation was cancelled."
MSM/roaming_end      profile="Corp" ssid="Corp" bssid=7A:45:58:94:F7:93 reason=0 "The operation was successful."
MSM/radio_state_change  phy=0 hw=on sw=off
```

`MSM/signal_quality_change` is not logged. The POLL line already covers signal changes.

## POLL lines

One POLL line describes every wireless adapter:

```
POLL  [Intel(R) Wi-Fi 7 BE200] ssid="Corp" bssid=7A:45:58:94:F7:93 rssi=-52dBm ch=153 (5GHz) phy=802.11ax auth=WPA3-Personal cipher=CCMP rx=1201000kbps tx=1201000kbps util=25% sta=3 caps=11r/11k
```

| Field | Meaning |
|---|---|
| `ssid`, `bssid` | Network name and the access point (radio) you are connected to. |
| `rssi` | Signal strength in dBm from the driver. Closer to 0 is stronger: -50 is excellent, -70 fair, -80 poor. |
| `ch`, band | Channel and band (2.4/5/6 GHz). |
| `phy` | Wi-Fi generation, e.g. 802.11ax (Wi-Fi 6). |
| `auth`, `cipher` | Security, e.g. WPA2-Enterprise / CCMP. |
| `rx`, `tx` | Current link rates in kbps. |
| `util`, `sta` | Channel utilisation and connected station count, if the access point advertises them (BSS Load). |
| `caps` | Fast-roaming features the access point supports: 11r (fast transition), 11k (neighbour reports), 11v (BSS transition). |

A disconnected adapter shows `state=disconnected` (or `associating`, `authenticating` …).

### When a POLL line is written

A line is written when any of these happen since the last line:

- the connection changes: network, access point, channel, band, PHY or security;
- RSSI moves by `rssiThreshold` dBm or more (default 3);
- the rx or tx rate swings by `rateChangePercent` % or more (default 50);
- retry% or FCS% moves by `errorPctThreshold` points or more (see [frame statistics](#stats-lines-frame-statistics));
- a four-way handshake fails;
- an adapter appears or disappears;
- **heartbeat:** `heartbeatSeconds` (default 15) passed without a line. Heartbeat lines end with `[heartbeat]`.

Comparisons are against the last line *written*, not the last poll, so a slow drift still adds up to a change. A heartbeat line does not reset that comparison. `debugPoll: true` writes every poll.

### NEIGHBORS lines

With `neighbors: true`, a NEIGHBORS line lists other access points broadcasting the same network name, with channel, signal and capabilities. These are the access points the adapter could roam to.

## STATS lines (frame statistics)

Many drivers expose 802.11 frame counters. When a POLL line is written, a STATS line shows how they grew since the previous one:

```
STATS  [Intel(R) Wi-Fi 7 BE200] tx=2750 rx=1840 retry=114 (4.1%) multi_retry=12 tx_fail=0 ack_fail=0 rts_fail=0 fcs_err=3 (0.2%) handshake_fail=0 decrypt_fail=0
```

- **retry%**: the share of sent frames that needed a retry. A high value means interference or a weak signal.
- **FCS%**: the share of received frames with a bad checksum.

Percentages are only trusted when the interval had at least `minStatsFrames` frames (default 500). Otherwise the line says `[retry% ignored: tx < 500 frames]`, and the value is not used for change detection or triggers.

**Untrustworthy drivers.** If a driver ever reports more retried frames than sent frames in one poll, which the 802.11 standard makes impossible, exmon logs one warning:

```
STATS  [...] driver counters look inconsistent (retry=1140 > tx=430 in one poll); retry/FCS change detection disabled
```

After that, that adapter's retry/FCS figures are printed for reference but never used. The MediaTek MT7927 driver does this. See [Troubleshooting](../troubleshooting.md#retry-is-over-100-or-stats-lines-look-wrong).

If the driver exposes no frame counters at all, one line says so and STATS lines are skipped.

## Metrics

Reported every poll for each connected adapter (subject = adapter name). See [Metrics](../reference/metrics.md#wi-fi):

`rssi_dbm`, `signal_quality`, `rx_kbps`, `tx_kbps`, `retry_pct`, `fcs_pct`, `handshake_fail`, `recent_disconnects`.

`recent_disconnects` counts `ACM/disconnected` events within the last `disconnectWindowMinutes` (default 30). It only counts disconnects seen while exmon was running, so it starts at 0 after a restart. It is reported once the adapter is connected again.

## Resilience

- If the WLAN service isn't running yet (for example at boot), exmon retries, waiting 5s, 10s, 20s … up to 60s.
- If the WLAN service restarts, or three polls fail in a row, exmon logs `re-opening wlanapi handle`, reconnects and carries on.
