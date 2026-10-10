# Issuing CA certificates

The parameter files read the CA that issues device certificates from the `EXMON_CA_PEM` environment variable (its PEM text), not from this folder. You can keep the PEM files here for reference:

- `dev-issuing-ca.pem`: the YubiKey test CA (`exmonctl ca init` writes it as `ca.pem`).
- `prod-issuing-ca.pem`: the Intune Cloud PKI issuing CA used only for exmon devices.

These are public certificates, not secrets. Event Grid trusts every certificate they issue (that also has a matching, enabled client), so use a CA dedicated to exmon.
