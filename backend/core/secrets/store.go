// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package secrets

import (
	"context"
	"encoding/binary"
	"errors"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// Secret is one envelope-encrypted secret at rest (ADR-059): the sealed value plus
// its envelope metadata, keyed by the (tenant_id, scope, name) handle. Every column
// is ciphertext or metadata — no cleartext is ever stored.
//
// Scoping is deliberately explicit rather than left entirely to the tenant-scope
// callback, because a secret is dual-scoped: a tenant-scoped row is a tenant's own
// credential (the callback enforces tenant_id = the acting tenant, fail-closed),
// while an instance-scoped row is a control-plane secret with NO tenant (tenant_id
// is the empty sentinel and the row is reached only through the sanctioned system
// context). The store's scoped() builds the exact predicate for each so an instance
// operation can never touch a tenant row and vice versa.
//
// tenant_id uses the empty string (not NULL) for instance scope so the
// (tenant_id, scope, name) unique index actually constrains instance rows: Postgres
// treats NULLs as distinct in a unique index, which would let two instance secrets
// share a name — the empty sentinel is a normal, comparable value that does not.
type Secret struct {
	gorm.Model
	rdb.TenantScoped

	// Scope is "instance" or "tenant", mirroring the SecretRef; it disambiguates
	// the two row families and is part of the unique handle.
	Scope string `gorm:"not null;size:16;index"`
	// Name is the SecretRef.Name (e.g. "channel/{token}/secret"). It may contain
	// '/', which is why the AAD is length-prefixed (see aadFor).
	Name string `gorm:"not null;size:256;index"`

	// Ciphertext/Nonce are the AES-256-GCM-sealed value and its nonce.
	Ciphertext []byte `gorm:"not null"`
	Nonce      []byte `gorm:"not null"`
	// WrappedDEK/KEKVersion/Alg are the envelope metadata: the DEK sealed under the
	// KEK, which KEK generation sealed it, and the value-encryption algorithm.
	WrappedDEK []byte `gorm:"not null"`
	KEKVersion int    `gorm:"not null"`
	Alg        string `gorm:"not null;size:32"`
}

// TableName pins the table name independent of struct-name pluralization.
func (Secret) TableName() string { return "secrets" }

// AuditLabel contributes the non-sensitive handle (scope/name) to the audit journal
// so a Put/Rotate/Delete records WHICH secret changed without exposing any value.
// The name is a stable handle, never the secret material.
func (s Secret) AuditLabel() string { return s.Scope + "/" + s.Name }

// instanceTenantSentinel is the tenant_id stored on instance-scoped rows: the empty
// string, a normal comparable value (unlike NULL) so the unique index constrains
// instance secrets, and one no real tenant token can hold (the token grammar
// forbids an empty token).
const instanceTenantSentinel = ""

// gormStore is the default Postgres-backed SecretStore: it seals values with the
// envelope crypto (a fresh per-secret DEK wrapped by the KEK provider) and persists
// the envelope through gorm, applying the fail-closed tenant scope per operation.
type gormStore struct {
	db *gorm.DB
	kp KeyProvider
}

// NewStore builds the default envelope-encrypting store over db, wrapping each
// per-secret DEK with kp (the instance KEK provider by default). db is the base,
// unbound *gorm.DB (e.g. rdb.Database); the store binds the tenant/system context
// itself per operation so the platform tenant-scope callback enforces isolation on
// tenant-scoped rows. A service builds this once at startup after forming its KEK.
func NewStore(db *gorm.DB, kp KeyProvider) SecretStore {
	return &gormStore{db: db, kp: kp}
}

// ctxDB binds the base db to the context appropriate for ref's scope: a tenant
// context for tenant scope (so the tenant-scope callback injects and enforces
// tenant_id, fail-closed) or a system context for instance scope (the sanctioned
// control-plane bypass — instance secrets have no tenant). It carries the original
// ctx forward so any auth claims still reach the audit journal.
func (s *gormStore) ctxDB(ctx context.Context, ref SecretRef) *gorm.DB {
	if ref.Scope == ScopeTenant {
		return s.db.WithContext(core.WithTenant(ctx, ref.Tenant))
	}
	return s.db.WithContext(core.WithSystemContext(ctx))
}

// scoped returns a query pinned to exactly the row(s) ref identifies. For tenant
// scope the tenant-scope callback adds tenant_id = ref.Tenant; scope+name select the
// handle. For instance scope there is no callback (system context), so tenant_id is
// filtered explicitly to the instance sentinel — an instance operation therefore can
// never match a tenant row that happens to share a name.
func (s *gormStore) scoped(ctx context.Context, ref SecretRef) *gorm.DB {
	db := s.ctxDB(ctx, ref).Model(&Secret{})
	// The tenant_id predicate is stated explicitly in BOTH branches — for tenant
	// scope the tenant-scope callback also injects it, but stating it here is
	// defense-in-depth (a consumer that built the store over a *gorm.DB without the
	// callback registered would still be tenant-isolated on Exists/Delete/Resolve),
	// mirroring the instance branch and the store's "scoping is deliberately
	// explicit" doctrine.
	if ref.Scope == ScopeTenant {
		return db.Where("tenant_id = ? AND scope = ? AND name = ?", ref.Tenant, string(ScopeTenant), ref.Name)
	}
	return db.Where("tenant_id = ? AND scope = ? AND name = ?", instanceTenantSentinel, string(ScopeInstance), ref.Name)
}

// Put creates or replaces the secret under ref. It seals the value (fresh DEK,
// wrapped by the KEK) with the handle bound as AAD, then upserts the envelope: an
// existing live row's envelope columns are replaced (a fresh DEK/nonce each time),
// otherwise a new row is inserted. The (tenant_id, scope, name) partial unique index
// is the backstop against a concurrent double-insert.
func (s *gormStore) Put(ctx context.Context, ref SecretRef, value []byte) error {
	if err := ref.Valid(); err != nil {
		return err
	}
	env, err := Seal(ctx, s.kp, value, aadFor(ref))
	if err != nil {
		return err
	}

	var existing Secret
	err = s.scoped(ctx, ref).First(&existing).Error
	switch {
	case err == nil:
		// Replace the envelope on the loaded row and Save it. Saving the populated
		// struct (rather than an Updates(map)) keeps gorm's Statement.ReflectValue a
		// struct, so the audit journal captures the handle (AuditLabel) and PK for a
		// rotation — a map/zero-struct destination would record neither (ADR-019 /
		// ADR-059 §3). Scope/name/tenant are rewritten to their existing values (a
		// no-op) and the update is confined to this row by PK plus the tenant-scope
		// callback (tenant) or the instance-scoped fetch that produced existing.ID.
		existing.Ciphertext = env.Ciphertext
		existing.Nonce = env.Nonce
		existing.WrappedDEK = env.WrappedDEK
		existing.KEKVersion = env.KEKVersion
		existing.Alg = env.Alg
		return s.ctxDB(ctx, ref).Save(&existing).Error
	case errors.Is(err, gorm.ErrRecordNotFound):
		row := &Secret{
			Scope:      string(ref.Scope),
			Name:       ref.Name,
			Ciphertext: env.Ciphertext,
			Nonce:      env.Nonce,
			WrappedDEK: env.WrappedDEK,
			KEKVersion: env.KEKVersion,
			Alg:        env.Alg,
		}
		// Instance rows carry the sentinel tenant_id; the tenant-scope create
		// callback stamps ref.Tenant on tenant rows, so it is left unset there.
		if ref.Scope == ScopeInstance {
			row.TenantId = instanceTenantSentinel
		}
		return s.ctxDB(ctx, ref).Create(row).Error
	default:
		return err
	}
}

// Resolve returns the cleartext for ref. SERVER-INTERNAL ONLY (never wire to a
// resolver). It fails closed on a tampered or relabeled envelope: Open verifies the
// handle-bound AAD and both GCM auth tags. Returns ErrSecretNotFound when absent.
func (s *gormStore) Resolve(ctx context.Context, ref SecretRef) ([]byte, error) {
	if err := ref.Valid(); err != nil {
		return nil, err
	}
	var row Secret
	if err := s.scoped(ctx, ref).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrSecretNotFound
		}
		return nil, err
	}
	env := &Envelope{
		Ciphertext: row.Ciphertext,
		Nonce:      row.Nonce,
		WrappedDEK: row.WrappedDEK,
		KEKVersion: row.KEKVersion,
		Alg:        row.Alg,
	}
	return Open(ctx, s.kp, env, aadFor(ref))
}

