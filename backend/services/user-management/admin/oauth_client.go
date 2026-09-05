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
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
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

// OAuthClientUpdateRequest is the data to update a client: its client_id identity is
// fixed and carried by the mutation's own argument.
//
// Every field carries the platform's three update states — absent leaves the stored
// value alone, an explicit null clears it, a value sets it.
//
// 🔴 THE TWO ALLOWLISTS CANNOT BE EMPTIED, AND THE REFUSAL IS BY NAME.
//
// RedirectUris and Scopes are `[String!]`, so the fold ALLOWS an explicit null or an
// empty list — for a list those are one request spelled two ways and both mean "empty".
// This mutation refuses the result anyway, through the same validateRedirectURIs /
// validateClientScopes the create path uses, because for an OAuth client each list
// emptied is a security control removed:
//
//   - no redirect URI means the exact-match allowlist matches nothing, so the client can
//     never complete an authorization. It is not a powerless client, it is a broken one.
//   - no scope means every token minted for it would carry an empty scope. The AS is
//     fail-closed, so today that grants nothing — but it makes the registration a claim
//     nobody can read, and the create path has never allowed it.
//
// Contrast RoleUpdateRequest.Authorities, which the create path DOES allow to be empty
// and which is therefore clearable here: the rule is that update and create agree about
// what a legal record is, not that lists are special.
type OAuthClientUpdateRequest struct {
	Name        dcgraphql.OptionalString
	Description dcgraphql.OptionalString
	// RedirectUris is spelled with the schema's casing rather than the model's
	// RedirectURIs: it is packed straight off the wire by field name.
	RedirectUris dcgraphql.OptionalStringList
	Scopes       dcgraphql.OptionalStringList
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

// UpdateOAuthClient applies a partial update to a client: an omitted field leaves the
// stored value alone, an explicit null clears it, a value sets it. Its client_id is
// fixed and named by the mutation's own argument.
//
// Both allowlists are validated as the RESULTING list rather than as what the caller
// sent — under a partial update those differ, and it is the resulting registration that
// has to be usable. Everything is decided before the first assignment, so a rejected
// list refuses the whole update rather than renaming the client and then failing.
func (s *Service) UpdateOAuthClient(ctx context.Context, clientId string, request *OAuthClientUpdateRequest) (*iam.OAuthClient, error) {
	c, err := s.loadOAuthClient(ctx, clientId)
	if err != nil {
		return nil, err
	}
	uris := request.RedirectUris.ApplyTo(c.RedirectURIs)
	if err := validateRedirectURIs(uris); err != nil {
		return nil, err
	}
	scopes := request.Scopes.ApplyTo(c.Scopes)
	if err := validateClientScopes(scopes); err != nil {
		return nil, err
	}
	c.Name = rdb.NullStrOf(request.Name.ApplyTo(dcgraphql.NullStr(c.Name)))
	c.Description = rdb.NullStrOf(request.Description.ApplyTo(dcgraphql.NullStr(c.Description)))
	c.RedirectURIs = uris
	c.Scopes = scopes
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
//
// The emptiness refusal serves the update path as well as the create one: `redirectUris:
// null` and `redirectUris: []` are one request spelled two ways, and both would leave an
// exact-match allowlist that matches nothing.
func validateRedirectURIs(uris []string) error {
	if len(uris) == 0 {
		// The field is named as the SCHEMA spells it — redirectUris, plural — so the
		// refusal points at something the caller actually sent. It read "redirectUri"
		// while the input field has always been `redirectUris`, which is a small thing
		// until a client greps the error for the field to highlight.
		return fmt.Errorf("at least one entry in redirectUris is required: an empty allowlist " +
			"matches nothing, so the client could never complete an authorization — send the " +
			"URIs it should have, or omit the field to leave them alone")
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
		return fmt.Errorf("at least one entry in scopes is required: a client registered for " +
			"none describes a permission set nobody can read — send the scopes it should " +
			"have, or omit the field to leave them alone")
	}
	for _, sc := range scopes {
		if !auth.IsSupportedScope(sc) {
			return fmt.Errorf("unknown scope %q (supported: %s)", sc, strings.Join(auth.SupportedScopes, ", "))
		}
	}
	return nil
}
