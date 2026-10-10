# Test setup: from scratch to a managed test PC

This walks through a complete **dev** environment:
- a YubiKey test CA;
- the Azure resources;
- one test PC that sends events to Log Analytics, connects to the control plane, and takes a cloud config and an update.

Every command is here, in order. Production differs only in where certificates and installs come from ([Intune](intune.md)).

Plan about two hours the first time.

## What you need

| | |
|---|---|
| **Admin workstation** | Windows with Go 1.27+, Git, [Azure CLI](https://learn.microsoft.com/cli/azure/install-azure-cli-windows) with Bicep, PowerShell 7, and [YubiKey Manager CLI](https://developers.yubico.com/yubikey-manager/) (`ykman`). |
| **YubiKey** | A YubiKey 5 series (PIV). A spare one: step 2 erases its PIV application. |
| **Azure** | A subscription where you can create resource groups, role assignments and an app registration; an Entra group for fleet admins with you in it. |
| **Test PC** | Windows 10/11 with a TPM 2.0, or a VM (then use a software key, below), and local admin rights. Nothing else needs installing on it. |

Neither PC needs to be Entra joined or Intune enrolled for the dev setup: the test PC's certificate comes from the YubiKey CA and its name from the test client pool. Only your **sign-in** matters: if a Conditional Access policy requires a compliant or joined device for Azure management, `az login` on an unmanaged workstation is refused, so use [Cloud Shell](cloud-shell.md) for the Azure steps and run `exmonctl` from a managed PC. (Production devices must be Entra joined and Intune managed; see [Intune](intune.md).)

Commands run in PowerShell. "Elevated" means **Run as administrator**.

## 1. Build exmon and exmonctl (admin workstation)

```powershell
git clone https://github.com/ushocker/exmon.git
cd exmon
git switch fleet-control-plane
```

Make a **development signing key**, so you can sign test configs and releases without Key Vault. The private key stays in your profile folder, outside the repository:

```powershell
go run ./cmd/exmonctl keys dev-init --private $env:USERPROFILE\.exmon\dev.pem
```

Build exmonctl, and two exmon versions (the second one is for the update test). `-tags exmondev` makes these builds trust the dev key; never deploy such a build to real users.

```powershell
go build -o dist\exmonctl.exe ./cmd/exmonctl
```

```powershell
go build -tags exmondev -ldflags "-X exmon/internal/version.Version=0.1.0" -o dist\0.1.0\exmon.exe .
```

```powershell
go build -tags exmondev -o dist\exmon-helper.exe ./cmd/exmon-helper
```

The helper is the small privileged service that applies updates and lets exmon use its certificate key; it must be built with the same `-tags exmondev`, because it checks release manifests with its own built-in keys.

```powershell
go build -tags exmondev -ldflags "-X exmon/internal/version.Version=0.1.1" -o dist\0.1.1\exmon.exe .
```

## 2. Set up the YubiKey test CA (admin workstation)

The CA's private key is created on the YubiKey and can't be copied off it. Each signature needs the PIN and a touch. [Cloud Shell step 11](cloud-shell.md#11-make-the-yubikey-test-ca-on-your-windows-workstation) has the same steps with installing `ykman` and a check after each one.

Run this step in an **elevated** PowerShell, elevated as your own account: ykman needs administrator rights on Windows, and `exmonctl ca init` looks for the YubiKey's certificate in the Personal store of the account running it.

1. Plug in the YubiKey and **reset PIV**. This erases any PIV keys on it:

   ```powershell
   ykman piv reset
   ```

2. **Change the PIN** (default `123456`), the **PUK** (default `12345678`), and the **management key** (stored on the key, protected by the PIN):

   ```powershell
   ykman piv access change-pin
   ```

   ```powershell
   ykman piv access change-puk
   ```

   ```powershell
   ykman piv access change-management-key --generate --protect
   ```

   Record the PIN and PUK in your password manager. Three wrong PINs lock the key; the PUK unlocks it.

3. **Generate the CA key** in slot 9c, needing the PIN and a touch for every use:

   ```powershell
   ykman piv keys generate --algorithm ECCP256 --pin-policy ALWAYS --touch-policy ALWAYS 9c ca-pub.pem
   ```

4. **Add a placeholder certificate** so Windows can find the key (touch when it blinks):

   ```powershell
   ykman piv certificates generate --subject "CN=exmon Test Device CA" --valid-days 1825 9c ca-pub.pem
   ```

5. **Unplug and replug** the YubiKey. Windows' built-in smart-card support copies the certificate into your Personal store.

6. **Make it a proper CA** (marked as a CA, may only sign end-entity certificates), signed by the YubiKey itself, then put it back on the key:

   ```powershell
   dist\exmonctl.exe ca init --out ca.pem
   ```

   ```powershell
   ykman piv certificates import 9c ca.pem
   ```

7. **Unplug and replug** again. Keep `ca.pem`: the deployment (step 3, or [Cloud Shell step 12](cloud-shell.md#12-add-the-device-ca)) needs it.

## 3. Azure (admin workstation)

The simplest way is [from Azure Cloud Shell](cloud-shell.md); it ends with the same outputs and `exmonctl-dev.json`, so skip to step 4 afterwards. The commands below do the same from your workstation.

```powershell
az login
```

```powershell
az group create -n rg-exmon-dev -l eastus
```

Create the **write-only ingest app**. Note all four values it prints; the secret is shown once:

```powershell
pwsh deploy\scripts\New-ExmonIngestApp.ps1 -Name exmon-ingest-dev -SecretMonths 3
```

`deploy\bicep\dev.bicepparam` reads its values from environment variables (it signs with the dev key, so it creates no Key Vault):

```powershell
$env:EXMON_ADMIN_GROUP_ID = az ad group show -g "<group name>" --query id -o tsv
```

```powershell
$env:EXMON_INGEST_SP_ID = "<service principal object id printed above>"
```

```powershell
$env:EXMON_CA_PEM = Get-Content -Raw ca.pem
```

Deploy:

```powershell
az deployment group create -g rg-exmon-dev -n exmon -p deploy\bicep\dev.bicepparam --query properties.outputs
```

Keep the outputs: `controlHost`, `azureMonitorEndpoint`, `azureMonitorDcrImmutableId`, `azureMonitorStreamName`, `updateAllowedHost`, `releasesUrl`, `namespace`.

Create `exmonctl-dev.json` next to `exmonctl.exe`:

```json
{
  "subscription": "<subscription id>",
  "resourceGroup": "rg-exmon-dev",
  "namespace": "<namespace output>",
  "host": "<controlHost output>"
}
```

Check you can see the test clients:

```powershell
dist\exmonctl.exe devices --profile exmonctl-dev.json
```

It should list `exmon-test-01` … `exmon-test-10`, ring `test`, state `Enabled`, with `-` for online, version and last status (nothing has connected yet). Role assignments can take a few minutes to apply.

## 4. The test PC's config

On the workstation, gather everything for the test PC in one folder, `dist\testpc` (inside `dist`, so git ignores it; the config will hold the ingest secret):

```powershell
New-Item -ItemType Directory -Force dist\testpc | Out-Null; Copy-Item dist\0.1.0\exmon.exe, dist\exmon-helper.exe dist\testpc\
```

```powershell
dist\testpc\exmon.exe --write-default-config --config dist\testpc\exmon.json
```

If you deployed from Cloud Shell, print the `law` remote's settings there (run `source ~/exmon-dev.env` first, or [rebuild it](cloud-shell.md#rebuilding-the-settings-file)):

```bash
jq -n --arg e "$EXMON_OUT_azureMonitorEndpoint" --arg d "$EXMON_OUT_azureMonitorDcrImmutableId" --arg s "$EXMON_OUT_azureMonitorStreamName" --arg t "$(az account show --query tenantId -o tsv)" --arg c "$EXMON_INGEST_CLIENT_ID" '{endpoint:$e, dcrImmutableId:$d, streamName:$s, tenantId:$t, clientId:$c, clientSecret:"PASTE-THE-SECRET-HERE"}'
```

Open the config:

```powershell
notepad dist\testpc\exmon.json
```

and make three changes:

1. **The `law` remote** (under `remotes`): set `"enabled": true`, and replace its whole `"settings": { … }` block with the one printed above. Then put the ingest app's secret (from [Cloud Shell step 7](cloud-shell.md#7-the-ingest-app-write-only-azure-monitor-credential)) in place of `PASTE-THE-SECRET-HERE`.
2. **Probe targets** (under `probe`): the starter's `endpoints` and `httpTests` are examples (`your-app.azurefd.net` …) that would only produce `PROBE/down` events. Delete every entry with `your` in its name, keeping `M365Portal` and `M365`, or add your own real targets.
3. Leave `control` alone: the install flags in step 5 set it.

Check the JSON is still valid (a missing or extra comma is the usual slip). It prints the `law` remote with the secret hidden:

```powershell
(Get-Content -Raw dist\testpc\exmon.json | ConvertFrom-Json).remotes | Where-Object name -eq law | Select-Object name, enabled, @{n='endpoint';e={$_.settings.endpoint}}, @{n='clientId';e={$_.settings.clientId}}, @{n='secretSet';e={$_.settings.clientSecret -notmatch 'PASTE|REPLACE'}}
```

`enabled` and `secretSet` must be `True`. exmon checks every setting itself when the service starts (step 5); a mistake then stops it with the setting at fault in the Application event log.

## 5. Enrol and install the test PC

Copy the `dist\testpc` folder (`exmon.exe`, `exmon-helper.exe`, `exmon.json`) to the test PC as `C:\exmon-setup`; the helper must sit next to `exmon.exe` when you run `install`. (Testing on the workstation itself? Use `dist\testpc` in place of `C:\exmon-setup`; `install` copies the programs to Program Files either way.) Then on the test PC, in an **elevated** PowerShell:

```powershell
cd C:\exmon-setup
```

1. **Create the device key** in the TPM and a certificate request, using one of the pool names:

   ```powershell
   .\exmon.exe enroll new --name exmon-test-01
   ```

   On a VM without a TPM, add `--allow-software-key`.

2. **Plug in the YubiKey** and sign. Type the PIN when asked, and touch the key when it blinks:

   ```powershell
   .\exmon.exe enroll sign-local --days 30
   ```

   **Unplug the YubiKey.** Check:

   ```powershell
   .\exmon.exe enroll show
   ```

3. **Install the service** pointing at the dev namespace. These builds aren't code-signed, so updates are told not to require a signature (the manifest's signature and SHA-256 still protect them):

   ```powershell
   .\exmon.exe install --control-host <controlHost> --control-cert-subject exmon-test-01 --control-cert-issuer "exmon Test Device CA" --update-hosts <updateAllowedHost> --update-require-authenticode=false
   ```

   Both certificate flags matter: the starter config names the production issuer (`exmon Issuing CA`), and a certificate must match both the subject and the issuer.

   If you deployed from Cloud Shell, it can print this command with your values filled in:

   ```bash
   echo ".\\exmon.exe install --control-host $EXMON_OUT_controlHost --control-cert-subject exmon-test-01 --control-cert-issuer \"exmon Test Device CA\" --update-hosts $EXMON_OUT_updateAllowedHost --update-require-authenticode=false"
   ```

4. **Put the config in place** (the folder is admin-only) and restart:

   ```powershell
   Copy-Item .\exmon.json C:\ProgramData\exmon\config\exmon.json
   ```

   ```powershell
   Restart-Service exmon
   ```

## 6. Check it works

On the test PC:

```powershell
Get-Content C:\ProgramData\exmon\exmon.log -Tail 40
```

Look for:
- `MONITOR remote law: azureMonitor https://… stream Custom-ExmonEvents (app …)`;
- `MONITOR control: wss://…:443/mqtt, certificate exmon-test-01, signing keys prod+dev`;
- `CONTROL/connected device=exmon-test-01`.

On the workstation:

```powershell
dist\exmonctl.exe devices --profile exmonctl-dev.json
```

`exmon-test-01` shows `online true` and version `0.1.0`. In the Azure portal, open the workspace, **Logs**, and after a few minutes:

```kusto
ExmonEvents_CL
| where TimeGenerated > ago(1h)
| project TimeGenerated, Host, Kind, Subject, Detail
| order by TimeGenerated desc
```

If something is missing, see [Troubleshooting](#troubleshooting) below.

## 7. Try the control plane

### A command

```powershell
dist\exmonctl.exe cmd send --profile exmonctl-dev.json --key-file $env:USERPROFILE\.exmon\dev.pem --command trace-now --device exmon-test-01
```

The test PC logs `CONTROL/applied kind=exmon-command …` and then path-trace lines.

### A cloud config

Save as `cloud-test.json`:

```json
{ "control": { "chirpMinutes": 1 }, "probe": { "intervalSeconds": 120 } }
```

```powershell
dist\exmonctl.exe config publish --profile exmonctl-dev.json --key-file $env:USERPROFILE\.exmon\dev.pem --file cloud-test.json --device exmon-test-01
```

The test PC logs:
- `CONTROL/applied kind=exmon-config … installed; restarting to apply it`;
- after the restart, `MONITOR cloud config: version … (control, probe)`;
- two minutes later, `CONTROL/config_committed`.

### The chirp

Block exmon's outbound traffic on the test PC (elevated):

```powershell
New-NetFirewallRule -DisplayName "exmon chirp test" -Direction Outbound -Program "C:\Program Files\exmon\exmon.exe" -Action Block
```

```powershell
Restart-Service exmon
```

Once the connection drops, the log shows `CONTROL/unreachable … reason=tcp` (or `timeout`) every minute (the cloud config set `chirpMinutes` to 1), with `for=` counting the time since it was last connected. Windows can take a few minutes to enforce a new rule, so exmon may still reconnect once after the restart and only lose the connection later; wait until the lines appear. Events wait in the spool. Remove the block:

```powershell
Remove-NetFirewallRule -DisplayName "exmon chirp test"
```

It reconnects (`CONTROL/connected … downtime=…`), and the spooled `CONTROL/unreachable` lines appear in Log Analytics.

### Offline: goodbye and last will

A normal stop says goodbye: the device shows offline at once.

```powershell
Stop-Service exmon
```

```powershell
dist\exmonctl.exe devices --profile exmonctl-dev.json --ring test
```

`exmon-test-01` shows `false`. Start it again (`Start-Service exmon`).

A crash says nothing, so the broker's **last will** marks it offline: about 2 minutes later (up to 90 s to notice the dead connection, plus the 30 s will delay). Windows restarts a crashed service after 5 seconds, and a reconnect within the will delay cancels the will (so a quick restart never flaps offline). To see the will, keep the service down while you "crash" it:

```powershell
sc.exe config exmon start= disabled
```

```powershell
Stop-Process -Name exmon -Force
```

After 2½ minutes `exmonctl devices` shows `false` with `-` for version and last status: the will replaced the retained status, and it carries only `online` and `reason`. Put the service back:

```powershell
sc.exe config exmon start= delayed-auto
```

```powershell
Start-Service exmon
```

### An update

Upload 0.1.1 to the releases container (your account is a Storage Blob Data Contributor through the admin group). Take the account name from `updateAllowedHost`:

```powershell
az storage blob upload --account-name <account> --container-name releases --name 0.1.1/exmon.exe --file dist\0.1.1\exmon.exe --auth-mode login
```

```powershell
dist\exmonctl.exe release publish --profile exmonctl-dev.json --key-file $env:USERPROFILE\.exmon\dev.pem --version 0.1.1 --file dist\0.1.1\exmon.exe --url <releasesUrl>/0.1.1/exmon.exe --device exmon-test-01 --spread 0 --skip-signature-check
```

The test PC logs `CONTROL/scheduled … update to 0.1.1`, `UPDATE/downloading`, `UPDATE/verified` and `UPDATE/handed_over`; the helper's own steps are in the Application event log (source `exmon-helper`). The service then restarts on 0.1.1, and about two minutes after connecting it logs `UPDATE/healthy`. `exmonctl devices` shows `0.1.1`.

### A rollback

To see a rollback, publish a version that never reports healthy. Test builds have a switch for that (it doesn't exist in production builds): `-X exmon/internal/updater.devNeverHealthy=yes`. The device takes the update normally; the new version connects but logs `UPDATE/test_unhealthy` instead of `UPDATE/healthy`, and after `healthMinutes` (10) the helper puts the old exe back. (A firewall block is not a reliable way to do this: Windows may re-check open connections when a rule changes, so the block can stop the manifest arriving too.)

Use a version newer than the one the device runs. Build and upload it (normal PowerShell, in the repository):

```powershell
go build -tags exmondev -ldflags "-X exmon/internal/version.Version=0.1.3 -X exmon/internal/updater.devNeverHealthy=yes" -o dist\0.1.3\exmon.exe .
```

```powershell
$acct = az storage account list -g rg-exmon-dev --query "[0].name" -o tsv
```

```powershell
az storage blob upload --account-name $acct --container-name releases --name 0.1.3/exmon.exe --file dist\0.1.3\exmon.exe --auth-mode login --overwrite
```

```powershell
dist\exmonctl.exe release publish --profile exmonctl-dev.json --key-file $env:USERPROFILE\.exmon\dev.pem --version 0.1.3 --file dist\0.1.3\exmon.exe --url "https://$acct.blob.core.windows.net/releases/0.1.3/exmon.exe" --device exmon-test-01 --spread 0 --skip-signature-check
```

The device logs `UPDATE/handed_over version=0.1.3`, then (on 0.1.3) `CONTROL/connected` and `UPDATE/test_unhealthy`. About 10 minutes after the swap the helper logs "rolled exmon back from 0.1.3 to …: no healthy report by …", and the restored version logs `UPDATE/rolled_back version=0.1.3 …`. `C:\Program Files\exmon\exmon.failed.exe` is the rejected version.

After it reconnects, the restored version receives the retained 0.1.3 manifest again and ignores it: `CONTROL/applied … 0.1.3 was rolled back on this device`. No second download.

## 8. Clean up

On the test PC (elevated):

```powershell
& "C:\Program Files\exmon\exmon.exe" enroll remove
```

```powershell
& "C:\Program Files\exmon\exmon.exe" uninstall
```

```powershell
Remove-Item -Recurse "C:\Program Files\exmon", C:\ProgramData\exmon
```

In Azure:

```powershell
az group delete -n rg-exmon-dev
```

```powershell
az ad app delete --id <ingest app client id>
```

Keep the YubiKey somewhere safe; it is the dev CA.

## Troubleshooting

| Symptom | Check |
|---|---|
| `enroll new`: can't create a TPM key | The PC has no usable TPM (`Get-Tpm`); on a VM use `--allow-software-key`. |
| `sign-local`: test CA not found | YubiKey not plugged in, or Windows hasn't copied its certificate yet: replug and wait a few seconds. `certutil -scinfo` shows what Windows sees. Running elevated as a different user than the one logged in? Run it as yourself. |
| `CONTROL/unreachable reason=tls` | A TLS-inspecting proxy, or the namespace doesn't trust the CA: check the CA deployed to the namespace (`EXMON_CA_PEM`) is the `ca.pem` from step 2. |
| `reason=auth_rejected` | No enabled client named `exmon-test-01`: compare `exmonctl devices` with the certificate's CN (`enroll show`). |
| `reason=websocket` | Wrong path; the default `/mqtt` should be right for Event Grid. |
| `REMOTE/send_failed remote=law … error="logs ingestion: Post …: EOF"` | The connection was closed before any HTTP response: something on the network blocks the ingestion host by name. Telemetry and ad blockers (router or DNS filters with SNI blocking, Pi-hole-style lists, web filters, some VPN apps) often block `*.monitor.azure.com` because Application Insights uses it. Test with `curl.exe -sS -o NUL -w "%{http_code}" -X POST https://<endpoint host>/` (any HTTP code, such as `401`, `404` or `411`, means it's reachable; `schannel: failed to receive handshake` means it's blocked) and allow `*.ingest.monitor.azure.com` in the filter. |
| No rows in Log Analytics | `REMOTE/send_failed remote=law` in the log: HTTP 403 for the first 30 minutes after deployment is normal (role assignment propagating). `AADSTS7000215` means a wrong secret. |
| `CONTROL/rejected … key "dev" is not trusted` | The exe wasn't built with `-tags exmondev`. |

More in the main [Troubleshooting](../troubleshooting.md) page.
