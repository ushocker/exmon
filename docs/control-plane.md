# Control plane

The control plane lets you manage a fleet centrally: see which devices are online and what they run, push configs, roll out updates by ring, and send a few fixed commands. It is a single outbound connection from each PC:

```
exmon ──MQTT v5 over WebSockets, TLS, port 443──► Azure Event Grid namespace (MQTT broker)
        client certificate from the PC's TPM               ▲
                                                           │ signed documents
                                               exmonctl (admin workstation) ─── Key Vault (signing keys)
```

- **One way in, and only signed.** A device only acts on documents signed by your Key Vault keys ([Signed documents](reference/signed-documents.md)). The broker, the network or a proxy can't forge or change one.
- **No HTTP fallback.** If the broker can't be reached, exmon keeps monitoring and sending events to its [remotes](remote.md) as usual, retries in the background, and **chirps** (see below) so you can find stuck devices from Log Analytics.
- **Outbound only.** Nothing listens on the PC.

## Turning it on

The control plane is off by default. It needs a [device certificate](enrollment.md) first. Then, from an elevated prompt:

```bash
exmon.exe install --control-host exmon-prod.eastus-1.ts.eventgrid.azure.net --control-cert-issuer "exmon Issuing CA"
```

`--control-host` turns it on. The certificate is picked by issuer (production: every device has a certificate from your Intune/Cloud PKI issuing CA) or by exact name (`--control-cert-subject exmon-test-01` for a test device). These are install-time settings: a cloud config can't change where a device connects or which certificate it uses.

The startup log shows:

```
MONITOR   control: wss://exmon-prod.eastus-1.ts.eventgrid.azure.net:443/mqtt, certificate any from exmon Issuing CA, signing keys prod, chirp every 15m
CONTROL/connected   device=6f1c…  broker=exmon-prod.eastus-1.ts.eventgrid.azure.net
```

## Settings (`control`)

