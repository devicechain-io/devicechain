// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package secrets is the DeviceChain secret-storage abstraction (ADR-059): a
// handle-referenced, envelope-encrypted store for provider and integration
// credentials (SMTP/webhook secrets, connector auth, external AI-provider keys).
//
// The design has two orthogonal, pluggable seams under one interface:
//
//   - the STORE BACKEND — where a value physically lives (default: envelope-
//     encrypted in the service's own Postgres; opt-in: an external secret
//     manager holding the value while we keep only a reference), and
//   - the KEK PROVIDER — for the Postgres backend, how each per-secret data key
//     is wrapped (default: a root key from the instance K8s Secret, zero new
//     infra; opt-in: a cloud KMS that never releases the KEK).
//
// This S1 slice ships the LIBRARY: the interfaces, the envelope crypto, and the
// default instance-KEK provider. The gorm-backed store, the instance-root-key
// config wiring, and the consumers (notification retrofit, connector auth,
// ai-inference) land in later slices against these exact interfaces.
//
// The load-bearing red line (ADR-059): the API never returns cleartext. A value
// is written through a write-only path and is only ever Resolve'd server-internal
// at use time (dispatch, an AI call). Cleartext exists transiently inside the
// resolving service and on the wire to the provider — that is inherent to
// presenting a credential — and never crosses the GraphQL/API boundary.
package secrets

import (
	"context"
	"errors"
)

// ErrSecretNotFound is returned by a store's Resolve/Exists path when no secret
// is stored under the given handle. Consumers treat it as "no credential"
// (fail-closed at the call site) rather than an infrastructure error.
var ErrSecretNotFound = errors.New("secret not found")

// Scope distinguishes a platform/control-plane-scoped secret (shared across the
// instance, e.g. the hosted offering's shared AI-provider key) from a tenant's
// own credential (connector auth, a bring-your-own AI-provider key).
type Scope string

const (
	// ScopeInstance is a control-plane-scoped secret (tenant_id = null), shared
	// across the whole instance.
	ScopeInstance Scope = "instance"
	// ScopeTenant is a single tenant's credential, isolated by the tenant-scope
	// predicate like any other tenant-scoped row.
	ScopeTenant Scope = "tenant"
)

// SecretRef is the stable handle a consumer stores in its own config in place of
// a value — a connector row stores its ref, an ai-inference provider config
// points at "ai/provider/{name}". Resolving the ref yields the cleartext
// server-internal at use time; the ref itself carries no secret material.
type SecretRef struct {
	Scope  Scope
	Tenant string // "" for instance scope
	Name   string // e.g. "connector/{id}/auth", "ai/provider/{name}", "channel/{token}/secret"
}

// Valid reports whether the ref is well-formed: a known scope, a Name, and a
// Tenant present exactly when (and only when) the scope is tenant. It is the
// fail-closed gate a store applies before any operation so a malformed handle
// can never silently read or write across a scope boundary.
func (r SecretRef) Valid() error {
	switch r.Scope {
	case ScopeInstance:
		if r.Tenant != "" {
			return errors.New("secrets: instance-scoped ref must not carry a tenant")
		}
	case ScopeTenant:
		if r.Tenant == "" {
			return errors.New("secrets: tenant-scoped ref requires a tenant")
		}
	default:
		return errors.New("secrets: ref has an unknown scope")
	}
	if r.Name == "" {
		return errors.New("secrets: ref requires a name")
	}
	return nil
}

// SecretStore is the backend-agnostic secret abstraction. Put/Rotate/Delete are
// the write-only mutations a GraphQL layer may drive; Resolve is server-internal
// ONLY (never reachable from the API) and yields cleartext at use time; Exists
// powers a write-only entity's hasSecret field without exposing the value.
//
// Implementations land in later slices (S2 = the gorm/Postgres store); this
// slice defines the contract the envelope crypto and KEK providers serve.
type SecretStore interface {
	// Put creates or replaces the value under ref (write-only).
	Put(ctx context.Context, ref SecretRef, value []byte) error
	// Resolve returns the cleartext for ref. SERVER-INTERNAL ONLY — never wire
	// this to a GraphQL resolver. Returns ErrSecretNotFound when absent.
	Resolve(ctx context.Context, ref SecretRef) ([]byte, error)
	// Rotate replaces the value under an existing ref (a fresh DEK, a bumped
	// version); the handle is stable across a rotation. Equivalent to Put.
	Rotate(ctx context.Context, ref SecretRef, value []byte) error
	// Delete removes the secret under ref.
	Delete(ctx context.Context, ref SecretRef) error
	// Exists reports whether a secret is stored under ref, without decrypting it.
	Exists(ctx context.Context, ref SecretRef) (bool, error)
}

// KeyProvider wraps and unwraps a per-secret data-encryption key (DEK). It is the
// pluggable KEK seam for the Postgres store backend: the default instance
// provider wraps with a root key from the instance K8s Secret; cloud-KMS
// providers (opt-in, a later slice) call the KMS so the KEK never leaves it.
//
// version identifies which KEK generation wrapped a DEK, so a future KEK rotation
// can unwrap old envelopes with the prior key while sealing new ones with the
// current key. v1 of the instance provider is single-version.
type KeyProvider interface {
	// Wrap seals a DEK under the current KEK, returning the wrapped bytes and the
	// KEK version that sealed them. The DEK is not retained.
	Wrap(ctx context.Context, dek []byte) (wrapped []byte, version int, err error)
	// Unwrap recovers a DEK sealed under the KEK identified by version. It fails
	// closed if the version is unknown or the wrapped bytes do not authenticate.
	Unwrap(ctx context.Context, wrapped []byte, version int) (dek []byte, err error)
	// Name is the provider's config identifier (e.g. "instance").
	Name() string
}
