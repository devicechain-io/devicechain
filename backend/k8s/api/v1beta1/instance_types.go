// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// InstanceSpec defines the desired state of Instance
type InstanceSpec struct {
	// Human-readable name displayed for instance.
	Name string `json:"name"`

	// Human-readable description displayed for instance.
	Description string `json:"description"`

	// Id of the instance configuration resource used to load config.
	ConfigurationId string `json:"configId"`

	// Instance configuration information.
	Configuration EntityConfiguration `json:"configuration"`

	// Profile names a curated set of functional areas to deploy (e.g.
	// "full", "telemetry", "ingest-only") — ADR-022 decision 2. Mutually
	// exclusive with EnabledFunctionalAreas; an empty profile with no explicit
	// set defaults to the full profile.
	// +optional
	Profile string `json:"profile,omitempty"`

	// EnabledFunctionalAreas is an explicit set of functional areas to deploy,
	// as an alternative to a named Profile (ADR-022 decision 2). The set is
	// rejected if it omits a required core area or an enabled area's hard
	// dependency.
	// +optional
	EnabledFunctionalAreas []string `json:"enabledFunctionalAreas,omitempty"`
}

// InstanceStatus defines the observed state of Instance
type InstanceStatus struct {
}

//+kubebuilder:object:root=true
//+kubebuilder:resource:scope=Cluster,shortName=dci
//+kubebuilder:subresource:status

// Instance is the Schema for the instances API
type Instance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InstanceSpec   `json:"spec,omitempty"`
	Status InstanceStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// InstanceList contains a list of Instance
type InstanceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Instance `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Instance{}, &InstanceList{})
}
