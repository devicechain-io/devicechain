// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package contract

// Attribute scope vocabulary (ADR-012). CLIENT = device-reported; SERVER =
// platform-only, hidden from the device; SHARED = platform-set, the device
// reads + subscribes (the server->device remote-config / OTA-target bridge).
type AttributeScope string

const (
	AttributeScopeClient AttributeScope = "CLIENT"
	AttributeScopeServer AttributeScope = "SERVER"
	AttributeScopeShared AttributeScope = "SHARED"
)

// Valid reports whether the scope names one of the known attribute scopes.
func (s AttributeScope) Valid() bool {
	switch s {
	case AttributeScopeClient, AttributeScopeServer, AttributeScopeShared:
		return true
	default:
		return false
	}
}

// String returns the underlying string value.
func (s AttributeScope) String() string {
	return string(s)
}
