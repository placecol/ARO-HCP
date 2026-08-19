#!/usr/bin/env python3
"""Generate a CSV catalog of every alert defined in the ARO-HCP repo.

Covers PrometheusRule-based alerts (the overwhelming majority) plus the
non-Prometheus alert types found by manual audit: Azure Monitor metric
alerts (Microsoft.Insights/metricAlerts) and legacy Grafana panel-embedded
alerts. "Customer Centric" is a judgment call (does firing this alert
implicate exactly one customer's resource, vs. fleet/service/infra health)
and is applied per-file with per-rule-name overrides for files that mix
both kinds of rules (documented inline below). Treat it as a reviewable
starting point, not ground truth.
"""
import csv
import re
import sys
from pathlib import Path

import yaml

REPO = Path("/home/cplace/code/ARO-HCP/main")

# Source-of-truth PrometheusRule files: excludes _test fixtures, the
# vendored .upstream.yaml reference copy, and generated zz_fixture/rendered
# Helm output (those are duplicates of the files listed here).
RULE_FILES = [
    "admin/alerts/admin-prometheusRule.yaml",
    "backend/alerts/backend-async-operations-prometheusRule.yaml",
    "backend/alerts/backend-prometheusRule.yaml",
    "backend/alerts/backend-retryhotloop-prometheusRule.yaml",
    "cluster-service/alerts/cluster-service-slo-rules.yaml",
    "fleet/alerts/fleet-prometheusRule.yaml",
    "frontend/alerts/frontend-latency-prometheusRule.yaml",
    "frontend/alerts/frontend-path-latency-prometheusRule.yaml",
    "frontend/alerts/frontend-prometheusRule.yaml",
    "frontend/alerts/mise-prometheusRule.yaml",
    "kube-applier/alerts/kube-applier-prometheusRule.yaml",
    "maestro/alerts/maestro-prometheusRule.yaml",
    "observability/alerts/access-cluster-slo-prometheusRule.yaml",
    "observability/alerts/arobit-prometheusRule.yaml",
    "observability/alerts/cluster-provision-slo-prometheusRule.yaml",
    "observability/alerts/HCPclusterOperators-prometheusRule.yaml",
    "observability/alerts/HCPkasRecord-prometheusRule-apiserver-requests.yaml",
    "observability/alerts/HCPkasRecord-prometheusRule-KSM.yaml",
    "observability/alerts/HCPkasRecord-prometheusRule-latency.yaml",
    "observability/alerts/hcp-test-clusters-prometheusRule.yaml",
    "observability/alerts/imageRegistryPolicy-prometheusRule.yaml",
    "observability/alerts/kubeContainerOOM-prometheusRule.yaml",
    "observability/alerts/kubeNode-prometheusRule.yaml",
    "observability/alerts/kubernetesControlPlane-prometheusRule.yaml",
    "observability/alerts/kusto-logs-age-prometheusRule.yaml",
    "observability/alerts/leaderelection-prometheusRule.yaml",
    "observability/alerts/mgmt-capacity-prometheusRule.yaml",
    "observability/alerts/msi-credential-refresher-prometheusRule.yaml",
    "observability/alerts/nodepool-slo-prometheusRule.yaml",
    "observability/alerts/prometheus-prometheusRule.yaml",
    "observability/alerts/serviceMemoryResources-prometheusRule.yaml",
    "observability/alerts/service-tag-public-ip-usage.yaml",
    "observability/alerts/userJourneyClusterUpgradeMonitor-prometheusRule.yaml",
]

