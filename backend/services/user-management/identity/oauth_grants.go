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
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/kv"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// AuthCodeBucket is the NATS KV bucket backing the OAuth 2.1 authorization-code
// store (ADR-047). Codes are single-use and short-lived: the bucket TTL bounds how
// long an issued code is redeemable, and redemption atomically deletes it (a
// revision-checked delete, like the refresh-token rotation) so a code cannot be
// replayed. The authorize endpoint (Slice C) writes codes here; the token endpoint
// redeems them.
const AuthCodeBucket = kv.BucketOAuthCodes

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
//
// It does carry the subject's SESSION EPOCH at authorize time, and redemption checks
// it through sessionIdentity like every other credential exchange — so a code issued
// just before a password reset, disable or delete cannot be redeemed after it.
type AuthorizationCode struct {
	ClientId      string            `json:"client_id"`
	RedirectURI   string            `json:"redirect_uri"`
	CodeChallenge string            `json:"code_challenge"` // PKCE S256 challenge (RFC 7636)
	Email         string            `json:"email"`          // the authenticated subject
	SessionEpoch  auth.SessionEpoch `json:"sep"`            // the subject's session at authorize time
	Tenant        string            `json:"tenant"`         // the tenant pinned at authorize time
	Scope         string            `json:"scope"`          // granted scope (space-delimited)
	Audience      []string          `json:"audience,omitempty"`
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
func errInvalidRequest(desc string) *oauthError {
	return &oauthError{Code: "invalid_request", Desc: desc, Status: 400}
}
func errInvalidGrant(desc string) *oauthError {
	return &oauthError{Code: "invalid_grant", Desc: desc, Status: 400}
}
func errInvalidScope(desc string) *oauthError {
	return &oauthError{Code: "invalid_scope", Desc: desc, Status: 400}
}
func errInvalidClient(desc string) *oauthError {
	return &oauthError{Code: "invalid_client", Desc: desc, Status: 401}
}
func errServer(desc string) *oauthError {
	return &oauthError{Code: "server_error", Desc: desc, Status: 500}
}

// AuthenticateClient enforces token-endpoint client authentication (ADR-047
// confidential-client fold-in / RFC 6749 §3.2.1). clientID is the client the
// request identifies (from HTTP Basic or the client_id form field); secret is the
// presented client secret; presented reports whether ANY secret was supplied.
//
//   - No clientID and no secret ⇒ nil: a bare public flow (e.g. a public client's
//     refresh) authenticates nothing — unchanged from before confidential clients.
//   - Unknown client ⇒ invalid_client, after a compare against a dummy hash so it
//     costs the same as a known confidential client's wrong secret.
//   - Disabled client, or a confidential client presenting no secret ⇒
//     invalid_client, with no compare at all.
//   - A confidential client presenting a secret ⇒ the secret is compared through the
//     credential checker. Client secrets are NOT throttled (CredentialPolicies says
//     why), so a client's own failures never delay its next request.
//   - A public client that nonetheless presents a secret ⇒ invalid_client: it has
//     no registered secret, so a presented one is a misconfiguration, not ignored.
//   - A database error loading the client is returned as-is, which the token
//     endpoint renders as server_error: a failed lookup is not a verdict on the
//     client.
//
// A KNOWN PUBLIC CLIENT never reaches the checker: it has no secret to compare. The
// client is therefore LOADED before the checker is consulted, and only the unknown and
// the confidential-with-a-secret cases reach it.
//
// ⚠️ TIMING IS NOT EQUALIZED ACROSS ALL OF THESE, and the claim is scoped on purpose:
// a known public or disabled client answers without a compare, an unknown one pays
// for one, so response time distinguishes "known public/disabled" from "unknown".
// That was true before the checker as well. It is low value — client_ids are not
// secret — and closing it would mean paying a compare for public clients too.
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
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	var hash string
	if client != nil {
		checkSecret, e := verifyClientAuth(client, presented)
		if e != nil {
			return e
		}
		if !checkSecret {
			return nil
		}
		hash = client.SecretHash
	}
	return m.checkClientSecret(ctx, clientID, secret, hash)
}

