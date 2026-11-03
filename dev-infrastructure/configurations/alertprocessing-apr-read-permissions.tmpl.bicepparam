using '../templates/alertprocessing-apr-permissions.bicep'

param alertprocessingMIName = '{{ .alertprocessing.managedIdentityName }}'
param alertprocessingMIResourceGroup = '{{ .svc.rg }}'
param useLeasedInfrastructureIdentities = {{ .infrastructureIdentities.useLeased }}
param infrastructureIdentityResourceGroup = '{{ .infrastructureIdentities.serviceResourceGroup }}'
// Monitoring Reader — the controller only reads rule groups and link-references the AMWs and
// action group in the regional RG; it never writes there.
param monitoringRoleGuid = '43d0d8ad-25c7-4714-9337-8ba259a9fe05'
