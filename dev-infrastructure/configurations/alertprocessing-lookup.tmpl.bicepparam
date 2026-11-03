using '../modules/alertprocessing/alertprocessing-lookup.bicep'

param useLeasedInfrastructureIdentities = {{ .infrastructureIdentities.useLeased }}
param infrastructureIdentityResourceGroup = '{{ .infrastructureIdentities.serviceResourceGroup }}'

param alertprocessingMsiName = '{{ .alertprocessing.managedIdentityName }}'