// checkClientSecret runs the secret compare through the credential checker. hash is
// the confidential client's stored hash, or "" for an unknown client — which the
// checker compares against a dummy at the same cost, and which never matches.
//
// Only a match and a mismatch are expected, because the client-secret kind is
// Unthrottled. Any other error — a throttle or an unavailable store, should the policy
// ever change without this function — is returned as-is and rendered as server_error:
// loud, rather than mistaken for a wrong secret.
func (m *Manager) checkClientSecret(ctx context.Context, clientID, secret, hash string) error {
	if m.credentials == nil {
		return errNoCredentialChecker
	}
	err := m.credentials.Check(ctx, credential.Principal{Kind: credential.KindOAuthClient, ID: clientID}, secret,
		func(context.Context) (string, error) { return hash, nil })
	switch {
	case err == nil:
		return nil
	case errors.Is(err, credential.ErrMismatch):
		return errInvalidClient("client authentication failed")
	default:
		return err
	}
}

// verifyClientAuth is the pure client-authentication decision for an already-loaded
// client (no I/O), so it is exhaustively unit-testable. It decides everything EXCEPT
// the secret compare, and reports whether that compare is needed:
//
//   - a disabled client is always rejected;
//   - a confidential client must present a secret, and checkSecret is true — the
//     caller then compares it through the credential checker;
//   - a public client must NOT present a secret (it has none registered), and
//     checkSecret is false.
func verifyClientAuth(client *iam.OAuthClient, presented bool) (checkSecret bool, _ *oauthError) {
	if !client.Enabled {
		return false, errInvalidClient("client is disabled")
	}
	if client.IsConfidential() {
		if !presented {
			return false, errInvalidClient("client authentication required")
		}
		return true, nil
	}
	if presented {
		return false, errInvalidClient("public client must not present a client_secret")
	}
	return false, nil
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

	tokens, err := m.mintScopedGrant(ctx, rec.Email, rec.SessionEpoch, rec.Tenant, rec.Scope, rec.Scope, rec.Audience, rec.ClientId)
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
			log.Error().Err(cerr).Msg("OAuth refresh could not load the client its token is bound to; the token is left unconsumed.")
			return nil, errServer(errGrantUnavailable)
		}
		if berr := checkRefreshClientBinding(boundClient, requestClientID, claims.Scope, client, found); berr != nil {
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
		if errors.Is(err, nats.ErrKeyNotFound) {
			return nil, errInvalidGrant("refresh token is invalid or expired")
		}
		log.Error().Err(err).Msg("OAuth refresh could not read the refresh-token store; the token is left unconsumed.")
		return nil, errServer(errGrantUnavailable)
	}
	rev := entry.Revision()

	// Every read BEFORE the token is consumed, as in Refresh: a store error leaves the
	// token redeemable (server_error, retry the same token), and a definite denial burns
	// it so it cannot revive if the membership or tenant is re-enabled before it expires.
	//
	// The access token carries the (possibly narrowed) scope; the rotated refresh
	// token keeps the ORIGINAL grant scope (RFC 6749 §6 — a per-request narrowing
	// bounds the access token, it does not permanently downgrade the grant), so a
	// client that narrows once does not irreversibly lose the rest of its grant. The
	// client binding is carried forward so the rotated token stays bound.
	grant, gerr := m.resolveScopedGrant(ctx, claims.Username, claims.SessionEpoch, claims.Tenant, scope, claims.Scope)
	if gerr != nil {
		if gerr.Code != "server_error" {
			// Revision-checked, so it only ever deletes the token this refresh read.
			if err := m.refreshKV.Delete(claims.ID, nats.LastRevision(rev)); err != nil && !errors.Is(err, nats.ErrKeyRevisionMismatch) {
				log.Warn().Err(err).Msg("A denied OAuth refresh could not burn its token; it stays denied at every later use.")
			}
		}
		return nil, gerr
	}
	if err := m.refreshKV.Delete(claims.ID, nats.LastRevision(rev)); err != nil {
		if errors.Is(err, nats.ErrKeyRevisionMismatch) {
			return nil, errInvalidGrant("refresh token is invalid or expired")
		}
		log.Error().Err(err).Msg("OAuth refresh could not claim the refresh token; it is left unconsumed.")
		return nil, errServer(errGrantUnavailable)
	}

	tokens, err := m.mintScoped(grant, []string(claims.Audience), claims.ClientId)
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
//
// 🔴 IT ALSO RE-CHECKS THE BOUND SCOPE AGAINST THE REGISTRATION, AND THAT IS THE HALF
// THAT WAS MISSING. scopesRegistered ran only at /authorize, so narrowing a client's
// registered scopes had no effect on any session already holding a refresh token: the
// scope rode the signed token forever and rotation renewed its TTL each time. The
// asymmetry made it worse than a plain gap — revoking the ROLE that grants an authority
// took effect on the very next refresh, because mintScopedGrant re-resolves roles, so
// an operator watching one lever work would reasonably assume the other did too. It did
// not, and the only kill switches that actually worked were disabling the client or the
// identity.
//
// It is invalid_grant rather than invalid_scope on purpose. A refresh request need not
// name a scope at all, so the fault is not in what was asked for — the grant this token
// represents has been partially revoked, which is the same category as the disabled and
// deleted cases immediately below, and the same remedy: re-authorize.
func checkRefreshClientBinding(boundClientID, requestClientID, boundScope string, client *iam.OAuthClient, found bool) *oauthError {
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
	if !scopesRegistered(client.Scopes, boundScope) {
		return errInvalidGrant("the client this refresh token was issued to is no longer registered for its scope")
	}
	return nil
}

