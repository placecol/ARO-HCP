// Throwaway synthetic alert rule group for end-to-end testing of the Alert Processing
// controller's suppression behavior. NOT part of the committed infra — create it directly in
// Azure (az deployment group create) and delete it when done:
//
//   RG=hcp-underlay-pers-usw3cpla
//   AMW=$(az resource show -g "$RG" --resource-type Microsoft.Monitor/accounts \
//         -n services-<yourRegionShort> --query id -o tsv)
//   LOC=$(az resource show --ids "$AMW" --query location -o tsv)
//   az deployment group create -g "$RG" --template-file apr-test-rule.bicep \
//     --parameters amwId="$AMW" location="$LOC"
//
//   # cleanup (loop 1 GC will then also remove the generated APR CR):
//   az resource delete --ids $(az resource show -g "$RG" \
//     --resource-type Microsoft.AlertsManagement/prometheusRuleGroups \
//     -n apr-suppression-test --query id -o tsv)

@description('ARM resource ID of the Azure Monitor Workspace (AMW) this rule group is scoped to. Use the SERVICES AMW so the controller fans out a services-scoped APR for it.')
param amwId string

@description('Location of the rule group; must match the AMW region.')
param location string

resource testRuleGroup 'Microsoft.AlertsManagement/prometheusRuleGroups@2023-03-01' = {
  name: 'apr-suppression-test'
  location: location
  properties: {
    description: 'Synthetic always-firing alert for APR suppression testing (safe to delete).'
    enabled: true
    scopes: [
      amwId
    ]
    rules: [
      {
        // Fans out to an APR CR named for "apr-suppression-test/AprSuppressionTest".
        alert: 'AprSuppressionTest'
        // vector(1) always returns a sample, so the alert fires every evaluation cycle.
        expression: 'vector(1)'
        for: 'PT1M'
        labels: {
          severity: 'warning'
          // Lands in the fired alert's alertContext; set your APR CR
          // spec.suppression.filter to "suppressme" to exercise suppression.
          test_target: 'suppressme'
        }
        annotations: {
          summary: 'Synthetic alert for APR suppression testing'
        }
      }
    ]
  }
}

output ruleGroupId string = testRuleGroup.id
