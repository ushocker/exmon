// The Event Grid namespace that is exmon's MQTT broker (control plane only;
// events go to Azure Monitor directly).
//
// Who may do what is decided here, so this file is the main security boundary
// for the control plane:
//   - devices (client group "devices": certificate-authenticated clients with
//     attribute role = 'device') may publish only to their OWN status and outbox
//     topics and subscribe only to their OWN inbox: the topic templates use
//     ${client.authenticationName}, the name from their certificate;
//   - admins (an Entra group, via RBAC on the "admin" topic space) may publish to
//     any device's inbox and read any status/outbox. exmonctl signs in as them.
// Nothing a device can do reaches another device's topics.

@description('Prefix for resource names, e.g. "exmon-prod".')
param namePrefix string

param location string = resourceGroup().location

@description('"dev" creates the test client pool; "prod" relies on the sync runbook.')
@allowed([ 'dev', 'prod' ])
param environment string

@description('PEM of the CA that issues device certificates: the test CA (dev) or your Cloud PKI issuing CA (prod). Empty = no CA yet (no device can connect).')
param issuingCaPem string = ''

@description('Throughput units (each: about 10,000 connected devices, 1,000 messages/s in and out).')
@minValue(1)
@maxValue(20)
param throughputUnits int = 1

@description('Object id of the Entra group whose members administer the fleet (exmonctl).')
param adminGroupId string = ''

@description('Dev only: how many test clients (exmon-test-01 ...) to create.')
@minValue(0)
@maxValue(99)
param testPoolSize int = 10

// Built-in role ids. Check them once in your tenant:
//   az role definition list --name "EventGrid TopicSpaces Publisher" --query [].name
@description('Role definition id of "EventGrid TopicSpaces Publisher".')
param topicSpacesPublisherRoleId string = 'a12b0b94-b317-4dcd-84a8-502ce99884c6'
@description('Role definition id of "EventGrid TopicSpaces Subscriber".')
param topicSpacesSubscriberRoleId string = '4b0f2fd7-60b4-4eca-896f-4435034f8bf5'

var readerRoleId = 'acdd72a7-3385-48ef-bd42-f606fba81ae7'

resource ns 'Microsoft.EventGrid/namespaces@2025-02-15' = {
  name: '${namePrefix}-eg'
  location: location
  sku: {
    name: 'Standard'
    capacity: throughputUnits
  }
  properties: {
    // No minimumTlsVersionAllowed: the API rejects it as read-only (Event Grid
    // namespaces already require TLS 1.2 or later).
    topicSpacesConfiguration: {
      state: 'Enabled'
      maximumSessionExpiryInHours: 8 // devices keep their session (queued commands) this long offline
    }
  }
}

// The CA that signs device certificates. A device is accepted only if its
// certificate chains to this CA AND a client with its name exists and is enabled.
// Skipped while issuingCaPem is empty (first deployment before the CA exists).
resource ca 'Microsoft.EventGrid/namespaces/caCertificates@2025-02-15' = if (!empty(issuingCaPem)) {
  parent: ns
  name: '${environment}-issuing-ca'
  properties: {
    description: 'Issues exmon device certificates (${environment})'
    encodedCertificate: issuingCaPem
  }
}

resource devices 'Microsoft.EventGrid/namespaces/clientGroups@2025-02-15' = {
  parent: ns
  name: 'devices'
  properties: {
    description: 'exmon devices'
    query: 'attributes.role = \'device\''
  }
}

// \${ ... } is a literal ${ ... } (not Bicep interpolation): Event Grid fills it
// in with the connecting client's authentication name.
resource devicePub 'Microsoft.EventGrid/namespaces/topicSpaces@2025-02-15' = {
  parent: ns
  name: 'device-pub'
  properties: {
    description: 'A device\'s own status and outbox'
    topicTemplates: [
      'exmon/v1/device/\${client.authenticationName}/status'
      'exmon/v1/device/\${client.authenticationName}/outbox'
    ]
  }
}

resource deviceSub 'Microsoft.EventGrid/namespaces/topicSpaces@2025-02-15' = {
  parent: ns
  name: 'device-sub'
  properties: {
    description: 'A device\'s own inbox'
    topicTemplates: [
      'exmon/v1/device/\${client.authenticationName}/inbox/#'
    ]
  }
}

resource admin 'Microsoft.EventGrid/namespaces/topicSpaces@2025-02-15' = {
  parent: ns
  name: 'admin'
  properties: {
    description: 'Every device\'s inbox, status and outbox, for exmonctl'
    topicTemplates: [
      'exmon/v1/device/+/inbox/#'
      'exmon/v1/device/+/status'
      'exmon/v1/device/+/outbox'
    ]
  }
}

resource devicesPublish 'Microsoft.EventGrid/namespaces/permissionBindings@2025-02-15' = {
  parent: ns
  name: 'devices-publish'
  properties: {
    clientGroupName: devices.name
    topicSpaceName: devicePub.name
    permission: 'Publisher'
  }
}

resource devicesSubscribe 'Microsoft.EventGrid/namespaces/permissionBindings@2025-02-15' = {
  parent: ns
  name: 'devices-subscribe'
  properties: {
    clientGroupName: devices.name
    topicSpaceName: deviceSub.name
    permission: 'Subscriber'
  }
}

// Admins sign in with Entra (exmonctl), so their access is RBAC on the admin
// topic space, not a client group.
resource adminPublish 'Microsoft.Authorization/roleAssignments@2022-04-01' = if (!empty(adminGroupId)) {
  name: guid(admin.id, adminGroupId, topicSpacesPublisherRoleId)
  scope: admin
  properties: {
    roleDefinitionId: subscriptionResourceId('Microsoft.Authorization/roleDefinitions', topicSpacesPublisherRoleId)
    principalId: adminGroupId
    principalType: 'Group'
  }
}

resource adminSubscribe 'Microsoft.Authorization/roleAssignments@2022-04-01' = if (!empty(adminGroupId)) {
  name: guid(admin.id, adminGroupId, topicSpacesSubscriberRoleId)
  scope: admin
  properties: {
    roleDefinitionId: subscriptionResourceId('Microsoft.Authorization/roleDefinitions', topicSpacesSubscriberRoleId)
    principalId: adminGroupId
    principalType: 'Group'
  }
}

resource adminRead 'Microsoft.Authorization/roleAssignments@2022-04-01' = if (!empty(adminGroupId)) {
  name: guid(ns.id, adminGroupId, readerRoleId)
  scope: ns
  properties: {
    roleDefinitionId: subscriptionResourceId('Microsoft.Authorization/roleDefinitions', readerRoleId)
    principalId: adminGroupId
    principalType: 'Group'
  }
}

// Dev only: a pool of test clients, so a YubiKey-signed test device can connect
// without anyone registering it. Their certificates' CN must be the client name.
resource testClients 'Microsoft.EventGrid/namespaces/clients@2025-02-15' = [for i in range(1, environment == 'dev' ? testPoolSize : 0): {
  parent: ns
  name: 'exmon-test-${padLeft(i, 2, '0')}'
  properties: {
    authenticationName: 'exmon-test-${padLeft(i, 2, '0')}'
    clientCertificateAuthentication: {
      validationScheme: 'SubjectMatchesAuthenticationName'
    }
    state: 'Enabled'
    attributes: {
      role: 'device'
      ring: 'test'
      managedBy: 'bicep'
    }
  }
  dependsOn: [ ca ]
}]

output namespaceName string = ns.name
output namespaceId string = ns.id
@description('control.host for exmon, and host for exmonctl.')
output mqttHost string = ns.properties.topicSpacesConfiguration.hostname
