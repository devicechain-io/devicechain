// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

type NestedConfiguration struct {
	Test string
}

type UserManagementConfiguration struct {
	Nested NestedConfiguration
}

// Creates the default device management configuration
func NewUserManagementConfiguration() *UserManagementConfiguration {
	return &UserManagementConfiguration{
		Nested: NestedConfiguration{
			Test: "test",
		},
	}
}
