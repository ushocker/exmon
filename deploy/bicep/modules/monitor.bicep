// Log Analytics workspace, the ExmonEvents_CL table, and the data collection rule
// (DCR) that exmon's "azureMonitor" remote sends to through the Logs Ingestion API.
//
//   exmon ──HTTPS──► DCR (kind Direct, its own logs-ingestion endpoint) ──► ExmonEvents_CL
//
// The ingest app (created by deploy/scripts/New-ExmonIngestApp.ps1) gets one role,
// Monitoring Metrics Publisher, on this DCR only: it can write rows to this table
// and nothing else, and it can't read anything.

@description('Prefix for resource names, e.g. "exmon-prod".')
param namePrefix string

param location string = resourceGroup().location

@description('Object id of the ingest app\'s service principal (enterprise application). Leave empty on the first deployment and redeploy once the app exists.')
param ingestPrincipalId string = ''

@description('Days to keep exmon rows. Analytics tables include 31 days free; more costs extra.')
@minValue(30)
@maxValue(730)
param retentionDays int = 90

@description('Use an existing workspace in THIS resource group instead of creating one (its resource id). Empty = create one. The ExmonEvents_CL table is added to it.')
param existingWorkspaceId string = ''

var tableName = 'ExmonEvents_CL'
var streamName = 'Custom-ExmonEvents'
var monitoringMetricsPublisher = '3913510d-42f4-4e42-8a64-420c390055eb'

// The columns, shared by the table and the DCR's input stream. They match
// azuremonitor.Row in internal/remote/azuremonitor.
var columns = [
  { name: 'TimeGenerated', type: 'datetime' }
  { name: 'Host', type: 'string' }
  { name: 'RecordId', type: 'string' }
  { name: 'Source', type: 'string' }
  { name: 'Kind', type: 'string' }
  { name: 'Subject', type: 'string' }
  { name: 'Detail', type: 'string' }
  { name: 'Severity', type: 'int' }
  { name: 'Held', type: 'boolean' }
  { name: 'ExmonVersion', type: 'string' }
]

resource newWorkspace 'Microsoft.OperationalInsights/workspaces@2023-09-01' = if (empty(existingWorkspaceId)) {
  name: '${namePrefix}-law'
  location: location
  properties: {
    sku: { name: 'PerGB2018' }
    retentionInDays: retentionDays
  }
}

var workspaceId = empty(existingWorkspaceId) ? newWorkspace.id : existingWorkspaceId
var workspaceName = last(split(workspaceId, '/'))

resource workspace 'Microsoft.OperationalInsights/workspaces@2023-09-01' existing = {
  name: workspaceName
}

resource table 'Microsoft.OperationalInsights/workspaces/tables@2022-10-01' = {
  parent: workspace
  name: tableName
  properties: {
    plan: 'Analytics'
    retentionInDays: retentionDays
    schema: {
      name: tableName
      columns: columns
    }
  }
  // No dependsOn newWorkspace: `workspace` takes its name from newWorkspace.id, so Bicep already orders them.
}

resource dcr 'Microsoft.Insights/dataCollectionRules@2023-03-11' = {
  name: '${namePrefix}-dcr'
  location: location
  kind: 'Direct' // gives the DCR its own logs-ingestion endpoint; no separate DCE needed
  properties: {
    streamDeclarations: {
      '${streamName}': { columns: columns }
    }
    destinations: {
      logAnalytics: [
        { name: 'workspace', workspaceResourceId: workspaceId }
      ]
    }
    dataFlows: [
      {
        streams: [ streamName ]
        destinations: [ 'workspace' ]
        transformKql: 'source'
        outputStream: 'Custom-${tableName}'
      }
    ]
  }
  dependsOn: [ table ]
}

resource publisher 'Microsoft.Authorization/roleAssignments@2022-04-01' = if (!empty(ingestPrincipalId)) {
  name: guid(dcr.id, ingestPrincipalId, monitoringMetricsPublisher)
  scope: dcr
  properties: {
    roleDefinitionId: subscriptionResourceId('Microsoft.Authorization/roleDefinitions', monitoringMetricsPublisher)
    principalId: ingestPrincipalId
    principalType: 'ServicePrincipal'
  }
}

@description('settings.dcrImmutableId for the azureMonitor remote.')
output dcrImmutableId string = dcr.properties.immutableId

@description('settings.endpoint for the azureMonitor remote.')
#disable-next-line BCP053 // older type definitions don't list endpoints on Direct DCRs
output ingestionEndpoint string = dcr.properties.endpoints.logsIngestion

@description('settings.streamName for the azureMonitor remote.')
output streamName string = streamName

output workspaceId string = workspaceId
