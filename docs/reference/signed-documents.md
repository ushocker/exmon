# Signed documents

Everything the control plane sends to a device (a cloud config, a release manifest or a command) is a **signed document**. Devices check the signature against public keys **built into exmon**, so a document is only accepted if it was signed with one of your Key Vault keys. It doesn't matter how it travelled: a compromised broker, network or proxy can't forge or alter one.

## Format

```json
{"alg":"ES256","keyId":"cfg-2026a","payload":"eyJraW5kIjoi…","signature":"q1u…"}
```

| Field | Meaning |
|---|---|
| `alg` | Always `ES256`: ECDSA P-256 with SHA-256. |
| `keyId` | Which built-in key signed it; the file name of the public key in `internal/signdoc/trust/<purpose>/<keyId>.pem`. |
| `payload` | The payload's exact bytes, base64url without padding. |
| `signature` | `r‖s` (64 bytes), base64url, over SHA-256 of the payload bytes. |

Every payload starts with the same header fields:

| Field | Meaning |
|---|---|
| `kind` | `exmon-config`, `exmon-release` or `exmon-command`. |
| `version` | A number that only ever goes up for that kind; exmonctl uses the time, e.g. `20261007153000`. |
| `issuedAt`, `expiresAt` | When it was made and when it stops being valid (RFC 3339). |
| `target.devices` | Optional list of device names; empty means any device that receives it. |

## What a device checks

In this order, and nothing in the payload is read before step 3:

1. The envelope is at most 64 KB and has no unknown fields.
2. `keyId` names a built-in key **for this kind**: configs and commands use the config keys, releases use the release keys. A config key can never approve new code.
3. The signature verifies.
4. `kind` matches; `expiresAt` hasn't passed and `issuedAt` isn't in the future (5 minutes' leeway for clock differences); the device is in `target.devices` if that is set.
5. `version` is newer than the last one of that kind the device applied (so an old document can't be replayed).
6. The payload is decoded strictly: unknown fields are errors.

A document that fails is logged as `CONTROL/rejected` with the reason, and nothing changes.

## The keys

| Key (Key Vault) | Signs | Public key built into exmon |
|---|---|---|
| `exmon-config-signing` | cloud configs, commands | `internal/signdoc/trust/config/<keyId>.pem` |
| `exmon-release-signing` | release manifests | `internal/signdoc/trust/release/<keyId>.pem` |

Both are ECDSA P-256 keys in an HSM-backed Key Vault (`deploy/bicep/modules/keyvault.bicep`). Their private halves never leave the vault: `exmonctl` sends the vault a SHA-256 digest and gets the signature back, using your own `az login` session, so who may sign is controlled by the vault's role assignments (*Key Vault Crypto User*).

### Adding a key to exmon

After deploying the vault, export each public key into the repository, commit it, and build a release:

```bash
exmonctl keys export --vault <vault-name> --key exmon-config-signing --out internal/signdoc/trust/config/cfg-2026a.pem
```

```bash
exmonctl keys export --vault <vault-name> --key exmon-release-signing --out internal/signdoc/trust/release/rel-2026a.pem
```

The file name becomes the key id. Put the same ids in the exmonctl profile (`configKeyId`, `releaseKeyId`); exmonctl labels its signatures with them.

### Rotating a key

Key Vault always signs with a key's **current version**, and exmonctl labels the signature with the key id from its profile. So **don't add a new version to a key that is in use**: from that moment exmonctl would sign with a key no device trusts yet. (exmonctl checks every Key Vault signature against the production keys built into it before publishing, so it refuses with "devices would reject this document" rather than sending it.) Rotate by adding a **new key** instead:

1. Create a new P-256 key in the vault, e.g. `exmon-config-signing-2027` (`az keyvault key create --vault-name <vault> --name exmon-config-signing-2027 --kty EC-HSM --curve P-256 --ops sign verify`).
2. Export it under a new id **alongside** the old file: `exmonctl keys export --vault <vault> --key exmon-config-signing-2027 --out internal/signdoc/trust/config/cfg-2027a.pem`.
3. Build, sign and release that version to the whole fleet ([Operations](../operations.md#releasing-a-new-version)). Devices now trust both keys. Check with `exmonctl rollout status` that every device runs it.
   **For the release key, the helper too.** `exmon-helper` re-checks every release manifest with its *own* built-in keys, and it is only ever updated by `exmon install`, never over the air. So a release-key rotation also needs a new Intune package (with the new `exmon-helper.exe`) installed everywhere before step 4; otherwise devices download the update and the helper refuses it.
4. Change the profile to the new key (`configKey: exmon-config-signing-2027`, `configKeyId: cfg-2027a`) and rebuild exmonctl from the same source.
5. In a later release, remove the old `.pem` file; then disable the old key in the vault.

Changing which keys a device trusts always takes a signed release, by design.

## Development keys

For testing without Key Vault, `exmonctl keys dev-init --private %USERPROFILE%\.exmon\dev.pem` creates a local key pair and puts the public key in `internal/signdoc/trust-dev/`. Only builds made with `go build -tags exmondev` trust it, and their status reports `keyset=prod+dev`. Never install an `exmondev` build on production PCs.
