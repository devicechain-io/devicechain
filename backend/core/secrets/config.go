// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package secrets

import "fmt"

// Store backend identifiers (ADR-059 §2a — where a value physically lives). v1
// builds only BackendPostgres; the external secret-manager backends are declared
// so config validation accepts them once their impls land (additive, no consumer
// change), but selecting one before it is implemented fails closed at build-time
// wiring rather than here.
const (
	// BackendPostgres stores the value envelope-encrypted in the service's own DB
	// (the default; the KEKProvider seam applies).
	BackendPostgres = "postgres"
	// BackendGCPSecretManager stores the value in GCP Secret Manager (deferred).
	BackendGCPSecretManager = "gcp-secret-manager"
	// BackendAWSSecretsManager stores the value in AWS Secrets Manager (deferred).
	BackendAWSSecretsManager = "aws-secrets-manager"
	// BackendVault stores the value in HashiCorp Vault (deferred).
	BackendVault = "vault"
)

// KEK provider identifiers (ADR-059 §2b — how a DEK is wrapped, Postgres backend
// only). v1 builds only InstanceKEKProvider; the cloud-KMS providers are declared
// for forward-compatible validation.
const (
	// KEKProviderGCPKMS wraps the DEK via GCP KMS (deferred).
	KEKProviderGCPKMS = "gcpkms"
	// KEKProviderAWSKMS wraps the DEK via AWS KMS (deferred).
	KEKProviderAWSKMS = "awskms"
	// KEKProviderVaultTransit wraps the DEK via Vault Transit (deferred).
	KEKProviderVaultTransit = "vault-transit"
)

// Config is the typed, fail-closed secret-storage configuration. It selects the
// store backend and (for the Postgres backend) the KEK provider. It carries no
// key material — the instance root key is delivered separately via the instance
// K8s Secret and passed to NewInstanceKeyProvider (wired in a later slice).
type Config struct {
	// Backend selects where values live. Default: BackendPostgres.
	Backend string
	// KEKProvider selects DEK wrapping for the Postgres backend. Default:
	// InstanceKEKProvider. Ignored (N/A) for external secret-manager backends,
	// which own their own encryption.
	KEKProvider string
}

// DefaultConfig is the zero-infra default: envelope-encrypted in Postgres, DEK
// wrapped by the instance root key.
func DefaultConfig() Config {
	return Config{Backend: BackendPostgres, KEKProvider: InstanceKEKProvider}
}

// withDefaults fills empty fields with the defaults so an omitted key means "the
// default backend/provider", not an invalid empty selection.
func (c Config) withDefaults() Config {
	if c.Backend == "" {
		c.Backend = BackendPostgres
	}
	if c.KEKProvider == "" {
		c.KEKProvider = InstanceKEKProvider
	}
	return c
}

// Validate fails closed on an unknown backend or KEK provider so a misspelled or
// unsupported selection is rejected at startup rather than silently defaulting to
// something the operator did not intend. It validates the selection is a known
// identifier; whether the selected backend/provider is actually built in this
// binary is enforced at wiring time (a declared-but-unimplemented option fails
// closed there), keeping this pure validation dependency-free.
func (c Config) Validate() error {
	c = c.withDefaults()
	switch c.Backend {
	case BackendPostgres, BackendGCPSecretManager, BackendAWSSecretsManager, BackendVault:
	default:
		return fmt.Errorf("secrets: unknown store backend %q", c.Backend)
	}
	// The KEK provider applies only to the Postgres backend; external secret
	// managers own encryption, so their KEKProvider field is not constrained.
	if c.Backend == BackendPostgres {
		switch c.KEKProvider {
		case InstanceKEKProvider, KEKProviderGCPKMS, KEKProviderAWSKMS, KEKProviderVaultTransit:
		default:
			return fmt.Errorf("secrets: unknown KEK provider %q", c.KEKProvider)
		}
	}
	return nil
}
