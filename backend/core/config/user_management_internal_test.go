// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import "testing"

func TestInternalAddrIsTheInternalPortNotThePublicOne(t *testing.T) {
	c := UserManagementConfiguration{Hostname: "user-management", Port: 8080, InternalPort: 8082}
	if got, want := c.InternalAddr(), "user-management:8082"; got != want {
		t.Fatalf("InternalAddr = %q, want %q", got, want)
	}
}

// An instance config rendered before the internal listener existed names no internal port;
// only that absence is defaulted, and an explicit value is honoured.
func TestApplyDefaultsFillsAbsentInternalPortOnly(t *testing.T) {
	c := &InstanceConfiguration{}
	c.ApplyDefaults()
	if got := c.Infrastructure.UserManagement.InternalPort; got != DefaultUserManagementInternalPort {
		t.Fatalf("absent internalPort = %d, want the default %d", got, DefaultUserManagementInternalPort)
	}
	c = &InstanceConfiguration{}
	c.Infrastructure.UserManagement.InternalPort = 9099
	c.ApplyDefaults()
	if got := c.Infrastructure.UserManagement.InternalPort; got != 9099 {
		t.Fatalf("explicit internalPort rewritten to %d", got)
	}
}
