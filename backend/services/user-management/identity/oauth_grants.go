// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// AuthCodeBucket is the NATS KV bucket backing the OAuth 2.1 authorization-code
// store (ADR-047). Codes are single-use and short-lived: the bucket TTL bounds how
// long an issued code is redeemable, and redemption atomically deletes it (a
// revision-checked delete, like the refresh-token rotation) so a code cannot be
// replayed. The authorize endpoint (Slice C) writes codes here; the token endpoint
// redeems them.
const AuthCodeBucket = "dc_oauth_codes"

// AuthCodeTTL bounds how long an issued authorization code is redeemable. Kept
// short per RFC 6749 §4.1.2 ("codes MUST be short-lived") — the code is exchanged
// for tokens within seconds of issuance, so a tight window shrinks the theft
// surface without affecting the flow.
const AuthCodeTTL = 60 * time.Second

// AuthorizationCode is the grant an issued authorization code stands in for
// (ADR-047). It captures the client + PKCE binding the token endpoint re-checks
// and the identity + tenant + scope the resulting tokens are minted for. It is
// deliberately a snapshot of *identity + tenant + scope*, NOT of resolved
// authorities: the token endpoint re-resolves the tenant grant at redemption and
// caps it to Scope, so a role change in the (brief) code lifetime is honored and
// the code holds no capability set.
type AuthorizationCode struct {
	ClientId      string   `json:"client_id"`
	RedirectURI   string   `json:"redirect_uri"`
	CodeChallenge string   `json:"code_challenge"` // PKCE S256 challenge (RFC 7636)
	Email         string   `json:"email"`          // the authenticated subject
	Tenant        string   `json:"tenant"`         // the tenant pinned at authorize time
	Scope         string   `json:"scope"`          // granted scope (space-delimited)
	Audience      []string `json:"audience,omitempty"`
}

// OAuthTokens is the result of a successful OAuth grant — the RFC 6749 §5.1 token
// response fields the endpoint renders.
type OAuthTokens struct {
	AccessToken  string
	RefreshToken string
	Scope        string
	ExpiresIn    int // access-token lifetime in seconds
}

// oauthError carries an RFC 6749 §5.2 error code so the token endpoint can render
// the right JSON body + status without the grant logic knowing about HTTP.
type oauthError struct {
	Code   string // RFC 6749 error code, e.g. "invalid_grant"
	Desc   string
	Status int
}

func (e *oauthError) Error() string { return e.Code + ": " + e.Desc }

// RFC 6749 §5.2 / §4.1.2.1 error constructors used across the token endpoint.
// invalid_client (401) applies once a client authenticates: a confidential client
// that fails secret verification, or a public client that presents a secret.
func errInvalidRequest(desc string) *oauthError { return &oauthError{"invalid_request", desc, 400} }
func errInvalidGrant(desc string) *oauthError   { return &oauthError{"invalid_grant", desc, 400} }
func errInvalidScope(desc string) *oauthError   { return &oauthError{"invalid_scope", desc, 400} }
func errInvalidClient(desc string) *oauthError  { return &oauthError{"invalid_client", desc, 401} }
func errServer(desc string) *oauthError         { return &oauthError{"server_error", desc, 500} }

// AuthenticateClient enforces token-endpoint client authentication (ADR-047
// confidential-client fold-in / RFC 6749 §3.2.1). clientID is the client the
// request identifies (from HTTP Basic or the client_id form field); secret is the
// presented client secret; presented reports whether ANY secret was supplied.
//
//   - No clientID and no secret ⇒ nil: a bare public flow (e.g. a public client's
//     refresh) authenticates nothing — unchanged from before confidential clients.
//   - Unknown or disabled client, or a confidential client with a missing/wrong
//     secret ⇒ invalid_client. A dummy bcrypt compare runs on the unknown-client
//     path so it costs the same as a wrong-secret path (no client-enumeration
//     timing oracle, mirroring the login equalizer).
//   - A public client that nonetheless presents a secret ⇒ invalid_client: it has
//     no registered secret, so a presented one is a misconfiguration, not ignored.
//
// PKCE still runs in the grant regardless — client authentication is defence in
// depth on top of it, never a replacement.
func (m *Manager) AuthenticateClient(ctx context.Context, clientID, secret string, presented bool) error {
	if clientID == "" {
		if presented {
			return errInvalidClient("client_secret provided without client_id")
		}
		return nil
	}
	client, err := m.iam.OAuthClientByClientId(ctx, clientID)
	if err != nil {
		_ = bcrypt.CompareHashAndPassword(m.dummyHash, []byte(secret))
		return errInvalidClient("client authentication failed")
	}
	if e := verifyClientAuth(client, secret, presented); e != nil {
		return e
	}
	return nil
}

