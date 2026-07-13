// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package secrets

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// newStoreDB builds an in-memory sqlite DB with the platform tenant-scope and audit
// callbacks registered (as a real service has) and the secrets table migrated via
// the shared migration — so the tests exercise the real scoping and the real schema.
func newStoreDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := rdb.RegisterAuditJournal(db); err != nil {
		t.Fatalf("register audit journal: %v", err)
	}
	if err := db.AutoMigrate(&rdb.AuditEvent{}); err != nil {
		t.Fatalf("migrate audit: %v", err)
	}
	if err := NewSecretStoreSchema().Migrate(db); err != nil {
		t.Fatalf("migrate secrets: %v", err)
	}
	return db
}

// newTestStore builds a store over a fresh DB and a fresh instance KEK.
func newTestStore(t *testing.T) SecretStore {
	t.Helper()
	return NewStore(newStoreDB(t), newTestKP(t))
}

func tenantRef(tenant, name string) SecretRef {
	return SecretRef{Scope: ScopeTenant, Tenant: tenant, Name: name}
}

func instanceRef(name string) SecretRef {
	return SecretRef{Scope: ScopeInstance, Name: name}
}

// TestStorePutResolveRoundTrip proves a value put under a handle resolves back to the
// same cleartext, for both tenant and instance scope — and that the caller need not
// pre-set any tenant in the context (the store derives scoping from the ref).
func TestStorePutResolveRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		ref  SecretRef
		val  []byte
	}{
		{"tenant", tenantRef("acme", "channel/tok/secret"), []byte("smtp-password")},
		{"instance", instanceRef("ai/provider/anthropic"), []byte("sk-ant-123")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.Put(ctx, tc.ref, tc.val); err != nil {
				t.Fatalf("put: %v", err)
			}
			got, err := s.Resolve(ctx, tc.ref)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if !bytes.Equal(got, tc.val) {
				t.Fatalf("round-trip mismatch: got %q want %q", got, tc.val)
			}
		})
	}
}

// TestStoreEncryptedAtRest proves the persisted row holds no cleartext: the plaintext
// appears in neither the ciphertext nor the wrapped DEK.
func TestStoreEncryptedAtRest(t *testing.T) {
	db := newStoreDB(t)
	s := NewStore(db, newTestKP(t))
	ctx := context.Background()
	secret := []byte("super-secret-value")

	if err := s.Put(ctx, tenantRef("acme", "n1"), secret); err != nil {
		t.Fatalf("put: %v", err)
	}
	var row Secret
	if err := db.WithContext(core.WithSystemContext(ctx)).Where("name = ?", "n1").First(&row).Error; err != nil {
		t.Fatalf("read raw row: %v", err)
	}
	if bytes.Contains(row.Ciphertext, secret) || bytes.Contains(row.WrappedDEK, secret) {
		t.Fatal("stored row must not contain the cleartext value")
	}
	if row.Alg != AlgAES256GCM || row.KEKVersion != instanceKEKVersion {
		t.Fatalf("envelope metadata wrong: alg=%q version=%d", row.Alg, row.KEKVersion)
	}
}

// TestStoreTenantIsolation proves one tenant cannot resolve another tenant's secret
// stored under the same name, and that both coexist with distinct values.
func TestStoreTenantIsolation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.Put(ctx, tenantRef("acme", "shared"), []byte("acme-val")); err != nil {
		t.Fatalf("put acme: %v", err)
	}
	if err := s.Put(ctx, tenantRef("globex", "shared"), []byte("globex-val")); err != nil {
		t.Fatalf("put globex: %v", err)
	}

	acme, err := s.Resolve(ctx, tenantRef("acme", "shared"))
	if err != nil || string(acme) != "acme-val" {
		t.Fatalf("acme resolve: val=%q err=%v", acme, err)
	}
	globex, err := s.Resolve(ctx, tenantRef("globex", "shared"))
	if err != nil || string(globex) != "globex-val" {
		t.Fatalf("globex resolve: val=%q err=%v", globex, err)
	}

	// A tenant with no secret under that name must miss, not leak another tenant's.
	if _, err := s.Resolve(ctx, tenantRef("initech", "shared")); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("expected ErrSecretNotFound for a tenant with no secret, got %v", err)
	}
}

