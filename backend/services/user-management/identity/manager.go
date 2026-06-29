// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package identity implements DeviceChain native authentication (ADR-008): it
// owns the instance RSA signing key, issues RS256 access/refresh tokens, and
// verifies credentials. It is the only component that holds the private key and
// the only sanctioned user of the tenant-unscoped system context
// (core.WithSystemContext) — for the login lookup, which must resolve a user
// (and thus a tenant) before any tenant is known.
package identity

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/google/uuid"
	nats "github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// RefreshBucket is the NATS KV bucket name backing the server-side refresh-token
// store. Each live refresh token's jti is a key; deleting it revokes the token.
const RefreshBucket = "dc_refresh_tokens"

// ErrInvalidCredentials is returned for every login failure (unknown user, bad
// password, or disabled account). It is deliberately uniform so the API does not
// reveal whether a username exists.
var ErrInvalidCredentials = errors.New("invalid credentials")

// ErrInvalidToken is returned when a refresh token fails signature verification
// or is absent from the server-side store (rotated, revoked, or expired).
var ErrInvalidToken = errors.New("invalid or expired token")

// BootstrapConfig describes the superuser seeded on first startup (ADR-033). The
// bootstrap is tenant-less: only the superuser identity is created, with no
// scaffold tenant or membership — the superuser lands in the admin console and
// creates the first tenant there.
type BootstrapConfig struct {
	// SuperuserEmail/SuperuserPassword identify the global superuser identity.
	SuperuserEmail    string
	SuperuserPassword string
}

// Manager owns native auth for the instance. Build it with NewManager, then
// call Initialize once the database and NATS KV are available.
type Manager struct {
	ms        *core.Microservice
	db        *rdb.RdbManager
	iam       *iam.Store
	locker    *messaging.DistributedLock
	accessTTL time.Duration

	refreshKV nats.KeyValue
	// dummyHash equalizes login timing on the user-not-found path so response
	// time does not reveal whether a username exists.
	dummyHash  []byte
	refreshTTL time.Duration
	bootstrap  BootstrapConfig
	issuerName string

	// mu guards the signing-key material, which a rotation replaces while the
	// service is live. The validator pointer is created once (request handlers
	// hold it) and its key set is updated in place via SetKeys, so it is not
	// itself guarded after Initialize.
	mu         sync.RWMutex
	issuer     *auth.Issuer
	validator  *auth.Validator
	publicKeys []*rsa.PublicKey
}

// TokenPair is the result of a successful login or refresh.
type TokenPair struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

// NewManager constructs a Manager. accessTTL/refreshTTL of 0 fall back to the
// auth package defaults. locker serializes signing-key generation/rotation and
// bootstrap seeding across replicas (ADR-007).
func NewManager(ms *core.Microservice, db *rdb.RdbManager, locker *messaging.DistributedLock, accessTTL, refreshTTL time.Duration, bootstrap BootstrapConfig) *Manager {
	return &Manager{ms: ms, db: db, iam: iam.NewStore(db), locker: locker, accessTTL: accessTTL, refreshTTL: refreshTTL, bootstrap: bootstrap}
}

