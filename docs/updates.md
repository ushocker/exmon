# Updates

The installed service can update itself when the [control plane](control-plane.md) announces a new version. Updates roll out **ring by ring** (and optionally to a percentage of a ring), are spread over time so thousands of PCs don't download at once, and are **rolled back automatically** if the new version doesn't run properly.

```
exmonctl release publish ──► signed manifest (retained) ──► inbox/release on each device in the ring
                                                               │
exmon (LocalService):    verify manifest ─► wait for its turn ─► download (allow-listed host)
                         ─► check size + SHA-256 + signature ─► hand over to exmon-helper
exmon-helper (SYSTEM):   verify it ALL again on its own copy ─► swap exe ─► restart exmon
                         ─► new version healthy within healthMinutes?  yes: done   no: old exe back
```

## Turning it on

Updates are off unless the service is installed with the hosts updates may come from. All of these are install-time settings; a cloud config can't change them:

```bash
exmon.exe install --control-host exmon-prod.eastus-1.ts.eventgrid.azure.net --control-cert-issuer "exmon Issuing CA" --update-hosts exmonreleasesprod.blob.core.windows.net --update-publisher "Contoso Ltd"
```

| Setting (`control.update`) | Flag | Default | Meaning |
|---|---|---|---|
| `enabled` | `--update-hosts` turns it on | `false` | Apply release manifests. |
| `allowedHosts` | `--update-hosts a,b` | none | Hosts downloads may come from (and redirect to), at most 5. HTTPS only. |
| `publisherCN` | `--update-publisher` | none | Common name of the code-signing certificate the new exe must be signed with. |
| `requireAuthenticode` | `--update-require-authenticode` | `true` | Require a valid code signature. Set `false` only to test unsigned builds; the manifest's signature and SHA-256 still apply. |
| `maxSizeMB` | | `64` | Refuse larger downloads (8–256). |
| `healthMinutes` | | `10` | The new version must be healthy within this long (5–60), or it is rolled back. |

Updates need the installed service **and** `exmon-helper.exe` (installed next to it by `exmon install`). The helper's settings (`publisherCN`, `requireAuthenticode`, `healthMinutes`, `maxSizeMB`) are copied onto its own command line when you run `install`, so run `install` again after changing them. A console run that receives a manifest logs `CONTROL/failed … updates are applied only by the installed service`.

## The release manifest

A signed `exmon-release` document ([format](reference/signed-documents.md)), signed with the **release** key, which can't sign configs (and the config key can't sign releases):

```json
{ "kind": "exmon-release", "version": 20261007160000,
  "issuedAt": "2026-10-07T16:00:00Z", "expiresAt": "2026-10-21T16:00:00Z",
  "releaseVersion": "1.5.0", "minVersion": "1.3.0",
  "url": "https://exmonreleasesprod.blob.core.windows.net/releases/1.5.0/exmon.exe",
  "sha256": "9f2c…", "size": 9437184,
  "percent": 25, "spreadMinutes": 120 }
```

| Field | Meaning |
|---|---|
| `releaseVersion` | The version on offer. Devices already on it do nothing; devices on a newer one do nothing unless `allowDowngrade` is set. |
| `minVersion` | Devices older than this refuse it (they need an intermediate release first). |
| `url`, `sha256`, `size` | Where to get the exe, and exactly what it must be. |
| `percent` | 1–100: the share of the targeted devices that take it. The same devices stay chosen as you raise it (25 → 50 → 100). Default 100. |
| `spreadMinutes` | Each device starts at its own fixed point within this many minutes after `issuedAt`, so downloads are spread out. A restart doesn't move it. |
| `allowDowngrade` | Allow going to an older `releaseVersion`: for a deliberate fleet-wide rollback. |
| `target.devices` | Optional: only these devices. |

`exmonctl release publish` builds, signs and publishes it; see [exmonctl](exmonctl.md).

## What a device does

exmon runs as LocalService and can't replace its own program, so an update is done in two halves: exmon prepares it, and the small [helper service](service.md#the-helper-service) (`exmon-helper`, LocalSystem) checks it again and installs it.

**exmon:**

1. **Decides.** It checks the manifest against its own settings (download host on the allow-list, size limit), its version, `minVersion`, and whether it is in the `percent`. It logs `CONTROL/scheduled … update to 1.5.0 scheduled in 37m` and acknowledges on the outbox.
2. **Waits** for its turn. If exmon restarts meanwhile, the retained manifest arrives again and the same turn is kept.
3. **Downloads** to `%ProgramData%\exmon\update\incoming`, over HTTPS, through the service's proxy if one is set. It stops reading at `size` bytes and checks the SHA-256 while downloading. Redirects are only followed to allowed hosts. (`UPDATE/downloading`)
4. **Checks** the code signature and publisher, as an early check that avoids a pointless hand-over. (`UPDATE/verified`)
5. **Hands over.** It writes `update\incoming\request.json` (the signed manifest exactly as received, and the file name) and starts `exmon-helper`. (`UPDATE/handed_over`)

