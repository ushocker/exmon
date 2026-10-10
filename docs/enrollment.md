# Device certificates (enrolment)

To connect to the control plane (MQTT over Event Grid), each PC needs a **client certificate** whose private key lives in the PC's **TPM** and can't be copied off it. The certificate's common name (CN) is the device's identity on the broker.

| | Production devices | Test devices |
|---|---|---|
| Key and certificate made by | Intune (SCEP profile, Cloud PKI) | `exmon enroll` |
| Key type | RSA 2048 in the TPM | ECDSA P-256 in the TPM |
| CN | the Entra device id | `exmon-test-NN` |
| Signed by | the production issuing CA (Cloud PKI) | the **test CA on a YubiKey** |
| Broker | prod Event Grid namespace | dev namespace, which has a pool of `exmon-test-NN` clients |
| Lifetime | 1 year, renewed by Intune | 1–90 days (default 30) |

Production enrolment is set up in Intune; see [Intune deployment](deploy/intune.md). This page covers the test path.

## The test CA on a YubiKey

The test CA's private key is generated **on the YubiKey** and never leaves it. Signing a device certificate needs the YubiKey plugged in, its PIN, and a touch. The test CA is trusted only by the **dev** namespace, so even a misused YubiKey can't reach production.

### One-time YubiKey setup (admin workstation)

You need [YubiKey Manager CLI](https://developers.yubico.com/yubikey-manager/) (`ykman`) on the admin workstation only. Test PCs need nothing installed: Windows has a built-in driver for the YubiKey's PIV smart-card function.

1. **Reset PIV** (only on a new or spare YubiKey; this erases its PIV keys):

   ```bash
   ykman piv reset
   ```

2. **Set your own PIN, PUK and management key.** The defaults are public (PIN `123456`, PUK `12345678`).

   ```bash
   ykman piv access change-pin
   ```

   ```bash
   ykman piv access change-puk
   ```

   ```bash
   ykman piv access change-management-key --generate --protect
   ```

   `--protect` stores the new management key on the YubiKey behind the PIN, so you don't have to keep it anywhere else.

3. **Generate the CA key in slot 9c** (Digital Signature), requiring the PIN and a touch for **every** signature:

   ```bash
   ykman piv keys generate --algorithm ECCP256 --pin-policy ALWAYS --touch-policy ALWAYS 9c ca-pub.pem
   ```

4. **Add a placeholder certificate**, so Windows can find the key:

   ```bash
   ykman piv certificates generate --subject "CN=exmon Test Device CA" --valid-days 1825 9c ca-pub.pem
   ```

5. **Replace it with a proper CA certificate** (marked as a CA, path length 0, certificate signing only), made by `exmonctl` and signed by the YubiKey itself:

   ```bash
   exmonctl ca init --out ca.pem
   ```

   ```bash
   ykman piv certificates import 9c ca.pem
   ```

6. **Give `ca.pem` to the Azure deployment** as the dev namespace's trusted CA (see [Azure deployment](deploy/azure.md)). It is a public certificate, not a secret.

Unplug and replug the YubiKey after step 5 so Windows picks up the new certificate. Keep a second YubiKey set up the same way only if you want a spare CA; a lost YubiKey is protected by its PIN (it locks after 3 wrong tries), and you can cut off everything it signed by removing its CA from the dev namespace.

## Enrolling a test PC

From an **elevated** prompt on the test PC, with `exmon.exe` copied there:

1. **Create the key and request.** The name must be one of the dev namespace's pool clients (`exmon-test-01`, `exmon-test-02` …):

   ```bash
   exmon.exe enroll new --name exmon-test-01
   ```

   This locks down `%ProgramData%\exmon` (as `install` does), creates a non-exportable ECDSA P-256 key in the TPM, and writes the certificate request to `%ProgramData%\exmon\state`. On a VM without a TPM, add `--allow-software-key` (test only).

2. **Plug in the YubiKey and sign:**

   ```bash
   exmon.exe enroll sign-local --days 30
   ```

   It asks for the YubiKey PIN (not shown as you type), then waits for a touch. The signed certificate goes into the computer's Personal store (`LocalMachine\My`), linked to the TPM key, and the exmon service (`NT SERVICE\exmon`) is given permission to use the key. **Unplug the YubiKey.**

   If the service isn't installed yet, `exmon-helper` grants that permission when `install` runs it.

3. **Install or update the service**, pointing it at the dev namespace and the new certificate (the command line is printed by step 2; see [Control plane](control-plane.md) for all settings).

If you'd rather not bring the YubiKey to the test PC, sign on the admin workstation instead:

```bash
exmon.exe enroll new --name exmon-test-01 --out exmon-test-01.csr
```

Copy `exmon-test-01.csr` to the workstation (it holds only the public key), then there:

```bash
exmonctl ca sign-csr --csr exmon-test-01.csr --days 30
```

Copy the resulting `exmon-test-01.pem` back and run:

```bash
exmon.exe enroll import --cert exmon-test-01.pem
```

### Other commands

| Command | Does |
|---|---|
| `exmon enroll show` | Shows the enrolment (name, key provider) and the certificate's issuer and validity. |
| `exmon enroll remove` | Deletes the device certificate, the TPM key and the enrolment state. Run it before handing the PC back. |

Test certificates expire after `--days`, after which the PC can't connect (`CONTROL/unreachable reason=no_client_cert`). Run `enroll new` and `enroll sign-local` again to renew.

## What is checked

- `enroll` only accepts names like `exmon-test-01`, and `exmonctl ca sign-csr` refuses to sign anything else, so the test CA can't issue production-looking identities.
- The request's signature is checked before signing, which proves the PC holds the matching private key.
- `enroll import` refuses a certificate that isn't for this PC's key, or is for another name.
- Device certificates are for client authentication only and can't sign other certificates.
