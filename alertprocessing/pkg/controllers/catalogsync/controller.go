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

// Package catalogsync implements the first reconcile loop described in the design: it polls
// the Azure Managed Prometheus rule groups on each AMW and maintains one AlertProcessingRule
// custom resource per alerting rule. It owns only the catalog fields (scope, alertRuleName,
// defaultActionGroupID) via a dedicated server-side-apply field manager, so it never clears
// the operator-managed spec.suppression. Rules that disappear from Azure are garbage
// collected (deleted), which lets the aprreconcile finalizer tear down the Azure resource.
package catalogsync

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/klog/v2"

	alertprocessingv1alpha1 "github.com/Azure/ARO-HCP/alertprocessing/pkg/apis/alertprocessing/v1alpha1"
	alertprocessingac "github.com/Azure/ARO-HCP/alertprocessing/pkg/generated/applyconfiguration/alertprocessing/v1alpha1"
	alertprocessingclient "github.com/Azure/ARO-HCP/alertprocessing/pkg/generated/clientset/versioned"
	alertprocessinginformers "github.com/Azure/ARO-HCP/alertprocessing/pkg/generated/informers/externalversions"
	alertprocessinglisters "github.com/Azure/ARO-HCP/alertprocessing/pkg/generated/listers/alertprocessing/v1alpha1"
)

const (
	// ControllerName identifies this controller in logs.
	ControllerName = "alertprocessing-catalog-sync"

	// CatalogFieldManager is the server-side-apply field manager this controller uses. It is
	// distinct from the operator-facing API's field manager so catalog applies never clear
	// spec.suppression.
	CatalogFieldManager = "alertprocessing-catalog"

	// ManagedByLabel marks AlertProcessingRule resources created by this controller, so GC
	// only ever considers resources it owns.
	ManagedByLabel = "alertprocessing.aro-hcp.azure.com/managed-by"
	// ScopeLabel records the AMW scope for quick selection/inspection.
	ScopeLabel = "alertprocessing.aro-hcp.azure.com/scope"
)

// ScopeAMW binds an AMW scope to the ARM resource ID of its Azure Monitor Workspace.
type ScopeAMW struct {
	Scope         alertprocessingv1alpha1.Scope
	AMWResourceID string
}

// ActionGroupResolver returns the default action group ARM ID to apply for a given rule.
type ActionGroupResolver func(scope alertprocessingv1alpha1.Scope, alertRuleName string) string

// Controller periodically reconciles the AlertProcessingRule catalog against Azure.
type Controller struct {
	client       alertprocessingclient.Interface
	lister       alertprocessinglisters.AlertProcessingRuleLister
	ruleLister   RuleLister
	scopes       []ScopeAMW
	actionGroup  ActionGroupResolver
	pollInterval time.Duration
}

// NewController builds a catalog-sync controller.
func NewController(
	client alertprocessingclient.Interface,
	informers alertprocessinginformers.SharedInformerFactory,
	ruleLister RuleLister,
	scopes []ScopeAMW,
	actionGroup ActionGroupResolver,
	pollInterval time.Duration,
) *Controller {
	return &Controller{
		client:       client,
		lister:       informers.Alertprocessing().V1alpha1().AlertProcessingRules().Lister(),
		ruleLister:   ruleLister,
		scopes:       scopes,
		actionGroup:  actionGroup,
		pollInterval: pollInterval,
	}
}

// Run reconciles immediately, then on the poll interval, until ctx is cancelled.
func (c *Controller) Run(ctx context.Context) {
	defer utilruntime.HandleCrash()

	logger := klog.FromContext(ctx).WithValues("controller", ControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller", "pollInterval", c.pollInterval, "scopes", len(c.scopes))

	c.reconcileAll(ctx)

	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("Stopping controller")
			return
		case <-ticker.C:
			c.reconcileAll(ctx)
		}
	}
}

type desiredRule struct {
	scope         alertprocessingv1alpha1.Scope
	alertRuleName string
	actionGroupID string
}

func (c *Controller) reconcileAll(ctx context.Context) {
	logger := klog.FromContext(ctx)

	desired, err := c.buildDesired(ctx)
	if err != nil {
		logger.Error(err, "Failed to list alert rules from Azure; skipping this cycle")
		return
	}

	for name, d := range desired {
		if err := c.applyCatalogEntry(ctx, name, d); err != nil {
			logger.Error(err, "Failed to apply catalog entry", "name", name)
		}
	}

	if err := c.garbageCollect(ctx, desired); err != nil {
		logger.Error(err, "Failed to garbage collect removed rules")
	}
}

// buildDesired queries Azure and returns the desired set of catalog entries keyed by CR name.
func (c *Controller) buildDesired(ctx context.Context) (map[string]desiredRule, error) {
	desired := map[string]desiredRule{}
	for _, s := range c.scopes {
		rules, err := c.ruleLister.ListAlertRules(ctx, s.AMWResourceID)
		if err != nil {
			return nil, fmt.Errorf("listing alert rules for scope %q: %w", s.Scope, err)
		}
		for _, alertRuleName := range rules {
			name := ResourceName(s.Scope, alertRuleName)
			desired[name] = desiredRule{
				scope:         s.Scope,
				alertRuleName: alertRuleName,
				actionGroupID: c.actionGroup(s.Scope, alertRuleName),
			}
		}
	}
	return desired, nil
}

// applyCatalogEntry server-side-applies only the catalog fields, preserving spec.suppression
// (owned by a different field manager). This is create-if-absent and update-in-place in one.
func (c *Controller) applyCatalogEntry(ctx context.Context, name string, d desiredRule) error {
	_, err := c.client.AlertprocessingV1alpha1().AlertProcessingRules().Apply(ctx, catalogApplyConfig(name, d), metav1.ApplyOptions{
		FieldManager: CatalogFieldManager,
		Force:        true,
	})
	return err
}

// catalogApplyConfig builds the server-side-apply configuration for a catalog entry. It sets
// only the catalog-owned fields and deliberately never sets spec.suppression, so that a
// different field manager's suppression is never claimed (and therefore never cleared) by
// this controller.
func catalogApplyConfig(name string, d desiredRule) *alertprocessingac.AlertProcessingRuleApplyConfiguration {
	return alertprocessingac.AlertProcessingRule(name).
		WithLabels(map[string]string{
			ManagedByLabel: ControllerName,
			ScopeLabel:     string(d.scope),
		}).
		WithSpec(alertprocessingac.AlertProcessingRuleSpec().
			WithScope(d.scope).
			WithAlertRuleName(d.alertRuleName).
			WithDefaultActionGroupID(d.actionGroupID))
}

// garbageCollect deletes AlertProcessingRule resources this controller owns whose rule no
// longer exists in Azure. Deletion is honored by the aprreconcile finalizer, which removes
// the Azure resource before the CR is actually removed.
func (c *Controller) garbageCollect(ctx context.Context, desired map[string]desiredRule) error {
	selector := labels.SelectorFromSet(labels.Set{ManagedByLabel: ControllerName})
	existing, err := c.lister.List(selector)
	if err != nil {
		return fmt.Errorf("listing existing rules: %w", err)
	}
	for _, cr := range existing {
		if _, keep := desired[cr.Name]; keep {
			continue
		}
		if cr.DeletionTimestamp != nil {
			continue // already being deleted
		}
		if err := c.client.AlertprocessingV1alpha1().AlertProcessingRules().Delete(ctx, cr.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting removed rule %q: %w", cr.Name, err)
		}
		klog.FromContext(ctx).Info("Garbage collected removed alert rule", "name", cr.Name)
	}
	return nil
}