# Default "is this about exactly one customer's resource" per file.
FILE_CUSTOMER_CENTRIC_DEFAULT = {
    "admin/alerts/admin-prometheusRule.yaml": False,
    "backend/alerts/backend-async-operations-prometheusRule.yaml": True,
    "backend/alerts/backend-prometheusRule.yaml": False,
    "backend/alerts/backend-retryhotloop-prometheusRule.yaml": False,
    "cluster-service/alerts/cluster-service-slo-rules.yaml": False,
    "fleet/alerts/fleet-prometheusRule.yaml": False,
    "frontend/alerts/frontend-latency-prometheusRule.yaml": False,
    "frontend/alerts/frontend-path-latency-prometheusRule.yaml": False,
    "frontend/alerts/frontend-prometheusRule.yaml": False,
    "frontend/alerts/mise-prometheusRule.yaml": False,
    "kube-applier/alerts/kube-applier-prometheusRule.yaml": False,
    "maestro/alerts/maestro-prometheusRule.yaml": False,
    "observability/alerts/access-cluster-slo-prometheusRule.yaml": False,
    "observability/alerts/arobit-prometheusRule.yaml": False,
    "observability/alerts/cluster-provision-slo-prometheusRule.yaml": False,
    "observability/alerts/HCPclusterOperators-prometheusRule.yaml": True,
    "observability/alerts/HCPkasRecord-prometheusRule-apiserver-requests.yaml": False,
    "observability/alerts/HCPkasRecord-prometheusRule-KSM.yaml": False,
    "observability/alerts/HCPkasRecord-prometheusRule-latency.yaml": False,
    "observability/alerts/hcp-test-clusters-prometheusRule.yaml": True,
    "observability/alerts/imageRegistryPolicy-prometheusRule.yaml": False,
    "observability/alerts/kubeContainerOOM-prometheusRule.yaml": True,
    "observability/alerts/kubeNode-prometheusRule.yaml": False,
    "observability/alerts/kubernetesControlPlane-prometheusRule.yaml": True,
    "observability/alerts/kusto-logs-age-prometheusRule.yaml": False,
    "observability/alerts/leaderelection-prometheusRule.yaml": False,
    "observability/alerts/mgmt-capacity-prometheusRule.yaml": False,
    "observability/alerts/msi-credential-refresher-prometheusRule.yaml": True,
    "observability/alerts/nodepool-slo-prometheusRule.yaml": False,
    "observability/alerts/prometheus-prometheusRule.yaml": False,
    "observability/alerts/serviceMemoryResources-prometheusRule.yaml": False,
    "observability/alerts/service-tag-public-ip-usage.yaml": False,
    "observability/alerts/userJourneyClusterUpgradeMonitor-prometheusRule.yaml": True,
}

# Per-alert-name overrides for files that mix customer-centric "stuck
# operation" detectors with fleet-wide SLO burn-rate/saturation alerts.
ALERT_NAME_OVERRIDES = {
    "userJourneyAccessClusterStuckOperation": True,
    "UJNodePoolStuckOperation": True,
}

# HCPkasRecord-* files and recording-rules/*.yaml contain only recording
# rules (`record:`), not alerts, so they naturally contribute zero rows.

