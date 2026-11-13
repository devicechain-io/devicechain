// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import "testing"

// A seeded OAuth client may not take the service-account client id prefix.
func TestSeedClientIdMayNotUseTheServiceAccountPrefix(t *testing.T) {
	if validateClientId("dcsa_x") == nil || validateClientId("Dcsa_x") == nil {
		t.Fatal("a seeded client id starting with dcsa_ must be refused")
	}
	if err := validateClientId("grafana"); err != nil {
		t.Fatalf("control: %v", err)
	}
}