// Initialize loads (or creates) the signing key, builds the issuer/validator,
// wires the refresh-token store, and seeds the bootstrap admin. Must run after
// the RdbManager is initialized (tables exist) and the refresh KV bucket is
// created.
func (m *Manager) Initialize(ctx context.Context, refreshKV nats.KeyValue) error {
	m.issuerName = fmt.Sprintf("dc-user-management:%s", m.ms.InstanceId)
	set, err := m.loadSigningKeys(ctx)
	if err != nil {
		return err
	}
	m.applyKeys(set)
	m.refreshKV = refreshKV

	dummy, err := bcrypt.GenerateFromPassword([]byte("dc-login-timing-equalizer"), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	m.dummyHash = dummy

	return m.seedSuperuser(ctx)
}

// applyKeys installs a loaded signing-key set: it rebuilds the issuer on the
// active key and publishes the full retained public-key set to the validator
// (in place, so handlers holding the pointer see it) and the JWKS.
func (m *Manager) applyKeys(set *signingKeySet) {
	keyMap := make(map[string]*rsa.PublicKey, len(set.publicKeys))
	for _, p := range set.publicKeys {
		keyMap[auth.Thumbprint(p)] = p
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.issuer = auth.NewIssuer(set.active, m.issuerName, m.accessTTL, m.refreshTTL)
	if m.validator == nil {
		m.validator = auth.NewValidatorFromKeys(keyMap)
	} else {
		m.validator.SetKeys(keyMap)
	}
	m.publicKeys = set.publicKeys
}

// RotateSigningKey rotates the instance signing key: the previous active key is
// retained (and still served in the JWKS) until past retention, after which it
// is pruned. New tokens are signed by the new key immediately; tokens signed by
// the retained key keep verifying until they expire.
func (m *Manager) RotateSigningKey(ctx context.Context, retention time.Duration) error {
	// A retired key must outlive every token it signed, so never prune sooner
	// than the refresh-token lifetime regardless of how retention was configured.
	if retention <= m.refreshTTL {
		retention = m.refreshTTL + 24*time.Hour
	}
	set, err := m.rotateSigningKey(ctx, retention)
	if err != nil {
		return err
	}
	m.applyKeys(set)
	return nil
}

// MaybeRotateOnAge rotates the signing key if the active key is older than
// maxAge. maxAge <= 0 disables age-based rotation. Called at startup so a
// long-lived instance does not sign with one key forever (ADR-008 follow-up).
func (m *Manager) MaybeRotateOnAge(ctx context.Context, maxAge, retention time.Duration) error {
	if maxAge <= 0 {
		return nil
	}
	age, err := m.activeKeyAge(ctx)
	if err != nil {
		return err
	}
	if age < maxAge {
		return nil
	}
	log.Info().Dur("age", age).Msg("Active signing key exceeded max age; rotating.")
	return m.RotateSigningKey(ctx, retention)
}

// Validator returns the access-token validator built from the local public keys
// (user-management validates its own API requests without a network fetch). The
// pointer is stable for the manager's lifetime; a rotation updates its key set
// in place.
func (m *Manager) Validator() *auth.Validator { return m.validator }

// JWKS returns the JWK Set of every retained public key, served so other services
// can select the right key by kid across a rotation.
func (m *Manager) JWKS() ([]byte, error) {
	m.mu.RLock()
	publics := m.publicKeys
	m.mu.RUnlock()
	return auth.BuildJWKS(publics)
}

// IdentityAuth is the result of a successful email/password login (ADR-033): an
// instance-scoped identity token plus the tenants the identity may act in. No
// tenant is chosen yet — the client picks one and exchanges it via SelectTenant
// for a tenant-scoped token pair.
type IdentityAuth struct {
	IdentityToken string
	ExpiresAt     time.Time
	Superuser     bool
	Memberships   []MembershipInfo
}

// MembershipInfo names a tenant the identity belongs to and the role tokens it
// holds there (carried for the tenant picker / display).
type MembershipInfo struct {
	Tenant string
	Roles  []string
}

// Login verifies an email/password and returns an identity token plus the
// identity's memberships (ADR-033). Failures are uniform (unknown email, bad
// password, or disabled) and timing-equalized so the API does not reveal whether
// an email exists.
func (m *Manager) Login(ctx context.Context, email, password string) (*IdentityAuth, error) {
	email = normalizeEmail(email)
	id, err := m.iam.IdentityByEmail(ctx, email)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if id == nil || !id.Enabled {
		_ = bcrypt.CompareHashAndPassword(m.dummyHash, []byte(password))
		m.recordAuth(ctx, rdb.AuditOpLoginFailed, email, "")
		return nil, ErrInvalidCredentials
	}
	if bcrypt.CompareHashAndPassword([]byte(id.PasswordHash), []byte(password)) != nil {
		m.recordAuth(ctx, rdb.AuditOpLoginFailed, email, "")
		return nil, ErrInvalidCredentials
	}
	m.recordAuth(ctx, rdb.AuditOpLogin, id.Email, "")

	m.mu.RLock()
	issuer := m.issuer
	m.mu.RUnlock()
	tok, err := issuer.IssueIdentity(id.Email, roleTokens(id.SystemRoles), id.SystemAuthorities(), uuid.NewString())
	if err != nil {
		return nil, err
	}
	return &IdentityAuth{
		IdentityToken: tok.Token,
		ExpiresAt:     tok.ExpiresAt,
		Superuser:     isSuperuser(id),
		Memberships:   membershipInfos(id.Memberships),
	}, nil
}

// SelectTenant exchanges a valid identity token for a tenant-scoped token pair
// (ADR-033). The identity must hold an (enabled) membership in the tenant, unless
// it is a superuser — which may enter any tenant with full authority, marked
// actingAsSuperuser on the token for audit.
func (m *Manager) SelectTenant(ctx context.Context, identityToken, tenant string) (*TokenPair, error) {
	claims, err := m.validator.ValidateIdentity(identityToken)
	if err != nil {
		return nil, ErrInvalidToken
	}
	id, err := m.iam.IdentityByEmail(ctx, claims.Email)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInvalidToken
		}
		return nil, err
	}
	if !id.Enabled {
		return nil, ErrInvalidToken
	}

	su := isSuperuser(id)
	mem := findMembership(id.Memberships, tenant)
	if mem == nil && !su {
		m.recordAuth(ctx, rdb.AuditOpLoginFailed, id.Email, tenant)
		return nil, ErrInvalidCredentials
	}

	var roles, authorities []string
	if mem != nil {
		if !mem.Enabled {
			return nil, ErrInvalidCredentials
		}
		roles = roleTokens(mem.TenantRoles)
		authorities = mem.TenantAuthorities()
	}
	if su {
		// Break-glass: a superuser acts in any tenant with full authority. The
		// token carries actingAsSuperuser so the audit log shows a superuser acted.
		authorities = []string{string(auth.AuthorityAll)}
	}
	m.recordAuth(ctx, rdb.AuditOpLogin, id.Email, tenant)
	return m.issueTenantTokens(tenant, id.Email, roles, authorities, su)
}