// Rotate replaces the value under an existing ref with a fresh DEK and a bumped
// version; the handle is stable across a rotation. It is equivalent to Put (ADR-059).
func (s *gormStore) Rotate(ctx context.Context, ref SecretRef, value []byte) error {
	return s.Put(ctx, ref, value)
}

// Delete removes the secret under ref (soft delete, so the handle frees for reuse
// via the partial unique index). It is idempotent: deleting an absent secret is not
// an error.
func (s *gormStore) Delete(ctx context.Context, ref SecretRef) error {
	if err := ref.Valid(); err != nil {
		return err
	}
	// Pass a handle-bearing struct so gorm's Statement.ReflectValue carries the
	// scope/name and the audit journal records WHICH secret was deleted (AuditLabel);
	// a zero &Secret{} would log an empty handle for the most destructive mutation.
	// These fields are not query conditions (Delete conditions come from scoped()'s
	// Where); they only populate the audited row.
	return s.scoped(ctx, ref).Delete(&Secret{Scope: string(ref.Scope), Name: ref.Name}).Error
}

// Exists reports whether a secret is stored under ref, without decrypting it (it
// powers a write-only entity's hasSecret field).
func (s *gormStore) Exists(ctx context.Context, ref SecretRef) (bool, error) {
	if err := ref.Valid(); err != nil {
		return false, err
	}
	var count int64
	if err := s.scoped(ctx, ref).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// aadFor builds the additional-authenticated-data that binds a sealed value to its
// handle. The fields are LENGTH-PREFIXED (a 4-byte big-endian length before each)
// rather than concatenated, because SecretRef.Name legitimately contains '/'
// (e.g. "connector/{id}/auth"): a naive join could make two distinct handles —
// {tenant:"a", name:"b/c"} and {tenant:"a/b", name:"c"} — yield identical AAD, so a
// ciphertext could be relabeled between them. Length framing makes the encoding
// injective, so distinct handles always produce distinct AAD. A version tag leads
// the encoding so the AAD format itself can evolve.
func aadFor(ref SecretRef) []byte {
	var b []byte
	b = appendField(b, []byte("dc-secret-aad-v1"))
	b = appendField(b, []byte(ref.Scope))
	b = appendField(b, []byte(ref.Tenant))
	b = appendField(b, []byte(ref.Name))
	return b
}

// appendField appends a 4-byte big-endian length prefix followed by f, so a
// sequence of fields decodes unambiguously regardless of the bytes within them.
func appendField(dst, f []byte) []byte {
	var lenbuf [4]byte
	binary.BigEndian.PutUint32(lenbuf[:], uint32(len(f)))
	dst = append(dst, lenbuf[:]...)
	return append(dst, f...)
}