// TestStoreInstanceTenantScopeSeparation proves an instance secret and a tenant
// secret can share a name without colliding, and neither scope's operation reaches
// the other's row.
func TestStoreInstanceTenantScopeSeparation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	name := "ai/provider/anthropic"

	if err := s.Put(ctx, instanceRef(name), []byte("platform-key")); err != nil {
		t.Fatalf("put instance: %v", err)
	}
	if err := s.Put(ctx, tenantRef("acme", name), []byte("byo-key")); err != nil {
		t.Fatalf("put tenant: %v", err)
	}

	inst, err := s.Resolve(ctx, instanceRef(name))
	if err != nil || string(inst) != "platform-key" {
		t.Fatalf("instance resolve: val=%q err=%v", inst, err)
	}
	ten, err := s.Resolve(ctx, tenantRef("acme", name))
	if err != nil || string(ten) != "byo-key" {
		t.Fatalf("tenant resolve: val=%q err=%v", ten, err)
	}

	// Deleting the instance secret must leave the tenant secret intact.
	if err := s.Delete(ctx, instanceRef(name)); err != nil {
		t.Fatalf("delete instance: %v", err)
	}
	if _, err := s.Resolve(ctx, instanceRef(name)); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("instance secret should be gone, got %v", err)
	}
	if ten, err := s.Resolve(ctx, tenantRef("acme", name)); err != nil || string(ten) != "byo-key" {
		t.Fatalf("tenant secret must survive instance delete: val=%q err=%v", ten, err)
	}
}

// TestStorePutReplaces proves a second Put under the same handle replaces the value
// (create-or-replace) rather than erroring or duplicating, with a fresh envelope.
func TestStorePutReplaces(t *testing.T) {
	db := newStoreDB(t)
	s := NewStore(db, newTestKP(t))
	ctx := context.Background()
	ref := tenantRef("acme", "rotate-me")

	if err := s.Put(ctx, ref, []byte("v1")); err != nil {
		t.Fatalf("put v1: %v", err)
	}
	var first Secret
	if err := db.WithContext(core.WithSystemContext(ctx)).Where("name = ?", "rotate-me").First(&first).Error; err != nil {
		t.Fatalf("read first: %v", err)
	}

	if err := s.Rotate(ctx, ref, []byte("v2")); err != nil {
		t.Fatalf("rotate v2: %v", err)
	}
	got, err := s.Resolve(ctx, ref)
	if err != nil || string(got) != "v2" {
		t.Fatalf("resolve after rotate: val=%q err=%v", got, err)
	}

	// Exactly one live row, with a fresh nonce (a new DEK sealed the new value).
	var count int64
	if err := db.WithContext(core.WithSystemContext(ctx)).Model(&Secret{}).Where("name = ?", "rotate-me").Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 live row after replace, got %d", count)
	}
	var second Secret
	if err := db.WithContext(core.WithSystemContext(ctx)).Where("name = ?", "rotate-me").First(&second).Error; err != nil {
		t.Fatalf("read second: %v", err)
	}
	if bytes.Equal(first.Nonce, second.Nonce) {
		t.Fatal("a replace must use a fresh nonce/DEK")
	}
}

// TestStoreExists proves Exists reflects presence without decrypting, and tracks
// Put/Delete.
func TestStoreExists(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	ref := tenantRef("acme", "e1")

	if ok, err := s.Exists(ctx, ref); err != nil || ok {
		t.Fatalf("expected absent, got ok=%v err=%v", ok, err)
	}
	if err := s.Put(ctx, ref, []byte("v")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if ok, err := s.Exists(ctx, ref); err != nil || !ok {
		t.Fatalf("expected present, got ok=%v err=%v", ok, err)
	}
	if err := s.Delete(ctx, ref); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if ok, err := s.Exists(ctx, ref); err != nil || ok {
		t.Fatalf("expected absent after delete, got ok=%v err=%v", ok, err)
	}
}

// TestStoreDeleteIdempotentAndFreesHandle proves deleting an absent secret is not an
// error, and that a deleted handle frees for reuse (the soft-delete-aware unique
// index) so a later Put under the same handle succeeds.
func TestStoreDeleteIdempotentAndFreesHandle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	ref := tenantRef("acme", "reuse")

	// Delete before any put: idempotent, no error.
	if err := s.Delete(ctx, ref); err != nil {
		t.Fatalf("delete-absent must be nil, got %v", err)
	}
	if err := s.Put(ctx, ref, []byte("v1")); err != nil {
		t.Fatalf("put v1: %v", err)
	}
	if err := s.Delete(ctx, ref); err != nil {
		t.Fatalf("delete v1: %v", err)
	}
	// Re-put under the freed handle must succeed (index frees on soft delete).
	if err := s.Put(ctx, ref, []byte("v2")); err != nil {
		t.Fatalf("re-put must succeed after delete: %v", err)
	}
	got, err := s.Resolve(ctx, ref)
	if err != nil || string(got) != "v2" {
		t.Fatalf("resolve after re-put: val=%q err=%v", got, err)
	}
}

// TestStoreResolveMissing proves an absent handle yields ErrSecretNotFound (not a raw
// infrastructure error), so consumers can fail closed at the call site.
func TestStoreResolveMissing(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Resolve(context.Background(), tenantRef("acme", "nope")); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("expected ErrSecretNotFound, got %v", err)
	}
}