// verifyClientAuth is the pure client-authentication decision for an already-loaded
// client (no I/O), so it is exhaustively unit-testable. A disabled client is always
// rejected. A confidential client must present a secret that bcrypt-matches its
// hash; a public client must NOT present a secret (it has none registered).
func verifyClientAuth(client *iam.OAuthClient, secret string, presented bool) *oauthError {
	if !client.Enabled {
		return errInvalidClient("client is disabled")
	}
	if client.IsConfidential() {
		if !presented {
			return errInvalidClient("client authentication required")
		}
		if bcrypt.CompareHashAndPassword([]byte(client.SecretHash), []byte(secret)) != nil {
			return errInvalidClient("client authentication failed")
		}
		return nil
	}
	if presented {
		return errInvalidClient("public client must not present a client_secret")
	}
	return nil
}

// SaveAuthorizationCode stores a freshly issued authorization code (ADR-047). The
// code string is the KV key; Create (not Put) is used so a key collision fails
// rather than silently overwriting a live code. The bucket TTL bounds redeemability.
// Called by the authorize endpoint (Slice C).
func (m *Manager) SaveAuthorizationCode(code string, rec AuthorizationCode) error {
	if m.codesKV == nil {
		return errors.New("authorization-code store not configured")
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	_, err = m.codesKV.Create(code, b)
	return err
}

// RedeemAuthorizationCode implements the authorization_code grant (ADR-047 / RFC
// 6749 §4.1.3 + RFC 7636). It atomically claims the one-shot code, re-checks the
// client + redirect_uri + PKCE verifier against what the code was issued for,
// re-resolves the tenant grant, caps it to the granted scope, and mints an OAuth
// access + refresh pair. Every failure is a generic invalid_grant (no oracle about
// which check failed) — including a client mismatch, which stays invalid_grant so a
// leaked code gives an attacker no distinguishable "that code is live" signal (the
// inline comment below explains why it is not invalid_client).
func (m *Manager) RedeemAuthorizationCode(ctx context.Context, code, clientId, redirectURI, codeVerifier string) (*OAuthTokens, error) {
	if m.codesKV == nil {
		return nil, errServer("authorization-code store not configured")
	}
	entry, err := m.codesKV.Get(code)
	if err != nil {
		return nil, errInvalidGrant("authorization code is invalid or expired")
	}
	// Atomically claim the code: a revision-checked delete means only one redemption
	// of a given code wins, so a code cannot be replayed or raced.
	if err := m.codesKV.Delete(code, nats.LastRevision(entry.Revision())); err != nil {
		return nil, errInvalidGrant("authorization code is invalid or expired")
	}
	var rec AuthorizationCode
	if err := json.Unmarshal(entry.Value(), &rec); err != nil {
		return nil, errServer("corrupt authorization code")
	}

	// The token request's client must match the one the code was issued to, and the
	// redirect_uri must match exactly (RFC 6749 §4.1.3) — both bind the code so a
	// leaked code cannot be redeemed by another client or to another destination.
	// A client mismatch is invalid_grant (RFC 6749 §5.2: "the authorization code …
	// was issued to another client"), NOT invalid_client — public clients never
	// authenticate — and keeping it invalid_grant also denies an attacker a
	// distinguishable response that would confirm a live code / name its owner.
	if clientId != rec.ClientId {
		return nil, errInvalidGrant("authorization code was issued to another client")
	}
	if redirectURI != rec.RedirectURI {
		return nil, errInvalidGrant("redirect_uri mismatch")
	}
	if !verifyPKCE(codeVerifier, rec.CodeChallenge) {
		return nil, errInvalidGrant("PKCE verification failed")
	}

	tokens, err := m.mintScopedGrant(ctx, rec.Email, rec.Tenant, rec.Scope, rec.Scope, rec.Audience, rec.ClientId)
	if err != nil {
		return nil, err
	}
	m.recordAuth(ctx, rdb.AuditOpLogin, rec.Email, rec.Tenant)
	return tokens, nil
}

// RefreshOAuth implements the refresh_token grant for OAuth sessions (ADR-047 / RFC
// 6749 §6). It validates + single-use-rotates an OAuth refresh token (one that
// carries a scope), re-resolves the grant, and re-mints — so a role change or
// revocation takes effect on refresh. requestedScope, if non-empty, may only
// NARROW the token's scope (RFC 6749 §6); a request to widen it is rejected. An
// ordinary (scope-less) refresh token is not accepted here — it belongs to the
// non-OAuth /refresh path (symmetric to that path rejecting scoped tokens).
//
// requestClientID is the client the request authenticated as (the token endpoint
// already verified its secret via AuthenticateClient). A refresh token minted for a
// CONFIDENTIAL client may only be refreshed by that same client (RFC 6749 §6/§10.4),
// so a stolen refresh token is useless without the client secret — enforced by
// checkRefreshClientBinding BEFORE the single-use rotation consumes the token.
func (m *Manager) RefreshOAuth(ctx context.Context, refreshToken, requestedScope, requestClientID string) (*OAuthTokens, error) {
	claims, err := m.validator.ValidateRefresh(refreshToken)
	if err != nil {
		return nil, errInvalidGrant("refresh token is invalid or expired")
	}
	if claims.Scope == "" {
		return nil, errInvalidGrant("not an OAuth refresh token")
	}

	// Enforce the client binding before consuming the one-shot refresh token: look up
	// the client the token was minted for and require the confidential ones to have
	// re-authenticated as themselves.
	if boundClient := claims.ClientId; boundClient != "" {
		client, cerr := m.iam.OAuthClientByClientId(ctx, boundClient)
		found := true
		if errors.Is(cerr, gorm.ErrRecordNotFound) {
			found = false
		} else if cerr != nil {
			return nil, errServer(cerr.Error())
		}
		if berr := checkRefreshClientBinding(boundClient, requestClientID, client, found); berr != nil {
			return nil, berr
		}
	}

	scope := claims.Scope
	if requestedScope != "" {
		if !isScopeSubset(requestedScope, claims.Scope) {
			return nil, errInvalidScope("requested scope exceeds the grant")
		}
		scope = requestedScope
	}

	entry, err := m.refreshKV.Get(claims.ID)
	if err != nil {
		return nil, errInvalidGrant("refresh token is invalid or expired")
	}
	if err := m.refreshKV.Delete(claims.ID, nats.LastRevision(entry.Revision())); err != nil {
		return nil, errInvalidGrant("refresh token is invalid or expired")
	}

	// The access token carries the (possibly narrowed) scope; the rotated refresh
	// token keeps the ORIGINAL grant scope (RFC 6749 §6 — a per-request narrowing
	// bounds the access token, it does not permanently downgrade the grant), so a
	// client that narrows once does not irreversibly lose the rest of its grant. The
	// client binding is carried forward so the rotated token stays bound.
	tokens, err := m.mintScopedGrant(ctx, claims.Username, claims.Tenant, scope, claims.Scope, []string(claims.Audience), claims.ClientId)
	if err != nil {
		return nil, err
	}
	m.recordAuth(ctx, rdb.AuditOpRefresh, claims.Username, claims.Tenant)
	return tokens, nil
}

// checkRefreshClientBinding is the pure rule for whether a refresh may proceed given
// the client the token was minted for (ADR-047 confidential fold-in). An unbound
// token (no client_id claim) is unrestricted. A token whose client no longer exists
// is rejected — deleting a client kills its sessions. A CONFIDENTIAL client's token
// requires the request to have authenticated as that same client (requestClientID
// matches, its secret already verified by AuthenticateClient), so a stolen refresh
// token cannot be used without the secret. A PUBLIC client's token stays lenient (it
// has no secret to enforce, and requiring client_id would break existing public
// clients that refresh with the token alone).
func checkRefreshClientBinding(boundClientID, requestClientID string, client *iam.OAuthClient, found bool) *oauthError {
	if boundClientID == "" {
		return nil
	}
	if !found {
		return errInvalidGrant("the client this refresh token was issued to no longer exists")
	}
	// A disabled client is the kill switch: its outstanding sessions die too, whether
	// the client is confidential or public (the confidential case is also caught at
	// the token endpoint, but a bare refresh with no client_id skips that check).
	if !client.Enabled {
		return errInvalidGrant("the client this refresh token was issued to is disabled")
	}
	if client.IsConfidential() && requestClientID != boundClientID {
		return errInvalidGrant("refresh token was issued to another client")
	}
	return nil
}

// mintScopedGrant re-resolves the identity's current grant in a tenant, caps the
// effective authorities to the granted scope, and mints an OAuth access + refresh
// pair. accessScope governs the access token (and the response); refreshScope
// governs the rotated refresh token — they differ only when a refresh request
// narrows scope, where the access token narrows but the refresh keeps the original
// grant scope. Shared by both grant types so the scope cap is applied in exactly
// one place. The identity/tenant checks mirror Refresh: a disabled identity, lost
// membership, or denied tenant fails the grant.
func (m *Manager) mintScopedGrant(ctx context.Context, email, tenant, accessScope, refreshScope string, audience []string, clientID string) (*OAuthTokens, error) {
	accessAllow, err := scopeAllowance(accessScope)
	if err != nil {
		return nil, errInvalidScope(err.Error())
	}
	refreshAllow, err := scopeAllowance(refreshScope)
	if err != nil {
		return nil, errInvalidScope(err.Error())
	}
	// Defence in depth: the tenant reaches the superuser branch of
	// resolveTenantGrant with no DB lookup, so grammar-check it here (as SelectTenant
	// does) rather than trust the value carried on the code / refresh token.
	if err := core.ValidateToken(tenant); err != nil {
		return nil, errInvalidGrant("invalid tenant")
	}

	id, err := m.iam.IdentityByEmail(ctx, email)
	if err != nil {
		// A vanished identity denies the grant (invalid_grant); a transient DB error
		// is a server_error, not a policy denial — else an infra blip is reported to
		// the client as grant-revoked, killing an otherwise-valid session.
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errInvalidGrant("subject is no longer valid")
		}
		return nil, errServer(err.Error())
	}
	if !id.Enabled {
		return nil, errInvalidGrant("subject is no longer valid")
	}
	su := isSuperuser(id)
	mem := findMembership(id.Memberships, tenant)
	if mem == nil && !su {
		return nil, errInvalidGrant("subject is no longer a member of the tenant")
	}
	roles, authorities, err := m.resolveTenantGrant(ctx, tenant, mem, su)
	if err != nil {
		if errors.Is(err, errTenantAccessDenied) {
			return nil, errInvalidGrant("tenant access denied")
		}
		return nil, errServer(err.Error())
	}

	// Cap the identity's *effective* authorities (the same set the console token
	// would carry, viewer baseline included) to each token's scope allowance.
	// Intersect caps even the superuser "*" to the allowance, so an OAuth session
	// can never exceed its scope.
	effective := effectiveAuthorities(authorities, su)
	accessCapped := auth.IntersectAuthorities(effective, accessAllow)
	refreshCapped := auth.IntersectAuthorities(effective, refreshAllow)

	m.mu.RLock()
	issuer := m.issuer
	m.mu.RUnlock()

	access, err := issuer.IssueOAuthAccess(tenant, email, roles, accessCapped, accessScope, audience, su, clientID, uuid.NewString())
	if err != nil {
		return nil, errServer(err.Error())
	}
	refreshJti := uuid.NewString()
	refresh, err := issuer.IssueOAuthRefresh(tenant, email, roles, refreshCapped, refreshScope, audience, clientID, refreshJti)
	if err != nil {
		return nil, errServer(err.Error())
	}
	if _, err := m.refreshKV.Put(refreshJti, []byte(email)); err != nil {
		return nil, errServer(err.Error())
	}
	return &OAuthTokens{
		AccessToken:  access.Token,
		RefreshToken: refresh.Token,
		Scope:        accessScope,
		ExpiresIn:    int(m.accessTTL.Seconds()),
	}, nil
}

