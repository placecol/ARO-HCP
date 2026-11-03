@description('The name of the Alert Processing controller MSI')
param alertprocessingMsiName string

param useLeasedInfrastructureIdentities bool = false
param infrastructureIdentityResourceGroup string = ''
var identityScope = resourceGroup(useLeasedInfrastructureIdentities
  ? infrastructureIdentityResourceGroup
  : resourceGroup().name)

//
//   A L E R T   P R O C E S S I N G   L O O K U P
//

resource alertprocessingIdentity 'Microsoft.ManagedIdentity/userAssignedIdentities@2023-01-31' existing = {
  scope: identityScope
  name: alertprocessingMsiName
}

output tenantId string = tenant().tenantId
output subscriptionId string = subscription().subscriptionId
output alertprocessingMsiClientId string = alertprocessingIdentity.properties.clientId
