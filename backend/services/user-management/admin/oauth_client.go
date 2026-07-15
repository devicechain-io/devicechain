// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/iam"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// ErrOAuthClientNotFound is the sentinel the resolver layer maps to a not-found
// result for a missing client_id.
var ErrOAuthClientNotFound = errors.New("oauth client not found")

// clientSecretBytes is the entropy of a generated confidential-client secret: 32
// random bytes → 43 base64url chars, comfortably under bcrypt's 72-byte input cap.
const clientSecretBytes = 32

// OAuthClientInput is the data to register an OAuth 2.1 client (ADR-047). ClientId
// is its stable public identifier; RedirectURIs is the exact-match allowlist;
// Scopes is the set it may request (each a supported scope). Confidential requests
// a client secret (server-generated, returned once) so the client authenticates at
// the token endpoint; false registers a public PKCE client (the MCP default).
type OAuthClientInput struct {
	ClientId     string
	Name         string
	Description  string
	RedirectURIs []string
	Scopes       []string
	Confidential bool
}

// OAuthClientMutableInput is the data to update a client: its client_id identity
// is fixed; only the name/description, redirect URIs, and scopes change.
type OAuthClientMutableInput struct {
	Name         string
	Description  string
	RedirectURIs []string
	Scopes       []string
}

// ListOAuthClients returns the client registry (ADR-047).
func (s *Service) ListOAuthClients(ctx context.Context) ([]iam.OAuthClient, error) {
	return s.iam.ListOAuthClients(ctx)
}

// CreateOAuthClient registers a client after validating its id, redirect URIs, and
// scopes. Enabled on creation. When in.Confidential is set, a secret is generated,
// bcrypt-hashed onto the row, and returned as the second result — the ONLY time the
// cleartext exists outside the caller; it is never stored or returned again. A
// public client returns an empty secret.
func (s *Service) CreateOAuthClient(ctx context.Context, in OAuthClientInput) (*iam.OAuthClient, string, error) {
	if err := validateClientId(in.ClientId); err != nil {
		return nil, "", err
	}
	if err := validateRedirectURIs(in.RedirectURIs); err != nil {
		return nil, "", err
	}
	if err := validateClientScopes(in.Scopes); err != nil {
		return nil, "", err
	}
	var secret, hash string
	if in.Confidential {
		var err error
		if secret, hash, err = generateClientSecret(); err != nil {
			return nil, "", err
		}
	}
	c := &iam.OAuthClient{
		ClientId: in.ClientId, RedirectURIs: in.RedirectURIs, Scopes: in.Scopes, Enabled: true,
		SecretHash:  hash,
		NamedEntity: rdb.NamedEntity{Name: rdb.NullStrOf(&in.Name), Description: rdb.NullStrOf(&in.Description)},
	}
	if err := s.iam.CreateOAuthClient(ctx, c); err != nil {
		return nil, "", err
	}
	out, err := s.iam.OAuthClientByClientId(ctx, in.ClientId)
	return out, secret, err
}

// RotateOAuthClientSecret mints a fresh secret for a client, replacing any existing
// hash (which immediately invalidates the previous secret), and returns the new
// cleartext once. It also promotes a public client to confidential. The cleartext is
// never stored or returned again.
func (s *Service) RotateOAuthClientSecret(ctx context.Context, clientId string) (*iam.OAuthClient, string, error) {
	c, err := s.loadOAuthClient(ctx, clientId)
	if err != nil {
		return nil, "", err
	}
	secret, hash, err := generateClientSecret()
	if err != nil {
		return nil, "", err
	}
	if err := s.iam.SetOAuthClientSecretHash(ctx, c, hash); err != nil {
		return nil, "", err
	}
	out, err := s.iam.OAuthClientByClientId(ctx, clientId)
	return out, secret, err
}

// generateClientSecret returns a fresh high-entropy client secret (base64url so it
// drops cleanly into a config file) alongside its bcrypt hash. Only the hash is
// persisted; the cleartext is shown to the operator once.
func generateClientSecret() (secret, hash string, err error) {
	buf := make([]byte, clientSecretBytes)
	if _, err = rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("generate client secret: %w", err)
	}
	secret = base64.RawURLEncoding.EncodeToString(buf)
	h, err := bcrypt.GenerateFromPassword([]byte(secret), bcrypt.DefaultCost)
	if err != nil {
		return "", "", fmt.Errorf("hash client secret: %w", err)
	}
	return secret, string(h), nil
}