| Key | Default | Meaning |
|---|---|---|
| `enabled` | `false` | Connect to the control plane. `--control-host` also turns it on. |
| `host` | none | The Event Grid namespace's MQTT hostname (`<namespace>.<region>-1.ts.eventgrid.azure.net`). |
| `port` | `443` | |
| `path` | `/mqtt` | WebSocket path. |
| `cert.store` | `LocalMachine` | Where the certificate is: `LocalMachine` (the computer's Personal store) or `CurrentUser` (testing). |
| `cert.subjectCN` | none | Use the certificate with exactly this common name. |
| `cert.issuerCN` | none | Use a certificate issued by the CA with this common name (newest valid one wins). One of `subjectCN`/`issuerCN` is required. |
| `keepAliveSeconds` | `60` | MQTT keep-alive. |
| `sessionExpiryMinutes` | `480` | How long the broker keeps the session (and commands sent meanwhile) while the PC is offline; Event Grid allows at most 8 hours. |
| `connectTimeoutSeconds` | `30` | Per connection attempt. |
| `chirpMinutes` | `15` | While the broker can't be reached, log `CONTROL/unreachable` this often. |
| `statusMinutes` | `15` | Republish the device's status this often. |
| `maxInboundKB` | `256` | Largest message the device accepts. |
| `tlsMaxVersion` | `""` | `"1.2"` or `"1.3"` to force a TLS version; empty = automatic. |

Flags: `--control-enabled`, `--control-host`, `--control-path`, `--control-cert-subject`, `--control-cert-issuer`. Ranges are in [Configuration](configuration.md#allowed-ranges).

The service runs as LocalService; the [helper service](service.md#the-helper-service) gives it permission to use the certificate's TPM key, at every boot, daily, and whenever exmon reports it can't use the key. Behind a proxy, set it with `exmon.exe install --proxy <url>`; a TLS-inspecting proxy must **not** inspect `*.ts.eventgrid.azure.net`, because the device authenticates with its own certificate inside TLS.

## How a device connects

1. It finds its certificate. It looks again on every connection attempt, so a certificate that arrives or renews later (Intune) is used without restarting exmon.
2. It opens `wss://<host>:443/mqtt` (subprotocol `mqtt`) through the proxy, if one is set, and presents the certificate. Event Grid accepts it if it chains to the namespace's trusted CA **and** a client with that name exists and is enabled.
3. It sends CONNECT with the certificate's common name as client id and username, a 256 KB packet limit, and a "last will" (`{"online":false,"reason":"lost"}`, retained). If the connection dies without a goodbye (crash, power loss, network gone), the broker notices when keep-alives stop (up to 1.5 × `keepAliveSeconds`, so 90 s by default) and publishes the will 30 seconds later: about two minutes in all. When exmon stops normally it publishes `{"online":false,"reason":"shutdown",…}` itself and then disconnects cleanly; a clean disconnect tells the broker to discard the will, which is why exmon says goodbye explicitly.
4. It subscribes to its own inbox and publishes its status.

If the connection drops it reconnects by itself, waiting 5 s, 10 s, 20 s … up to 10 minutes between attempts (±20% random, so a fleet doesn't reconnect all at once).

Some TPMs can't make the RSA-PSS signatures TLS 1.3 needs. exmon tests this with the device key and falls back to TLS 1.2 if needed, logging `CONTROL/tls_fallback` once.

## Topics

Each device can only use its own topics; the broker enforces this (see [Azure deployment](deploy/azure.md)).

| Topic | Direction | Retained | Content |
|---|---|---|---|
| `exmon/v1/device/<id>/status` | device → | yes | The device's status (below), or `{"online":false}` when it stops or is lost. |
| `exmon/v1/device/<id>/outbox` | device → | no | Results: `{"kind","version","ok","detail","time"}` for each document applied or command run. |
| `exmon/v1/device/<id>/inbox/config` | → device | yes | Signed config ([cloud config](#cloud-config)). |
| `exmon/v1/device/<id>/inbox/release` | → device | yes | Signed release manifest ([updates](updates.md)). |
| `exmon/v1/device/<id>/inbox/cmd` | → device | no | Signed command. |

`<id>` is the certificate's common name: the Entra device id for Intune devices, `exmon-test-NN` for test devices.

Config and release documents are **retained**, so a device that was off for days still gets the latest one when it reconnects. Receiving the same version again does nothing.

### Status

```json
{"online":true,"device":"exmon-test-01","version":"1.4.0","keyset":"prod","configVersion":20261007153000,"uptimeS":3600,"time":"2026-10-07T12:00:00Z"}
```

Sent when the device connects, every `statusMinutes`, and on request (`report-status`).

## Chirp: finding devices that can't connect

There is no fallback channel, so a device that can't reach the broker says so through its normal event stream. After `chirpMinutes` (default 15) of continuous failure, and every `chirpMinutes` after that, it logs:

```
CONTROL/unreachable   broker=exmon-prod.eastus-1.ts.eventgrid.azure.net for=45m0s reason=tls attempts=9 last="…"
```

That line goes to the local log and to every remote whose selectors include `CONTROL/*` (the Azure Monitor example does), so you can find stuck devices centrally:

```kusto
ExmonEvents_CL
| where Kind == "CONTROL/unreachable" and TimeGenerated > ago(1d)
| summarize last = arg_max(TimeGenerated, Detail) by Host
```

| `reason` | Usually means |
|---|---|
| `no_client_cert` | No valid certificate matches `control.cert`: not enrolled yet, or expired. |
| `key_access` | The certificate is there, but exmon may not use its key yet. exmon starts `exmon-helper` to grant it (`CONTROL/helper_started`); if this persists, check the helper's event log. |
| `dns` | The broker's name doesn't resolve (DNS filtering, offline). |
| `tcp` | Port 443 to the broker is blocked, or no network. |
| `proxy` | The proxy refused or failed. |
| `tls` | TLS failed. Often a TLS-inspecting proxy (exclude `*.ts.eventgrid.azure.net`), or the broker didn't accept the certificate's CA. |
| `websocket` | The WebSocket upgrade failed: wrong `path`, or a proxy that blocks WebSockets. |
| `auth_rejected` | The broker refused this device: no client with this name, or it is disabled. |
| `broker_error` | The broker refused for another reason (see `last`). |
| `timeout` | Attempts are timing out. |

When it connects again it logs `CONTROL/connected … downtime=45m after 9 failed attempt(s)`.

## Commands

A signed command document runs one of a fixed list. There is no way to run anything else.

| Command | Does |
|---|---|
| `report-status` | Publishes the status now. |
| `trace-now` | Runs a [path trace](monitors/path-trace.md) now. |
| `check-now` | Runs a [probe](monitors/probes.md) round now. |

Results go to the outbox topic and the log (`CONTROL/applied kind=exmon-command …`). Commands aren't retained; one sent while a device is offline is delivered when it reconnects, if within `sessionExpiryMinutes`.

## Cloud config

A signed `exmon-config` document on `inbox/config` changes a device's settings without touching its local `exmon.json`. Its payload carries the settings in a `config` object, in the same shape as `exmon.json`:

```json
{ "kind": "exmon-config", "version": 20261007153000,
  "issuedAt": "2026-10-07T15:30:00Z", "expiresAt": "2026-10-14T15:30:00Z",
  "target": { "devices": [] },
  "config": {
    "probe": { "intervalSeconds": 300, "endpoints": [ { "name": "ERP", "target": "erp.corp.example", "port": 443 } ] },
    "remotes": [ { "name": "law", "type": "azureMonitor", "settings": { "...": "..." },
                   "include": [ { "kinds": ["INCIDENT/*"] } ] } ],
    "control": { "chirpMinutes": 30 }
  } }
```

### Layers

With the control plane on, settings come from four layers, each overriding the one before:

1. built-in defaults;
2. the local `exmon.json`;
3. **the cloud config** (`%ProgramData%\exmon\config\cloud\active.json`);
4. command-line flags (the service's install flags).

As in a local file, keys the cloud config leaves out keep their value from below, and lists (such as `remotes` or `endpoints`) replace rather than merge.

### What a cloud config can't set

| Can set | Can't set (local config or install flags only) |
|---|---|
| `wifi`, `nic`, `vpn`, `probe`, `power`; `incident` except `dir`; `baseline` except `path`; `remotes` (without `spoolDir`); `control.chirpMinutes`, `control.statusMinutes` | `log`, `outputRoots`, `incident.dir`, `baseline.path`, any remote's `spoolDir`, `outbox` remotes (they write local files), everything else under `control` (broker, certificate) |

Stricter ranges also apply to what a cloud config sets: `probe.intervalSeconds` at least 60, `probe.pathTrace.intervalMinutes` at least 10 (or 0), Wi-Fi polls at least 2 s, adapter and VPN polls at least 10 s. With the usual limits (at most 25 endpoints, 25 HTTP tests and 10 DNS names) that caps a device at roughly 185 probe actions a minute, however a cloud config is written. Probes also never contact link-local, cloud-metadata or multicast addresses, and follow at most 5 redirects ([Probes](monitors/probes.md#addresses-probes-never-contact)).

### Applying one safely

1. The document is verified (signature, kind, expiry, target, newer version).
2. It is **tried** exactly as it would load: merged with the local config and flags and validated (for the service, also against the output folders). A config that wouldn't load is refused (`CONTROL/failed`) and nothing changes.
3. It is stored as `active.json`; the previous one is kept as `last-known-good.json`, and the change is marked as on trial (`state\pending-config.json`).
4. exmon acknowledges on the outbox and **restarts itself in-process** a few seconds later. The service stays Running; events waiting in the remote spools are kept.
5. If exmon runs with it for 2 minutes, it is committed (`CONTROL/config_committed`). If exmon fails to start with it, or starts 3 times without getting that far (for example, it crashes), or 10 minutes pass, **the previous one is put back** automatically and a warning says so.

The stored file is verified again on every start. If it has been changed on disk, or this build no longer trusts its key, it is ignored (with a warning) and the local config runs.

To return a device to its local config only, publish a cloud config with an empty `config` object, or delete `config\cloud\active.json` and restart the service.

### Rotating the Azure Monitor secret

Because `remotes` can be set from the cloud, the [Azure Monitor](azure-monitor.md) client secret can be rotated without visiting PCs: add a second secret to the ingest app, publish a cloud config with it, wait until every device has applied it (check the status `configVersion`), then remove the old secret.

## Safety limits

- Only signed documents are acted on; signatures are checked before the content is read, and documents are decoded strictly (unknown fields are errors).
- An older document than the one last applied is refused; the same one again is ignored.
- At most one config or release document is applied per 5 minutes, so a burst can't restart exmon in a loop.
- Messages over `maxInboundKB` are refused by the broker before they reach exmon.

## Log lines

| Kind | Meaning |
|---|---|
| `CONTROL/connected` | Connected (with the downtime if it had been unreachable). |
| `CONTROL/disconnected` | The connection dropped; exmon reconnects by itself. |
| `CONTROL/unreachable` | The chirp: still can't connect, and why. |
| `CONTROL/tls_fallback` | Using TLS 1.2 because the key can't do TLS 1.3's signatures. |
| `CONTROL/applied` | A document or command was applied (`kind=… version=…`). For a cloud config this means installed; exmon restarts to use it. |
| `CONTROL/config_committed` | A new cloud config ran healthily for 2 minutes and is now final. |
| `CONTROL/rejected` | A document was refused (bad signature, older version, wrong kind, too soon …). |
| `CONTROL/failed` | A valid document couldn't be applied. |
| `CONTROL/trace_now` | A `trace-now` command asked for a path trace. |
| `CONTROL/helper_started`, `CONTROL/helper_failed` | exmon started `exmon-helper` to (re)grant key access, or couldn't. |
| `CONTROL/scheduled` | A release manifest was accepted; the update waits for this device's turn ([Updates](updates.md)). |
