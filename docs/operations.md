# Operations: configs, updates and commands (production)

Day-to-day fleet changes, signed with the **Key Vault** keys. Everything here runs on an admin workstation with `exmonctl`; nothing is done on the PCs themselves. The [test setup](deploy/test-install.md) does the same things with a local dev key (`--key-file`); here there is no `--key-file`, and every signature comes from the vault.

| To… | Command | Signed with | Takes effect |
|---|---|---|---|
| Change settings | `exmonctl config publish` | config key | within seconds of the device receiving it (it restarts itself in-process) |
| Install a new version | `exmonctl release publish` | release key | after each device's spread delay; the helper swaps it in and rolls back if it isn't healthy |
| Run a check now | `exmonctl cmd send` | config key | immediately |

Configs and releases are **retained**: a device that is offline gets them when it reconnects, and a new device gets them when it first connects. Commands are not.

## Before you start

### Once per workstation

1. **Tools:** the [Azure CLI](https://learn.microsoft.com/cli/azure/install-azure-cli-windows) (`winget install -e --id Microsoft.AzureCLI`), Go, and the repository.
2. **Build exmonctl from the same commit as the exmon release your fleet runs:**

   ```powershell
   go build -o dist\exmonctl.exe ./cmd/exmonctl
   ```

   exmonctl validates cloud configs with exmon's own rules and checks every Key Vault signature against the production keys built into it (`internal/signdoc/trust`). Built from older or newer source, it may accept a config the fleet refuses, or refuse one it would accept. Never build it with `-tags exmondev` for production use.

3. **The profile** `exmonctl-prod.json`, in the folder you run exmonctl from (git-ignored). In Cloud Shell, with the prod settings file loaded (`source ~/exmon-prod.env`, see [Deploying from Cloud Shell](deploy/cloud-shell.md)):

   ```bash
   jq -n --arg sub "$(az account show --query id -o tsv)" --arg rg "$EXMON_RG" --arg ns "$EXMON_OUT_namespace" --arg host "$EXMON_OUT_controlHost" --arg vault "$EXMON_OUT_signingVaultName" --arg aa "$EXMON_OUT_automationAccount" '{subscription:$sub, resourceGroup:$rg, namespace:$ns, host:$host, vault:$vault, configKey:"exmon-config-signing", configKeyId:"cfg-2026a", releaseKey:"exmon-release-signing", releaseKeyId:"rel-2026a", automationAccount:$aa, runbook:"Sync-ExmonClients"}'
   ```

   `configKeyId` and `releaseKeyId` are the names of the `.pem` files you exported into `internal/signdoc/trust` ([Production deployment](deploy/production.md#5-build-the-signing-keys-into-exmon)). All the commands below assume `--profile exmonctl-prod.json`.

### Each session

Your account needs the fleet-admin group's roles: *Key Vault Crypto User* to sign, *EventGrid TopicSpaces Publisher/Subscriber* to publish and read statuses, *Reader* on the namespace to list devices. If membership is eligible through PIM, activate it first, **then** sign in, so the token carries it:

```powershell
az login
```

```powershell
dist\exmonctl.exe devices --profile exmonctl-prod.json --ring canary
```

If `devices` lists the canary devices with their versions, you are ready. A `warning: no statuses` line means the MQTT sign-in failed (Publisher/Subscriber roles missing or not yet active).

## Changing settings: cloud configs

A cloud config is a JSON file with just the settings to change, shaped like `exmon.json`. It is layered on top of each device's local `exmon.json`; [what it can and can't set](control-plane.md#what-a-cloud-config-cant-set).

**Each publish replaces the previous cloud config completely.** Keys it leaves out fall back to the local `exmon.json`, not to the previous cloud config. So keep **one file per environment in source control** (e.g. `configs/prod.json`), change it, and publish the whole file every time.

### 1. Edit and check

Edit `configs/prod.json`, for example:

```json
{
  "probe": {
    "intervalSeconds": 300,
    "endpoints": [ { "name": "ERP", "target": "erp.corp.example", "port": 443 } ]
  },
  "control": { "chirpMinutes": 15 }
}
```

Validate and sign it without publishing. This applies exactly the rules a device does, and signs with Key Vault:

```powershell
dist\exmonctl.exe config publish --profile exmonctl-prod.json --file configs\prod.json --ring canary --dry-run
```

### 2. Canary first

```powershell
dist\exmonctl.exe config publish --profile exmonctl-prod.json --file configs\prod.json --ring canary
```

Each device verifies it, tries it, restarts itself in-process (the service stays Running) and **commits it after 2 minutes healthy**, or puts the previous one back by itself. Watch for it in Log Analytics after a few minutes:

```kusto
ExmonEvents_CL
| where TimeGenerated > ago(1h) and Kind in ("CONTROL/applied", "CONTROL/config_committed", "CONTROL/failed", "CONTROL/rejected")
| where Detail has "exmon-config" or Kind == "CONTROL/config_committed"
| summarize arg_max(TimeGenerated, Kind, Detail) by Host
| summarize count() by Kind
```

Everything should end in `CONTROL/config_committed`. `CONTROL/failed` means a device refused it (the `Detail` says why: usually a value its local config makes invalid). A revert shows in the device log as `cloud config version … didn't run healthily … reverted`.

### 3. The other rings

```powershell
dist\exmonctl.exe config publish --profile exmonctl-prod.json --file configs\prod.json --ring early
```

```powershell
dist\exmonctl.exe config publish --profile exmonctl-prod.json --file configs\prod.json --ring broad
```

Rings are independent: a device takes whatever was last published to **its** inbox. Publishing the same file to all rings leaves the whole fleet on one config.

### Undoing a change

- **Go back to the previous settings:** publish the previous version of the file again (from source control). It gets a new version number, so devices accept it.
- **Remove the cloud layer altogether** (devices run only their local `exmon.json`): publish an empty object, `{}`.
- **Stop new devices getting a config** without touching devices that already have it: `exmonctl retained clear --kind config --ring <ring>`.

A device applies at most one config or release every 5 minutes; a second one inside that gap is refused (`CONTROL/rejected … too soon`) and applies when it is sent again.

### Rotating the Azure Monitor secret

The ingest secret lives in the `law` remote's `settings`, which a cloud config can set, so it can be rotated without touching PCs:

1. Add a second secret to the ingest app (Cloud Shell): `pwsh deploy/scripts/New-ExmonIngestApp.ps1 -Name exmon-ingest-prod -AddSecret -SecretMonths 12`. Both secrets now work.
2. Put the whole `law` remote, with the new secret, into `configs/prod.json` (`remotes` is a list and replaces the local one, so include every remote you want, without `spoolDir`), and publish it to every ring as above.
3. Check every device has it: `configVersion` in its status (`exmonctl devices` shows the last status; Log Analytics shows `CONTROL/config_committed`).
4. Delete the old secret in Entra (**App registrations → exmon-ingest-prod → Certificates & secrets**).

Keep the file with the secret out of source control (or keep a template there and add the secret only when publishing). The signed document carries it in clear text to each device's inbox, over TLS, readable only by that device and the fleet admins.

## Releasing a new version

### 1. Build

Production builds have **no** `-tags exmondev`, and are built from a commit whose `internal/signdoc/trust` holds the production public keys:

```powershell
go build -ldflags "-X exmon/internal/version.Version=1.5.0" -o dist\1.5.0\exmon.exe .
```

Version numbers are `major.minor.patch` and must always go up (a device ignores a release that isn't newer than what it runs, unless you deliberately allow a downgrade).

Only `exmon.exe` is updated over the air. `exmon-helper.exe` changes only through the Intune package ([below](#when-the-helper-changes)).

### 2. Sign the exe

Devices require a valid Authenticode signature from your publisher (`--update-publisher` at install). Sign with your code-signing certificate or Azure Trusted Signing, for example:

```powershell
signtool sign /fd SHA256 /tr http://timestamp.digicert.com /td SHA256 /n "Contoso Ltd" dist\1.5.0\exmon.exe
```

```powershell
Get-AuthenticodeSignature dist\1.5.0\exmon.exe | Format-List Status, SignerCertificate
```

`Status` must be `Valid`.

### 3. Upload and publish to canary

`deploy/scripts/Publish-Release.ps1` checks the signature, uploads the exe (refusing to overwrite an existing version: released versions are immutable) and publishes the manifest signed with the release key:

```powershell
pwsh deploy\scripts\Publish-Release.ps1 -Version 1.5.0 -File dist\1.5.0\exmon.exe -StorageAccount <storage account> -Ring canary -SpreadMinutes 30 -ProfilePath exmonctl-prod.json -Exmonctl dist\exmonctl.exe
```

The storage account is the first part of `updateAllowedHost` (`<account>.blob.core.windows.net`), or `az storage account list -g rg-exmon-prod --query "[0].name" -o tsv`.

Each device waits for its own point within the spread, downloads, checks the hash and signature, and hands the exe to `exmon-helper`, which checks everything again, swaps it in and restarts exmon. The new version reports healthy after running 2 minutes **with the control plane connected**; if it doesn't within `healthMinutes` (10), the helper puts the old version back.

### 4. Watch

```powershell
dist\exmonctl.exe rollout status --profile exmonctl-prod.json
```

```kusto
ExmonEvents_CL
| where Kind startswith "UPDATE/" and TimeGenerated > ago(1d)
| summarize arg_max(TimeGenerated, Kind, Detail) by Host
| summarize count() by Kind
```

You want `UPDATE/healthy` everywhere and no `UPDATE/rolled_back` or `UPDATE/failed`. The helper's own messages are in each PC's Application event log (source `exmon-helper`).

### 5. The other rings

Publish the **same file and URL** to the next ring; nothing is uploaded again:

```powershell
dist\exmonctl.exe release publish --profile exmonctl-prod.json --version 1.5.0 --file dist\1.5.0\exmon.exe --url https://<storage account>.blob.core.windows.net/releases/1.5.0/exmon.exe --ring early --spread 60
```

For a large ring, start with part of it: `--percent 25`, then publish again with `--percent 100`. The same devices stay chosen as you raise it.

### Stopping or reversing a rollout

- **Stop:** don't publish to more rings. To keep a ring's offline devices from getting it later, clear the retained manifest: `exmonctl retained clear --kind release --ring <ring>`. (A device that is online and already waiting for its spread slot still installs it, unless it restarts first.)
- **Roll back a ring:** publish the previous version with `--allow-downgrade`. Its exe is still in the container; exmonctl needs the same file locally to compute its hash, so keep every released exe (e.g. `dist\1.4.0\exmon.exe`, or download it from the container):

  ```powershell
  dist\exmonctl.exe release publish --profile exmonctl-prod.json --version 1.4.0 --file dist\1.4.0\exmon.exe --url https://<storage account>.blob.core.windows.net/releases/1.4.0/exmon.exe --ring canary --spread 0 --allow-downgrade
  ```

A device that rolled back by itself ignores that manifest from then on; fix the problem and release a new version.

### When the helper changes

`exmon-helper.exe` is never updated over the air (it is the part that checks updates, so it can't be replaced by one). When it changes, or when the **release** signing key rotates (the helper checks manifests with its own built-in keys), build a new Intune package with the new `exmon.exe` and `exmon-helper.exe` and update the Win32 app ([Intune](deploy/intune.md#5-the-win32-app)). Its install runs `exmon install` again, which replaces both.

## Commands

```powershell
dist\exmonctl.exe cmd send --profile exmonctl-prod.json --command check-now --device <device id>
```

| Command | Does on the device |
|---|---|
| `report-status` | publishes its status now |
| `trace-now` | runs a path trace to every probe target now |
| `check-now` | runs a probe round now |

Use `--ring <ring>` instead of `--device` for a whole ring. Commands are not retained: an offline device gets it if it reconnects within its session expiry (8 hours), otherwise never. The device id is the certificate CN, which in production is the **Entra device id** (`exmonctl devices` lists them).

## When something goes wrong

| Message | Meaning |
|---|---|
| `devices would reject this document (…)` | The vault key's current version doesn't match the `.pem` built into exmon under that key id, or exmonctl was built from different source than the fleet. Did someone add a key version? See [Rotating a key](reference/signed-documents.md#rotating-a-key). Nothing was published. |
| `Key Vault: 403 …` | Your account lacks *Key Vault Crypto User* on the vault (or the PIM activation hasn't reached the token: `az logout`, `az login`). |
| `warning: no statuses: …` | The MQTT sign-in failed: Publisher/Subscriber roles missing, or not yet active. |
| `connect to …: …` on publish | Same as above, or the network blocks `*.ts.eventgrid.azure.net:443`. |
| Device: `CONTROL/rejected … too soon` | Two configs/releases within 5 minutes; send it again later. |
| Device: `CONTROL/rejected … older than the applied version` | A document older than the last one it applied (e.g. two admins publishing at once). Publish again: each publish gets a new, newer version. |
| Device: `UPDATE/failed … step=…` | `download` or `verify` (hash, size, code signature) failed; the error says which. The running version is untouched. The helper's own refusals are in the Application event log (source `exmon-helper`, "update refused: …"). |
| Device: `UPDATE/rolled_back` | The new version didn't report healthy: it crashed, or couldn't connect to the control plane. Look at the device log just after the swap. |

See also [Troubleshooting](troubleshooting.md), [exmonctl](exmonctl.md) for every option, and [Control plane](control-plane.md) for how devices handle documents.
