// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package secrets

import (
	"errors"
	"strings"
	"testing"
)

// goodRootKey is a well-formed 256-bit root key source, so a construction that
// fails in these tests fails on the selection under test and not on the key.
func goodRootKey() ([]byte, error) { return make([]byte, dekSize), nil }

// TestNewRejectsDeclaredButUnbuiltBackend is the gate: every backend identifier
// Config.Validate accepts but this binary does not build must be refused at
// construction. Accepting one and building the Postgres store anyway would
// silently resolve a known option to a different one — an operator selecting an
// external secret manager would get credentials in the service's own database.
func TestNewRejectsDeclaredButUnbuiltBackend(t *testing.T) {
	db := newStoreDB(t)
	for _, backend := range []string{BackendVault, BackendAWSSecretsManager, BackendGCPSecretManager} {
		// The premise: validation accepts it, so only New can catch it.
		if err := (Config{Backend: backend}).Validate(); err != nil {
			t.Fatalf("premise broken: backend %q must pass Validate: %v", backend, err)
		}
		store, err := New(Config{Backend: backend}, db, goodRootKey)
		if err == nil {
			t.Fatalf("backend %q is declared but not built: New must refuse it, got store %#v", backend, store)
		}
		if store != nil {
			t.Fatalf("backend %q: New returned an error AND a non-nil store %#v", backend, store)
		}
		if !strings.Contains(err.Error(), "not built in this binary") {
			t.Fatalf("backend %q: error must name the declared-but-unbuilt condition, got %q", backend, err)
		}
		if !strings.Contains(err.Error(), backend) {
			t.Fatalf("backend %q: error must name the selected backend, got %q", backend, err)
		}
		// An operator who learns only that the selection is refused still does not
		// know what to put in its place, so the message names the built one too.
		if !strings.Contains(err.Error(), BackendPostgres) {
			t.Fatalf("backend %q: error must name the backend this build supports, got %q", backend, err)
		}
	}
}

// TestNewRejectsDeclaredButUnbuiltKEKProvider is the same gate on the other seam:
// a cloud-KMS provider validates but is not built, and must not fall through to
// the instance provider — that would wrap every DEK with a root key the operator
// chose to keep inside a KMS.
func TestNewRejectsDeclaredButUnbuiltKEKProvider(t *testing.T) {
	db := newStoreDB(t)
	for _, provider := range []string{KEKProviderGCPKMS, KEKProviderAWSKMS, KEKProviderVaultTransit} {
		if err := (Config{Backend: BackendPostgres, KEKProvider: provider}).Validate(); err != nil {
			t.Fatalf("premise broken: provider %q must pass Validate: %v", provider, err)
		}
		store, err := New(Config{Backend: BackendPostgres, KEKProvider: provider}, db, goodRootKey)
		if err == nil {
			t.Fatalf("KEK provider %q is declared but not built: New must refuse it, got store %#v", provider, store)
		}
		if store != nil {
			t.Fatalf("KEK provider %q: New returned an error AND a non-nil store %#v", provider, store)
		}
		if !strings.Contains(err.Error(), "not built in this binary") {
			t.Fatalf("KEK provider %q: error must name the declared-but-unbuilt condition, got %q", provider, err)
		}
		if !strings.Contains(err.Error(), provider) {
			t.Fatalf("KEK provider %q: error must name the selected provider, got %q", provider, err)
		}
		if !strings.Contains(err.Error(), InstanceKEKProvider) {
			t.Fatalf("KEK provider %q: error must name the provider this build supports, got %q", provider, err)
		}
	}
}

// TestNewBuildsTheDefaultSelection is the counterweight: refusing the unbuilt
// options is only safe while the selection that IS built still constructs. Both
// the explicit default and the zero value (an omitted key means "the default")
// must build, and the store must actually work.
func TestNewBuildsTheDefaultSelection(t *testing.T) {
	for name, cfg := range map[string]Config{
		"explicit": {Backend: BackendPostgres, KEKProvider: InstanceKEKProvider},
		"zero":     {},
		"default":  DefaultConfig(),
	} {
		store, err := New(cfg, newStoreDB(t), goodRootKey)
		if err != nil {
			t.Fatalf("%s: the built selection must construct: %v", name, err)
		}
		if store == nil {
			t.Fatalf("%s: New returned a nil store and no error", name)
		}
		// A store that constructs but cannot round-trip a secret is not a pass.
		ref := SecretRef{Scope: ScopeInstance, Name: "probe/" + name}
		if err := store.Put(t.Context(), ref, []byte("value-"+name)); err != nil {
			t.Fatalf("%s: put through the constructed store: %v", name, err)
		}
		got, err := store.Resolve(t.Context(), ref)
		if err != nil {
			t.Fatalf("%s: resolve through the constructed store: %v", name, err)
		}
		if string(got) != "value-"+name {
			t.Fatalf("%s: round-trip returned %q", name, got)
		}
	}
}

// TestNewRejectsUnknownIdentifier proves New keeps Config.Validate's fail-closed
// rejection of a misspelled selection — an unknown identifier is not quietly
// treated as the default.
func TestNewRejectsUnknownIdentifier(t *testing.T) {
	db := newStoreDB(t)
	if _, err := New(Config{Backend: "sqlite"}, db, goodRootKey); err == nil {
		t.Fatal("unknown backend must be rejected")
	}
	if _, err := New(Config{Backend: BackendPostgres, KEKProvider: "rot13"}, db, goodRootKey); err == nil {
		t.Fatal("unknown KEK provider must be rejected")
	}
}

// TestNewRejectsUnusableRootKey proves the KEK-formation failures fail closed
// through New: a service that cannot form its KEK must not receive a store.
func TestNewRejectsUnusableRootKey(t *testing.T) {
	db := newStoreDB(t)
	want := errors.New("root key unavailable")
	if _, err := New(DefaultConfig(), db, func() ([]byte, error) { return nil, want }); !errors.Is(err, want) {
		t.Fatalf("a root-key source error must propagate, got %v", err)
	}
	if _, err := New(DefaultConfig(), db, func() ([]byte, error) { return make([]byte, 8), nil }); err == nil {
		t.Fatal("a wrong-length root key must be rejected")
	}
	if _, err := New(DefaultConfig(), db, nil); err == nil {
		t.Fatal("a nil root key source must be rejected")
	}
	if _, err := New(DefaultConfig(), nil, goodRootKey); err == nil {
		t.Fatal("a nil database handle must be rejected")
	}
}

// TestNewChecksBackendBeforeAskingForARootKey proves the ordering RootKeySource
// exists for: a deployment selecting an external backend owns its own keys, so it
// must be told the backend is not built rather than that an instance root key it
// does not need is missing.
func TestNewChecksBackendBeforeAskingForARootKey(t *testing.T) {
	asked := false
	source := func() ([]byte, error) {
		asked = true
		return nil, errors.New("no instance root key is configured")
	}
	_, err := New(Config{Backend: BackendVault}, newStoreDB(t), source)
	if err == nil {
		t.Fatal("an unbuilt backend must be refused")
	}
	if asked {
		t.Fatal("the root key source must not be consulted for a backend that is not built")
	}
	if !strings.Contains(err.Error(), "not built in this binary") {
		t.Fatalf("error must name the unbuilt backend, got %q", err)
	}
}
