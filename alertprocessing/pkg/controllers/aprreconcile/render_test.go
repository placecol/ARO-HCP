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
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/alertprocessingrules/armalertprocessingrules"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	alertprocessingv1alpha1 "github.com/Azure/ARO-HCP/alertprocessing/pkg/apis/alertprocessing/v1alpha1"
)

const (
	testAMWScope   = "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Monitor/accounts/hcps-amw"
	testActionGrp  = "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Insights/actionGroups/icm"
	testAlertRule  = "kubernetes-apps/KubePodNotReady"
	testFilterText = "ocm-arohcpint-2tp8ergr17b7anmbk8c6csepetd3lidg"
)

func mkCR(suppression *alertprocessingv1alpha1.Suppression) *alertprocessingv1alpha1.AlertProcessingRule {
	return &alertprocessingv1alpha1.AlertProcessingRule{
		ObjectMeta: metav1.ObjectMeta{Name: "kubepodnotready-hcps"},
		Spec: alertprocessingv1alpha1.AlertProcessingRuleSpec{
			Scope:                alertprocessingv1alpha1.ScopeHCPs,
			AlertRuleName:        testAlertRule,
			DefaultActionGroupID: testActionGrp,
			Suppression:          suppression,
		},
	}
}

func mkSuppression(start, end time.Time) *alertprocessingv1alpha1.Suppression {
	return &alertprocessingv1alpha1.Suppression{
		Filter: testFilterText,
		Jira:   "ARO-1234",
		Reason: "Noisy customer",
		Start:  metav1.NewTime(start),
		End:    metav1.NewTime(end),
	}
}

// alertContextValues extracts the values of the single AlertContext DoesNotContain condition.
func alertContextValues(t *testing.T, apr armalertprocessingrules.AlertProcessingRule) []string {
	t.Helper()
	for _, c := range apr.Properties.Conditions {
		if c.Field != nil && *c.Field == armalertprocessingrules.FieldAlertContext {
			if c.Operator == nil || *c.Operator != armalertprocessingrules.OperatorDoesNotContain {
				t.Fatalf("alertContext condition operator = %v, want DoesNotContain", c.Operator)
			}
			out := make([]string, 0, len(c.Values))
			for _, v := range c.Values {
				out = append(out, *v)
			}
			return out
		}
	}
	t.Fatal("no AlertContext condition found")
	return nil
}

func TestDesired(t *testing.T) {
	base := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	start := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name         string
		cr           *alertprocessingv1alpha1.AlertProcessingRule
		now          time.Time
		wantForm     alertprocessingv1alpha1.AppliedForm
		wantACValues []string
	}{
		{
			name:         "no suppression -> default",
			cr:           mkCR(nil),
			now:          base,
			wantForm:     alertprocessingv1alpha1.AppliedFormDefault,
			wantACValues: []string{SentinelFilter},
		},
		{
			name:         "suppression in window -> suppressing",
			cr:           mkCR(mkSuppression(start, end)),
			now:          base,
			wantForm:     alertprocessingv1alpha1.AppliedFormSuppressing,
			wantACValues: []string{SentinelFilter, testFilterText},
		},
		{
			name:         "suppression expired -> default",
			cr:           mkCR(mkSuppression(start, end)),
			now:          end.Add(time.Hour),
			wantForm:     alertprocessingv1alpha1.AppliedFormDefault,
			wantACValues: []string{SentinelFilter},
		},
		{
			name:         "suppression not yet started -> default",
			cr:           mkCR(mkSuppression(start, end)),
			now:          start.Add(-time.Hour),
			wantForm:     alertprocessingv1alpha1.AppliedFormDefault,
			wantACValues: []string{SentinelFilter},
		},
		{
			name:         "boundary now==start is in-window (inclusive)",
			cr:           mkCR(mkSuppression(start, end)),
			now:          start,
			wantForm:     alertprocessingv1alpha1.AppliedFormSuppressing,
			wantACValues: []string{SentinelFilter, testFilterText},
		},
		{
			name:         "boundary now==end is out-of-window (exclusive)",
			cr:           mkCR(mkSuppression(start, end)),
			now:          end,
			wantForm:     alertprocessingv1alpha1.AppliedFormDefault,
			wantACValues: []string{SentinelFilter},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			apr, form := Desired(tc.cr, tc.now, testAMWScope)

			if form != tc.wantForm {
				t.Errorf("form = %q, want %q", form, tc.wantForm)
			}
			if got := alertContextValues(t, apr); !equalStrings(got, tc.wantACValues) {
				t.Errorf("alertContext values = %v, want %v", got, tc.wantACValues)
			}

			// Scope is always the provided AMW.
			if len(apr.Properties.Scopes) != 1 || *apr.Properties.Scopes[0] != testAMWScope {
				t.Errorf("scopes = %v, want [%s]", apr.Properties.Scopes, testAMWScope)
			}
			// AlertRuleName condition is always present with Equals.
			assertAlertRuleNameEquals(t, apr, testAlertRule)
			// Actions must be exactly one AddActionGroups with the default action group; never RemoveAll.
			assertAddActionGroupsOnly(t, apr, testActionGrp)
			// Never a Schedule.
			if apr.Properties.Schedule != nil {
				t.Errorf("Schedule must be nil (scheduling is controller-side)")
			}
		})
	}
}

func assertAlertRuleNameEquals(t *testing.T, apr armalertprocessingrules.AlertProcessingRule, want string) {
	t.Helper()
	for _, c := range apr.Properties.Conditions {
		if c.Field != nil && *c.Field == armalertprocessingrules.FieldAlertRuleName {
			if c.Operator == nil || *c.Operator != armalertprocessingrules.OperatorEquals {
				t.Errorf("alertRuleName operator = %v, want Equals", c.Operator)
			}
			if len(c.Values) != 1 || *c.Values[0] != want {
				t.Errorf("alertRuleName values = %v, want [%s]", c.Values, want)
			}
			return
		}
	}
	t.Error("no AlertRuleName condition found")
}

func assertAddActionGroupsOnly(t *testing.T, apr armalertprocessingrules.AlertProcessingRule, wantAG string) {
	t.Helper()
	if len(apr.Properties.Actions) != 1 {
		t.Fatalf("actions len = %d, want 1", len(apr.Properties.Actions))
	}
	add, ok := apr.Properties.Actions[0].(*armalertprocessingrules.AddActionGroups)
	if !ok {
		t.Fatalf("action type = %T, want *AddActionGroups (never RemoveAllActionGroups)", apr.Properties.Actions[0])
	}
	if len(add.ActionGroupIDs) != 1 || *add.ActionGroupIDs[0] != wantAG {
		t.Errorf("action group ids = %v, want [%s]", add.ActionGroupIDs, wantAG)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
