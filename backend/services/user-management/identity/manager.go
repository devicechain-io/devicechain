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
	"errors"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
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
	accessTTL time.Duration

	issuer    *auth.Issuer
	validator *auth.Validator
	publicPEM []byte
	refreshKV nats.KeyValue
	// dummyHash equalizes login timing on the user-not-found path so response
	// time does not reveal whether a username exists.
	dummyHash  []byte
	refreshTTL time.Duration
	bootstrap  BootstrapConfig
}

// TokenPair is the result of a successful login or refresh.
type TokenPair struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

// NewManager constructs a Manager. accessTTL/refreshTTL of 0 fall back to the
// auth package defaults.
func NewManager(ms *core.Microservice, db *rdb.RdbManager, accessTTL, refreshTTL time.Duration, bootstrap BootstrapConfig) *Manager {
	return &Manager{ms: ms, db: db, accessTTL: accessTTL, refreshTTL: refreshTTL, bootstrap: bootstrap}
}

// Initialize loads (or creates) the signing key, builds the issuer/validator,
// wires the refresh-token store, and seeds the bootstrap admin. Must run after
// the RdbManager is initialized (tables exist) and the refresh KV bucket is
// created.
func (m *Manager) Initialize(ctx context.Context, refreshKV nats.KeyValue) error {
	priv, pubPEM, err := m.loadOrCreateSigningKey(ctx)
	if err != nil {
		return err
	}
	m.issuer = auth.NewIssuer(priv, fmt.Sprintf("dc-user-management:%s", m.ms.InstanceId), m.accessTTL, m.refreshTTL)
	m.validator = auth.NewValidator(&priv.PublicKey)
	m.publicPEM = pubPEM
	m.refreshKV = refreshKV

	dummy, err := bcrypt.GenerateFromPassword([]byte("dc-login-timing-equalizer"), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	m.dummyHash = dummy

	return m.seedBootstrapAdmin(ctx)
}

// Validator returns the access-token validator built from the local public key
// (user-management validates its own API requests without a network fetch).
func (m *Manager) Validator() *auth.Validator { return m.validator }

// PublicKeyPEM returns the PKIX PEM public key served to the other services.
func (m *Manager) PublicKeyPEM() []byte { return m.publicPEM }

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

	access, err := m.issuer.IssueAccess(user.TenantId, user.Username, roles, uuid.NewString())
	if err != nil {
		return nil, err
	}

	refreshJti := uuid.NewString()
	refresh, err := m.issuer.IssueRefresh(user.TenantId, user.Username, roles, refreshJti)
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
	return m.ms.WithDistributedLock(ctx, 5*time.Second, 5, func(ctx context.Context) error {
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