// effectiveAuthorities is the full authority set a grant would carry: the
// superuser's "*", or a member's role authorities unioned with the read-only
// viewer baseline (mirroring issueTenantTokens) — the set the scope cap intersects.
func effectiveAuthorities(authorities []string, sudo bool) []string {
	if sudo {
		return []string{string(auth.AuthorityAll)}
	}
	return unionStrings(authorities, viewerAuthorities)
}

// scopeAllowance returns the authorities a (space-delimited) scope set permits: the
// union of each member scope's allowance. An unknown scope is an error (fail-closed
// — a token is never minted for a scope with no defined allowance).
func scopeAllowance(scope string) ([]string, error) {
	var out []string
	for _, s := range auth.ParseScope(scope) {
		switch s {
		case auth.ScopeReadOnly:
			out = unionStrings(out, viewerAuthorities)
		default:
			return nil, fmt.Errorf("unknown scope %q", s)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no scope requested")
	}
	return out, nil
}

// isScopeSubset reports whether every scope in sub is present in super — the RFC
// 6749 §6 rule that a refresh may only narrow, never widen, the granted scope.
func isScopeSubset(sub, super string) bool {
	have := make(map[string]struct{})
	for _, s := range auth.ParseScope(super) {
		have[s] = struct{}{}
	}
	for _, s := range auth.ParseScope(sub) {
		if _, ok := have[s]; !ok {
			return false
		}
	}
	return true
}

// verifyPKCE checks a PKCE code_verifier against the stored S256 code_challenge
// (RFC 7636 §4.6): BASE64URL(SHA256(verifier)) must equal the challenge. Only the
// S256 method is supported (the AS advertises S256 only); the compare is
// constant-time. An empty verifier or challenge never verifies.
func verifyPKCE(verifier, challenge string) bool {
	if verifier == "" || challenge == "" {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(computed), []byte(challenge)) == 1
}
