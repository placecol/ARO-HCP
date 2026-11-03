using '../templates/alertprocessing-apr-permissions.bicep'

param alertprocessingMIName = '{{ .alertprocessing.managedIdentityName }}'
param alertprocessingMIResourceGroup = '{{ .svc.rg }}'
param useLeasedInfrastructureIdentities = {{ .infrastructureIdentities.useLeased }}
param infrastructureIdentityResourceGroup = '{{ .infrastructureIdentities.serviceResourceGroup }}'
// Monitoring Contributor — the controller creates/updates/deletes the APR resources here.
param monitoringRoleGuid = '749f88d5-cbae-40b8-bcfc-e573ddc772fa'
