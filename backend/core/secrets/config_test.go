// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package secrets

import "testing"

// TestConfigDefaults proves the zero-value config validates as the zero-infra
// default (postgres + instance KEK) — an omitted key means "the default", not an
// invalid empty selection.
func TestConfigDefaults(t *testing.T) {
	if err := (Config{}).Validate(); err != nil {
		t.Fatalf("empty config must validate as the default: %v", err)
	}
	d := DefaultConfig()
	if d.Backend != BackendPostgres || d.KEKProvider != InstanceKEKProvider {
		t.Fatalf("DefaultConfig = %+v, want postgres/instance", d)
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("DefaultConfig must validate: %v", err)
	}
}

// TestConfigRejectsUnknown proves fail-closed validation of both seams.
func TestConfigRejectsUnknown(t *testing.T) {
	if err := (Config{Backend: "sqlite"}).Validate(); err == nil {
		t.Fatal("unknown backend must be rejected")
	}
	if err := (Config{Backend: BackendPostgres, KEKProvider: "rot13"}).Validate(); err == nil {
		t.Fatal("unknown KEK provider must be rejected")
	}
}

// TestConfigKnownBackendsAndProviders proves every declared backend and provider
// identifier is accepted by validation (they are additive; whether each is built
// in the binary is a wiring-time concern, not a validation one).
func TestConfigKnownBackendsAndProviders(t *testing.T) {
	backends := []string{BackendPostgres, BackendGCPSecretManager, BackendAWSSecretsManager, BackendVault}
	for _, b := range backends {
		if err := (Config{Backend: b, KEKProvider: InstanceKEKProvider}).Validate(); err != nil {
			t.Fatalf("backend %q must validate: %v", b, err)
		}
	}
	providers := []string{InstanceKEKProvider, KEKProviderGCPKMS, KEKProviderAWSKMS, KEKProviderVaultTransit}
	for _, p := range providers {
		if err := (Config{Backend: BackendPostgres, KEKProvider: p}).Validate(); err != nil {
			t.Fatalf("KEK provider %q must validate: %v", p, err)
		}
	}
}

// TestConfigExternalBackendIgnoresKEK proves the KEK provider is not constrained
// for external secret-manager backends (they own their own encryption), so a
// bogus KEKProvider alongside an external backend still validates.
func TestConfigExternalBackendIgnoresKEK(t *testing.T) {
	if err := (Config{Backend: BackendVault, KEKProvider: "irrelevant"}).Validate(); err != nil {
		t.Fatalf("external backend must ignore the KEK provider: %v", err)
	}
}
