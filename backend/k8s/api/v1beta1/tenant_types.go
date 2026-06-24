// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TenantSpec defines the desired state of Tenant
type TenantSpec struct {
	// Human-readable name displayed for tenant.
	Name string `json:"name"`

	// Human-readable description displayed for tenant.
	Description string `json:"description"`
}

// Bootstrap lifecycle states for a tenant's dataset.
const (
	TenantNotBootstrapped = "NotBootstrapped"
	TenantBootstrapping   = "Bootstrapping"
	TenantBootstrapped    = "Bootstrapped"
	TenantBootstrapFailed = "BootstrapFailed"
)

// TenantStatus defines the observed state of Tenant
type TenantStatus struct {
	// BootstrapState tracks progress of seeding the tenant's initial dataset.
	// Shared microservices and the bootstrap-ordering system read this to gate
	// startup of dependent functional areas (ADR-001).
	BootstrapState string `json:"bootstrapState,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:resource:shortName=dct
//+kubebuilder:subresource:status

// Tenant is the Schema for the tenants API
type Tenant struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TenantSpec   `json:"spec,omitempty"`
	Status TenantStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// TenantList contains a list of Tenant
type TenantList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Tenant `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Tenant{}, &TenantList{})
}
