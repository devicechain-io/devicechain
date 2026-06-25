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
	"github.com/devicechain-io/dc-user-management/model"
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

// BootstrapConfig describes the admin user seeded on first startup.
type BootstrapConfig struct {
	Tenant   string
	Username string
	Password string
}

// Manager owns native auth for the instance. Build it with NewManager, then
// call Initialize once the database and NATS KV are available.
type Manager struct {
	ms        *core.Microservice
	db        *rdb.RdbManager
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
	return &Manager{ms: ms, db: db, locker: locker, accessTTL: accessTTL, refreshTTL: refreshTTL, bootstrap: bootstrap}
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

	return m.seedBootstrapAdmin(ctx)
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

// Login verifies a username/password and returns a fresh token pair.
func (m *Manager) Login(ctx context.Context, username, password string) (*TokenPair, error) {
	user, err := m.lookupUser(ctx, username)
	if err != nil {
		return nil, err
	}
	if user == nil || !user.Enabled {
		// Spend comparable time hashing so timing does not betray existence.
		_ = bcrypt.CompareHashAndPassword(m.dummyHash, []byte(password))
		return nil, ErrInvalidCredentials
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		return nil, ErrInvalidCredentials
	}
	return m.issueTokens(user)
}

// Refresh exchanges a valid, unrevoked refresh token for a new token pair,
// rotating the refresh token (the presented one is invalidated).
func (m *Manager) Refresh(ctx context.Context, refreshToken string) (*TokenPair, error) {
	claims, err := m.validator.ValidateRefresh(refreshToken)
	if err != nil {
		return nil, ErrInvalidToken
	}
	// The token must still be present in the server-side store.
	if _, err := m.refreshKV.Get(claims.ID); err != nil {
		return nil, ErrInvalidToken
	}
	// Rotate: invalidate the presented token before minting a replacement.
	_ = m.refreshKV.Delete(claims.ID)

	// Confirm the account is still enabled (a disabled user cannot refresh).
	user, err := m.lookupUser(ctx, claims.Username)
	if err != nil {
		return nil, err
	}
	if user == nil || !user.Enabled {
		return nil, ErrInvalidToken
	}
	return m.issueTokens(user)
}

// lookupUser resolves a user by globally-unique username. This is the sole
// sanctioned use of the tenant-unscoped system context (core.WithSystemContext):
// login must find the user, and thereby the tenant, before any tenant is known.
func (m *Manager) lookupUser(ctx context.Context, username string) (*model.User, error) {
	var user model.User
	err := m.db.DB(core.WithSystemContext(ctx)).Where("username = ?", username).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// issueTokens mints an access + refresh token pair for the user and records the
// refresh token's jti in the server-side store.
func (m *Manager) issueTokens(user *model.User) (*TokenPair, error) {
	roles := []string{} // RBAC roles are a Phase 2 concern (roadmap).

	// Snapshot the issuer under the lock so a concurrent rotation cannot swap it
	// mid-issue (the access and refresh tokens are then signed by one key).
	m.mu.RLock()
	issuer := m.issuer
	m.mu.RUnlock()

	access, err := issuer.IssueAccess(user.TenantId, user.Username, roles, uuid.NewString())
	if err != nil {
		return nil, err
	}

	refreshJti := uuid.NewString()
	refresh, err := issuer.IssueRefresh(user.TenantId, user.Username, roles, refreshJti)
	if err != nil {
		return nil, err
	}
	if _, err := m.refreshKV.Put(refreshJti, []byte(user.Username)); err != nil {
		return nil, err
	}

	return &TokenPair{AccessToken: access.Token, RefreshToken: refresh.Token, ExpiresAt: access.ExpiresAt}, nil
}

// seedBootstrapAdmin creates the configured admin user on first startup (when no
// users exist), under a distributed lock so replicas seed exactly once.
func (m *Manager) seedBootstrapAdmin(ctx context.Context) error {
	return m.locker.WithLock(ctx, m.ms.FunctionalArea, func(ctx context.Context) error {
		var count int64
		if err := m.db.DB(core.WithSystemContext(ctx)).Model(&model.User{}).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return nil
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(m.bootstrap.Password), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		admin := model.User{Username: m.bootstrap.Username, Enabled: true, PasswordHash: string(hash)}
		admin.TenantId = m.bootstrap.Tenant
		if err := m.db.DB(core.WithTenant(ctx, m.bootstrap.Tenant)).Create(&admin).Error; err != nil {
			return err
		}
		log.Warn().Str("username", m.bootstrap.Username).Str("tenant", m.bootstrap.Tenant).
			Msg("Seeded bootstrap admin with the default password — CHANGE IT IMMEDIATELY.")
		return nil
	})
}
