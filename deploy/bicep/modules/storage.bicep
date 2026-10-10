// Storage for exmon release binaries. Devices download from the "releases"
// container over HTTPS; the downloads are public (the exe contains nothing secret)
// because their integrity comes from the signed manifest (SHA-256, size) and the
// code signature, not from secrecy. Only the release pipeline / admins can write.

@description('Prefix for resource names, e.g. "exmon-prod".')
param namePrefix string

param location string = resourceGroup().location

@description('Object id of the Entra group allowed to upload releases (Storage Blob Data Contributor).')
param adminGroupId string = ''

var accountName = take(toLower('${replace(namePrefix, '-', '')}rel${uniqueString(resourceGroup().id)}'), 24)
var blobContributor = 'ba92f5b4-2d11-453d-a403-e96b0029c9fe'

resource account 'Microsoft.Storage/storageAccounts@2023-05-01' = {
  name: accountName
  location: location
  kind: 'StorageV2'
  sku: { name: 'Standard_ZRS' }
  properties: {
    minimumTlsVersion: 'TLS1_2'
    supportsHttpsTrafficOnly: true
    allowBlobPublicAccess: true // only the releases container is public, read-only
    allowSharedKeyAccess: false // uploads use Entra sign-in, not account keys
  }
}

resource blobs 'Microsoft.Storage/storageAccounts/blobServices@2023-05-01' = {
  parent: account
  name: 'default'
}

resource releases 'Microsoft.Storage/storageAccounts/blobServices/containers@2023-05-01' = {
  parent: blobs
  name: 'releases'
  properties: {
    publicAccess: 'Blob' // anonymous read of a blob by its exact URL; no listing
  }
}

resource uploaders 'Microsoft.Authorization/roleAssignments@2022-04-01' = if (!empty(adminGroupId)) {
  name: guid(releases.id, adminGroupId, blobContributor)
  scope: releases
  properties: {
    roleDefinitionId: subscriptionResourceId('Microsoft.Authorization/roleDefinitions', blobContributor)
    principalId: adminGroupId
    principalType: 'Group'
  }
}

output accountName string = account.name
@description('control.update.allowedHosts for exmon.')
output downloadHost string = '${account.name}.blob.${environment().suffixes.storage}'
output releasesUrl string = '${account.properties.primaryEndpoints.blob}releases'
