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

// MicroserviceConfigurationSpec defines the desired state of MicroserviceConfiguration
type MicroserviceConfigurationSpec struct {
	// Unique functional area of microservice.
	FunctionalArea string `json:"functionalArea"`

	// Docker image information for microservice runtime.
	Image string `json:"image"`

	// Instance configuration information.
	Configuration EntityConfiguration `json:"configuration"`
}

// MicroserviceConfigurationStatus defines the observed state of MicroserviceConfiguration
type MicroserviceConfigurationStatus struct {
}

//+kubebuilder:object:root=true
//+kubebuilder:resource:scope=Cluster,shortName=dcmc
//+kubebuilder:subresource:status

// MicroserviceConfiguration is the Schema for the microserviceconfigurations API
type MicroserviceConfiguration struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MicroserviceConfigurationSpec   `json:"spec,omitempty"`
	Status MicroserviceConfigurationStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// MicroserviceConfigurationList contains a list of MicroserviceConfiguration
type MicroserviceConfigurationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MicroserviceConfiguration `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MicroserviceConfiguration{}, &MicroserviceConfigurationList{})
}
