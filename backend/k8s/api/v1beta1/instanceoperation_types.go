// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +kubebuilder:validation:Enum=Install;Bootstrap;Upgrade;Teardown
type OperationAction string

const (
	ActionInstall   OperationAction = "Install"
	ActionBootstrap OperationAction = "Bootstrap"
	ActionUpgrade   OperationAction = "Upgrade"
	ActionTeardown  OperationAction = "Teardown"
)

// +kubebuilder:validation:Enum=Pending;Refused;Running;Succeeded;Failed
type OperationPhase string

const (
	OperationPending   OperationPhase = "Pending"
	OperationRefused   OperationPhase = "Refused"
	OperationRunning   OperationPhase = "Running"
	OperationSucceeded OperationPhase = "Succeeded"
	OperationFailed    OperationPhase = "Failed"
)

// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="operation spec is immutable"
// +kubebuilder:validation:XValidation:rule="self.action == 'Install' ? (!has(self.instance) && !has(self.instanceUID)) : has(self.instance)",message="instance is required except for cluster-level Install, which forbids instance and instanceUID"
// +kubebuilder:validation:XValidation:rule="self.action in ['Bootstrap', 'Install'] || (has(self.instanceUID) && size(self.instanceUID) > 0)",message="instanceUID is required except for Bootstrap and Install"
type InstanceOperationSpec struct {
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +optional
	Instance string `json:"instance,omitempty"`
	// +kubebuilder:validation:MaxLength=36
	InstanceUID string `json:"instanceUID,omitempty"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=36
	EnrollmentUID string          `json:"enrollmentUID"`
	Action        OperationAction `json:"action"`
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`
	ImageVersion string `json:"imageVersion,omitempty"`
	// +kubebuilder:validation:Minimum=1
	ProtocolVersion int32 `json:"protocolVersion"`
}

// The parent rule protects removal; this rule protects edits while status exists.
// Conditions may change independently of the terminal verdict so their observation
// and transition timestamps can be maintained without rewriting the decision.
// +kubebuilder:validation:XValidation:rule="has(self.phase)",message="status requires phase"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.phase) || !(oldSelf.phase in ['Refused', 'Succeeded', 'Failed']) || (self.phase == oldSelf.phase && has(self.reason) == has(oldSelf.reason) && (!has(oldSelf.reason) || self.reason == oldSelf.reason) && has(self.message) == has(oldSelf.message) && (!has(oldSelf.message) || self.message == oldSelf.message) && has(self.decidedAt) == has(oldSelf.decidedAt) && (!has(oldSelf.decidedAt) || self.decidedAt == oldSelf.decidedAt))",message="terminal verdict is immutable"
type InstanceOperationStatus struct {
	// +kubebuilder:validation:Required
	Phase OperationPhase `json:"phase"`
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:Pattern=`^[A-Z][A-Za-z0-9]*$`
	Reason string `json:"reason,omitempty"`
	// +kubebuilder:validation:MaxLength=32768
	Message                 string       `json:"message,omitempty"`
	DecidedAt               *metav1.Time `json:"decidedAt,omitempty"`
	ObservedGeneration      int64        `json:"observedGeneration,omitempty"`
	ExecutorProtocolVersion int32        `json:"executorProtocolVersion,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

const OperationConditionDecided = "Decided"

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=dcop
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Instance",type=string,JSONPath=`.spec.instance`
// +kubebuilder:printcolumn:name="Action",type=string,JSONPath=`.spec.action`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.reason`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.status) || !has(oldSelf.status.phase) || !(oldSelf.status.phase in ['Refused', 'Succeeded', 'Failed']) || (has(self.status) && has(self.status.phase))",message="terminal status cannot be removed"
type InstanceOperation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              InstanceOperationSpec   `json:"spec"`
	Status            InstanceOperationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type InstanceOperationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InstanceOperation `json:"items"`
}

func init() { SchemeBuilder.Register(&InstanceOperation{}, &InstanceOperationList{}) }
