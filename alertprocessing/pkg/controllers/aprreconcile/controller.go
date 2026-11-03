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

// Package aprreconcile implements the second reconcile loop described in the design:
// it renders each AlertProcessingRule custom resource into a single long-lived Azure
// Alert Processing Rule resource. Action groups are only ever added or updated (never
// removed), and the suppression time window is enforced entirely here (the Azure resource
// carries no schedule), so an elapsed window simply reverts the resource to its default
// form on the next reconcile.
package aprreconcile

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/alertprocessingrules/armalertprocessingrules"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	applyv1 "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	alertprocessingv1alpha1 "github.com/Azure/ARO-HCP/alertprocessing/pkg/apis/alertprocessing/v1alpha1"
	alertprocessingac "github.com/Azure/ARO-HCP/alertprocessing/pkg/generated/applyconfiguration/alertprocessing/v1alpha1"
	alertprocessingclient "github.com/Azure/ARO-HCP/alertprocessing/pkg/generated/clientset/versioned"
	alertprocessinginformers "github.com/Azure/ARO-HCP/alertprocessing/pkg/generated/informers/externalversions"
	alertprocessinglisters "github.com/Azure/ARO-HCP/alertprocessing/pkg/generated/listers/alertprocessing/v1alpha1"
)

const (
	// ControllerName identifies this controller in logs and the workqueue metric label.
	ControllerName = "alertprocessing-apr-reconcile"

	// Finalizer blocks removal of an AlertProcessingRule until this controller has deleted
	// the corresponding Azure resource.
	Finalizer = "alertprocessing.aro-hcp.azure.com/apr-cleanup"
)

// ScopeResolver maps an AlertProcessingRule scope to the ARM resource ID of the AMW it
// applies to.
type ScopeResolver func(scope alertprocessingv1alpha1.Scope) (string, error)

// Controller reconciles AlertProcessingRule custom resources into Azure resources.
type Controller struct {
	client        alertprocessingclient.Interface
	lister        alertprocessinglisters.AlertProcessingRuleLister
	informerSync  cache.InformerSynced
	azure         APRClient
	scopeResolver ScopeResolver
	workqueue     workqueue.TypedRateLimitingInterface[cache.ObjectName]
	nowFunc       func() time.Time
}

// NewController builds a Controller and wires the AlertProcessingRule informer to its queue.
func NewController(
	client alertprocessingclient.Interface,
	informers alertprocessinginformers.SharedInformerFactory,
	azure APRClient,
	scopeResolver ScopeResolver,
) *Controller {
	c := &Controller{
		client:        client,
		lister:        informers.Alertprocessing().V1alpha1().AlertProcessingRules().Lister(),
		azure:         azure,
		scopeResolver: scopeResolver,
		workqueue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[cache.ObjectName](),
			workqueue.TypedRateLimitingQueueConfig[cache.ObjectName]{Name: ControllerName},
		),
		nowFunc: time.Now,
	}

	informer := informers.Alertprocessing().V1alpha1().AlertProcessingRules().Informer()
	// Errors only occur if the informer has already been stopped, which cannot happen here.
	_, _ = informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueue(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueue(obj) },
	})
	c.informerSync = informer.HasSynced
	return c
}

