# Production deployment: step by step

The order to go from nothing to a managed production fleet, with what the dev deployment taught us. The details of each part are on its own page; this page is the checklist that ties them together:

| Step | Where | Details |
|---|---|---|
| 1. Network allow-list | your firewall / proxy / filters | [below](#1-network-allow-list) |
| 2. Exmon's own certificate authority | Intune Cloud PKI | [Intune 1–2](intune.md#1-a-certificate-authority-only-for-exmon) |
| 3. Azure resources | Cloud Shell | [Deploying from Cloud Shell](cloud-shell.md) |
| 4. Rings and the sync runbook | Entra, Cloud Shell | [Intune 4](intune.md#4-rings) |
| 5. Build the signing keys into exmon | admin workstation | [Signed documents](../reference/signed-documents.md#adding-a-key-to-exmon) |
| 6. Build and sign the release | admin workstation | [Operations](../operations.md#releasing-a-new-version) |
| 7. The production `exmon.json` | admin workstation | [Configuration](../configuration.md) |
| 8. SCEP profile and Win32 app | Intune | [Intune 3, 5](intune.md#3-scep-certificate-profile) |
| 9. Pilot on the canary ring, then widen | Intune, exmonctl | [below](#9-pilot) |

Do the [test setup](test-install.md) first if you haven't: it exercises the same pieces on one PC, and every problem below showed up there.

## 1. Network allow-list

Do this first. The dev deployment's worst surprise was a network filter that silently dropped every TLS connection to `*.monitor.azure.com` (a common "Microsoft telemetry" block-list entry, because Application Insights uses it). The only symptom was `REMOTE/send_failed remote=law … EOF`.

**From every PC** (as the services, so not through a per-user proxy; set one with `exmon install --proxy` if outbound HTTPS must use one):

| Host | Port | For | Notes |
|---|---|---|---|
| `<namespace>.<region>-1.ts.eventgrid.azure.net` (the `controlHost` output) | 443 | control plane (MQTT over WebSockets) | **No TLS inspection**: the device authenticates with its client certificate inside TLS, which an inspecting proxy breaks (`CONTROL/unreachable reason=tls`). |
| `*.ingest.monitor.azure.com` (the DCR's `…logs.z1.ingest.monitor.azure.com`) | 443 | events to Log Analytics | Often on telemetry block lists: allow it explicitly. |
| `login.microsoftonline.com` | 443 | the ingest app's token | |
| `<storage account>.blob.core.windows.net` (the `updateAllowedHost` output) | 443 | update downloads | |
| your code-signing CA's CRL/OCSP hosts (e.g. `ocsp.digicert.com`, `crl3.digicert.com`) | 80 | the update's code-signature check | Revocation is checked for the whole chain; if these are blocked, updates fail at `step=verify`. |
| `www.msftconnecttest.com` | 80 | network-ready test after start/wake ([Power](../power.md)) | Or point `power.testURL` at an internal page. |
| your probe targets | as configured | probes | |

Test from a PC (any HTTP status code means reachable; `schannel: failed to receive handshake` means blocked by name):

```powershell
curl.exe -sS -o NUL -w "%{http_code}`n" -X POST https://<dcr host>.logs.z1.ingest.monitor.azure.com/
```

```powershell
curl.exe -sS -o NUL -w "%{http_code}`n" https://<controlHost>/mqtt
```

**From admin workstations** additionally: `management.azure.com`, `*.vault.azure.net` and the Event Grid host (exmonctl), and `<storage account>.blob.core.windows.net` for uploads.

## 2. Exmon's own certificate authority

Create the Cloud PKI root and a dedicated **issuing CA `exmon Issuing CA`** ([Intune 1](intune.md#1-a-certificate-authority-only-for-exmon)), and the trusted-certificate profiles ([Intune 2](intune.md#2-trust-the-ca-on-the-devices)). Event Grid trusts every certificate this CA issues (that has a matching enabled client), so don't reuse a general-purpose CA.

Download the issuing CA certificate. Cloud PKI gives a `.cer` (DER); Event Grid takes PEM text. Convert it on your workstation:

```powershell
certutil -encode "exmon Issuing CA.cer" prod-issuing-ca.pem
```

The first line of `prod-issuing-ca.pem` must be `-----BEGIN CERTIFICATE-----`. Note the CA's **SCEP URI** for step 8.

## 3. Azure resources

Follow [Deploying from Cloud Shell](cloud-shell.md) with the [prod differences](cloud-shell.md#prod-differences):

- settings file `~/exmon-prod.env` with `EXMON_ENV=prod`, `EXMON_RG=rg-exmon-prod`, a **prod** admin group (`exmon-admins-prod`, ideally with PIM-eligible membership) and `EXMON_INGEST_APP_NAME=exmon-ingest-prod`; replace `exmon-dev.env` with `exmon-prod.env` in every command;
- ingest secret for 12 months: `-SecretMonths 12`; put it in your password manager straight away;
- the CA from step 2 instead of the YubiKey's (Cloud Shell step 12, uploading `prod-issuing-ca.pem`);
- prod also creates the **Key Vault** with the two signing keys, so `EXMON_OUT_signingVaultName` is set.

Things that caught us in dev:

- **Cloud Shell without a storage account is ephemeral**: the settings file vanishes with the session. Mount storage, or use [Rebuilding the settings file](cloud-shell.md#rebuilding-the-settings-file).
- **Role assignments take time.** The ingest app may get HTTP 403 for up to 30 minutes after the deployment; admin roles need a fresh `az login` after you join the group.
- **Don't add versions to the signing keys** in the portal. To rotate, add a new key ([Rotating a key](../reference/signed-documents.md#rotating-a-key)).

## 4. Rings and the sync runbook

Create the ring groups `exmon-ring-canary`, `exmon-ring-early`, `exmon-ring-broad` ([Intune 4](intune.md#4-rings)). Put a dozen IT PCs in canary.

The runbook is downloaded by Azure Automation from a URL, so it must be reachable **without sign-in**. If the repository is private, a raw GitHub link won't work; host it in the public releases container instead, pinned by commit (from a workstation with the repository):

```powershell
$acct = az storage account list -g rg-exmon-prod --query "[0].name" -o tsv; $commit = git rev-parse --short HEAD
```

```powershell
az storage blob upload --account-name $acct --container-name releases --name "runbook/Sync-ExmonClients-$commit.ps1" --file deploy\automation\Sync-ExmonClients.ps1 --auth-mode login
```

Then in Cloud Shell set the URL and deploy again:

```bash
echo "export EXMON_RUNBOOK_URI=https://<storage account>.blob.core.windows.net/releases/runbook/Sync-ExmonClients-<commit>.ps1" >> ~/exmon-prod.env && source ~/exmon-prod.env && az deployment group create -g $EXMON_RG -n exmon -p deploy/bicep/prod.bicepparam --query properties.provisioningState -o tsv
```

Refresh the outputs (Cloud Shell step 9), then grant the runbook read access to Entra (a Privileged Role Administrator must run this):

```bash
pwsh deploy/scripts/Grant-RunbookGraphPermissions.ps1 -IdentityObjectId $EXMON_OUT_runbookIdentityObjectId
```

## 5. Build the signing keys into exmon

On the admin workstation, signed in with `az login` as a fleet admin (the export reads the public keys only):

```powershell
dist\exmonctl.exe keys export --vault <signingVaultName> --key exmon-config-signing --out internal/signdoc/trust/config/cfg-2026a.pem
```

```powershell
dist\exmonctl.exe keys export --vault <signingVaultName> --key exmon-release-signing --out internal/signdoc/trust/release/rel-2026a.pem
```

Commit both files. Every production build from now on trusts these keys, and only these. Then make the profile `exmonctl-prod.json` ([Operations](../operations.md#once-per-workstation)) with `configKeyId: cfg-2026a` and `releaseKeyId: rel-2026a`, and **rebuild exmonctl** from this commit so its own signature check knows the keys.

## 6. Build and sign the release

From the commit with the keys, **without** `-tags exmondev`:

```powershell
go build -ldflags "-X exmon/internal/version.Version=1.0.0" -o dist\1.0.0\exmon.exe .
```

```powershell
go build -o dist\1.0.0\exmon-helper.exe ./cmd/exmon-helper
```

Sign **both** with your code-signing certificate (or Azure Trusted Signing), and check they are `Valid` ([Operations: sign](../operations.md#2-sign-the-exe)). The publisher CN you sign with is the `--update-publisher` value in step 8.

After install, a PC's `MONITOR control:` log line says which keys the build trusts: it must be `signing keys prod`, never `prod+dev` (a `-tags exmondev` test build).

## 7. The production `exmon.json`

Start from the starter config, as in the test setup:

```powershell
dist\1.0.0\exmon.exe --write-default-config --config dist\prod-package\exmon.json
```

- **`law` remote:** `enabled: true` and the `settings` from the deployment outputs and the ingest app (Cloud Shell prints them: [test setup step 4](test-install.md#4-the-test-pcs-config), with the prod settings file);
- **probes:** delete **every** example target (`your-app.azurefd.net`, `your-sql…`, `yourstorage…`, `your-erp…`, `yourtenant.sharepoint.com`); in dev, a leftover one raised a Sev2 incident every round. Add your real endpoints;
- **`control`:** leave it; `install.cmd` sets it.

Most later changes go out as [cloud configs](../operations.md#changing-settings-cloud-configs) instead of a new package, so this file is the baseline.

## 8. SCEP profile and Win32 app

1. The SCEP profile ([Intune 3](intune.md#3-scep-certificate-profile)): device certificate, `CN={{AAD_Device_ID}}`, **TPM KSP, otherwise fail**. Assign it to the ring groups.
2. The package folder `dist\prod-package`: `exmon.exe` and `exmon-helper.exe` (both signed, from step 6), `exmon.json` (step 7), and `install.cmd`, `uninstall.cmd` from `deploy/intune`. **The helper must be in the package**: `install` sets it up next to exmon, and without it updates and key access don't work.
3. Edit the top of `install.cmd`:

   | Variable | Value |
   |---|---|
   | `CONTROL_HOST` | `controlHost` output |
   | `CERT_ISSUER` | `exmon Issuing CA` |
   | `UPDATE_HOST` | `updateAllowedHost` output |
   | `PUBLISHER` | your code-signing certificate's CN |

4. Package, create the Win32 app and its detection script ([Intune 5](intune.md#5-the-win32-app)). Assign it as **Required** to `exmon-ring-canary` only, for now.

The package contains the ingest secret (in `exmon.json`). Intune encrypts packages, and the config lands in an admin-only folder, but keep `dist\prod-package` out of source control and delete it after uploading.

## 9. Pilot

On a canary PC, after Intune has applied the profile and the app (a sync from **Settings → Accounts → Access work or school → Info → Sync** speeds it up), elevated:

```powershell
Get-ChildItem Cert:\LocalMachine\My | Where-Object { $_.Issuer -like '*exmon Issuing CA*' } | Format-List Subject, NotAfter, HasPrivateKey
```

```powershell
Get-Service exmon, exmon-helper | Format-Table Name, Status, StartType
```

```powershell
Select-String -Path C:\ProgramData\exmon\exmon.log -Pattern 'MONITOR (control|remote)|CONTROL/|REMOTE/send_failed' | Select-Object -Last 10
```

You want: a certificate with `HasPrivateKey True`; `exmon` Running (the helper is normally Stopped: it runs only when needed); `MONITOR control: … signing keys prod`; `CONTROL/connected device=<Entra device id>`; no `REMOTE/send_failed`.

On the workstation (the device appears once the sync runbook has created its client, within the hour, or run `exmonctl sync run`):

```powershell
dist\exmonctl.exe devices --profile exmonctl-prod.json --ring canary
```

And in Log Analytics after 5–15 minutes (the first rows in a new table are slow):

```kusto
ExmonEvents_CL
| where TimeGenerated > ago(1h)
| summarize count() by Host, Kind
```

Then try the control plane on the canary ring: a [command](../operations.md#commands) (`check-now`), a [cloud config](../operations.md#changing-settings-cloud-configs), and the first [release](../operations.md#releasing-a-new-version) (1.0.1). Only then assign the app to `exmon-ring-early`, and later `exmon-ring-broad`.

| On a canary PC | Usually |
|---|---|
| `CONTROL/unreachable reason=no_client_cert` | The SCEP certificate hasn't arrived yet, or its issuer CN isn't `exmon Issuing CA`. exmon connects by itself once it is there. |
| `reason=key_access` | The helper hasn't granted the key yet; it starts the helper itself. Check the helper's event log if it persists. |
| `reason=auth_rejected` | No enabled Event Grid client for this device: it isn't in a ring group, or the runbook hasn't run (`exmonctl sync run`). |
| `reason=tls` | TLS inspection on the path, or the namespace doesn't have the right CA (step 3). |
| `reason=tcp` / `dns` / `timeout` | The network allow-list (step 1). |
| `REMOTE/send_failed remote=law … EOF` | A filter blocking `*.ingest.monitor.azure.com` (step 1). |
| `… 403` on `law` | The ingest app's role assignment isn't active yet (up to 30 minutes), or `ingestPrincipalId` was empty at deployment. |
| `CONTROL/tls_fallback` (once) | Normal on TPMs that can't do RSA-PSS: it uses TLS 1.2. |

More in [Troubleshooting](../troubleshooting.md). Day-to-day work after this is in [Operations](../operations.md).
