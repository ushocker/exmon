// Key Vault holding exmon's two document-signing keys. Their public halves are
// built into exmon (internal/signdoc/trust); the private halves never leave the
// vault's HSM. exmonctl asks the vault to sign a digest, using the admin's own
// Entra sign-in (az login), so access is controlled by RBAC (and PIM if you use it).
//
//   exmon-config-signing   signs cloud configs and commands
//   exmon-release-signing  signs release manifests (a separate key, so a config
//                          mistake can never approve new code)

@description('Prefix for resource names, e.g. "exmon-prod". Key Vault names are global and at most 24 characters.')
param namePrefix string

param location string = resourceGroup().location

@description('Object id of the Entra group whose members may sign (Key Vault Crypto User).')
param signerGroupId string = ''

@description('Premium puts the keys in an HSM. Standard keeps them in software.')
@allowed([ 'premium', 'standard' ])
param sku string = 'premium'

// Vault names must be globally unique: add a short hash of the resource group.
var vaultName = take('${replace(namePrefix, '-', '')}kv${uniqueString(resourceGroup().id)}', 24)
var keyType = sku == 'premium' ? 'EC-HSM' : 'EC'
var cryptoUser = '12338af0-0e69-4776-bea7-57ae8d297424' // Key Vault Crypto User: sign/verify, read public keys

resource vault 'Microsoft.KeyVault/vaults@2023-07-01' = {
  name: vaultName
  location: location
  properties: {
    tenantId: subscription().tenantId
    sku: { family: 'A', name: sku }
    enableRbacAuthorization: true // access by role assignments, not access policies
    enablePurgeProtection: true   // a deleted key can't be purged during the retention period
    softDeleteRetentionInDays: 90
    publicNetworkAccess: 'Enabled' // exmonctl runs on admin workstations; tighten with firewall rules if you can
  }
}

resource configKey 'Microsoft.KeyVault/vaults/keys@2023-07-01' = {
  parent: vault
  name: 'exmon-config-signing'
  properties: {
    kty: keyType
    curveName: 'P-256'
    keyOps: [ 'sign', 'verify' ]
  }
}

resource releaseKey 'Microsoft.KeyVault/vaults/keys@2023-07-01' = {
  parent: vault
  name: 'exmon-release-signing'
  properties: {
    kty: keyType
    curveName: 'P-256'
    keyOps: [ 'sign', 'verify' ]
  }
}

resource signers 'Microsoft.Authorization/roleAssignments@2022-04-01' = if (!empty(signerGroupId)) {
  name: guid(vault.id, signerGroupId, cryptoUser)
  scope: vault
  properties: {
    roleDefinitionId: subscriptionResourceId('Microsoft.Authorization/roleDefinitions', cryptoUser)
    principalId: signerGroupId
    principalType: 'Group'
  }
}

output vaultName string = vault.name
output vaultUri string = vault.properties.vaultUri
