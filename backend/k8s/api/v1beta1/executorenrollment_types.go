// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type ExecutorEnrollmentSpec struct {
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="owner is immutable"
	Owner string `json:"owner"`
	// +kubebuilder:validation:Pattern=`^[^@\s]+@sha256:[a-f0-9]{64}$`
	ExecutorImage string `json:"executorImage"`
	// +kubebuilder:validation:Enum=OwnerOffline
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="escrowDelivery is immutable"
	EscrowDelivery string `json:"escrowDelivery"`
	// +kubebuilder:validation:Minimum=1
	ProtocolVersion int32 `json:"protocolVersion"`
}

type ExecutorCapability struct {
	ProtocolVersion int32 `json:"protocolVersion"`
	// +listType=set
	ExecutableActions []string `json:"executableActions"`
}

type ExecutorEnrollmentStatus struct {
	ObservedGeneration int64               `json:"observedGeneration,omitempty"`
	Capability         *ExecutorCapability `json:"capability,omitempty"`
	// +listType=map
	// +listMapKey=type
	Conditions      []metav1.Condition `json:"conditions,omitempty"`
	ObservedImage   string             `json:"observedImage,omitempty"`
	ObservedImageID string             `json:"observedImageID,omitempty"`
}

const (
	ExecutorConditionReady            = "Ready"
	ExecutorReasonEnrolled            = "Enrolled"
	ExecutorReasonImageMismatch       = "ImageMismatch"
	ExecutorReasonNotObserved         = "NotObserved"
	ExecutorReasonProtocolUnsupported = "ProtocolUnsupported"
)

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=dce
// +kubebuilder:subresource:status
// +kubebuilder:validation:XValidation:rule="self.metadata.name == 'cluster'",message="enrollment must be named cluster"
type ExecutorEnrollment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ExecutorEnrollmentSpec   `json:"spec"`
	Status            ExecutorEnrollmentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ExecutorEnrollmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ExecutorEnrollment `json:"items"`
}

func init() { SchemeBuilder.Register(&ExecutorEnrollment{}, &ExecutorEnrollmentList{}) }
