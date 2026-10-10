// The Automation account that keeps the production namespace's clients in step
// with Entra: every device in an "exmon-ring-*" group gets an Event Grid client
// named after its Entra device id (the CN of its Intune certificate), with its
// ring as an attribute; devices that leave every ring group are disabled.
//
// Its managed identity can only manage clients in this one namespace (a custom
// role), plus read devices and groups in Graph (granted by
// deploy/scripts/Grant-RunbookGraphPermissions.ps1: Bicep can't grant Graph
// app roles without the preview Graph extension). There are no webhooks: nothing
// can call it from outside.

@description('Prefix for resource names, e.g. "exmon-prod".')
param namePrefix string

param location string = resourceGroup().location

@description('Resource id of the Event Grid namespace whose clients are managed.')
param namespaceId string

@description('Raw URL of Sync-ExmonClients.ps1 (e.g. from your repository or a storage account).')
param runbookUri string

@description('Prefix of the Entra groups that define rings, e.g. "exmon-ring-" for exmon-ring-canary.')
param ringGroupPrefix string = 'exmon-ring-'

@description('Ring order: a device in several ring groups gets the earliest.')
param ringOrder array = [ 'canary', 'early', 'broad' ]

@description('First run of the hourly schedule (UTC). Defaults to an hour from deployment.')
param scheduleStart string = dateTimeAdd(utcNow(), 'PT1H')

var runbookName = 'Sync-ExmonClients'
var namespaceName = last(split(namespaceId, '/'))

resource account 'Microsoft.Automation/automationAccounts@2023-11-01' = {
  name: '${namePrefix}-automation'
  location: location
  identity: { type: 'SystemAssigned' }
  properties: {
    sku: { name: 'Basic' }
    publicNetworkAccess: true
    disableLocalAuth: true // no webhooks or account keys
  }
}

resource runbook 'Microsoft.Automation/automationAccounts/runbooks@2023-11-01' = {
  parent: account
  name: runbookName
  location: location
  properties: {
    runbookType: 'PowerShell72'
    description: 'Sync Entra ring groups to Event Grid clients'
    publishContentLink: { uri: runbookUri }
    logProgress: false
    logVerbose: false
  }
}

resource schedule 'Microsoft.Automation/automationAccounts/schedules@2023-11-01' = {
  parent: account
  name: 'hourly'
  properties: {
    frequency: 'Hour'
    interval: 1 // the shortest Automation allows; exmonctl sync run starts it now
    startTime: scheduleStart
    timeZone: 'UTC'
  }
}

resource job 'Microsoft.Automation/automationAccounts/jobSchedules@2023-11-01' = {
  parent: account
  name: guid(account.id, runbookName, 'hourly')
  properties: {
    runbook: { name: runbook.name }
    schedule: { name: schedule.name }
    parameters: {
      NamespaceId: namespaceId
      RingGroupPrefix: ringGroupPrefix
      RingOrder: join(ringOrder, ',')
    }
  }
}

// Least privilege: manage clients in this namespace, nothing else.
resource clientManager 'Microsoft.Authorization/roleDefinitions@2022-04-01' = {
  name: guid(resourceGroup().id, 'exmon-client-manager')
  properties: {
    roleName: 'exmon client manager (${namePrefix})'
    description: 'Create, update, disable and delete Event Grid namespace clients'
    type: 'CustomRole'
    assignableScopes: [ resourceGroup().id ]
    permissions: [
      {
        actions: [
          'Microsoft.EventGrid/namespaces/read'
          'Microsoft.EventGrid/namespaces/clients/*'
        ]
        notActions: []
      }
    ]
  }
}

resource ns 'Microsoft.EventGrid/namespaces@2025-02-15' existing = {
  name: namespaceName
}

resource canManageClients 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  name: guid(ns.id, account.id, clientManager.id)
  scope: ns
  properties: {
    roleDefinitionId: clientManager.id
    principalId: account.identity.principalId
    principalType: 'ServicePrincipal'
  }
}

output automationAccountName string = account.name
output runbookName string = runbook.name
@description('Give this to Grant-RunbookGraphPermissions.ps1.')
output identityObjectId string = account.identity.principalId
