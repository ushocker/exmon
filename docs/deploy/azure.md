# Azure deployment

Everything exmon needs in Azure is in `deploy/bicep`, deployed once per environment into its own resource group. A few steps Bicep can't do are scripts in `deploy/scripts`.

| Resource | Module | What for |
|---|---|---|
| Log Analytics workspace, `ExmonEvents_CL` table, data collection rule | `monitor.bicep` | Where devices send events ([Azure Monitor](../azure-monitor.md)). |
| Event Grid namespace (MQTT broker), CA certificate, client group, topic spaces, permission bindings, admin roles; dev: test client pool | `eventgrid.bicep` | The [control plane](../control-plane.md). |
| Key Vault (Premium/HSM) with two P-256 keys | `keyvault.bicep` | Signing configs/commands and releases ([Signed documents](../reference/signed-documents.md)). |
| Storage account, public read-only `releases` container | `storage.bicep` | Release downloads ([Updates](../updates.md)). |
| Prod: Automation account, runbook, hourly schedule, custom role | `automation.bicep` | Keeping Event Grid clients in step with Intune/Entra ring groups. |

## Before you start

- Azure CLI 2.60 or later with Bicep (`az bicep upgrade`), signed in with rights to create resources and role assignments in the resource group (Owner, or Contributor plus User Access Administrator).
- An **Entra group of fleet admins**. Its object id is `adminGroupId`. Members may sign documents, publish to devices, read clients and upload releases. Consider making membership eligible through PIM.
- The **issuing CA certificate** for device certificates, as PEM (it can be added in a second deployment):
  - dev: the YubiKey test CA ([Device certificates](../enrollment.md#the-test-ca-on-a-yubikey));
  - prod: the Cloud PKI issuing CA for exmon ([Intune](intune.md#1-a-certificate-authority-only-for-exmon)).

## Steps

The easiest way to run these is [from Azure Cloud Shell](cloud-shell.md): the same steps as paste-in commands, with every value saved for the next step. The steps below are the reference.

### 1. Resource groups

```bash
az group create -n rg-exmon-dev -l eastus
```

```bash
az group create -n rg-exmon-prod -l eastus
```

### 2. The ingest app (write-only Azure Monitor credential)

```bash
pwsh deploy/scripts/New-ExmonIngestApp.ps1 -Name exmon-ingest-prod -SecretMonths 12
```

Note the tenant id, client id, secret, and the service principal's object id. Export the object id as `EXMON_INGEST_SP_ID` (step 3). The secret is shown only once; keep it in your password manager until it is in the exmon config.

### 3. Parameters

`deploy/bicep/dev.bicepparam` and `prod.bicepparam` read the tenant-specific values from environment variables, which must be **exported** in the shell that runs `az deployment`:

| Variable | Parameter | Value |
|---|---|---|
| `EXMON_ADMIN_GROUP_ID` (required) | `adminGroupId` | The fleet-admin group's object id. |
| `EXMON_CA_PEM` | `issuingCaPem` | The issuing CA certificate's PEM text. Empty deploys everything except the CA (no device can connect until it is set). |
| `EXMON_INGEST_SP_ID` | `ingestPrincipalId` | From step 2. Empty skips its role assignment. |
| `EXMON_RUNBOOK_URI` (prod) | `runbookUri` | A raw URL of `deploy/automation/Sync-ExmonClients.ps1`, pinned to a commit; Automation downloads it from there. Empty: no sync runbook. |

Edit the files themselves for the rest:

| Parameter | Value |
|---|---|
| `deployKeyVault` | `true` in prod. In dev, `false` to sign with a local dev key, or `true` for a dev vault. |
| `testPoolSize` | Dev: how many `exmon-test-NN` clients. |
| `throughputUnits` | 1 per ~3,300 devices (retained documents are the limit), up to 20. |

### 4. Deploy

```bash
az deployment group create -g rg-exmon-prod -n exmon -p deploy/bicep/prod.bicepparam
```

The outputs are what you need next:

| Output | Goes to |
|---|---|
| `azureMonitorEndpoint`, `azureMonitorDcrImmutableId`, `azureMonitorStreamName` | the `azureMonitor` remote's settings |
| `controlHost` | `exmon install --control-host`, and `host` in `exmonctl.json` |
| `updateAllowedHost` | `exmon install --update-hosts` |
| `releasesUrl` | release download URLs |
| `namespace`, `signingVaultName`, `automationAccount` | `exmonctl.json` |
| `runbookIdentityObjectId` | step 5 |

### 5. Prod: let the sync runbook read Entra

```bash
pwsh deploy/scripts/Grant-RunbookGraphPermissions.ps1 -IdentityObjectId <runbookIdentityObjectId>
```

This grants read-only `Device.Read.All` and `GroupMember.Read.All`; it needs a Privileged Role Administrator.

### 6. Build the signing keys into exmon

```bash
exmonctl keys export --vault <signingVaultName> --key exmon-config-signing --out internal/signdoc/trust/config/cfg-2026a.pem
```

```bash
exmonctl keys export --vault <signingVaultName> --key exmon-release-signing --out internal/signdoc/trust/release/rel-2026a.pem
```

Commit them and build the release you deploy. Put the same ids (`cfg-2026a`, `rel-2026a`) in `exmonctl.json` as `configKeyId` and `releaseKeyId`.

### 7. exmonctl profile

Create `exmonctl.json` from the outputs (see [exmonctl](../exmonctl.md#the-profile)), then check:

```bash
exmonctl devices
```

## What the broker allows

| Who | May | How |
|---|---|---|
| A device (client group `devices`: `attributes.role = 'device'`) | publish `exmon/v1/device/<its own name>/status` and `/outbox`; subscribe `/inbox/#` | topic templates with `${client.authenticationName}`; it can't reach another device's topics |
| Fleet admins (Entra group) | publish to any device's inbox; read any status and outbox | RBAC on the `admin` topic space (*EventGrid TopicSpaces Publisher/Subscriber*) |
| Anyone else | nothing | |

A device is accepted only if its certificate **chains to the namespace's CA** and **a client with its name exists and is enabled**. Event Grid doesn't check certificate revocation lists, so to cut a device off, disable or delete its client:

```bash
az resource update --ids /subscriptions/<sub>/resourceGroups/rg-exmon-prod/providers/Microsoft.EventGrid/namespaces/<namespace>/clients/<device id> --set properties.state=Disabled
```

In prod the sync runbook disables clients whose device left every ring group or was disabled in Entra, and deletes them 30 days later.

## Rotating things

| What | How |
|---|---|
| Ingest app secret | `New-ExmonIngestApp.ps1 -AddSecret`, publish a cloud config with the new secret ([control plane](../control-plane.md#rotating-the-azure-monitor-secret)), check every device reports the new `configVersion`, then remove the old secret in Entra. |
| Signing keys | New key version in the vault, export it under a new id **alongside** the old file, release that build fleet-wide, then sign with the new id ([details](../reference/signed-documents.md#rotating-a-key)). |
| Issuing CA | Add the new CA as a second `caCertificates` entry, let Intune issue new certificates, then remove the old CA. |

## Capacity and cost

- **Event Grid:** one throughput unit allows about 10,000 connected clients and 10,000 retained messages. Each device has up to three retained messages (status, config, release), so plan **one unit per ~3,300 devices**. Control traffic is a few messages per device per day, inside the free million operations a month. A unit is about $29 a month.
- **Log Analytics:** charged per GB ingested; see [Data usage](../data-usage.md).
- **Key Vault, storage, Automation:** a few dollars a month.

## Verified on the first deployment

The details that couldn't be checked when the templates were written were confirmed on the first dev deployment (October 2026):

- Event Grid accepts the CA certificate as PEM text (`caCertificates`);
- the *EventGrid TopicSpaces Publisher/Subscriber* role ids in `eventgrid.bicep` are the built-in ones;
- exmonctl's Entra token sign-in (`OAUTH2-JWT`) works without extra namespace settings;
- the WebSocket path is `/mqtt` (exmon's `control.path` default);
- `SubjectMatchesAuthenticationName` matches the certificate's CN (`exmon-test-01`).
