#!/usr/bin/env bash
# Generates a CSV report of alert fired/resolved events for a given Prometheus
# alert rule, including which action groups were triggered, whether any were
# suppressed, and which alert processing rule(s) caused the suppression,
# based on Alerts Management API history.
set -euo pipefail

if [[ $# -lt 4 ]]; then
  echo "Usage: $0 <subscription-id> <resource-group> <prometheus-rule-group> <rule-name> [output-csv] [time-range]" >&2
  echo "Example: $0 1d3378d3-5a3f-4712-85a1-2485495dfc4b hcp-underlay-pers-usw3cpla kube-container-oom-rules KubeContainerOOMKilled report.csv 7d" >&2
  exit 1
fi

SUBSCRIPTION_ID="$1"
RESOURCE_GROUP="$2"
RULE_GROUP="$3"
RULE_NAME="$4"
OUTPUT_CSV="${5:-alert-history-report.csv}"
TIME_RANGE="${6:-7d}"

ALERT_RULE_ID="/subscriptions/${SUBSCRIPTION_ID}/resourcegroups/${RESOURCE_GROUP}/providers/Microsoft.AlertsManagement/prometheusRuleGroups/${RULE_GROUP}"

alerts_json=$(az rest --method get \
  --url "https://management.azure.com/subscriptions/${SUBSCRIPTION_ID}/providers/Microsoft.AlertsManagement/alerts?api-version=2019-05-05-preview&alertRule=${ALERT_RULE_ID}&timeRange=${TIME_RANGE}")

mapfile -t alert_ids < <(echo "$alerts_json" | jq -r --arg rule "$RULE_NAME" \
  '.value[] | select(.name | endswith("/" + $rule)) | .id')

if [[ ${#alert_ids[@]} -eq 0 ]]; then
  echo "No alert instances found for rule '${RULE_NAME}' in rule group '${RULE_GROUP}' over the last ${TIME_RANGE}."
  exit 0
fi

echo "time,alert,condition,triggered,suppressed,suppressed_by" > "$OUTPUT_CSV"

# jq program: walk an alert instance's history chronologically, group action-group
# triggered/suppressed events under the fired/resolved transition that precedes them,
# and emit one CSV row per transition.
JQ_PROGRAM='
  (.properties.modifications // []) | sort_by(.modifiedAt) as $mods |
  reduce $mods[] as $m (
    {rows: [], current: null};
    if ($m.modificationEvent == "AlertCreated" or $m.modificationEvent == "MonitorConditionChange") then
      (if .current != null then .rows += [.current] else . end)
      | .current = {
          time: $m.modifiedAt,
          condition: (if $m.modificationEvent == "AlertCreated" then "Fired" else $m.newValue end),
          triggered: [],
          suppressedBy: []
        }
    elif (.current == null) then
      .
    elif ($m.description // "" | test("suppress"; "i")) then
      .current.suppressedBy += [(($m.description | match("alert processing rules?: (?<r>[^)]+)"; "i").captures[0].string) // $m.description)]
    elif ($m.modificationEvent == "ActionsTriggered") then
      .current.triggered += [(($m.description | match("[Aa]ction group (?<ag>\\S+)").captures[0].string) // $m.description)]
    else
      .
    end
  )
  | (.rows + (if .current != null then [.current] else [] end))
  | .[]
  | [.time, $alert, .condition, (.triggered | join(",")), ((.suppressedBy | length) > 0), (.suppressedBy | join(","))]
  | @csv
'

for id in "${alert_ids[@]}"; do
  alert_name=$(echo "$alerts_json" | jq -r --arg id "$id" '.value[] | select(.id == $id) | .name | split("/")[-1]')

  history_json=$(az rest --method get --url "https://management.azure.com${id}/history?api-version=2019-05-05-preview")

  echo "$history_json" | jq -r --arg alert "$alert_name" "$JQ_PROGRAM" >> "$OUTPUT_CSV"
done

echo "Wrote $(($(wc -l < "$OUTPUT_CSV") - 1)) row(s) to $OUTPUT_CSV"
