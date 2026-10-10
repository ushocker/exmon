# Trusted signing keys (production)

Public keys built into every exmon build. The file name without `.pem` is the key id.

- `config/<keyId>.pem`: may sign cloud configs and commands.
- `release/<keyId>.pem`: may sign release manifests.

Export them from Key Vault with:

    exmonctl keys export --vault <vault-name> --key exmon-config-signing  --out internal/signdoc/trust/config/cfg-2026a.pem
    exmonctl keys export --vault <vault-name> --key exmon-release-signing --out internal/signdoc/trust/release/rel-2026a.pem

Only public keys go here. Changing this folder changes which documents every new build accepts, so review it like code.
