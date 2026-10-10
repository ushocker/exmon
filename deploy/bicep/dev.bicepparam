using 'main.bicep'

// Values that differ per tenant come from environment variables, so nothing in
// this file needs editing: docs/deploy/cloud-shell.md keeps them in ~/exmon-dev.env.
// They must be exported (`export NAME=value`) in the shell that runs `az deployment`.

param environment = 'dev'

// Object id of your fleet-admin Entra group. Required.
param adminGroupId = readEnvironmentVariable('EXMON_ADMIN_GROUP_ID')

// Object id of the ingest app's service principal (deploy/scripts/New-ExmonIngestApp.ps1).
// Empty skips the role assignment; set it and redeploy.
param ingestPrincipalId = readEnvironmentVariable('EXMON_INGEST_SP_ID', '')

// PEM text of the YubiKey test CA (`exmonctl ca init`, see docs/enrollment.md).
// Empty deploys everything except the CA; devices can't connect until you set it and redeploy.
param issuingCaPem = readEnvironmentVariable('EXMON_CA_PEM', '')

param retentionDays = 30
param testPoolSize = 10

// Dev can sign with a local dev key (exmonctl --key-file); set true to get its own vault.
param deployKeyVault = false