// mintScopedGrant re-resolves the identity's current grant in a tenant, caps the
// effective authorities to the granted scope, and mints an OAuth access + refresh
// pair. accessScope governs the access token (and the response); refreshScope
// governs the rotated refresh token — they differ only when a refresh request
// narrows scope, where the access token narrows but the refresh keeps the original
// grant scope. Shared by both grant types so the scope cap is applied in exactly
// one place. The identity is resolved through sessionIdentity with the epoch the
// code or refresh token carried, exactly as Refresh does: a deleted or disabled
// identity, or one whose session has ended since (a password reset), fails the
// grant, as do a lost membership and a denied tenant.
func (m *Manager) mintScopedGrant(ctx context.Context, email string, epoch auth.SessionEpoch, tenant, accessScope, refreshScope string, audience []string, clientID string) (*OAuthTokens, error) {
	grant, gerr := m.resolveScopedGrant(ctx, email, epoch, tenant, accessScope, refreshScope)
	if gerr != nil {
		return nil, gerr
	}
	return m.mintScoped(grant, audience, clientID)
}

// errGrantUnavailable is the server_error description when a grant could not be re-checked
// because a store failed. It is fixed text: the cause is logged, never sent to the client.
const errGrantUnavailable = "the grant could not be checked right now; try again"

// scopedGrant is a re-resolved OAuth grant, ready to mint: everything mintScoped needs, and
// nothing it has to read.
type scopedGrant struct {
	email, tenant             string
	epoch                     auth.SessionEpoch
	roles                     []string
	accessCapped, refreshCaps []string
	accessScope, refreshScope string
	su                        bool
}

