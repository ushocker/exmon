# Sending events to Azure Monitor

The `azureMonitor` remote type sends events straight into a Log Analytics workspace through the Azure Monitor **Logs Ingestion API**. There is no agent, forwarder or relay in between: each PC posts its own events over HTTPS.

```
exmon ──HTTPS 443──► data collection rule (DCR) ──► ExmonEvents_CL table in Log Analytics
        token from Entra ID for a "write-only" app
```

## What it needs

This branch has no deployment scripts: it uses Azure resources that already exist. They are the ones the `fleet-control-plane` branch's `deploy/bicep/modules/monitor.bicep` and `deploy/scripts/New-ExmonIngestApp.ps1` create, or any set built by hand to the same shape:

| Piece | Notes |
|---|---|
| Log Analytics workspace | |
| `ExmonEvents_CL` table | Columns in [the table](#the-table). |
| Data collection rule, kind `Direct`, stream `Custom-ExmonEvents` | Has its own ingestion endpoint; no separate data collection endpoint is needed. |
| Entra app + client secret | The "ingest app". |
| Role *Monitoring Metrics Publisher* for the app, on the DCR only | The app's only access. |

### Steps

1. Collect the values for the config:
   - from the DCR (in the Azure portal, open the rule and choose **JSON View**): `properties.endpoints.logsIngestion` is the `endpoint`, and `properties.immutableId` is the `dcrImmutableId`;
   - from the app registration: the **Directory (tenant) ID** and **Application (client) ID**, and a client secret (**Certificates & secrets**).

2. Add the remote to `exmon.json`. The starter config (`--write-default-config`) already has one, named `law`, with placeholder values and `"enabled": false`: fill in the settings and set `"enabled": true`.

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

3. Restart exmon. The startup log shows `remote law: azureMonitor https://… stream Custom-ExmonEvents (app <id>)`; the secret is never logged. Rows appear within a few minutes:

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
| `streamName` | yes | `Custom-` and the stream's name, as declared in the DCR: `Custom-ExmonEvents`. |
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
| `ExmonVersion` | string | the sending exmon's version: `dev` unless the exe was built with `-ldflags "-X exmon/internal/version.Version=…"` |

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

The `clientSecret` is in the config file in plain text, the same trade-off most log forwarders make:

- **It can only write.** The app's only role is Monitoring Metrics Publisher on one DCR. Someone who copies it can add rows to `ExmonEvents_CL`, but can't read, change or delete anything.
- **Every local user can read it on this branch.** `install` gives Users read on `%ProgramData%\exmon\config` ([service permissions](service.md#security)), and on `C:\Program Files\exmon` for an `exmon.json` next to the exe. That is accepted for a small set of test PCs. The `fleet-control-plane` branch makes the config folder admin-only; use that before a wider rollout.
- **Rotate it before it expires:** add a second secret to the app in Entra (**Certificates & secrets**); both work while you update the config everywhere, then delete the old one.
- exmon never prints it: the startup line, errors and `--write-default-config` never contain it.
- **It only goes to the token endpoint.** exmon refuses HTTP redirects from Entra and from the ingestion endpoint (neither redirects), because following one would post the token request, secret included, or the events to another host.

## Cost

Ingestion is charged per GB stored in the workspace; the HTTPS transport is free. See [Data usage](data-usage.md) for volumes per selector choice.
