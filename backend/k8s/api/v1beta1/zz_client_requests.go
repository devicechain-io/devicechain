// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package v1beta1

// ------------------
// Instance Mangement
// ------------------

// Information required to create a DeviceChain instance.
type InstanceCreateRequest struct {
	Id              string
	Name            string
	Description     string
	ConfigurationId string
}

// Information required to get a DeviceChain instance.
type InstanceGetRequest struct {
	Id string
}