// CurrentTenant resolves the control-plane tenant record the caller is acting
// within, keyed by the tenant token carried in their access token. It reads the
// tenant-unscoped control-plane table (iam.Store uses the system context), so it
// works from a tenant-scoped data-plane request.
func (m *Manager) CurrentTenant(ctx context.Context, token string) (*iam.Tenant, error) {
	return m.iam.TenantByToken(ctx, token)
}

// recordAuth writes an authentication audit event (ADR-019) best-effort: a
// failure to record is logged but never fails the authentication itself.
func (m *Manager) recordAuth(ctx context.Context, operation, actor, tenant string) {
	if err := m.db.RecordAuthEvent(ctx, operation, actor, tenant); err != nil {
		log.Error().Err(err).Str("operation", operation).Msg("Failed to record auth audit event")
	}
}

// Refresh exchanges a valid, unrevoked tenant refresh token for a new pair,
// rotating it. Authorities are re-resolved from the identity's current
// membership, so a role change takes effect on the next refresh.
func (m *Manager) Refresh(ctx context.Context, refreshToken string) (*TokenPair, error) {
	claims, err := m.validator.ValidateRefresh(refreshToken)
	if err != nil {
		return nil, ErrInvalidToken
	}
	if _, err := m.refreshKV.Get(claims.ID); err != nil {
		return nil, ErrInvalidToken
	}
	// Rotate: invalidate the presented token before minting a replacement.
	_ = m.refreshKV.Delete(claims.ID)

	// The refresh token's subject is the identity email; the tenant is its claim.
	id, err := m.iam.IdentityByEmail(ctx, claims.Username)
	if err != nil || !id.Enabled {
		return nil, ErrInvalidToken
	}
	su := isSuperuser(id)
	mem := findMembership(id.Memberships, claims.Tenant)
	if mem == nil && !su {
		return nil, ErrInvalidToken
	}
	var roles, authorities []string
	if mem != nil {
		if !mem.Enabled {
			return nil, ErrInvalidToken
		}
		roles = roleTokens(mem.TenantRoles)
		authorities = mem.TenantAuthorities()
	}
	if su {
		authorities = []string{string(auth.AuthorityAll)}
	}
	m.recordAuth(ctx, rdb.AuditOpRefresh, id.Email, claims.Tenant)
	return m.issueTenantTokens(claims.Tenant, id.Email, roles, authorities, su)
}

