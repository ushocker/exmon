using 'main.bicep'

// Values that differ per tenant come from environment variables, so nothing in
// this file needs editing: docs/deploy/cloud-shell.md keeps them in ~/exmon-prod.env.
// They must be exported (`export NAME=value`) in the shell that runs `az deployment`.

param environment = 'prod'

// Object id of your fleet-admin Entra group. Required.
param adminGroupId = readEnvironmentVariable('EXMON_ADMIN_GROUP_ID')

// PEM text of the Intune Cloud PKI issuing CA used ONLY for exmon device certificates (docs/deploy/intune.md).
// Empty deploys everything except the CA; devices can't connect until you set it and redeploy.
param issuingCaPem = readEnvironmentVariable('EXMON_CA_PEM', '')

// Object id of the ingest app's service principal (deploy/scripts/New-ExmonIngestApp.ps1).
// Empty skips the role assignment; set it and redeploy.
param ingestPrincipalId = readEnvironmentVariable('EXMON_INGEST_SP_ID', '')

param retentionDays = 90
param throughputUnits = 1

// Raw URL of deploy/automation/Sync-ExmonClients.ps1, pinned to a commit. Empty = no sync runbook yet.
param runbookUri = readEnvironmentVariable('EXMON_RUNBOOK_URI', '')
param ringOrder = [ 'canary', 'early', 'broad' ]

// To send into an existing workspace (e.g. your SIEM's) in this resource group, give its resource id:
// param existingWorkspaceId = '/subscriptions/<sub>/resourceGroups/<rg>/providers/Microsoft.OperationalInsights/workspaces/<name>'
