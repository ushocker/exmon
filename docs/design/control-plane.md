# Control plane (proposal)

> **Status: proposal, not implemented.** This records the decisions made so far for centrally managing a fleet of exmon installs, in the spirit of Fleet Management for Falcon LogScale collectors. Nothing here exists in the code yet. Build it on a branch if and when the project goes the "full server" route.
>
> Read together with [Cloud config security](cloud-config-security.md) (the risks specific to a cloud-delivered config) and [Config security review](config-security-review.md) (the local findings, whose fixes should land first).

## Goals

1. **Cloud-delivered configuration.** Each machine's config comes from a central service, assigned by group, instead of being edited by hand.
2. **Remote updates.** New exmon versions are rolled out automatically, verified on the device, and rolled back if they misbehave.
3. **Fleet visibility.** See which machines are online, what version and config they run, and whether they are healthy.

Event delivery is **not** part of the control plane: events go straight to the SIEM (see below).

## Decisions

| # | Decision | Why |
|---|---|---|
| 1 | **MQTT is used only for control-plane traffic**: desired config, desired version, commands, device status and presence. | Instant push to every online device over one outbound connection. MQTT's last-will message gives online/offline status for free. Retained messages suit "desired state". |
| 2 | **Events go directly to the SIEM over HTTPS**, not through MQTT. The SIEM destination is part of the (cloud-delivered) config. | SIEM ingest APIs (e.g. LogScale) are HTTP. This avoids running a bridge. The existing remote queue (disk spool, retries, backoff, redelivery after reboot) already provides reliability; only the real `Sender` needs writing. |
| 3 | **Large files never go over MQTT.** Update binaries are downloaded over HTTPS; MQTT only announces them. | Brokers cap message sizes, and downloads should come from a CDN or blob storage. |
| 4 | **Updates and config use a "desired state" model**: a signed, retained manifest per group, which each device converges to. MQTT does not send imperative "do this now" commands for updates. | Offline devices catch up when they reconnect. Duplicate delivery (QoS 1) is harmless. The rollout state is visible. |
| 5 | **Every config and release manifest is signed** (Ed25519). The public key is built into exmon; the private key stays offline or in an HSM/key vault. | The broker, network, CDN or a TLS-inspecting proxy could be compromised. A signature makes the content trustworthy regardless of how it travelled. |
| 6 | **Update downloads are public; there is no download key or passkey.** | The client contains nothing sensitive, and integrity comes from signature verification, not secrecy. A shared or short-lived key would add risk (leaks, logging) for no gain. |
| 7 | **Update verification is layered.** Signed manifest (version only goes up, expiry, group) → download host on a local allow-list → size limit → SHA-256 → Authenticode valid **and** from our pinned publisher → swap → health check → roll back on failure. | A hash in a message only proves the download matches the message. The signature is the security boundary. Pinning the publisher stops "any validly signed exe" being accepted, and the version check stops downgrades to old signed builds. |
| 8 | **No secrets in the binary or in plain config.** Device credentials come from enrollment and are protected with DPAPI or the TPM. SIEM ingest tokens are either per device or encrypted to the device's key. | The exe is public and the config is saved to disk. |
| 9 | **The cloud document can't set everything.** The whole config comes from the cloud, but its document has no fields for filesystem paths or control-plane identity and URLs. Those come from the binary or from install-time flags. Everything else (thresholds, triggers, intervals, probe targets, selectors) is cloud-managed within limits built into the binary. | A limit inside the config would be set by the same party it's meant to limit. See [Cloud config security](cloud-config-security.md) C1–C3. |
| 10 | **Prerequisites before any of this:** Windows Service mode (done), the local fixes in the [Config security review](config-security-review.md#roadmap), and the built-in limits in [Cloud config security](cloud-config-security.md#c2-traffic-abuse-at-fleet-scale). | Updating needs service control, and a cloud config must not be able to exceed what the binary allows. |

## Architecture

```
                 ┌─────────────────────────── control plane ───────────────────────────┐
                 │  admin UI / API ── signs ──► MQTT broker (TLS, per-device auth, ACLs)  │
                 │  (config editor,             retained: group config, group release    │
                 │   groups, rollouts)          live: device commands, device status     │
                 └──────────────────────────────────────┬──────────────────────────────┘
                                                        │ MQTT over TLS / WSS 443 (outbound only)
   ┌─────────────────────────── exmon (Windows service) ┴─────────────────────────────┐
   │ controlplane client ─► verify signature ─► apply config (graceful restart of run) │
   │                       └► updater: download ─► verify ─► swap ─► health ─► rollback │
   │ monitors ─► bus ─► remote.Queue (disk spool) ─► SIEM Sender ──HTTPS──► SIEM        │
   └───────────────────────────────────────────────────────────────────────────────────┘
                     update binaries ◄──HTTPS── public CDN / blob storage
```

## MQTT layout

| Topic | Direction | Retained | Content |
|---|---|---|---|
| `exmon/v1/group/<group>/config` | server → devices | yes | Signed config document |
| `exmon/v1/group/<group>/release` | server → devices | yes | Signed release manifest |
| `exmon/v1/device/<id>/cmd` | server → device | no | Signed command from a fixed list: `check-now`, `trace-now`, `reload` |
| `exmon/v1/device/<id>/status` | device → server | yes | Version, config version/hash, health, update progress |
| (last will on `.../status`) | broker | yes | `{"online": false}` when a device drops |

- **Broker permissions are a security boundary.** A device may subscribe only to its own groups and its own `cmd` topic, and publish only to its own `status`. Test these permissions as carefully as code.
- **Transport:** TLS with a per-device client certificate from enrollment. Prefer port 443 (MQTT over WebSockets) for corporate networks.
- **Fallback:** if MQTT is blocked, a device can poll an HTTPS endpoint for the same signed documents every 5–15 minutes. This is optional, but keeps MQTT an optimisation rather than a single point of failure.
- **Commands are a fixed, typed list. There is never a generic "run this".**

## Signed documents

Every document is `{ "payload": {...}, "signature": "ed25519:<base64>" }`, and the signature covers the exact payload bytes. Common payload fields: `kind`, `group`, `version`, `issuedAt`, `expiresAt`.

### Release manifest

```json
{
  "payload": {
    "kind": "exmon-release",
    "group": "ring-canary",
    "version": "1.4.0",
    "minVersion": "1.3.0",
    "url": "https://updates.example.com/exmon/1.4.0/exmon.exe",
    "sha256": "9f2c…",
    "size": 9437184,
    "issuedAt": "2026-10-03T14:00:00Z",
    "expiresAt": "2026-10-10T00:00:00Z"
  },
  "signature": "ed25519:…"
}
```

### Config document

The config `payload` is the cloud-managed subset of `exmon.json` plus `kind: "exmon-config"`, `group` and a `version` number that only ever goes up. It is decoded strictly: unknown keys are rejected. Brokers typically allow messages of a few hundred KB, which is plenty for a config.

## Device behaviour

### Applying a config
1. Verify the signature, the group, that the version is newer, and the expiry.
2. Decode strictly and validate, including the stricter cloud ranges in [Cloud config security](cloud-config-security.md#c2-traffic-abuse-at-fleet-scale).
3. Write it to the protected config folder, keeping the previous config as last-known-good.
4. Log a `CONFIG/applied` line with the version and a diff summary.
5. Do a graceful in-process restart of `run()`: flush incidents, close the bus, drain the consumers, start again. The spool means no events are lost.
6. If the new config fails to start, or fails health checks, revert to last-known-good and report the failure.

### Updating
1. A retained release manifest arrives, or `check-now` is received. Verify it (signature, group, version > current, not expired, `minVersion`).
2. Check the URL host against the **locally configured** allow-list. Refuse redirects to hosts that aren't on it.
3. Download over HTTPS into the staging folder (SYSTEM/Administrators only), stopping at `size` bytes.
4. Check the SHA-256, then Authenticode via `WinVerifyTrust` with the pinned publisher. Keep the file open so it can't change between the check and the swap.
5. Rename `exmon.exe` → `exmon.old.exe` (allowed while running), move the new exe into place, and restart through the Service Control Manager.
6. The new version must report healthy on its status topic within N minutes. Otherwise the service restores `exmon.old.exe`.
7. Report each step on the status topic: `accepted → downloading → verified → applied → healthy`, or `failed:<reason>`.

Rollouts move through rings (e.g. `ring-canary` → `ring-early` → `ring-broad`) by publishing the release to each group in turn, watching the reported health between steps.

## SIEM delivery

- Implement the `remote.Sender` interface for the SIEM's HTTP ingest API (e.g. LogScale's ingest with an ingest token). The spool, batching, retries, backoff and redelivery are already built.
- The SIEM URL and selectors come from the config. Because the endpoint receives every forwarded event (user, host, Wi-Fi details, IPs), it is either restricted by a local allow-list of SIEM hosts, or trusted only because the config is signed with a key held offline. Ideally both.
- The ingest token is not stored in plain config (decision 8).

## Enrollment (outline)

1. The installer runs with a one-time enrollment token (per site or per batch, short-lived).
2. The device generates a key pair (DPAPI or TPM), sends a certificate request plus the token to the enrollment API, and receives a client certificate, its device id and its initial groups.
3. Afterwards it authenticates to the broker (and to any HTTPS fallback) with that certificate. A device can be revoked centrally.

## What exists today that this builds on

- `config.Load` already produces one validated `Options` from defaults + file + flags. A cloud document becomes another layer, between the file and the flags.
- `run()` in `main.go` is self-contained with an orderly shutdown, which makes the graceful restart straightforward.
- The remote queue's disk spool, backoff and redelivery. Only a real `Sender` is missing.
- `internal/device` already gives a stable machine description for status reports.

## Suggested phases

1. **Windows Service mode** (install/uninstall, recovery options). *Done.*
2. **Local hardening** from the [Config security review](config-security-review.md#roadmap) (elevated console trust check, numeric limits, output containment). Worth doing even without a cloud.
3. **SIEM `Sender`** (HTTPS ingest), with a locally configured endpoint first.
4. **Built-in cloud limits** from [Cloud config security](cloud-config-security.md) (rate limit, link-local deny, redirect cap, a cloud document without path fields).
5. **Enrollment and identity.**
6. **MQTT client:** status/presence, signed config delivery, graceful reload, last-known-good.
7. **Updater:** signed manifests, verification, swap, health check, rollback, rings.

## Open questions

- Which SIEM, and its ingest API and token model?
- Broker: managed (Azure Event Grid MQTT, AWS IoT Core) or self-hosted (EMQX, HiveMQ, Mosquitto)?
- Who holds the signing keys, and where (HSM, key vault, offline)? How are keys rotated (the exe could pin more than one public key)?
- Is the optional HTTPS fallback needed for the target networks?
- How is enrollment delivered (installer parameter, MDM/Intune)?
- Privacy policy: which fields may leave the machine (user name, SSIDs, IPs)?
- Should `trace-now` and other on-demand commands exist, or only desired state?
