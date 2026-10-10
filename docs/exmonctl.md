# exmonctl

`exmonctl` is the administrator's tool for an exmon fleet. It runs on an admin workstation, never on monitored PCs. It signs documents with the Key Vault keys and publishes them over MQTT, lists devices, and manages the YubiKey test CA.

This page lists its commands and options. For the production procedures (publishing a config ring by ring, releasing and rolling back a version), see [Operations](operations.md). Before publishing anything signed with Key Vault, exmonctl checks the signature against the production keys built into it, and refuses with "devices would reject this document" if they don't match; build it from the same source as the exmon your fleet runs.

```bash
go build -o exmonctl.exe ./cmd/exmonctl
```

It needs the [Azure CLI](https://learn.microsoft.com/cli/azure/) signed in (`az login`). exmonctl stores no credentials: every call uses a fresh token from `az`, so MFA, Conditional Access and PIM apply. What you can do depends on your Azure roles:

| Action | Role |
|---|---|
| Sign configs, commands, releases | *Key Vault Crypto User* on the signing vault |
| Publish, read statuses | *EventGrid TopicSpaces Publisher* and *Subscriber* on the namespace's `admin` topic space |
| List devices | *Reader* on the Event Grid namespace |
| `sync run` | *Automation Job Operator* on the Automation account |

`deploy/bicep` grants the first two to an admin group you name.

## The profile

Commands read the environment's Azure names from `exmonctl.json` in the current folder (or `--profile <file>`). It holds no secrets:

```json
{
  "subscription": "00000000-0000-0000-0000-000000000000",
  "resourceGroup": "rg-exmon-prod",
  "namespace": "exmon-prod-eg",
  "host": "exmon-prod-eg.eastus-1.ts.eventgrid.azure.net",
  "vault": "exmonprodkvabc123",
  "configKey": "exmon-config-signing",
  "configKeyId": "cfg-2026a",
  "releaseKey": "exmon-release-signing",
  "releaseKeyId": "rel-2026a",
  "automationAccount": "exmon-prod-automation",
  "runbook": "Sync-ExmonClients"
}
```

The deployment prints these values (see [Azure deployment](deploy/azure.md)). Keep one profile per environment, e.g. `exmonctl-dev.json` and `exmonctl-prod.json`. `configKeyId` and `releaseKeyId` must match the file names of the public keys built into exmon ([Signed documents](reference/signed-documents.md#adding-a-key-to-exmon)).

Every command also takes `--profile`, `--host` (override the broker) and `--key-file` (sign with a local dev key instead of Key Vault; test builds only).

## Commands

### Devices

```bash
exmonctl devices --ring canary
```

Lists the namespace's devices (ring, enabled/disabled) with their last status: online, exmon version, time. `ONLINE` is `stale` when a device still claims to be online but hasn't reported for `--stale-minutes` (default 75; devices republish every `control.statusMinutes`, 15 by default, so keep it above twice that): its goodbye and the broker's last will both failed to arrive.

```bash
exmonctl rollout status
```

Counts devices by the version they report, e.g. `412  1.5.0`, `31  1.4.0`, `5  1.4.0 (offline)`.

### Cloud config

```bash
exmonctl config publish --file prod-config.json --ring canary
```

`prod-config.json` is just the settings, shaped like `exmon.json`:

```json
{ "probe": { "intervalSeconds": 300,
             "endpoints": [ { "name": "ERP", "target": "erp.corp.example", "port": 443 } ] } }
```

It is validated exactly as a device will (allowed keys, cloud limits, every rule), signed with the config key, and published **retained** to each device's `inbox/config`, so devices that are offline get it when they reconnect. `--dry-run` only validates and signs; `--expires-days` (default 365) is how long devices that join later can still receive it. It keeps working after that. See [Cloud config](control-plane.md#cloud-config).

### Releases

```bash
exmonctl release publish --version 1.5.0 --file dist\exmon.exe --url https://exmonreleasesprod.blob.core.windows.net/releases/1.5.0/exmon.exe --ring canary --spread 60
```

Upload the exe first (see `deploy/scripts/Publish-Release.ps1`, which does both). exmonctl checks the exe's code signature, computes its size and SHA-256, signs the manifest with the **release** key and publishes it retained to `inbox/release`. Options: `--percent 25` (share of the ring), `--spread 60` (minutes), `--min-version 1.3.0`, `--allow-downgrade` (for a rollback), `--skip-signature-check` (unsigned test builds only). See [Updates](updates.md).

### Commands

```bash
exmonctl cmd send --command trace-now --device exmon-test-01
```

`report-status`, `trace-now` or `check-now`, signed with the config key. Not retained; the broker drops it if the device doesn't collect it within an hour.

### Clearing retained documents

```bash
exmonctl retained clear --kind release --ring canary
```

Deletes the retained config or release from the inboxes, so devices joining later don't get it. Devices keep whatever they already applied.

### Sync

```bash
exmonctl sync run
```

Starts the `Sync-ExmonClients` runbook now (it normally runs hourly), so a device just added to a ring group can connect without waiting.

### Test CA and keys

| Command | Does |
|---|---|
| `exmonctl ca init` | Make the YubiKey's slot 9c key a proper CA ([Device certificates](enrollment.md#the-test-ca-on-a-yubikey)). |
| `exmonctl ca sign-csr --csr x.csr` | Sign a test device's request with the YubiKey CA. |
| `exmonctl keys export --vault … --key … --out …` | Write a Key Vault key's public half for building into exmon. |
| `exmonctl keys dev-init --private %USERPROFILE%\.exmon\dev.pem` | Make a local signing key for `-tags exmondev` test builds. |
