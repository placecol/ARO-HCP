// Copyright 2026 Microsoft Corporation
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package catalogsync

import (
	"context"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	alertprocessingv1alpha1 "github.com/Azure/ARO-HCP/alertprocessing/pkg/apis/alertprocessing/v1alpha1"
	alertprocessingclient "github.com/Azure/ARO-HCP/alertprocessing/pkg/generated/clientset/versioned"
	fakeclient "github.com/Azure/ARO-HCP/alertprocessing/pkg/generated/clientset/versioned/fake"
	alertprocessinginformers "github.com/Azure/ARO-HCP/alertprocessing/pkg/generated/informers/externalversions"
)

const (
	hcpsAMW  = "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Monitor/accounts/hcps-amw"
	svcsAMW  = "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Monitor/accounts/services-amw"
	actionAG = "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Insights/actionGroups/icm"
)

type fakeRuleLister struct {
	byAMW map[string][]string
}

func (f *fakeRuleLister) ListAlertRules(_ context.Context, amwResourceID string) ([]string, error) {
	return f.byAMW[amwResourceID], nil
}

func testScopes() []ScopeAMW {
	return []ScopeAMW{
		{Scope: alertprocessingv1alpha1.ScopeHCPs, AMWResourceID: hcpsAMW},
		{Scope: alertprocessingv1alpha1.ScopeServices, AMWResourceID: svcsAMW},
	}
}

func constAG(_ alertprocessingv1alpha1.Scope, _ string) string { return actionAG }

// TestBuildDesired covers the fan-out: one entry per (scope, alert rule), with scope derived
// from which AMW the rule was found on, deterministic naming, and the resolved action group.
func TestBuildDesired(t *testing.T) {
	lister := &fakeRuleLister{byAMW: map[string][]string{
		hcpsAMW: {"kubernetes-apps/KubePodNotReady", "kubernetes-apps/KubePodCrashLooping"},
		svcsAMW: {"frontend/HighLatency"},
	}}
	c := &Controller{ruleLister: lister, scopes: testScopes(), actionGroup: constAG}

	desired, err := c.buildDesired(context.Background())
	if err != nil {
		t.Fatalf("buildDesired: %v", err)
	}
	if len(desired) != 3 {
		t.Fatalf("want 3 entries, got %d", len(desired))
	}

	hcpsName := ResourceName(alertprocessingv1alpha1.ScopeHCPs, "kubernetes-apps/KubePodNotReady")
	got, ok := desired[hcpsName]
	if !ok {
		t.Fatalf("missing hcps entry %q", hcpsName)
	}
	if got.scope != alertprocessingv1alpha1.ScopeHCPs || got.alertRuleName != "kubernetes-apps/KubePodNotReady" || got.actionGroupID != actionAG {
		t.Errorf("hcps entry = %+v", got)
	}

	svcName := ResourceName(alertprocessingv1alpha1.ScopeServices, "frontend/HighLatency")
	if desired[svcName].scope != alertprocessingv1alpha1.ScopeServices {
		t.Errorf("services entry scope = %q, want services", desired[svcName].scope)
	}
}

// TestCatalogApplyConfig_OmitsSuppression is the linchpin guarantee: the catalog apply never
// sets spec.suppression, so under server-side apply it can never clear an operator's
// suppression (which is owned by a different field manager).
func TestCatalogApplyConfig_OmitsSuppression(t *testing.T) {
	cfg := catalogApplyConfig("some-name", desiredRule{
		scope:         alertprocessingv1alpha1.ScopeHCPs,
		alertRuleName: "kubernetes-apps/KubePodNotReady",
		actionGroupID: actionAG,
	})
	if cfg.Spec == nil {
		t.Fatal("spec must be set")
	}
	if cfg.Spec.Suppression != nil {
		t.Error("catalog apply must NOT set spec.suppression (would clobber operator suppression)")
	}
	if cfg.Spec.Scope == nil || *cfg.Spec.Scope != alertprocessingv1alpha1.ScopeHCPs {
		t.Error("catalog apply must set scope")
	}
	if cfg.Spec.DefaultActionGroupID == nil || *cfg.Spec.DefaultActionGroupID != actionAG {
		t.Error("catalog apply must set defaultActionGroupID")
	}
	if cfg.Labels[ManagedByLabel] != ControllerName {
		t.Error("catalog apply must set managed-by label")
	}
}

// TestGarbageCollect deletes managed rules that Azure no longer reports, and leaves others.
func TestGarbageCollect(t *testing.T) {
	stale := ResourceName(alertprocessingv1alpha1.ScopeHCPs, "old/RemovedAlert")
	kept := ResourceName(alertprocessingv1alpha1.ScopeHCPs, "kubernetes-apps/KubePodNotReady")

	client := fakeclient.NewSimpleClientset(
		managedRule(stale, "old/RemovedAlert"),
		managedRule(kept, "kubernetes-apps/KubePodNotReady"),
	)
	c, ctx := newSyncedController(t, client)

	// Desired still contains "kept" but not "stale".
	desired := map[string]desiredRule{
		kept: {scope: alertprocessingv1alpha1.ScopeHCPs, alertRuleName: "kubernetes-apps/KubePodNotReady", actionGroupID: actionAG},
	}
	if err := c.garbageCollect(ctx, desired); err != nil {
		t.Fatalf("garbageCollect: %v", err)
	}

	if _, err := client.AlertprocessingV1alpha1().AlertProcessingRules().Get(ctx, stale, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("stale rule should be deleted, got err=%v", err)
	}
	if _, err := client.AlertprocessingV1alpha1().AlertProcessingRules().Get(ctx, kept, metav1.GetOptions{}); err != nil {
		t.Errorf("kept rule should remain: %v", err)
	}
}

func managedRule(name, alertRuleName string) *alertprocessingv1alpha1.AlertProcessingRule {
	return &alertprocessingv1alpha1.AlertProcessingRule{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{ManagedByLabel: ControllerName},
		},
		Spec: alertprocessingv1alpha1.AlertProcessingRuleSpec{
			Scope:                alertprocessingv1alpha1.ScopeHCPs,
			AlertRuleName:        alertRuleName,
			DefaultActionGroupID: actionAG,
		},
	}
}

// newSyncedController builds a controller with an informer started and synced over client.
func newSyncedController(t *testing.T, client alertprocessingclient.Interface) (*Controller, context.Context) {
	t.Helper()
	informers := alertprocessinginformers.NewSharedInformerFactory(client, 0)
	_ = informers.Alertprocessing().V1alpha1().AlertProcessingRules().Informer()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	informers.Start(ctx.Done())
	informers.WaitForCacheSync(ctx.Done())
	return NewController(client, informers, &fakeRuleLister{}, testScopes(), constAG, time.Minute), ctx
}
