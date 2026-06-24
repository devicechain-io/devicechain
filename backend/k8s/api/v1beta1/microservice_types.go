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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MicroserviceSpec defines the desired state of Microservice
type MicroserviceSpec struct {
	// Human-readable name displayed for tenant.
	Name string `json:"name"`

	// Human-readable description displayed for tenant.
	Description string `json:"description"`

	// Unique functional area of microservice.
	FunctionalArea string `json:"functionalArea"`

	// Docker image information for microservice runtime.
	Image string `json:"image"`

	// Indicates pull policy used for pulling Docker image.
	ImagePullPolicy corev1.PullPolicy `json:"imagePullPolicy,omitempty"`

	// Id of the microservice configuration resource used to load config.
	ConfigurationId string `json:"configId"`
}

// MicroserviceStatus defines the observed state of Microservice
type MicroserviceStatus struct {
}

//+kubebuilder:object:root=true
//+kubebuilder:resource:shortName=dcm
//+kubebuilder:subresource:status

// Microservice is the Schema for the microservices API
type Microservice struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MicroserviceSpec   `json:"spec,omitempty"`
	Status MicroserviceStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// MicroserviceList contains a list of Microservice
type MicroserviceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Microservice `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Microservice{}, &MicroserviceList{})
}