// issueTenantTokens mints a tenant access + refresh pair for a global identity
// and records the refresh jti in the server-side store.
func (m *Manager) issueTenantTokens(tenant, email string, roles, authorities []string, sudo bool) (*TokenPair, error) {
	m.mu.RLock()
	issuer := m.issuer
	m.mu.RUnlock()

	access, err := issuer.IssueTenantAccess(tenant, email, roles, authorities, sudo, uuid.NewString())
	if err != nil {
		return nil, err
	}
	refreshJti := uuid.NewString()
	refresh, err := issuer.IssueRefresh(tenant, email, roles, authorities, refreshJti)
	if err != nil {
		return nil, err
	}
	if _, err := m.refreshKV.Put(refreshJti, []byte(email)); err != nil {
		return nil, err
	}
	return &TokenPair{AccessToken: access.Token, RefreshToken: refresh.Token, ExpiresAt: access.ExpiresAt}, nil
}

// seedSuperuser creates the superuser identity (system role `superuser`, authority
// `*`) on first startup, when no identity exists, under a distributed lock so
// replicas seed exactly once. The bootstrap is tenant-less (ADR-033 phase 4): no
// scaffold tenant or membership is created — the superuser lands in the admin
// console and creates the first tenant there. A convenience `tenant-admin` tenant
// role is seeded in the catalog so the admin has a full-authority role to assign.
func (m *Manager) seedSuperuser(ctx context.Context) error {
	return m.locker.WithLock(ctx, m.ms.FunctionalArea, func(ctx context.Context) error {
		n, err := m.iam.CountIdentities(ctx)
		if err != nil {
			return err
		}
		if n > 0 {
			return nil
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(m.bootstrap.SuperuserPassword), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		email := normalizeEmail(m.bootstrap.SuperuserEmail)
		all := []string{string(auth.AuthorityAll)}
		if err := m.iam.SeedSuperuser(ctx, email, string(hash), all, all); err != nil {
			return err
		}
		log.Warn().Str("email", email).
			Msg("Seeded superuser (system role=superuser, authority=*) with the default password — CHANGE IT IMMEDIATELY.")
		return nil
	})
}

// normalizeEmail lower-cases and trims an email so lookups and uniqueness are
// case-insensitive. Delegates to the shared iam normalizer so the auth and admin
// paths agree.
func normalizeEmail(e string) string { return iam.NormalizeEmail(e) }

// roleTokens projects roles to their token strings.
func roleTokens(roles []iam.Role) []string {
	out := make([]string, 0, len(roles))
	for _, r := range roles {
		out = append(out, r.Token)
	}
	return out
}

// isSuperuser reports whether the identity holds the superuser system role.
func isSuperuser(id *iam.Identity) bool {
	for _, r := range id.SystemRoles {
		if r.Token == iam.SuperuserRoleToken {
			return true
		}
	}
	return false
}

// findMembership returns the identity's membership in the tenant, or nil.
func findMembership(ms []iam.Membership, tenant string) *iam.Membership {
	for i := range ms {
		if ms[i].TenantId == tenant {
			return &ms[i]
		}
	}
	return nil
}

// membershipInfos projects memberships to the login response shape.
func membershipInfos(ms []iam.Membership) []MembershipInfo {
	out := make([]MembershipInfo, 0, len(ms))
	for _, mm := range ms {
		out = append(out, MembershipInfo{Tenant: mm.TenantId, Roles: roleTokens(mm.TenantRoles)})
	}
	return out
}
