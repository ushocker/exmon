// exmon's Azure side. Deploy once per environment (dev, prod) into its own
// resource group:
//
//   az deployment group create -g rg-exmon-dev -f deploy/bicep/main.bicep -p deploy/bicep/dev.bicepparam
//
// See docs/deploy/azure.md for the order of steps, and for what Bicep can't do
// (the ingest app's secret, Graph permissions for the sync runbook).

targetScope = 'resourceGroup'

@description('"dev" or "prod". Dev creates the test client pool; prod creates the sync runbook.')
@allowed([ 'dev', 'prod' ])
param environment string

@description('Prefix for resource names.')
param namePrefix string = 'exmon-${environment}'

param location string = resourceGroup().location

@description('Object id of the Entra group of fleet admins: they may sign (Key Vault), publish (Event Grid admin topic space), read clients and upload releases.')
param adminGroupId string

@description('PEM of the CA that issues device certificates: the YubiKey test CA (dev) or the Cloud PKI issuing CA (prod). Empty on a first deployment before the CA exists: no device can connect until it is set.')
param issuingCaPem string = ''

@description('Object id of the ingest app\'s service principal (from New-ExmonIngestApp.ps1). Empty on the very first deployment.')
param ingestPrincipalId string = ''

@description('Days to keep exmon rows in Log Analytics.')
param retentionDays int = 90

@description('Use an existing Log Analytics workspace in this resource group (resource id) instead of creating one.')
param existingWorkspaceId string = ''

@description('Create the signing Key Vault here. Set false in dev to share prod\'s vault (dev builds can also use a local dev key).')
param deployKeyVault bool = true

@description('Event Grid throughput units (about 10,000 devices and ~3,300 retained device documents each).')
param throughputUnits int = 1

@description('Dev: number of test clients exmon-test-01 ... to create.')
param testPoolSize int = 10

@description('Prod: raw URL of deploy/automation/Sync-ExmonClients.ps1 for the Automation runbook.')
param runbookUri string = ''

@description('Prod: ring order for the sync runbook (earliest wins when a device is in several ring groups).')
param ringOrder array = [ 'canary', 'early', 'broad' ]

var withSync = environment == 'prod' && !empty(runbookUri)

module monitor 'modules/monitor.bicep' = {
  name: 'monitor'
  params: {
    namePrefix: namePrefix
    location: location
    ingestPrincipalId: ingestPrincipalId
    retentionDays: retentionDays
    existingWorkspaceId: existingWorkspaceId
  }
}

module keyvault 'modules/keyvault.bicep' = if (deployKeyVault) {
  name: 'keyvault'
  params: {
    namePrefix: namePrefix
    location: location
    signerGroupId: adminGroupId
  }
}

module eventgrid 'modules/eventgrid.bicep' = {
  name: 'eventgrid'
  params: {
    namePrefix: namePrefix
    location: location
    environment: environment
    issuingCaPem: issuingCaPem
    throughputUnits: throughputUnits
    adminGroupId: adminGroupId
    testPoolSize: testPoolSize
  }
}

module storage 'modules/storage.bicep' = {
  name: 'storage'
  params: {
    namePrefix: namePrefix
    location: location
    adminGroupId: adminGroupId
  }
}

module automation 'modules/automation.bicep' = if (withSync) {
  name: 'automation'
  params: {
    namePrefix: namePrefix
    location: location
    namespaceId: eventgrid.outputs.namespaceId
    runbookUri: runbookUri
    ringOrder: ringOrder
  }
}

// For exmon.json (the azureMonitor remote and the control settings).
output azureMonitorEndpoint string = monitor.outputs.ingestionEndpoint
output azureMonitorDcrImmutableId string = monitor.outputs.dcrImmutableId
output azureMonitorStreamName string = monitor.outputs.streamName
output controlHost string = eventgrid.outputs.mqttHost
output updateAllowedHost string = storage.outputs.downloadHost
output releasesUrl string = storage.outputs.releasesUrl

// For exmonctl.json.
output namespace string = eventgrid.outputs.namespaceName
output signingVaultName string = deployKeyVault ? keyvault!.outputs.vaultName : ''
output automationAccount string = withSync ? automation!.outputs.automationAccountName : ''

@description('Prod: give this to deploy/scripts/Grant-RunbookGraphPermissions.ps1.')
output runbookIdentityObjectId string = withSync ? automation!.outputs.identityObjectId : ''