**exmon-helper** (see [what the helper checks](#what-the-helper-checks)):

6. **Verifies everything again** and refuses anything that fails. Nothing changes and the request is deleted.
7. **Swaps.** It stops exmon, renames `exmon.exe` to `exmon.old.exe`, moves its verified copy into place, records the trial in `update\status\trial.json`, and starts exmon.
8. **Health check.** The new version counts as healthy once it has run for 2 minutes **and** connected to the control plane. It logs `UPDATE/healthy` and tells the helper (`state\update-health.json`), which then stops. An update that breaks the control connection is therefore rolled back, so a bad release can't cut devices off from the next one.
9. **Rollback.** If that doesn't happen within `healthMinutes`, the helper stops exmon, renames the new exe to `exmon.failed.exe`, puts `exmon.old.exe` back and starts it. The old version logs `UPDATE/rolled_back version=1.5.0 was not healthy (…)`. If the PC restarts during the trial, the helper picks it up again at boot. The rolled-back manifest stays retained in the device's inbox, but the device ignores it from then on (`CONTROL/applied … 1.5.0 was rolled back on this device`); to try again, publish a new release.

Any failure in exmon's half logs `UPDATE/failed … step=… error=…`. A refusal by the helper is in the Application event log (source `exmon-helper`, "update refused: …"). Either way the running version is untouched. Publish a corrected manifest (with a new `version`) to try again.

### What the helper checks

The helper runs as SYSTEM, so it **trusts nothing exmon wrote**: a compromised exmon could put anything in `update\incoming`. Before it changes anything it checks:

| Check | Against |
|---|---|
| The manifest's signature | its own built-in **release** keys (not exmon's word) |
| Kind, expiry, size limit, field shapes | the manifest itself, and its own `--max-size-mb` |
| Not a replay | the last manifest version **it** applied (its own admin-only records) |
| Newer than installed (unless `allowDowngrade` is signed) | the version the installed `exmon.exe` reports (Program Files is admin-only) |
| Size and SHA-256 | the manifest, measured on its **own copy** in `update\verified`, which exmon can't write |
| Code signature and publisher | Windows' WinVerifyTrust, and the publisher baked into its own command line at install |
| The file name | only `exmon-<version>.exe` inside `update\incoming`; no paths |

A "healthy" report written by exmon can only stop a rollback; it can't make the helper install anything.

## Rolling out by ring

Devices are in rings by their client attributes in Event Grid (see [Intune deployment](deploy/intune.md#4-rings)). A typical rollout:

1. Upload the signed exe and publish the manifest to the canary ring.
2. Watch Log Analytics for `UPDATE/healthy` and `UPDATE/rolled_back`:

   ```kusto
   ExmonEvents_CL
   | where Kind startswith "UPDATE/" and TimeGenerated > ago(1d)
   | summarize arg_max(TimeGenerated, Kind, Detail) by Host
   | summarize count() by Kind
   ```

3. Publish to the next ring, perhaps with `percent: 25`, then 100.

To stop a rollout, stop publishing it to more rings. To roll everyone back, publish the previous version with `allowDowngrade: true`.

## Files

| File | Meaning |
|---|---|
| `C:\Program Files\exmon\exmon.old.exe` | The previous version, kept after a successful update (for a manual rollback). |
| `C:\Program Files\exmon\exmon.failed.exe` | A version that was rolled back. |
| `%ProgramData%\exmon\update\incoming\` | exmon's download and `request.json` for the helper; emptied by the helper. |
| `%ProgramData%\exmon\update\verified\` | The helper's own copy, checked and then moved into Program Files. exmon can't access it. |
| `%ProgramData%\exmon\update\status\trial.json` | The update on trial, and its outcome (written by the helper, readable by exmon). |
| `%ProgramData%\exmon\update\helper-state\state.json` | The last manifest version the helper applied (against replays). |
| `%ProgramData%\exmon\state\update-health.json` | The new version's health report to the helper. |

## Intune and the updater

If exmon is deployed as an Intune Win32 app, its detection rule must only check that the service and exe **exist**, not their version. Otherwise Intune would "repair" every self-updated PC back to the version it packaged. See [Intune deployment](deploy/intune.md).