// UpdateOAuthClient replaces a client's mutable fields (name/description, redirect
// URIs, scopes). Its client_id is fixed.
func (s *Service) UpdateOAuthClient(ctx context.Context, clientId string, in OAuthClientMutableInput) (*iam.OAuthClient, error) {
	if err := validateRedirectURIs(in.RedirectURIs); err != nil {
		return nil, err
	}
	if err := validateClientScopes(in.Scopes); err != nil {
		return nil, err
	}
	c, err := s.loadOAuthClient(ctx, clientId)
	if err != nil {
		return nil, err
	}
	c.Name = rdb.NullStrOf(&in.Name)
	c.Description = rdb.NullStrOf(&in.Description)
	c.RedirectURIs = in.RedirectURIs
	c.Scopes = in.Scopes
	if err := s.iam.UpdateOAuthClient(ctx, c); err != nil {
		return nil, err
	}
	return s.iam.OAuthClientByClientId(ctx, clientId)
}

// SetOAuthClientEnabled flips a client's enabled flag. A disabled client is
// rejected at the authorize/token endpoints, so this is the kill switch for a
// compromised or retired client without deleting its registration.
func (s *Service) SetOAuthClientEnabled(ctx context.Context, clientId string, enabled bool) (*iam.OAuthClient, error) {
	c, err := s.loadOAuthClient(ctx, clientId)
	if err != nil {
		return nil, err
	}
	if err := s.iam.SetOAuthClientEnabled(ctx, c, enabled); err != nil {
		return nil, err
	}
	return s.iam.OAuthClientByClientId(ctx, clientId)
}

// DeleteOAuthClient removes a client registration. Idempotent: a missing client
// returns (false, nil).
func (s *Service) DeleteOAuthClient(ctx context.Context, clientId string) (bool, error) {
	c, err := s.iam.OAuthClientByClientId(ctx, clientId)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := s.iam.DeleteOAuthClient(ctx, c); err != nil {
		return false, err
	}
	return true, nil
}

// loadOAuthClient fetches a client by client_id, translating gorm's not-found into
// the package sentinel.
func (s *Service) loadOAuthClient(ctx context.Context, clientId string) (*iam.OAuthClient, error) {
	c, err := s.iam.OAuthClientByClientId(ctx, clientId)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrOAuthClientNotFound
	}
	return c, err
}

// validateClientId enforces a safe, bounded client_id: non-empty, at most 128
// chars, and drawn from an unambiguous URL/config-safe charset so it can appear in
// query strings and config files without escaping surprises.
func validateClientId(id string) error {
	if id == "" {
		return fmt.Errorf("clientId is required")
	}
	if len(id) > 128 {
		return fmt.Errorf("clientId must be at most 128 characters")
	}
	for _, r := range id {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '-' || r == '_' || r == '.'
		if !ok {
			return fmt.Errorf("clientId may contain only letters, digits, '-', '_', '.' (got %q)", id)
		}
	}
	return nil
}

// validateRedirectURIs requires at least one redirect URI and validates each
// against the OAuth 2.1 rules (https, or http for loopback; no fragment).
func validateRedirectURIs(uris []string) error {
	if len(uris) == 0 {
		return fmt.Errorf("at least one redirectUri is required")
	}
	for _, u := range uris {
		if err := auth.ValidateRedirectURI(u); err != nil {
			return fmt.Errorf("redirectUri: %w", err)
		}
	}
	return nil
}

// validateClientScopes requires at least one scope and rejects any the AS does not
// grant (fail-closed) — a client cannot be registered for a scope that does not
// exist.
func validateClientScopes(scopes []string) error {
	if len(scopes) == 0 {
		return fmt.Errorf("at least one scope is required")
	}
	for _, sc := range scopes {
		if !auth.IsSupportedScope(sc) {
			return fmt.Errorf("unknown scope %q (supported: %s)", sc, strings.Join(auth.SupportedScopes, ", "))
		}
	}
	return nil
}
