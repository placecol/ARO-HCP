@description('Name of the default action group the Alert Processing controller applies to every rule')
param actionGroupName string = 'alertprocessing-default'

@description('Short display name for the action group (<= 12 chars)')
param actionGroupShortName string = 'aprctl'

@description('Optional email receiver. When empty the action group is created with no receivers.')
param notificationEmail string = ''

@description('Whether the action group is enabled')
param enabled bool = true

// The Alert Processing controller keeps one long-lived APR resource per alert rule and only
// ever adds/updates this action group, so alerts always flow to it. In environments without
// managed ICM action groups (e.g. personal dev) this provides a concrete action group to
// apply; set notificationEmail to actually receive the alerts.
resource alertprocessingActionGroup 'Microsoft.Insights/actionGroups@2024-10-01-preview' = {
  name: actionGroupName
  location: 'global'
  properties: {
    enabled: enabled
    groupShortName: actionGroupShortName
    emailReceivers: empty(notificationEmail)
      ? []
      : [
          {
            name: 'oncall'
            emailAddress: notificationEmail
            useCommonAlertSchema: true
          }
        ]
  }
}

output actionGroupId string = alertprocessingActionGroup.id