func (c *Controller) enqueue(obj interface{}) {
	name, err := cache.DeletionHandlingObjectToName(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	c.workqueue.Add(name)
}

// Run starts the controller workers and blocks until ctx is cancelled.
func (c *Controller) Run(ctx context.Context, workers int) error {
	defer utilruntime.HandleCrash()
	defer c.workqueue.ShutDown()

	logger := klog.FromContext(ctx).WithValues("controller", ControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller, waiting for cache sync")

	if ok := cache.WaitForCacheSync(ctx.Done(), c.informerSync); !ok {
		return fmt.Errorf("failed to wait for caches to sync")
	}

	logger.Info("Starting workers", "count", workers)
	for range workers {
		go wait.UntilWithContext(ctx, c.runWorker, time.Second)
	}
	<-ctx.Done()
	logger.Info("Shutting down workers")
	return nil
}

func (c *Controller) runWorker(ctx context.Context) {
	for c.processNext(ctx) {
	}
}

func (c *Controller) processNext(ctx context.Context) bool {
	key, shutdown := c.workqueue.Get()
	if shutdown {
		return false
	}
	defer c.workqueue.Done(key)

	requeueAfter, err := c.sync(ctx, key.Name)
	if err != nil {
		utilruntime.HandleErrorWithContext(ctx, err, "error syncing AlertProcessingRule; requeuing", "name", key.Name)
		c.workqueue.AddRateLimited(key)
		return true
	}
	c.workqueue.Forget(key)
	if requeueAfter > 0 {
		c.workqueue.AddAfter(key, requeueAfter)
	}
	return true
}

// sync reconciles a single AlertProcessingRule. It returns a non-zero duration when the
// item should be revisited (to flip forms at a suppression window edge).
func (c *Controller) sync(ctx context.Context, name string) (time.Duration, error) {
	logger := klog.FromContext(ctx).WithValues("name", name)

	cr, err := c.lister.Get(name)
	if apierrors.IsNotFound(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}

	if cr.DeletionTimestamp != nil {
		return 0, c.finalize(ctx, cr)
	}

	if !slices.Contains(cr.Finalizers, Finalizer) {
		if err := c.addFinalizer(ctx, cr); err != nil {
			return 0, err
		}
		// The update re-enqueues via the informer; reconcile the fresh object then.
		return 0, nil
	}

	amwScopeID, err := c.scopeResolver(cr.Spec.Scope)
	if err != nil {
		return 0, fmt.Errorf("resolving AMW scope for %q: %w", cr.Spec.Scope, err)
	}

	desired, form := Desired(cr, c.nowFunc(), amwScopeID)

	existing, err := c.azure.Get(ctx, name)
	if err != nil {
		return 0, fmt.Errorf("reading Azure alert processing rule: %w", err)
	}
	// Union the desired default action group with any already present on the live resource —
	// action groups are only ever added, never removed.
	mergeActionGroups(&desired, existing)

	if err := c.azure.CreateOrUpdate(ctx, name, desired); err != nil {
		return 0, fmt.Errorf("writing Azure alert processing rule: %w", err)
	}
	logger.Info("Reconciled Azure alert processing rule", "form", form)

	if err := c.updateStatus(ctx, cr, form); err != nil {
		return 0, fmt.Errorf("updating status: %w", err)
	}

	return requeueAfterForWindow(cr.Spec.Suppression, c.nowFunc()), nil
}

// finalize deletes the Azure resource and removes the finalizer. The underlying alert rule
// is gone, so there is nothing left to route or suppress.
func (c *Controller) finalize(ctx context.Context, cr *alertprocessingv1alpha1.AlertProcessingRule) error {
	if !slices.Contains(cr.Finalizers, Finalizer) {
		return nil
	}
	if err := c.azure.Delete(ctx, cr.Name); err != nil {
		return fmt.Errorf("deleting Azure alert processing rule: %w", err)
	}
	updated := cr.DeepCopy()
	updated.Finalizers = slices.DeleteFunc(updated.Finalizers, func(f string) bool { return f == Finalizer })
	_, err := c.client.AlertprocessingV1alpha1().AlertProcessingRules().Update(ctx, updated, metav1.UpdateOptions{})
	return err
}

func (c *Controller) addFinalizer(ctx context.Context, cr *alertprocessingv1alpha1.AlertProcessingRule) error {
	updated := cr.DeepCopy()
	updated.Finalizers = append(updated.Finalizers, Finalizer)
	_, err := c.client.AlertprocessingV1alpha1().AlertProcessingRules().Update(ctx, updated, metav1.UpdateOptions{})
	return err
}

func (c *Controller) updateStatus(ctx context.Context, cr *alertprocessingv1alpha1.AlertProcessingRule, form alertprocessingv1alpha1.AppliedForm) error {
	ready := applyv1.Condition().
		WithType("Ready").
		WithStatus(metav1.ConditionTrue).
		WithReason("Reconciled").
		WithMessage(fmt.Sprintf("Azure alert processing rule reconciled in %q form", form)).
		WithObservedGeneration(cr.Generation).
		WithLastTransitionTime(metav1.NewTime(c.nowFunc()))

	status := alertprocessingac.AlertProcessingRuleStatus().
		WithAppliedForm(form).
		WithObservedGeneration(cr.Generation).
		WithConditions(ready)

	cfg := alertprocessingac.AlertProcessingRule(cr.Name).WithStatus(status)
	_, err := c.client.AlertprocessingV1alpha1().AlertProcessingRules().ApplyStatus(ctx, cfg, metav1.ApplyOptions{FieldManager: ControllerName})
	return err
}

// mergeActionGroups sets desired's single AddActionGroups to the deduplicated union of the
// action groups it already carries and those present on the live resource.
func mergeActionGroups(desired *armalertprocessingrules.AlertProcessingRule, existing *armalertprocessingrules.AlertProcessingRule) {
	union := map[string]struct{}{}
	var ordered []string
	add := func(id string) {
		if id == "" {
			return
		}
		if _, seen := union[id]; seen {
			return
		}
		union[id] = struct{}{}
		ordered = append(ordered, id)
	}

	// Desired (default action group) first, then anything already live.
	for _, action := range desired.Properties.Actions {
		if ag, ok := action.(*armalertprocessingrules.AddActionGroups); ok {
			for _, id := range ag.ActionGroupIDs {
				if id != nil {
					add(*id)
				}
			}
		}
	}
	for _, id := range existingActionGroupIDs(existing) {
		add(id)
	}

	ids := make([]*string, 0, len(ordered))
	for _, id := range ordered {
		ids = append(ids, to.Ptr(id))
	}
	desired.Properties.Actions = []armalertprocessingrules.ActionClassification{
		&armalertprocessingrules.AddActionGroups{ActionGroupIDs: ids},
	}
}

// requeueAfterForWindow returns how long until the next suppression window edge (start or
// end), so the controller flips the Azure resource between default and suppressing forms
// without a background sweeper. It returns 0 when there is no upcoming edge.
func requeueAfterForWindow(s *alertprocessingv1alpha1.Suppression, now time.Time) time.Duration {
	if s == nil {
		return 0
	}
	if now.Before(s.Start.Time) {
		return s.Start.Time.Sub(now)
	}
	if now.Before(s.End.Time) {
		return s.End.Time.Sub(now)
	}
	return 0
}
