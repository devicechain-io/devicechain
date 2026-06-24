/**
 * Copyright © 2022 DeviceChain
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TenantMicroserviceSpec defines the desired state of TenantMicroservice
type TenantMicroserviceSpec struct {
	// Microservice id
	MicroserviceId string `json:"microserviceId"`

	// Tenant id
	TenantId string `json:"tenantId"`

	// Tenant-specific microservice configuration.
	Configuration EntityConfiguration `json:"configuration"`
}

// TenantMicroserviceStatus defines the observed state of TenantMicroservice
type TenantMicroserviceStatus struct {
}

//+kubebuilder:object:root=true
//+kubebuilder:resource:shortName=dctm
//+kubebuilder:subresource:status

// TenantMicroservice is the Schema for the tenantmicroservices API
type TenantMicroservice struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TenantMicroserviceSpec   `json:"spec,omitempty"`
	Status TenantMicroserviceStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// TenantMicroserviceList contains a list of TenantMicroservice
type TenantMicroserviceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TenantMicroservice `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TenantMicroservice{}, &TenantMicroserviceList{})
}
