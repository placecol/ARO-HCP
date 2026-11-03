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
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/alertprocessingrules/armalertprocessingrules"

	alertprocessingv1alpha1 "github.com/Azure/ARO-HCP/alertprocessing/pkg/apis/alertprocessing/v1alpha1"
)

const (
	// SentinelFilter is a value no real alert context will ever contain. It keeps the
	// alertContext "doesNotContain" condition well-formed in the default (non-suppressing)
	// form so the rule's action groups always apply. Suppression is expressed by OR-merging
	// an additional filter into this same condition.
	SentinelFilter = "NO ALERT CONTAINS THIS TEXT"

	// resourceLocation is the ARM location for alert processing rules (global resources).
	resourceLocation = "Global"

	// managedTagKey/Value mark resources this controller owns.
	managedTagKey   = "aroHCPPurpose"
	managedTagValue = "alert-processing-rule"
)

// InWindow reports whether now falls inside the half-open suppression window [start, end).
// A nil suppression is never in-window.
func InWindow(s *alertprocessingv1alpha1.Suppression, now time.Time) bool {
	if s == nil {
		return false
	}
	start := s.Start.Time
	end := s.End.Time
	return !now.Before(start) && now.Before(end)
}

// Desired renders the APR custom resource into the desired Azure Alert Processing Rule
// resource and reports which form was produced. It is a pure function: it performs no
// network calls and does not merge with any pre-existing action groups on the live
// resource (that union is the controller's responsibility, since it needs the live state).
//
// Default form (no suppression, or current time outside the window): the alertContext
// "doesNotContain" condition carries only the sentinel, so the action groups always apply.
// Suppressing form (suppression set AND now within [start, end)): the suppression filter
// is OR-merged into the same "doesNotContain" condition.
func Desired(cr *alertprocessingv1alpha1.AlertProcessingRule, now time.Time, amwScopeID string) (armalertprocessingrules.AlertProcessingRule, alertprocessingv1alpha1.AppliedForm) {
	alertContextValues := []*string{to.Ptr(SentinelFilter)}
	form := alertprocessingv1alpha1.AppliedFormDefault
	if InWindow(cr.Spec.Suppression, now) {
		alertContextValues = append(alertContextValues, to.Ptr(cr.Spec.Suppression.Filter))
		form = alertprocessingv1alpha1.AppliedFormSuppressing
	}

	apr := armalertprocessingrules.AlertProcessingRule{
		Location: to.Ptr(resourceLocation),
		Tags: map[string]*string{
			managedTagKey: to.Ptr(managedTagValue),
		},
		Properties: &armalertprocessingrules.AlertProcessingRuleProperties{
			Enabled: to.Ptr(true),
			Scopes:  []*string{to.Ptr(amwScopeID)},
			Conditions: []*armalertprocessingrules.Condition{
				{
					Field:    to.Ptr(armalertprocessingrules.FieldAlertRuleName),
					Operator: to.Ptr(armalertprocessingrules.OperatorEquals),
					Values:   []*string{to.Ptr(cr.Spec.AlertRuleName)},
				},
				{
					Field:    to.Ptr(armalertprocessingrules.FieldAlertContext),
					Operator: to.Ptr(armalertprocessingrules.OperatorDoesNotContain),
					Values:   alertContextValues,
				},
			},
			// Only ever add/update action groups — never RemoveAllActionGroups — so every
			// alert keeps flowing to its sinks. The controller unions this default action
			// group with any already present on the live resource before writing.
			Actions: []armalertprocessingrules.ActionClassification{
				&armalertprocessingrules.AddActionGroups{
					ActionGroupIDs: []*string{to.Ptr(cr.Spec.DefaultActionGroupID)},
				},
			},
			// No Schedule: the suppression window is enforced entirely by this controller's
			// reconcile loop, not by the Azure resource's schedule.
		},
	}
	return apr, form
}
