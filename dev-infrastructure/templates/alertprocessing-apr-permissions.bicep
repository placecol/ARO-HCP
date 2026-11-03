@description('The name of the Alert Processing controller managed identity')
param alertprocessingMIName string

@description('The resource group containing the Alert Processing controller managed identity')
param alertprocessingMIResourceGroup string

param useLeasedInfrastructureIdentities bool = false
param infrastructureIdentityResourceGroup string = ''

@description('GUID of the built-in monitoring role to grant on this resource group. Defaults to Monitoring Contributor; pass Monitoring Reader for read-only (e.g. the regional RG, where the controller only reads rule groups and link-references the AMWs and action group).')
// Monitoring Contributor: 749f88d5-cbae-40b8-bcfc-e573ddc772fa
// Monitoring Reader:      43d0d8ad-25c7-4714-9337-8ba259a9fe05
param monitoringRoleGuid string = '749f88d5-cbae-40b8-bcfc-e573ddc772fa'

var monitoringRoleId = subscriptionResourceId(
  'Microsoft.Authorization/roleDefinitions',
  monitoringRoleGuid
)

resource alertprocessingMSI 'Microsoft.ManagedIdentity/userAssignedIdentities@2023-01-31' existing = {
  scope: resourceGroup(useLeasedInfrastructureIdentities ? infrastructureIdentityResourceGroup : alertprocessingMIResourceGroup)
  name: alertprocessingMIName
}

resource monitoringRoleAssignment 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  scope: resourceGroup()
  name: guid(resourceGroup().id, alertprocessingMSI.id, monitoringRoleId)
  properties: {
    roleDefinitionId: monitoringRoleId
    principalId: alertprocessingMSI.properties.principalId
    principalType: 'ServicePrincipal'
  }
}
