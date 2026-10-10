# Sending events to Azure Monitor

The `azureMonitor` remote type sends events straight into a Log Analytics workspace through the Azure Monitor **Logs Ingestion API**. There is no agent, forwarder or relay in between: each PC posts its own events over HTTPS.

```
exmon ──HTTPS 443──► data collection rule (DCR) ──► ExmonEvents_CL table in Log Analytics
        token from Entra ID for a "write-only" app
```

## What it needs

| Piece | Made by | Notes |
|---|---|---|
| Log Analytics workspace | `deploy/bicep` (or an existing one) | |
| `ExmonEvents_CL` table | `deploy/bicep` | Columns below. |
| Data collection rule, kind `Direct` | `deploy/bicep` | Has its own ingestion endpoint; no separate data collection endpoint is needed. |
| Entra app + secret | `deploy/scripts/New-ExmonIngestApp.ps1` | Bicep can't create secrets. |
| Role *Monitoring Metrics Publisher* for the app, on the DCR only | `deploy/bicep` (`ingestPrincipalId`) | The app's only access. |

### Steps

1. Create the app and its secret (Azure CLI signed in):

   ```bash
   pwsh deploy/scripts/New-ExmonIngestApp.ps1 -Name exmon-ingest-prod -SecretMonths 12
   ```

   It prints `tenantId`, `clientId`, `clientSecret` and the service principal's object id.

2. Put the object id in `deploy/bicep/prod.bicepparam` (`ingestPrincipalId`) and deploy:

   ```bash
   az deployment group create -g rg-exmon-prod -f deploy/bicep/main.bicep -p deploy/bicep/prod.bicepparam
   ```

   The outputs are the `endpoint`, `dcrImmutableId` and `streamName` for the config.

3. Add the remote to `exmon.json`:

   ```json
   "remotes": [
     { "name": "law", "type": "azureMonitor",
       "settings": {
         "endpoint": "https://exmon-prod-dcr-abcd.eastus-1.ingest.monitor.azure.com",
         "dcrImmutableId": "dcr-0123456789abcdef0123456789abcdef",
         "streamName": "Custom-ExmonEvents",
         "tenantId": "<tenant id>",
         "clientId": "<app id>",
         "clientSecret": "<secret>"
       },
       "batchSize": 200,
       "include": [
         { "kinds": ["INCIDENT/*"] },
         { "kinds": ["PROBE/down", "PROBE/up", "NIC/up", "NIC/down", "VPN/connected", "VPN/disconnected", "POWER/not_ready"] }
       ] }
   ]
   ```

4. Restart exmon. The startup log shows `remote law: azureMonitor https://… stream Custom-ExmonEvents (app <id>)`; the secret is never logged. Rows appear within a few minutes:

   ```kusto
   ExmonEvents_CL
   | where TimeGenerated > ago(1h)
   | project TimeGenerated, Host, Kind, Subject, Detail, Severity
   | order by TimeGenerated desc
   ```

New role assignments can take up to 30 minutes to apply. Until then sends fail with `HTTP 403` and are retried; nothing is lost.

## Settings

| Key | Required | Meaning |
|---|---|---|
| `endpoint` | yes | The DCR's logs ingestion endpoint (`https://…ingest.monitor.azure.com`). |
| `dcrImmutableId` | yes | `dcr-` and 32 hex digits. |
| `streamName` | yes | `Custom-ExmonEvents` with the Bicep in this repository. |
| `tenantId` | yes | Entra tenant (directory) id. |
| `clientId` | yes | The ingest app's application (client) id. |
| `clientSecret` | yes | The ingest app's secret. Never written to any log. |
| `timeoutSeconds` | no | Per HTTP call, 5–120 (default 30). |
| `authority` | no | Entra endpoint for sovereign clouds; default `https://login.microsoftonline.com`. |
| `scope` | no | Token scope for sovereign clouds; default `https://monitor.azure.com//.default`. |

The remote's queue settings (`batchSize`, `maxSpoolMB`, retries …) work as for any remote; see [Configuration](configuration.md#remotes-where-events-are-forwarded). A `batchSize` of 100–500 suits this type: each call can carry up to about 1 MB, and exmon splits larger batches itself.

## The table

| Column | Type | From |
|---|---|---|
| `TimeGenerated` | datetime | when the event happened (UTC) |
| `Host` | string | computer name |
| `RecordId` | string | the record's unique id |
| `Source` | string | `wifi`, `nic`, `probe`, `incident`, … |
| `Kind` | string | e.g. `INCIDENT/triggered` ([all kinds](reference/log-format.md#event-kinds)) |
| `Subject` | string | adapter, probe or tunnel name |
| `Detail` | string | the log line's detail, cut at 32 KB |
| `Severity` | int | 1–5 on incident events, else 0 |
| `Held` | boolean | always false today (held events are never sent) |
| `ExmonVersion` | string | the sending exmon's version |

Delivery is at least once. After a crash or a failed part of a split batch, a row can arrive twice; remove duplicates in queries with `summarize arg_max(TimeGenerated, *) by RecordId`.

## When sending fails

| Answer | What exmon does |
|---|---|
| `204` | Done. |
| `401` | Gets a new token and retries. |
| `403` | Retries. The role assignment is missing or still applying. |
| `413` (too large) | Splits the call in half and retries once. |
| `429` / `5xx` | Retries, waiting at least as long as `Retry-After` asks. |
| `400` and other client errors | The data or the DCR/stream name is wrong, so retrying can't help: the batch is dropped and `REMOTE/dropped` is logged. |
| Token errors (e.g. wrong or expired secret) | Retries with backoff, keeping the events on disk until the secret is fixed. `REMOTE/send_failed` shows Entra's error code, such as `AADSTS7000215` (invalid secret). |

## About the secret

The `clientSecret` is in the config file in plain text. That is a deliberate trade-off, the same one most log forwarders make:

- **It can only write.** The app's only role is Monitoring Metrics Publisher on one DCR. Someone who copies it can add rows to `ExmonEvents_CL`, but can't read, change or delete anything.
- **Only administrators can read it.** The service's config folder is admin-only ([service permissions](service.md#security)). A copy in a user-readable folder is a risk: use the machine-wide config location.
- **Rotate it before it expires:** `New-ExmonIngestApp.ps1 -AddSecret` adds a second secret, both work while you update the config everywhere, then remove the old one in Entra.
- exmon never prints it: the startup line, errors and `--write-default-config` never contain it.

## Cost

Ingestion is charged per GB stored in the workspace; the HTTPS transport is free. See [Data usage](data-usage.md) for volumes per selector choice.
