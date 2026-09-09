// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package secrets

import (
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// RootKeySource supplies the decoded 256-bit instance root key for the default
// instance KEK provider. It is a function rather than a []byte so the key is
// obtained only once the selected backend and KEK provider are known to be built
// in this binary.
//
// That is what lets the built-vs-declared check move into core without moving the
// error a misconfigured deployment sees. Each caller already ordered its checks
// backend-first, so a deployment selecting an external secret manager — which owns
// its own keys — is refused for the unbuilt backend, not for an instance root key
// it does not need. A []byte parameter would force every caller to decode before
// calling New, putting the key error in front of the backend error.
//
// config.SecretsConfiguration.DecodedRootKey satisfies this as a method value.
type RootKeySource func() ([]byte, error)

// New builds the configured secret store, failing closed on any selection this
// binary cannot actually serve (ADR-059). It is the single wiring point every
// service uses, so the built-vs-declared check exists once rather than in each
// service's main.
//
// Config.Validate accepts every DECLARED backend and KEK provider — the external
// secret managers and cloud-KMS providers are additive identifiers whose impls
// have not landed. Accepting one here and quietly building the Postgres store
// anyway would resolve a known option to a different one: an operator selecting
// "vault" to keep credentials out of DeviceChain's storage would get them
// envelope-encrypted in the service's own database, with no error at startup, at
// write, or at read. A declared-but-unbuilt selection is therefore terminal here,
// the same shape as blob.New and connectorspec.ErrUnsupportedType — recognized,
// not executable, never silently substituted.
func New(cfg Config, db *gorm.DB, rootKey RootKeySource) (SecretStore, error) {
	cfg = cfg.withDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	// Enumerate the unbuilt identifiers explicitly rather than testing "not
	// postgres", so a backend added to Validate but not wired here falls to the
	// default arm and still fails closed instead of being treated as built.
	switch cfg.Backend {
	case BackendPostgres:
	case BackendGCPSecretManager, BackendAWSSecretsManager, BackendVault:
		return nil, fmt.Errorf("secrets: store backend %q is declared but not built in this binary; this build supports %q", cfg.Backend, BackendPostgres)
	default:
		// Unreachable today: Validate accepts exactly the identifiers the arms above
		// name. It becomes reachable the day a backend is added to Validate and not
		// to New, so the message names THAT cause rather than calling the identifier
		// unknown — Validate has just accepted it, so "unknown" would misdirect.
		return nil, fmt.Errorf("secrets: store backend %q passed validation but is not wired in New; this build supports %q", cfg.Backend, BackendPostgres)
	}
	// Reached only for the Postgres backend, where Validate has already constrained
	// the KEK provider to a declared identifier.
	switch cfg.KEKProvider {
	case InstanceKEKProvider:
	case KEKProviderGCPKMS, KEKProviderAWSKMS, KEKProviderVaultTransit:
		return nil, fmt.Errorf("secrets: KEK provider %q is declared but not built in this binary; this build supports %q", cfg.KEKProvider, InstanceKEKProvider)
	default:
		// Unreachable today, for the same reason as the backend default arm above.
		return nil, fmt.Errorf("secrets: KEK provider %q passed validation but is not wired in New; this build supports %q", cfg.KEKProvider, InstanceKEKProvider)
	}
	if db == nil {
		return nil, errors.New("secrets: the Postgres backend requires a database handle")
	}
	if rootKey == nil {
		return nil, errors.New("secrets: the instance KEK provider requires a root key source")
	}
	raw, err := rootKey()
	if err != nil {
		return nil, err
	}
	kp, err := NewInstanceKeyProvider(raw)
	if err != nil {
		return nil, err
	}
	return NewStore(db, kp), nil
}
