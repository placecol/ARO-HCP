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

// Package v1alpha1 contains the API types for the alertprocessing API group.
//
// The AlertProcessingRule resource is a region-level catalog entry: the controller
// maintains one AlertProcessingRule per Azure Managed Prometheus alert rule (per AMW
// scope). The controller reconciles each AlertProcessingRule into a single long-lived
// Azure "Alert Processing Rule" resource whose action groups are only ever added or
// updated (never removed), so suppressed alerts keep flowing to downstream sinks.
// Suppression is expressed as a time-bounded alertContext filter, set on the spec by
// an operator-facing API, and is honored by the controller only while the current time
// falls within the suppression window.
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Scope identifies the Azure Monitor Workspace (AMW) family an alert rule belongs to.
// +kubebuilder:validation:Enum=hcps;services
type Scope string

const (
	// ScopeHCPs targets the hosted-control-plane AMW family.
	ScopeHCPs Scope = "hcps"
	// ScopeServices targets the service-cluster AMW family.
	ScopeServices Scope = "services"
)

// AppliedForm records which rendered form the controller last applied to Azure.
type AppliedForm string

const (
	// AppliedFormDefault means the Azure resource carries only the sentinel
	// alertContext filter (no active suppression).
	AppliedFormDefault AppliedForm = "default"
	// AppliedFormSuppressing means the Azure resource additionally carries the
	// suppression filter (suppression is set and the current time is in-window).
	AppliedFormSuppressing AppliedForm = "suppressing"
)

// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// AlertProcessingRule is the desired state of a single Azure Alert Processing Rule.
// There is one AlertProcessingRule per (alertRuleName, scope). The catalog controller
// owns the catalog fields (scope, alertRuleName, defaultActionGroupID) and creates one
// AlertProcessingRule per alert rule discovered on the AMWs; the operator-facing API
// owns spec.suppression. These two writers use distinct server-side-apply field managers
// so the catalog controller never clears an active suppression.
type AlertProcessingRule struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// +required
	// spec defines the desired state of the AlertProcessingRule.
	Spec AlertProcessingRuleSpec `json:"spec"`

	// +optional
	// status contains the observed state of the AlertProcessingRule.
	Status AlertProcessingRuleStatus `json:"status,omitempty,omitzero"`
}

// AlertProcessingRuleSpec is the desired state of an AlertProcessingRule.
type AlertProcessingRuleSpec struct {
	// +kubebuilder:validation:Required
	// scope is the AMW family (hcps or services) whose alert this rule suppresses.
	// It is derived from which AMW the originating Prometheus rule group is scoped to.
	Scope Scope `json:"scope"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=511
	// alertRuleName is the "group/alertname" identifier of the Azure Managed Prometheus
	// alert rule to match (for example "kubernetes-apps/KubePodNotReady"). It is matched
	// against the fired alert with the Equals operator.
	AlertRuleName string `json:"alertRuleName"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// defaultActionGroupID is the ARM resource ID of the action group (for example the
	// ICM action group) that is always applied to matching alerts. The controller only
	// ever adds or updates action groups on the Azure resource; it never removes them.
	DefaultActionGroupID string `json:"defaultActionGroupID"`

	// +optional
	// suppression, when set and currently in-window, extends the alertContext filter so
	// that matching alerts are suppressed. It is written exclusively by the operator-facing
	// API; the catalog controller preserves it. Clearing suppression (or the window
	// elapsing) returns the Azure resource to its default form.
	Suppression *Suppression `json:"suppression,omitempty"`
}

// Suppression is a time-bounded alertContext filter that suppresses a subset of a rule's
// alerts without removing any action groups.
// +kubebuilder:validation:XValidation:rule="self.end > self.start",message="end must be after start"
type Suppression struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// filter is the substring OR-merged into the alertContext "doesNotContain" condition.
	// Alerts whose context contains this value are suppressed while the window is active.
	Filter string `json:"filter"`

	// +optional
	// jira is the tracking issue associated with this suppression.
	Jira string `json:"jira,omitempty"`

	// +optional
	// reason is a human-readable justification for the suppression.
	Reason string `json:"reason,omitempty"`

	// +kubebuilder:validation:Required
	// start is the inclusive start of the suppression window.
	Start metav1.Time `json:"start"`

	// +kubebuilder:validation:Required
	// end is the exclusive end of the suppression window. It must be after start.
	End metav1.Time `json:"end"`
}

// AlertProcessingRuleStatus is the observed state of an AlertProcessingRule.
type AlertProcessingRuleStatus struct {
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// conditions reports the latest reconciliation state (for example Ready).
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// +optional
	// azureResourceID is the ARM resource ID of the managed Azure Alert Processing Rule.
	AzureResourceID string `json:"azureResourceID,omitempty"`

	// +optional
	// appliedForm is the rendered form last applied to Azure ("default" or "suppressing").
	AppliedForm AppliedForm `json:"appliedForm,omitempty"`

	// +optional
	// observedGeneration is the spec generation last reconciled.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// AlertProcessingRuleList is a list of AlertProcessingRule resources.
type AlertProcessingRuleList struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ListMeta `json:"metadata,omitempty,omitzero"`

	Items []AlertProcessingRule `json:"items"`
}
