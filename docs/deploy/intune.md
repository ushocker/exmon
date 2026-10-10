# Intune deployment (production)

In production, Intune does the per-device work, so no PC ever needs a person or a YubiKey:

- **Cloud PKI** issues each PC a client certificate whose key is created **in its TPM**;
- a **Win32 app** installs exmon and its config;
- **Entra groups** decide each PC's ring, and the sync runbook mirrors them into Event Grid.

```
Intune ── SCEP profile ──► TPM key + certificate CN=<Entra device id> (LocalMachine\My)
       ── Win32 app ─────► exmon.exe install --control-host … --control-cert-issuer …
Entra group exmon-ring-canary ──► sync runbook (hourly) ──► Event Grid client <device id>, ring=canary
```

You need Intune Plan 1 and the Microsoft Cloud PKI add-on (or an existing ADCS with the Intune certificate connector; the steps are similar).

This page is the Intune reference. For the whole production rollout in order (network allow-list, Azure, keys, build, Intune, pilot), with what the dev deployment taught us, follow [Production deployment](production.md).

## 1. A certificate authority only for exmon

Event Grid trusts **every** certificate the namespace's CA issues (if a matching client exists), so give exmon its own issuing CA rather than a general-purpose one.

1. Intune admin center: **Tenant administration → Cloud PKI → Create**.
2. Create a **root CA** (or reuse your Cloud PKI root), then an **issuing CA** under it:
   - name: `exmon Issuing CA`;
   - extended key usage: **Client Authentication** only;
   - key size RSA 2048 or larger.
