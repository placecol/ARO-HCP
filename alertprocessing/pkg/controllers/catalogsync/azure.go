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
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/prometheusrulegroups/armprometheusrulegroups"
)

// RuleLister enumerates the Azure Managed Prometheus alert rules scoped to a given AMW.
// It is an interface so the controller can be tested with a fake.
type RuleLister interface {
	// ListAlertRules returns the "group/alertname" identifiers of every alerting rule whose
	// Prometheus rule group is scoped to the given AMW resource ID. Recording rules are
	// excluded. The returned list is the authoritative catalog for that AMW; rules absent
	// from it are treated as removed.
	ListAlertRules(ctx context.Context, amwResourceID string) ([]string, error)
}

// azureRuleLister is the production RuleLister backed by the prometheusRuleGroups API.
type azureRuleLister struct {
	client *armprometheusrulegroups.Client
}

// NewAzureRuleLister wraps an armprometheusrulegroups.Client.
func NewAzureRuleLister(client *armprometheusrulegroups.Client) RuleLister {
	return &azureRuleLister{client: client}
}

func (l *azureRuleLister) ListAlertRules(ctx context.Context, amwResourceID string) ([]string, error) {
	pager := l.client.NewListBySubscriptionPager(nil)
	var out []string
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("listing prometheus rule groups: %w", err)
		}
		for _, group := range page.Value {
			if group == nil || group.Properties == nil || group.Name == nil {
				continue
			}
			if !groupScopedToAMW(group.Properties.Scopes, amwResourceID) {
				continue
			}
			for _, rule := range group.Properties.Rules {
				// Alerting rules have Alert set; recording rules have Record set — skip those.
				if rule == nil || rule.Alert == nil || *rule.Alert == "" {
					continue
				}
				out = append(out, *group.Name+"/"+*rule.Alert)
			}
		}
	}
	return out, nil
}

// groupScopedToAMW reports whether any of the rule group's scopes equals the AMW resource ID
// (ARM resource IDs are case-insensitive).
func groupScopedToAMW(scopes []*string, amwResourceID string) bool {
	for _, s := range scopes {
		if s != nil && strings.EqualFold(*s, amwResourceID) {
			return true
		}
	}
	return false
}
