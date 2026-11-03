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

package aprreconcile

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/alertprocessingrules/armalertprocessingrules"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fakeclient "github.com/Azure/ARO-HCP/alertprocessing/pkg/generated/clientset/versioned/fake"
)

// fakeAPR is an in-memory APRClient.
type fakeAPR struct {
	store   map[string]*armalertprocessingrules.AlertProcessingRule
	created map[string]armalertprocessingrules.AlertProcessingRule
	deleted []string
}

func newFakeAPR() *fakeAPR {
	return &fakeAPR{
		store:   map[string]*armalertprocessingrules.AlertProcessingRule{},
		created: map[string]armalertprocessingrules.AlertProcessingRule{},
	}
}

func (f *fakeAPR) Get(_ context.Context, name string) (*armalertprocessingrules.AlertProcessingRule, error) {
	return f.store[name], nil
}

func (f *fakeAPR) CreateOrUpdate(_ context.Context, name string, apr armalertprocessingrules.AlertProcessingRule) error {
	f.created[name] = apr
	stored := apr
	f.store[name] = &stored
	return nil
}

func (f *fakeAPR) Delete(_ context.Context, name string) error {
	f.deleted = append(f.deleted, name)
	delete(f.store, name)
	return nil
}

func actionGroupIDs(t *testing.T, apr armalertprocessingrules.AlertProcessingRule) []string {
	t.Helper()
	if len(apr.Properties.Actions) != 1 {
		t.Fatalf("want exactly one action, got %d", len(apr.Properties.Actions))
	}
	add, ok := apr.Properties.Actions[0].(*armalertprocessingrules.AddActionGroups)
	if !ok {
		t.Fatalf("action is %T, want *AddActionGroups", apr.Properties.Actions[0])
	}
	var ids []string
	for _, id := range add.ActionGroupIDs {
		ids = append(ids, *id)
	}
	return ids
}

func TestMergeActionGroups_UnionNeverRemoves(t *testing.T) {
	cr := mkCR(nil)
	desired, _ := Desired(cr, time.Now(), testAMWScope)

	// Live resource already carries an extra, externally-added action group.
	extra := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Insights/actionGroups/extra"
	existing := &armalertprocessingrules.AlertProcessingRule{
		Properties: &armalertprocessingrules.AlertProcessingRuleProperties{
			Actions: []armalertprocessingrules.ActionClassification{
				&armalertprocessingrules.AddActionGroups{ActionGroupIDs: []*string{to.Ptr(extra), to.Ptr(testActionGrp)}},
			},
		},
	}

	mergeActionGroups(&desired, existing)

	ids := actionGroupIDs(t, desired)
	if !slices.Contains(ids, testActionGrp) {
		t.Errorf("default action group missing: %v", ids)
	}
	if !slices.Contains(ids, extra) {
		t.Errorf("pre-existing action group was removed: %v", ids)
	}
	// Deduplicated: testActionGrp present once.
	if c := countString(ids, testActionGrp); c != 1 {
		t.Errorf("default action group appears %d times, want 1: %v", c, ids)
	}
}

func TestRequeueAfterForWindow(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	start := now.Add(time.Hour)
	end := now.Add(2 * time.Hour)

	if d := requeueAfterForWindow(nil, now); d != 0 {
		t.Errorf("nil suppression => %v, want 0", d)
	}
	if d := requeueAfterForWindow(mkSuppression(start, end), now); d != time.Hour {
		t.Errorf("before start => %v, want 1h (time to start)", d)
	}
	if d := requeueAfterForWindow(mkSuppression(now.Add(-time.Hour), end), now); d != 2*time.Hour {
		t.Errorf("in window => %v, want 2h (time to end)", d)
	}
	if d := requeueAfterForWindow(mkSuppression(now.Add(-2*time.Hour), now.Add(-time.Hour)), now); d != 0 {
		t.Errorf("expired => %v, want 0", d)
	}
}

func TestFinalize_DeletesAzureAndRemovesFinalizer(t *testing.T) {
	cr := mkCR(nil)
	cr.Finalizers = []string{Finalizer}
	now := metav1.Now()
	cr.DeletionTimestamp = &now

	client := fakeclient.NewSimpleClientset(cr)
	azure := newFakeAPR()
	azure.store[cr.Name] = &armalertprocessingrules.AlertProcessingRule{} // exists in Azure

	c := &Controller{client: client, azure: azure, nowFunc: time.Now}

	if err := c.finalize(context.Background(), cr); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	if !slices.Contains(azure.deleted, cr.Name) {
		t.Errorf("Azure resource %q was not deleted: %v", cr.Name, azure.deleted)
	}
	got, err := client.AlertprocessingV1alpha1().AlertProcessingRules().Get(context.Background(), cr.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if slices.Contains(got.Finalizers, Finalizer) {
		t.Errorf("finalizer was not removed: %v", got.Finalizers)
	}
}

func countString(s []string, want string) int {
	n := 0
	for _, v := range s {
		if v == want {
			n++
		}
	}
	return n
}