NON_PROMETHEUS_ALERTS = [
    # name, short description, source location, type, customer_centric, has_resource_id
    ("Cosmos DB Normalized RU Consumption High",
     "Cosmos DB normalized RU consumption > 70% over 10m, evaluated every 1m.",
     "dev-infrastructure/modules/metrics/cosmos-alerts.bicep", "AzureMetricAlert", False, False),
    ("AMW Approaching Active TimeSeries Limit",
     "Azure Monitor Workspace active time series utilization > 75%.",
     "dev-infrastructure/modules/metrics/amw-ingestion-alerts.bicep", "AzureMetricAlert", False, False),
    ("AMW High Risk Active TimeSeries Limit",
     "Azure Monitor Workspace active time series utilization > 95%, throttling imminent.",
     "dev-infrastructure/modules/metrics/amw-ingestion-alerts.bicep", "AzureMetricAlert", False, False),
    ("AMW Approaching Event Ingestion Limit",
     "Azure Monitor Workspace events/min ingestion utilization > 75%.",
     "dev-infrastructure/modules/metrics/amw-ingestion-alerts.bicep", "AzureMetricAlert", False, False),
    ("AMW High Risk Event Ingestion Limit",
     "Azure Monitor Workspace events/min ingestion utilization > 95%, throttling imminent.",
     "dev-infrastructure/modules/metrics/amw-ingestion-alerts.bicep", "AzureMetricAlert", False, False),
    ("AMW Low Event Ingestion Utilization",
     "Azure Monitor Workspace events/min ingestion below threshold; possible broken remote-write.",
     "dev-infrastructure/modules/metrics/amw-ingestion-alerts.bicep", "AzureMetricAlert", False, False),
    ("Kusto Ingestion Latency Above Average",
     "Kusto cluster ingestion latency above its dynamic baseline (3 of 4 periods).",
     "dev-infrastructure/modules/metrics/kusto-alerts.bicep", "AzureMetricAlert", False, False),
    ("Kusto Ingestion Latency High",
     "Kusto cluster ingestion latency exceeds 15 minutes.",
     "dev-infrastructure/modules/metrics/kusto-alerts.bicep", "AzureMetricAlert", False, False),
    ("Missing TCP SYN-ACK",
     "Cilium dashboard legacy panel alert: missing TCPv4 SYN-ACKs.",
     "observability/grafana-dashboards/infra-dashboards/cilium.json", "GrafanaLegacyPanelAlert", False, False),
    ("Missing TCPv6 SYN-ACKs alert",
     "Cilium dashboard legacy panel alert: missing TCPv6 SYN-ACKs.",
     "observability/grafana-dashboards/infra-dashboards/cilium.json", "GrafanaLegacyPanelAlert", False, False),
    ("Missing ICMPv4 Echo-Reply alert",
     "Cilium dashboard legacy panel alert: missing ICMPv4 Echo-Replies.",
     "observability/grafana-dashboards/infra-dashboards/cilium.json", "GrafanaLegacyPanelAlert", False, False),
    ("DNS Request/Response Symmetry alert",
     "Cilium dashboard legacy panel alert: DNS request/response asymmetry.",
     "observability/grafana-dashboards/infra-dashboards/cilium.json", "GrafanaLegacyPanelAlert", False, False),
]


def clean_text(s: str) -> str:
    if not s:
        return ""
    s = re.sub(r"\s+", " ", s.strip())
    return s[:150] + ("..." if len(s) > 150 else "")


def contains_resource_id(rule: dict) -> bool:
    blob = " ".join([
        rule.get("expr", "") or rule.get("expression", "") or "",
        str(rule.get("labels", {})),
        str(rule.get("annotations", {})),
    ])
    return "resource_id" in blob


def extract_rows():
    rows = []
    for rel in RULE_FILES:
        path = REPO / rel
        with open(path) as f:
            doc = yaml.safe_load(f)
        groups = (doc.get("spec", {}) or {}).get("groups", []) or []
        default_cc = FILE_CUSTOMER_CENTRIC_DEFAULT[rel]
        for group in groups:
            for rule in group.get("rules", []) or []:
                if "alert" not in rule:
                    continue  # recording rule, not an alert
                name = rule["alert"]
                annotations = rule.get("annotations", {}) or {}
                desc = annotations.get("summary") or annotations.get("description") or ""
                has_rid = contains_resource_id(rule)
                cc = ALERT_NAME_OVERRIDES.get(name, default_cc)
                rows.append([
                    name,
                    clean_text(desc),
                    rel,
                    "PrometheusRule",
                    "Yes" if cc else "No",
                    "Yes" if has_rid else "No",
                ])
    for name, desc, loc, typ, cc, has_rid in NON_PROMETHEUS_ALERTS:
        rows.append([name, clean_text(desc), loc, typ, "Yes" if cc else "No", "Yes" if has_rid else "No"])
    return rows


def main():
    out_path = sys.argv[1] if len(sys.argv) > 1 else "/tmp/aro_hcp_alert_catalog.csv"
    rows = extract_rows()
    with open(out_path, "w", newline="") as f:
        w = csv.writer(f)
        w.writerow(["Name", "Short Description", "Source Location", "Type", "Customer Centric", "Has resource_id"])
        w.writerows(rows)
    print(f"Wrote {len(rows)} rows to {out_path}")


if __name__ == "__main__":
    main()