3. Download the issuing CA certificate and convert it to PEM (`certutil -encode "exmon Issuing CA.cer" prod-issuing-ca.pem`; the first line must be `-----BEGIN CERTIFICATE-----`). The Azure deployment reads it from `EXMON_CA_PEM` ([Cloud Shell step 12](cloud-shell.md#12-add-the-device-ca)).
4. Note the issuing CA's **SCEP URI**.

## 2. Trust the CA on the devices

**Devices → Configuration → Create → Windows 10 and later → Templates → Trusted certificate**, twice:

- the Cloud PKI **root** certificate, store **Computer certificate store - Root**;
- the **issuing** CA certificate, store **Computer certificate store - Intermediate**.

Assign both to the same device groups as the SCEP profile.

## 3. SCEP certificate profile

**Devices → Configuration → Create → Windows 10 and later → Templates → SCEP certificate**:

| Setting | Value |
|---|---|
| Certificate type | **Device** (lands in the computer's Personal store, `LocalMachine\My`) |
| Subject name format | `CN={{AAD_Device_ID}}` |
| Subject alternative name | none needed |
| Certificate validity period | 1 year |
| Key storage provider (KSP) | **Enroll to Trusted Platform Module (TPM) KSP, otherwise fail** |
| Key usage | Digital signature |
| Key size (bits) | 2048 |
| Hash algorithm | SHA-2 |
| Root certificate | the trusted root profile from step 2 |
| Extended key usage | Client Authentication (`1.3.6.1.5.5.7.3.2`) |
| Renewal threshold (%) | 20 |
| SCEP server URLs | the issuing CA's SCEP URI |

Assign it to all devices that will run exmon. The key never leaves the TPM; Intune renews the certificate before it expires, and exmon picks up the renewed one by itself, with no restart.

The certificate's CN is the **Entra device id**, which is also the device's Event Grid client name and its MQTT identity (`exmon/v1/device/<device id>/…`).

## 4. Rings

Rings are Entra groups named `exmon-ring-<ring>`, one per ring, in rollout order:

| Group | Typically |
|---|---|
| `exmon-ring-canary` | IT's own PCs, a dozen or so |
| `exmon-ring-early` | a few percent across sites and hardware models |
| `exmon-ring-broad` | everyone else |

Use assigned (static) groups, or dynamic device groups, for example:

```
(device.deviceOSType -eq "Windows") and (device.devicePhysicalIds -any (_ -eq "[OrderID]:exmon-canary"))
```

A device in several ring groups gets the earliest ring. The ring order is the `ringOrder` parameter of the deployment.

The **sync runbook** reads these groups every hour and creates an Event Grid client for each device (`ring` attribute set, `managedBy=sync`). It disables clients whose device left every ring group or was disabled, and deletes them after 30 days. Run `exmonctl sync run` to sync straight away after changing a group.

A device can't choose its own ring: the ring is an attribute of its Event Grid client, set only by the runbook.

## 5. The Win32 app

### Package

1. Put in one folder:
   - `exmon.exe`: the signed release, built with its version (`go build -ldflags "-X exmon/internal/version.Version=1.4.0"`);
   - `exmon-helper.exe`: the signed helper (`go build ./cmd/exmon-helper`), which `install` sets up as the `exmon-helper` service. It applies updates and grants exmon use of the certificate's key after every renewal. It is **only** updated through this package, never over the air, so build a new package when the helper changes or the release signing key rotates ([Operations](../operations.md#when-the-helper-changes));
   - both built **without** `-tags exmondev`, from the commit holding the production signing keys ([Production deployment 5–6](production.md#5-build-the-signing-keys-into-exmon));
   - `exmon.json`: the production config (remotes including the Azure Monitor secret, probes …);
   - `install.cmd`, `uninstall.cmd` from `deploy/intune/`, with the values at the top of `install.cmd` filled in.
2. Package it with the [Win32 Content Prep Tool](https://learn.microsoft.com/mem/intune/apps/apps-win32-prepare):

   ```bash
   IntuneWinAppUtil.exe -c exmon-package -s install.cmd -o out
   ```

### App settings

| Setting | Value |
|---|---|
| Install command | `install.cmd` |
| Uninstall command | `uninstall.cmd` |
| Install behavior | **System** |
| Device restart behavior | No specific action |
| Detection rules | **Custom script**: `deploy/intune/detect.ps1` |
| Requirements | Windows 10 1809+ (x64) |
| Dependencies | none (the SCEP profile can arrive before or after) |

`install.cmd` runs `exmon.exe install` with:
- `--control-host` (the namespace's MQTT host);
- `--control-cert-issuer "exmon Issuing CA"`;
- `--update-hosts` (the release storage host);
- `--update-publisher` (your code-signing certificate's CN).

It then copies `exmon.json` into the admin-only `%ProgramData%\exmon\config` and restarts the service.

**Detection checks only that the services and exe exist, not the version.** exmon updates itself through the control plane. A version check would make Intune "repair" every updated PC back to the packaged version. Don't add supersedence for new versions either: use `exmonctl release publish` (see [Updates](../updates.md)). Update the Intune package now and then so new installs start recent.

Assign the app as **Required** to the ring groups (or all exmon devices). Start with `exmon-ring-canary` only and [check the pilot devices](production.md#9-pilot) before adding the other rings.

### If the certificate isn't there yet

If the app installs before the SCEP certificate arrives, exmon runs and monitors normally. It just can't connect to the control plane: it chirps `CONTROL/unreachable reason=no_client_cert` and connects as soon as the certificate appears, without a restart.

## 6. Check a device

On the PC:

```powershell
Get-ChildItem Cert:\LocalMachine\My | Where-Object { $_.Issuer -like '*exmon Issuing CA*' } | Format-List Subject, NotAfter, HasPrivateKey
```

```powershell
Get-Content C:\ProgramData\exmon\exmon.log -Tail 50 | Select-String 'CONTROL/'
```

In Log Analytics:

```kusto
ExmonEvents_CL
| where Kind in ("CONTROL/connected", "CONTROL/unreachable") and TimeGenerated > ago(1d)
| summarize arg_max(TimeGenerated, Kind, Detail) by Host
```

## Removing a device

Retire or delete it in Intune and remove it from the ring groups. The next sync disables its Event Grid client, which cuts it off even though its certificate is still valid (Event Grid doesn't check revocation). For an urgent cut-off, disable the client directly (see [Azure deployment](azure.md#what-the-broker-allows)).
