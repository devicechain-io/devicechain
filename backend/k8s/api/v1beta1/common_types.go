// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"encoding/json"
)

// Opaque configuration data specific to an entity.
type EntityConfiguration struct {
	//+kubebuilder:validation:Type=object
	//+kubebuilder:validation:Schemaless
	//+kubebuilder:pruning:PreserveUnknownFields
	json.RawMessage `json:",inline"`
}
