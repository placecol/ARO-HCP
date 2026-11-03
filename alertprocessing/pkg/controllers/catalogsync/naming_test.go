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
	"regexp"
	"strings"
	"testing"

	alertprocessingv1alpha1 "github.com/Azure/ARO-HCP/alertprocessing/pkg/apis/alertprocessing/v1alpha1"
)

var rfc1123 = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func TestResourceName(t *testing.T) {
	cases := []struct {
		name          string
		scope         alertprocessingv1alpha1.Scope
		alertRuleName string
	}{
		{"typical", alertprocessingv1alpha1.ScopeHCPs, "kubernetes-apps/KubePodNotReady"},
		{"services scope", alertprocessingv1alpha1.ScopeServices, "kubernetes-apps/KubePodNotReady"},
		{"no group", alertprocessingv1alpha1.ScopeHCPs, "SomeAlertWithoutGroup"},
		{"long name", alertprocessingv1alpha1.ScopeServices, "really-long-group/" + strings.Repeat("A", 200)},
		{"weird chars", alertprocessingv1alpha1.ScopeHCPs, "grp/Alert_With.Weird:Chars!!"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResourceName(tc.scope, tc.alertRuleName)
			if len(got) > maxNameLength {
				t.Errorf("name %q length %d exceeds %d", got, len(got), maxNameLength)
			}
			if !rfc1123.MatchString(got) {
				t.Errorf("name %q is not RFC-1123 compliant", got)
			}
			// Deterministic.
			if again := ResourceName(tc.scope, tc.alertRuleName); again != got {
				t.Errorf("name not deterministic: %q != %q", got, again)
			}
		})
	}

	// Same alert, different scope => different name.
	hcps := ResourceName(alertprocessingv1alpha1.ScopeHCPs, "kubernetes-apps/KubePodNotReady")
	svcs := ResourceName(alertprocessingv1alpha1.ScopeServices, "kubernetes-apps/KubePodNotReady")
	if hcps == svcs {
		t.Errorf("scopes must yield distinct names, both = %q", hcps)
	}

	// Different alerts that sanitize to the same prefix must not collide (hash disambiguates).
	a := ResourceName(alertprocessingv1alpha1.ScopeHCPs, "group-a/Same")
	b := ResourceName(alertprocessingv1alpha1.ScopeHCPs, "group-b/Same")
	if a == b {
		t.Errorf("distinct rules collided to %q", a)
	}
}