// TestStoreInvalidRefRejected proves a malformed handle is rejected fail-closed on
// every operation, before any query touches the store.
func TestStoreInvalidRefRejected(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	bad := SecretRef{Scope: ScopeTenant, Name: "x"} // tenant scope with no tenant

	if err := s.Put(ctx, bad, []byte("v")); err == nil {
		t.Fatal("Put must reject an invalid ref")
	}
	if _, err := s.Resolve(ctx, bad); err == nil {
		t.Fatal("Resolve must reject an invalid ref")
	}
	if err := s.Rotate(ctx, bad, []byte("v")); err == nil {
		t.Fatal("Rotate must reject an invalid ref")
	}
	if err := s.Delete(ctx, bad); err == nil {
		t.Fatal("Delete must reject an invalid ref")
	}
	if _, err := s.Exists(ctx, bad); err == nil {
		t.Fatal("Exists must reject an invalid ref")
	}
}

// TestStoreHandleBindingRejectsRelabel proves a stored envelope is bound to its
// handle: transplanting one secret's envelope columns onto another handle's row (a
// relabel attack an attacker with DB write could attempt) makes that row fail to
// resolve, because the AAD derived from the target handle no longer matches.
func TestStoreHandleBindingRejectsRelabel(t *testing.T) {
	db := newStoreDB(t)
	s := NewStore(db, newTestKP(t))
	ctx := context.Background()

	if err := s.Put(ctx, tenantRef("acme", "n1"), []byte("v1")); err != nil {
		t.Fatalf("put n1: %v", err)
	}
	if err := s.Put(ctx, tenantRef("acme", "n2"), []byte("v2")); err != nil {
		t.Fatalf("put n2: %v", err)
	}

	sysctx := core.WithSystemContext(ctx)
	var r1 Secret
	if err := db.WithContext(sysctx).Where("name = ?", "n1").First(&r1).Error; err != nil {
		t.Fatalf("read n1: %v", err)
	}
	// Graft n1's envelope onto n2's row.
	if err := db.WithContext(sysctx).Model(&Secret{}).Where("name = ?", "n2").Updates(map[string]any{
		"ciphertext":  r1.Ciphertext,
		"nonce":       r1.Nonce,
		"wrapped_dek": r1.WrappedDEK,
		"kek_version": r1.KEKVersion,
		"alg":         r1.Alg,
	}).Error; err != nil {
		t.Fatalf("graft: %v", err)
	}
	// Resolving n2 must now fail: the ciphertext authenticates only under n1's AAD.
	if _, err := s.Resolve(ctx, tenantRef("acme", "n2")); err == nil {
		t.Fatal("resolve of a relabeled envelope must fail (handle-bound AAD)")
	}
}

// TestAadForInjective proves the AAD encoding is injective across the '/' boundary:
// two distinct handles that a naive join would collapse to the same string produce
// distinct AAD, so a ciphertext can never be relabeled between them.
func TestAadForInjective(t *testing.T) {
	a := aadFor(SecretRef{Scope: ScopeTenant, Tenant: "a", Name: "b/c"})
	b := aadFor(SecretRef{Scope: ScopeTenant, Tenant: "a/b", Name: "c"})
	if bytes.Equal(a, b) {
		t.Fatal("distinct handles must produce distinct AAD (length-prefixing prevents the '/' collision)")
	}
}

// TestStorePutWritesAuditRow proves a Put is captured by the audit journal by
// construction (ADR-019 / ADR-059 §3): a mutation row records the actor and the
// non-sensitive handle, never the value.
func TestStorePutWritesAuditRow(t *testing.T) {
	db := newStoreDB(t)
	s := NewStore(db, newTestKP(t))
	ctx := context.Background()

	if err := s.Put(ctx, instanceRef("ai/provider/anthropic"), []byte("sk-secret")); err != nil {
		t.Fatalf("put: %v", err)
	}
	var events []rdb.AuditEvent
	if err := db.WithContext(core.WithSystemContext(ctx)).Where("table_name = ?", "secrets").Find(&events).Error; err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 audit row for the Put, got %d", len(events))
	}
	e := events[0]
	if e.Operation != "create" || e.Actor != "system" {
		t.Fatalf("audit row wrong: op=%q actor=%q", e.Operation, e.Actor)
	}
	if e.EntityLabel != "instance/ai/provider/anthropic" {
		t.Fatalf("audit label should be the handle, got %q", e.EntityLabel)
	}
	if bytes.Contains([]byte(e.EntityLabel), []byte("sk-secret")) {
		t.Fatal("audit row must never contain the secret value")
	}
}
