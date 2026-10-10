# Deploying from Azure Cloud Shell

This is the [Azure deployment](azure.md) as commands to paste, one block at a time, into **Azure Cloud Shell (Bash)**. Cloud Shell already has the Azure CLI, Bicep, PowerShell 7 and `jq`, and is signed in as you, so nothing needs installing.

It deploys the **dev** environment (the one the [test setup](test-install.md) uses). For prod, use `prod` wherever `dev` appears, and see [Prod differences](#prod-differences).

Every value a later step needs is saved in `~/exmon-dev.env`, in your Cloud Shell home folder. **If Cloud Shell reconnects, run `cd ~/exmon && source ~/exmon-dev.env` before carrying on.**

Your home folder only survives between sessions if Cloud Shell has a storage account mounted. An **ephemeral** session (no storage account) starts empty every time: `cd ~/exmon` then fails with *No such file or directory*. Either switch to persistent storage (**Settings → Reset user settings**, then **Mount storage account** on the next start), or, after each restart, upload the package again ([step 1](#1-get-the-deployment-files-into-cloud-shell)) and [rebuild the settings file](#rebuilding-the-settings-file).

## 1. Get the deployment files into Cloud Shell

On your workstation, in the repository (PowerShell), package the `deploy` folder:

```powershell
New-Item -ItemType Directory -Force dist | Out-Null; tar -czf dist\exmon-deploy.tgz deploy
```

In Cloud Shell, choose **Manage files → Upload** in the toolbar and upload `dist\exmon-deploy.tgz`. It lands in your home folder. Unpack it:

```bash
mkdir -p ~/exmon && tar -xzf ~/exmon-deploy.tgz -C ~/exmon && cd ~/exmon && ls deploy/bicep
```

You should see `dev.bicepparam  main.bicep  modules  prod.bicepparam  certs`. After changing anything in `deploy/`, upload a fresh package and run the same command again; it overwrites the old files.

## 2. Check the subscription and Bicep

```bash
az account show --query "{subscription:name, id:id, tenant:tenantId, user:user.name}" -o table
```

If that isn't the subscription you want:

```bash
az account set --subscription "<subscription name or id>"
```

Make sure Bicep is current:

```bash
az bicep upgrade && az bicep version
```

## 3. Start the settings file

Choose the resource group name, region and admin group name here; every later step reads them from this file.

```bash
cat > ~/exmon-dev.env <<'EOF'
export EXMON_ENV=dev
export EXMON_RG=rg-exmon-dev
export EXMON_LOCATION=eastus
export EXMON_ADMIN_GROUP_NAME=exmon-admins-dev
export EXMON_INGEST_APP_NAME=exmon-ingest-dev
EOF
source ~/exmon-dev.env && env | grep ^EXMON_
```

## 4. Register the resource providers

A subscription that has never used a service must register its provider once. This is harmless if they are already registered.

```bash
for p in Microsoft.EventGrid Microsoft.Insights Microsoft.OperationalInsights Microsoft.Storage Microsoft.KeyVault Microsoft.Automation; do az provider register --namespace $p --wait -o none && echo "$p registered"; done
```

## 5. Resource group

```bash
az group create -n $EXMON_RG -l $EXMON_LOCATION -o table
```

## 6. Fleet-admin group

Members of this Entra group may sign documents, publish to devices, see the Event Grid clients and upload releases. To use an existing group, put its name in `EXMON_ADMIN_GROUP_NAME` in step 3 and skip the create command.

Create the group:

```bash
az ad group create --display-name "$EXMON_ADMIN_GROUP_NAME" --mail-nickname "$EXMON_ADMIN_GROUP_NAME" --query "{name:displayName, id:id}" -o table
```

Add yourself:

```bash
az ad group member add --group "$EXMON_ADMIN_GROUP_NAME" --member-id "$(az ad signed-in-user show --query id -o tsv)" && echo added
```

Save its id:

```bash
echo "export EXMON_ADMIN_GROUP_ID=$(az ad group show -g "$EXMON_ADMIN_GROUP_NAME" --query id -o tsv)" >> ~/exmon-dev.env && source ~/exmon-dev.env && echo $EXMON_ADMIN_GROUP_ID
```

A new group membership only shows up in sign-in tokens issued after it, so later steps that use it (exmonctl, uploading releases) need a fresh sign-in.

## 7. The ingest app (write-only Azure Monitor credential)

```bash
pwsh deploy/scripts/New-ExmonIngestApp.ps1 -Name $EXMON_INGEST_APP_NAME -SecretMonths 3
```

It prints the tenant id, client id and **client secret**. The secret is shown only once: put it in your password manager now. It goes into the test PC's `exmon.json` later ([test setup step 4](test-install.md#4-the-test-pcs-config)).

Save the app's ids (not the secret):

```bash
echo "export EXMON_INGEST_CLIENT_ID=$(az ad app list --display-name $EXMON_INGEST_APP_NAME --query [0].appId -o tsv)" >> ~/exmon-dev.env && echo "export EXMON_INGEST_SP_ID=$(az ad sp list --display-name $EXMON_INGEST_APP_NAME --query [0].id -o tsv)" >> ~/exmon-dev.env && source ~/exmon-dev.env && env | grep ^EXMON_INGEST
```

## 8. Deploy

The parameter files read the tenant-specific values from the `EXMON_*` variables, so nothing needs editing. The CA certificate is optional at this point: without it everything is deployed except the CA, and devices can't connect until you add it ([step 12](#12-add-the-device-ca)).

```bash
source ~/exmon-dev.env && az deployment group create -g $EXMON_RG -n exmon -p deploy/bicep/$EXMON_ENV.bicepparam --query properties.provisioningState -o tsv
```

It takes a few minutes and prints `Succeeded`. A line saying *The configuration value of bicep.use_binary_from_path has been set to 'false'* is Azure CLI housekeeping and can be ignored.

If it fails, fix the cause and run the same command again: a deployment only adds or changes what's missing, so whatever was already created stays. To see which resource failed:

```bash
az deployment operation group list -g $EXMON_RG -n exmon --query "[?properties.provisioningState=='Failed'].{resource:properties.targetResource.resourceName, error:properties.statusMessage.error.message}" -o table
```

## 9. Save the outputs

```bash
az deployment group show -g $EXMON_RG -n exmon --query properties.outputs -o json | jq -r 'to_entries[] | "export EXMON_OUT_\(.key)=\"\(.value.value)\""' >> ~/exmon-dev.env && source ~/exmon-dev.env && env | grep ^EXMON_OUT_ | sort
```

| Output | Goes to |
|---|---|
| `EXMON_OUT_azureMonitorEndpoint`, `…DcrImmutableId`, `…StreamName` | the `law` remote's settings in the test PC's `exmon.json` |
| `EXMON_OUT_controlHost` | `exmon install --control-host`, and `host` in `exmonctl-dev.json` |
| `EXMON_OUT_updateAllowedHost` | `exmon install --update-hosts` |
| `EXMON_OUT_releasesUrl` | release download URLs |
| `EXMON_OUT_namespace` | `namespace` in `exmonctl-dev.json` |

Print the `exmonctl` profile for your workstation:

```bash
jq -n --arg sub "$(az account show --query id -o tsv)" --arg rg "$EXMON_RG" --arg ns "$EXMON_OUT_namespace" --arg host "$EXMON_OUT_controlHost" '{subscription:$sub, resourceGroup:$rg, namespace:$ns, host:$host}'
```

Save that as `exmonctl-dev.json` next to `exmonctl.exe`.

## 10. Check the deployment

The test client pool:

```bash
az eventgrid namespace client list -g $EXMON_RG --namespace-name $EXMON_OUT_namespace --query "[].{name:name, state:state}" -o table
```

That lists `exmon-test-01` … `exmon-test-10`, all `Enabled`.

The Event Grid role ids:

```bash
az role definition list --name "EventGrid TopicSpaces Publisher" --query [].name -o tsv && az role definition list --name "EventGrid TopicSpaces Subscriber" --query [].name -o tsv
```

These two ids must be `a12b0b94-b317-4dcd-84a8-502ce99884c6` and `4b0f2fd7-60b4-4eca-896f-4435034f8bf5` (the defaults in `eventgrid.bicep`).

The ingest app's one role, on the data collection rule:

```bash
az role assignment list --assignee $EXMON_INGEST_SP_ID --all --query "[].{role:roleDefinitionName, scope:scope}" -o table
```

That shows `Monitoring Metrics Publisher` on `…/dataCollectionRules/exmon-dev-dcr`, and nothing else.

## 11. Make the YubiKey test CA (on your Windows workstation)

Cloud Shell can't see a USB device, so this step runs in **PowerShell on your workstation**, in the exmon repository (`D:\Code\Go\exmon` or wherever you cloned it), on the `fleet-control-plane` branch. You need Go and a YubiKey 5 (PIV). The CA's private key is created on the YubiKey and can never be copied off it; every signature needs the PIN and a touch.

**This erases the YubiKey's PIV application** (smart-card certificates, e.g. for Windows sign-in). FIDO2, OTP and OATH on the same key are untouched. Use a spare YubiKey if you use PIV for anything else.

**Run this step in an elevated PowerShell** (Run as administrator). On Windows, ykman needs administrator rights to talk to the YubiKey (some commands, such as `ykman info`, fail without them). Elevate **as your own account**, not a separate admin account: Windows puts the YubiKey's certificate in the signed-in user's Personal store, and `exmonctl ca init` looks for it in the store of the account running it.

### Install YubiKey Manager (ykman)

```powershell
winget install --id Yubico.YubiKeyManagerCLI -e
```

The installer doesn't add `ykman` to `PATH`. Add its folder for this window:

```powershell
$env:Path += ';C:\Program Files\Yubico\YubiKey Manager CLI'
```

And permanently for your account (new windows):

```powershell
[Environment]::SetEnvironmentVariable('Path', [Environment]::GetEnvironmentVariable('Path', 'User') + ';C:\Program Files\Yubico\YubiKey Manager CLI', 'User')
```

Check:

```powershell
ykman --version
```

If `winget` isn't available, install the latest `yubikey-manager-…-win64.msi` from <https://developers.yubico.com/yubikey-manager/Releases/> instead; it installs to the same folder.

### Build exmonctl

```powershell
go build -o dist\exmonctl.exe ./cmd/exmonctl
```

### Set up the YubiKey

1. Plug in the YubiKey and check ykman sees it:

   ```powershell
   ykman info
   ```

2. **Reset PIV** (asks to confirm; erases PIV only):

   ```powershell
   ykman piv reset
   ```

3. **Change the PIN** (default `123456`; 6–8 characters):

   ```powershell
   ykman piv access change-pin
   ```

4. **Change the PUK** (default `12345678`), which unlocks the PIN after three wrong tries:

   ```powershell
   ykman piv access change-puk
   ```

5. **Replace the default management key** with a random one stored on the YubiKey, protected by the PIN (it asks for the PIN):

   ```powershell
   ykman piv access change-management-key --generate --protect
   ```

   Put the PIN and PUK in your password manager.

6. **Generate the CA key** in slot 9c (Digital Signature), needing the PIN and a touch for every use. It writes the public key to `ca-pub.pem`:

   ```powershell
   ykman piv keys generate --algorithm ECCP256 --pin-policy ALWAYS --touch-policy ALWAYS 9c ca-pub.pem
   ```

7. **Add a placeholder certificate**, so Windows can find the key. Touch the YubiKey when it blinks:

   ```powershell
   ykman piv certificates generate --subject "CN=exmon Test Device CA" --valid-days 1825 9c ca-pub.pem
   ```

8. **Unplug the YubiKey, wait a few seconds, plug it back in.** Windows' built-in smart-card support copies the certificate into your Personal store. Check:

   ```powershell
   Get-ChildItem Cert:\CurrentUser\My | Where-Object Subject -eq 'CN=exmon Test Device CA' | Format-List Subject, Thumbprint, HasPrivateKey
   ```

   It shows one certificate with `HasPrivateKey : True`. If not, wait a few more seconds and run it again, or check `certutil -scinfo`.

9. **Make the real CA certificate** (marked as a CA, allowed to sign only device certificates, valid 5 years), signed by the YubiKey itself. Type the PIN when asked and touch the YubiKey when it blinks:

   ```powershell
   dist\exmonctl.exe ca init --out ca.pem
   ```

10. **Put it on the YubiKey** in place of the placeholder (asks for the PIN):

    ```powershell
    ykman piv certificates import 9c ca.pem
    ```

11. **Unplug and replug** again, then check Windows now has the CA certificate (`Issuer` and `Subject` both `CN=exmon Test Device CA`):

    ```powershell
    Get-ChildItem Cert:\CurrentUser\My | Where-Object Subject -eq 'CN=exmon Test Device CA' | Format-List Subject, Issuer, NotAfter, HasPrivateKey
    ```

    You'll usually see two entries: the old placeholder (expiring a day earlier) and the CA. Both use the same YubiKey key, and exmon only picks the one marked as a CA, but remove the placeholder to avoid confusion. This deletes only that cached certificate, nothing on the YubiKey:

    ```powershell
    Get-ChildItem Cert:\CurrentUser\My | Where-Object { $_.Subject -eq 'CN=exmon Test Device CA' -and -not ($_.Extensions | Where-Object { $_ -is [System.Security.Cryptography.X509Certificates.X509BasicConstraintsExtension] -and $_.CertificateAuthority }) } | Remove-Item
    ```

    Run the check again; one entry is left. `ca.pem` is the certificate Azure needs. It's public, not a secret. Keep a copy with your deployment notes.

## 12. Add the device CA

Back in **Cloud Shell**: upload `ca.pem` from the repository folder with **Manage files → Upload**, then keep it with the deployment and tell the settings file to read it:

```bash
mkdir -p ~/exmon/ca && mv ~/ca.pem ~/exmon/ca/$EXMON_ENV-issuing-ca.pem && head -1 ~/exmon/ca/$EXMON_ENV-issuing-ca.pem && echo 'export EXMON_CA_PEM="$(cat ~/exmon/ca/$EXMON_ENV-issuing-ca.pem)"' >> ~/exmon-dev.env
```

The first line printed must be `-----BEGIN CERTIFICATE-----`. Then deploy again. It only adds the CA; nothing else changes:

```bash
source ~/exmon-dev.env && az deployment group create -g $EXMON_RG -n exmon -p deploy/bicep/$EXMON_ENV.bicepparam --query properties.provisioningState -o tsv
```

Check the namespace has it:

```bash
az eventgrid namespace ca-certificate list -g $EXMON_RG --namespace-name $EXMON_OUT_namespace --query "[].{name:name, state:provisioningState}" -o table
```

That shows `dev-issuing-ca`, `Succeeded`.

## 13. Check admin access from your workstation

`exmonctl` runs on your workstation and gets its Azure tokens from the Azure CLI there, so it needs the CLI installed and signed in. Signing in after step 6 also gives you a token that includes your new admin group membership.

In **PowerShell** on the workstation (not elevated):

```powershell
winget install -e --id Microsoft.AzureCLI
```

Open a new PowerShell window, then sign in to the same tenant:

```powershell
az login
```

In Cloud Shell, print the profile:

```bash
jq -n --arg sub "$(az account show --query id -o tsv)" --arg rg "$EXMON_RG" --arg ns "$EXMON_OUT_namespace" --arg host "$EXMON_OUT_controlHost" '{subscription:$sub, resourceGroup:$rg, namespace:$ns, host:$host}'
```

Save the output as `exmonctl-dev.json` in the repository folder on your workstation (it is git-ignored), then:

```powershell
cd D:\Code\Go\exmon
```

```powershell
dist\exmonctl.exe devices --profile exmonctl-dev.json
```

It lists `exmon-test-01` … `exmon-test-10`, ring `test`, state `Enabled`, with `-` for online, version and last status (no device has connected yet). A `warning: no statuses: …` line means the broker sign-in failed while the Azure part worked. This checks that your admin group can read the namespace (Azure Resource Manager) and sign in to the broker with an Entra token (MQTT). Role assignments can take a few minutes after the deployment.

The Azure side is done. Carry on with [test setup step 4](test-install.md#4-the-test-pcs-config) on your workstation and test PC (you have already done its steps 1 to 3 here, except building the exmon test builds and the dev signing key in step 1).

## Rebuilding the settings file

Everything in `~/exmon-dev.env` can be read back from Azure (the ingest app's secret is never in it). After a new or ephemeral session, unpack the package again ([step 1](#1-get-the-deployment-files-into-cloud-shell)) and paste this, with the names you chose in step 3:

```bash
cat > ~/exmon-dev.env <<'EOF'
export EXMON_ENV=dev
export EXMON_RG=rg-exmon-dev
export EXMON_LOCATION=eastus
export EXMON_ADMIN_GROUP_NAME=exmon-admins-dev
export EXMON_INGEST_APP_NAME=exmon-ingest-dev
EOF
source ~/exmon-dev.env
echo "export EXMON_ADMIN_GROUP_ID=$(az ad group show -g "$EXMON_ADMIN_GROUP_NAME" --query id -o tsv)" >> ~/exmon-dev.env
echo "export EXMON_INGEST_CLIENT_ID=$(az ad app list --display-name $EXMON_INGEST_APP_NAME --query [0].appId -o tsv)" >> ~/exmon-dev.env
echo "export EXMON_INGEST_SP_ID=$(az ad sp list --display-name $EXMON_INGEST_APP_NAME --query [0].id -o tsv)" >> ~/exmon-dev.env
az deployment group show -g $EXMON_RG -n exmon --query properties.outputs -o json | jq -r 'to_entries[] | "export EXMON_OUT_\(.key)=\"\(.value.value)\""' >> ~/exmon-dev.env
source ~/exmon-dev.env && env | grep ^EXMON_ | sort
```

If the CA was already added ([step 12](#12-add-the-device-ca)), upload `ca.pem` and run that step's first command again; otherwise the next deployment would deploy without it. (Bicep leaves an existing CA in place when the parameter is empty, but don't rely on that.)

## Prod differences

- Use `~/exmon-prod.env` with `EXMON_ENV=prod`, `rg-exmon-prod`, a prod admin group and `exmon-ingest-prod` (`-SecretMonths 12`).
- The CA is the Intune Cloud PKI issuing CA for exmon ([Intune](intune.md#1-a-certificate-authority-only-for-exmon)).
- Prod creates the Key Vault, and the sync runbook once `EXMON_RUNBOOK_URI` is set to a raw URL of `deploy/automation/Sync-ExmonClients.ps1` pinned to a commit. After that deployment, grant the runbook's identity read access to Entra (needs a Privileged Role Administrator):

  ```bash
  pwsh deploy/scripts/Grant-RunbookGraphPermissions.ps1 -IdentityObjectId $EXMON_OUT_runbookIdentityObjectId
  ```

## Cleaning up

```bash
az group delete -n $EXMON_RG --yes
```

```bash
az ad app delete --id $EXMON_INGEST_CLIENT_ID
```

The Key Vault (prod) is soft-deleted with purge protection, so its name stays reserved for 90 days.
