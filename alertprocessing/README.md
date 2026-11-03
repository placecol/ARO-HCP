# alertprocessing

A standalone controller-manager that owns the Azure **Alert Processing Rule (APR)**
resources for a region, implementing the design in `../apr-controller-design.md`
(ARO-30315 spike).

## Why

Previously the admin API created/deleted APR Azure resources directly and suppressed
alerts with `RemoveAllActionGroups`, which strips *every* action group — so suppressed
alerts stopped flowing to the Event Hub and on to Kusto, losing the record of what fired.

This controller instead keeps **one long-lived APR resource per alert rule** and only ever
**adds/updates** action groups, never removes them. Suppression is expressed as a
time-bounded `alertContext doesNotContain` filter, and the time window is enforced entirely
by the reconcile loop (the Azure resource carries no schedule), so an elapsed window simply
reverts the resource to its default form on the next reconcile — no background cleanup task.

## Architecture

- **CRD** `AlertProcessingRule` (`alertprocessing.aro-hcp.azure.com/v1alpha1`, cluster-scoped).
  One CR per `(scope, alertRuleName)`. Catalog fields (`scope`, `alertRuleName`,
  `defaultActionGroupID`) are owned by loop 1 via a dedicated server-side-apply field
  manager; `spec.suppression` is owned by the operator-facing API, so loop 1 never clears it.
- **Loop 1 — `catalogsync`** (`pkg/controllers/catalogsync`): polls Azure Managed Prometheus
  `prometheusRuleGroups` on the hcps and services AMWs, fans out one CR per alert rule
  (scope derived from which AMW the rule group is scoped to), and garbage-collects CRs whose
  rule no longer exists in Azure.
- **Loop 2 — `aprreconcile`** (`pkg/controllers/aprreconcile`): renders each CR into the
  desired Azure APR resource (`pkg/controllers/aprreconcile/render.go`, a pure function) and
  writes it, unioning the default action group with any already present. A finalizer deletes
  the Azure resource before the CR is removed. Re-enqueues at suppression window edges.

## Development

```bash
make update-codegen   # regenerate deepcopy/clientset/listers/informers/applyconfiguration
make build            # build the binary
make test             # unit tests
```

Codegen follows the mgmt-agent variant (no `gen_openapi` step); the apply configurations are
generated via applyconfiguration-gen's built-in heuristics.

## Remaining integration (deferred infra)

The service, its Helm chart (`deploy/`), `pipeline.yaml`, and `values.yaml` are complete and
the chart renders (`helm template ./deploy`). The following shared-repo + infra wiring is the
remaining step to make it roll out end-to-end:

1. **config** — add an `alertprocessing` block (image, `k8s.{namespace,replicas,
   serviceAccountName,resources}`) and `monitoring.alertProcessing.{hcpsWorkspaceResourceId,
   servicesWorkspaceResourceId,defaultActionGroupId,catalogPollInterval}` to
   `config/config.yaml` and `config/config.schema.json` (top-level `required` + the
   `monitoring` definition), then `cd config && make materialize` to regenerate
   `config/rendered/**` and the `zz_fixture_TestHelmTemplate_*` golden.
2. **topology** — add `Microsoft.Azure.ARO.HCP.AlertProcessing` → `alertprocessing/pipeline.yaml`
   under the Service tree in `topology.yaml`.
3. **root Makefile** — add `alertprocessing` to `services_svc_pipelines`, a
   `build-alertprocessing` target, and the `record-override` entries.
4. **bicep MSI** — a service-cluster MSI with role assignments (Monitoring Reader on the two
   AMWs; alerts-management contributor on the regional RG) and a pipeline lookup step
   producing `subscriptionId` + `alertprocessingMsiClientId` + `tenantId` for `values.yaml`.
