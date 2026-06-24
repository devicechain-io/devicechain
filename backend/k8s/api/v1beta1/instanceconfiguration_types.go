// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// InstanceConfigurationSpec defines the desired state of InstanceConfiguration
type InstanceConfigurationSpec struct {
	// Instance configuration information.
	Configuration EntityConfiguration `json:"configuration"`
}

// InstanceConfigurationStatus defines the observed state of InstanceConfiguration
type InstanceConfigurationStatus struct {
}

//+kubebuilder:object:root=true
//+kubebuilder:resource:scope=Cluster,shortName=dcic
//+kubebuilder:subresource:status

// InstanceConfiguration is the Schema for the instanceconfigurations API
type InstanceConfiguration struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InstanceConfigurationSpec   `json:"spec,omitempty"`
	Status InstanceConfigurationStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// InstanceConfigurationList contains a list of InstanceConfiguration
type InstanceConfigurationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InstanceConfiguration `json:"items"`
}

func init() {
	SchemeBuilder.Register(&InstanceConfiguration{}, &InstanceConfigurationList{})
}