// resolveScopedGrant is the READ half of mintScopedGrant: it re-resolves the identity's
// grant in the tenant and caps its authorities to each scope, touching no token. It is
// split out so RefreshOAuth can do every read before it consumes the refresh token. A
// policy refusal is invalid_grant or invalid_scope; a store error is a server_error with
// fixed text (errGrantUnavailable), its cause logged rather than returned.
func (m *Manager) resolveScopedGrant(ctx context.Context, email string, epoch auth.SessionEpoch, tenant, accessScope, refreshScope string) (scopedGrant, *oauthError) {
	accessAllow, err := scopeAllowance(accessScope)
	if err != nil {
		return scopedGrant{}, errInvalidScope(err.Error())
	}
	refreshAllow, err := scopeAllowance(refreshScope)
	if err != nil {
		return scopedGrant{}, errInvalidScope(err.Error())
	}
	// Defence in depth: the tenant reaches the superuser branch of
	// resolveTenantGrant with no DB lookup, so grammar-check it here (as SelectTenant
	// does) rather than trust the value carried on the code / refresh token.
	if err := core.ValidateToken(tenant); err != nil {
		return scopedGrant{}, errInvalidGrant("invalid tenant")
	}

	id, err := m.sessionIdentity(ctx, email, epoch)
	if err != nil {
		// An ended session — vanished, disabled, or re-keyed identity — denies the
		// grant (invalid_grant); a transient DB error is a server_error, not a policy
		// denial — else an infra blip is reported to the client as grant-revoked,
		// killing an otherwise-valid session.
		if errors.Is(err, errSessionEnded) {
			return scopedGrant{}, errInvalidGrant("subject is no longer valid")
		}
		log.Error().Err(err).Msg("Could not load the identity to re-check an OAuth grant.")
		return scopedGrant{}, errServer(errGrantUnavailable)
	}
	su := isSuperuser(id)
	mem := findMembership(id.Memberships, tenant)
	if mem == nil && !su {
		return scopedGrant{}, errInvalidGrant("subject is no longer a member of the tenant")
	}
	roles, authorities, err := m.resolveTenantGrant(ctx, tenant, mem, su)
	if err != nil {
		if errors.Is(err, errTenantAccessDenied) {
			return scopedGrant{}, errInvalidGrant("tenant access denied")
		}
		log.Error().Err(err).Msg("Could not load the tenant to re-check an OAuth grant.")
		return scopedGrant{}, errServer(errGrantUnavailable)
	}

	// Cap the identity's *effective* authorities (the same set the console token
	// would carry, viewer baseline included) to each token's scope allowance.
	// Intersect caps even the superuser "*" to the allowance, so an OAuth session
	// can never exceed its scope.
	return scopedGrant{
		email: email, tenant: tenant, epoch: auth.SessionEpoch(id.SessionEpoch), roles: roles,
		accessCapped: capToScope(authorities, su, accessAllow),
		refreshCaps:  capToScope(authorities, su, refreshAllow),
		accessScope:  accessScope, refreshScope: refreshScope, su: su,
	}, nil
}

// mintScoped is the WRITE half of mintScopedGrant: it issues the OAuth access + refresh pair
// for a resolved grant and records the new refresh jti.
func (m *Manager) mintScoped(g scopedGrant, audience []string, clientID string) (*OAuthTokens, error) {
	m.mu.RLock()
	issuer := m.issuer
	m.mu.RUnlock()

	access, err := issuer.IssueOAuthAccess(g.tenant, g.email, g.roles, g.accessCapped, g.accessScope, audience, g.su, clientID, uuid.NewString())
	if err != nil {
		return nil, errServer(err.Error())
	}
	refreshJti := uuid.NewString()
	refresh, err := issuer.IssueOAuthRefresh(g.tenant, g.email, g.epoch, g.roles, g.refreshCaps, g.refreshScope, audience, clientID, refreshJti)
	if err != nil {
		return nil, errServer(err.Error())
	}
	if _, err := m.refreshKV.Put(refreshJti, []byte(g.email)); err != nil {
		return nil, errServer(err.Error())
	}
	return &OAuthTokens{
		AccessToken:  access.Token,
		RefreshToken: refresh.Token,
		Scope:        g.accessScope,
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

// capToScope is the whole scope cap in one place: a grant's effective authorities
// (the set a console token would carry, viewer baseline included, or the superuser's
// "*") intersected with a scope allowance. IntersectAuthorities caps "*" too, so the
// result never exceeds allow and never exceeds what the subject holds — the allowance
// is a ceiling, the roles are the grant, and a token carries the smaller of the two.
func capToScope(authorities []string, sudo bool, allow []string) []string {
	return auth.IntersectAuthorities(effectiveAuthorities(authorities, sudo), allow)
}

// scopeAllowance returns the authorities a (space-delimited) scope set permits: the
// union of each member scope's ceiling, read from the one table in core/auth. An
// unknown scope is an error (fail-closed — a token is never minted for a scope with
// no defined allowance), which is also what keeps a multi-scope request honest: a
// client asking for "read-only location" gets the union of both ceilings, and a
// client asking for "read-only" gets a token that cannot reach position however the
// subject's roles are configured.
//
// 🔴 A ceiling, not a grant. IntersectAuthorities emits only what the subject holds,
// so requesting a scope grants nothing on its own — asking for `location` when no
// role gave you location:read yields a token without it.
func scopeAllowance(scope string) ([]string, error) {
	var out []string
	for _, s := range auth.ParseScope(scope) {
		allow, ok := auth.ScopeAllowance(s)
		if !ok {
			return nil, fmt.Errorf("unknown scope %q", s)
		}
		out = unionStrings(out, allow)
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
