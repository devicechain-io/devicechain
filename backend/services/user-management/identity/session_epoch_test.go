// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/credential/credentialtest"
	dctest "github.com/devicechain-io/dc-microservice/test"
	"github.com/devicechain-io/dc-user-management/admin"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// A password reset, a disable, or a delete ends every session — driven end to end.
//
// Everything here goes through the real paths a client reaches: the admin Service's
// own mutations change the credential, the Manager's Login / SelectTenant / Refresh /
// IssueAuthorizationCode / RedeemAuthorizationCode / RefreshOAuth consume it, the
// tokens are signed and read back by a real RSA issuer and validator, and the
// refresh-token and authorization-code stores are REAL JetStream KV buckets — so the
// revision-checked single-use rotation behaves exactly as it does in production,
// rather than as a stub was written to behave.
//
// Every refusal below is paired with a control that shows the SAME credential, or one
// minted the same way, succeeding before the change. Without it a refusal proves
// nothing: a path broken for some unrelated reason refuses too.

const (
	sessClient   = "mcp-desktop"
	sessRedirect = "https://example.invalid/cb"
	sessVerifier = "a-pkce-verifier-long-enough-to-be-realistic-0123456789"
)

type sessionEnv struct {
	*mintTestEnv
	admin *admin.Service
	key   *rsa.PrivateKey
}

// jetStreamKV starts an in-process JetStream server and returns two KV buckets on it:
// the refresh-token store and the authorization-code store.
func jetStreamKV(t *testing.T) (refresh, codes nats.KeyValue) {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, ServerName: "session-epoch-test",
		JetStream: true, StoreDir: dctest.JetStreamStoreDir(t),
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(10*time.Second), "the test broker never became ready")
	t.Cleanup(srv.Shutdown)

	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)
	refresh, err = js.CreateKeyValue(&nats.KeyValueConfig{Bucket: "refresh-tokens"})
	require.NoError(t, err)
	codes, err = js.CreateKeyValue(&nats.KeyValueConfig{Bucket: "oauth-codes"})
	require.NoError(t, err)
	return refresh, codes
}

func newSessionEnv(t *testing.T) *sessionEnv {
	t.Helper()
	base := newMintTestEnv(t)

	// A key this test holds, so it can also sign the one kind of token the issuer now
	// refuses to mint: one with no session epoch at all, as every token issued before
	// the epoch existed is.
	key, err := auth.GenerateKeyPair()
	require.NoError(t, err)
	base.m.issuer = auth.NewIssuer(key, "https://as.example.com", time.Minute, time.Hour)
	base.m.validator = auth.NewValidator(&key.PublicKey)
	base.validator = base.m.validator

	refresh, codes := jetStreamKV(t)
	base.m.refreshKV = refresh
	base.m.codesKV = codes
	// Login compares through the credential checker, which owns the timing-equalizing
	// dummy compare. This test is about sessions, not the backoff, so its policy is
	// generous enough that no sequence of sign-ins here is ever throttled.
	generous := credential.Policy{Free: 1000, Base: time.Second, Cap: time.Minute}
	checker, err := credential.NewChecker(credentialtest.NewStore(), map[credential.Kind]credential.Policy{
		credential.KindIdentity: generous, credential.KindOAuthClient: generous,
	})
	require.NoError(t, err)
	base.m.credentials = checker

	base.seedTenant(t, "acme")
	require.NoError(t, base.store.CreateOAuthClient(context.Background(), &iam.OAuthClient{
		ClientId: sessClient, RedirectURIs: []string{sessRedirect},
		Scopes: []string{auth.ScopeReadOnly}, Enabled: true,
	}))
	return &sessionEnv{mintTestEnv: base, admin: admin.NewService(base.store, 0, 0, nil), key: key}
}

// createMember creates an identity through the admin Service — the same path an
// operator uses — with a membership in acme.
func (e *sessionEnv) createMember(t *testing.T, email, password string) {
	t.Helper()
	ctx := context.Background()
	_, err := e.admin.CreateIdentity(ctx, admin.CreateIdentityInput{Email: email, Password: password, Enabled: true})
	require.NoError(t, err)
	_, err = e.admin.AddMembership(ctx, email, "acme", nil)
	require.NoError(t, err)
}

func (e *sessionEnv) db() *gorm.DB { return e.m.db.Database }

func (e *sessionEnv) rowEpoch(t *testing.T, email string) auth.SessionEpoch {
	t.Helper()
	id, err := e.store.IdentityByEmail(context.Background(), email)
	require.NoError(t, err)
	return auth.SessionEpoch(id.SessionEpoch)
}

// login signs in and enters acme: the console's two steps.
func (e *sessionEnv) login(t *testing.T, email, password string) (identityToken string, pair *TokenPair) {
	t.Helper()
	ctx := context.Background()
	ia, err := e.m.Login(ctx, email, password)
	require.NoError(t, err)
	pair, err = e.m.SelectTenant(ctx, ia.IdentityToken, "acme")
	require.NoError(t, err)
	return ia.IdentityToken, pair
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// issueCode drives the consent step's two calls with a real identity token.
func (e *sessionEnv) issueCode(t *testing.T, identityToken string) (string, error) {
	t.Helper()
	ctx := context.Background()
	client, err := e.m.ResolveAuthorizeClient(ctx, sessClient, sessRedirect)
	require.NoError(t, err)
	subject, err := e.m.IdentitySubject(identityToken)
	require.NoError(t, err)
	return e.m.IssueAuthorizationCode(ctx, client, AuthorizeParams{
		ResponseType: "code", ClientID: sessClient, RedirectURI: sessRedirect,
		Scope: auth.ScopeReadOnly, CodeChallenge: pkceChallenge(sessVerifier), CodeChallengeMethod: "S256",
	}, subject, "acme")
}

func (e *sessionEnv) redeem(code string) (*OAuthTokens, error) {
	return e.m.RedeemAuthorizationCode(context.Background(), code, sessClient, sessRedirect, sessVerifier)
}

// oauthSession runs the whole authorize → token flow and returns the tokens.
func (e *sessionEnv) oauthSession(t *testing.T, identityToken string) *OAuthTokens {
	t.Helper()
	code, err := e.issueCode(t, identityToken)
	require.NoError(t, err)
	toks, err := e.redeem(code)
	require.NoError(t, err)
	return toks
}

func (e *sessionEnv) refreshOAuth(token string) (*OAuthTokens, error) {
	return e.m.RefreshOAuth(context.Background(), token, "", sessClient)
}

// forge signs a token with the instance key directly, bypassing the issuer's refusal
// of an empty epoch. With register set, its jti is stored in the refresh KV the way
// a real mint stores it, so the ONLY thing that can refuse it is the epoch check.
func (e *sessionEnv) forge(t *testing.T, claims auth.Claims, register bool) string {
	t.Helper()
	now := time.Now()
	claims.ID = uuid.NewString()
	claims.Subject = claims.Username
	claims.IssuedAt = jwt.NewNumericDate(now)
	claims.NotBefore = jwt.NewNumericDate(now)
	claims.ExpiresAt = jwt.NewNumericDate(now.Add(time.Hour))
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, &claims)
	tok.Header["kid"] = auth.Thumbprint(&e.key.PublicKey)
	signed, err := tok.SignedString(e.key)
	require.NoError(t, err)
	if register {
		_, err := e.m.refreshKV.Put(claims.ID, []byte(claims.Email))
		require.NoError(t, err)
	}
	return signed
}

func tenantRefreshClaims(email string, epoch auth.SessionEpoch) auth.Claims {
	return auth.Claims{Tenant: "acme", Username: email, Email: email, SessionEpoch: epoch, TokenType: auth.TokenTypeRefresh}
}

func oauthRefreshClaims(email string, epoch auth.SessionEpoch) auth.Claims {
	c := tenantRefreshClaims(email, epoch)
	c.Scope = auth.ScopeReadOnly
	c.ClientId = sessClient
	return c
}

func identityClaims(email string, epoch auth.SessionEpoch) auth.Claims {
	return auth.Claims{Username: email, Email: email, SessionEpoch: epoch, TokenType: auth.TokenTypeIdentity}
}

// The counterweight to everything else in this file: with no credential change, a
// session keeps working across generations, and each rotated refresh token carries
// the identity's current epoch forward.
func TestAnOrdinaryRefreshCarriesTheEpochForward(t *testing.T) {
	e := newSessionEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	idTok, pair := e.login(t, "pat@example.com", "pw-1")
	want := e.rowEpoch(t, "pat@example.com")
	require.NotEmpty(t, want)

	idClaims, err := e.validator.ValidateIdentity(idTok)
	require.NoError(t, err)
	require.Equal(t, want, idClaims.SessionEpoch, "the identity token must carry the row's epoch")

	gen2, err := e.m.Refresh(context.Background(), pair.RefreshToken)
	require.NoError(t, err)
	c, err := e.validator.ValidateRefresh(gen2.RefreshToken)
	require.NoError(t, err)
	require.Equal(t, want, c.SessionEpoch, "the rotated refresh token must carry the row's epoch")

	_, err = e.m.Refresh(context.Background(), gen2.RefreshToken)
	require.NoError(t, err, "a second-generation refresh must succeed")
	_, err = e.m.Refresh(context.Background(), gen2.RefreshToken)
	require.ErrorIs(t, err, ErrInvalidToken, "a rotated refresh token is single-use")

	// And the OAuth path the same way.
	toks := e.oauthSession(t, idTok)
	oc, err := e.validator.ValidateRefresh(toks.RefreshToken)
	require.NoError(t, err)
	require.Equal(t, want, oc.SessionEpoch)
	renewed, err := e.refreshOAuth(toks.RefreshToken)
	require.NoError(t, err)
	_, err = e.refreshOAuth(renewed.RefreshToken)
	require.NoError(t, err, "a second-generation OAuth refresh must succeed")
}

// 🔴 THE DEFECT. An admin password reset left every refresh token already issued able
// to keep rotating. Now it ends the tenant session, and the identity token cannot be
// exchanged for a new one either — while signing in with the NEW password still works.
func TestAPasswordResetEndsEveryTenantSession(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	e.createMember(t, "pat@example.com", "pw-1")
	idTok, pair := e.login(t, "pat@example.com", "pw-1")
	_, control := e.login(t, "pat@example.com", "pw-1")

	_, err := e.m.Refresh(ctx, control.RefreshToken)
	require.NoError(t, err, "control: before the reset a refresh token of this session works")
	_, err = e.m.Memberships(ctx, idTok)
	require.NoError(t, err, "control: before the reset the identity token lists memberships")

	_, err = e.admin.SetPassword(ctx, "pat@example.com", "pw-2")
	require.NoError(t, err)

	_, err = e.m.Refresh(ctx, pair.RefreshToken)
	require.ErrorIs(t, err, ErrInvalidToken, "a refresh token issued before the reset still rotated")
	_, err = e.m.SelectTenant(ctx, idTok, "acme")
	require.ErrorIs(t, err, ErrInvalidToken,
		"an identity token issued before the reset was exchanged for a fresh refresh chain")
	_, err = e.m.Memberships(ctx, idTok)
	require.ErrorIs(t, err, ErrInvalidToken)

	_, err = e.m.Login(ctx, "pat@example.com", "pw-1")
	require.ErrorIs(t, err, ErrInvalidCredentials, "the old password must no longer sign in")
	_, fresh := e.login(t, "pat@example.com", "pw-2")
	_, err = e.m.Refresh(ctx, fresh.RefreshToken)
	require.NoError(t, err, "a session started after the reset must work")
}

// The same reset ends the OAuth sessions: a refresh token, a code issued before the
// reset and redeemed after it, and the consent step with a pre-reset identity token.
func TestAPasswordResetEndsEveryOAuthSession(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	e.createMember(t, "pat@example.com", "pw-1")
	idTok, _ := e.login(t, "pat@example.com", "pw-1")

	session := e.oauthSession(t, idTok)
	control := e.oauthSession(t, idTok)
	_, err := e.refreshOAuth(control.RefreshToken)
	require.NoError(t, err, "control: before the reset an OAuth refresh works")
	pendingCode, err := e.issueCode(t, idTok)
	require.NoError(t, err, "control: before the reset the consent step issues a code")

	_, err = e.admin.SetPassword(ctx, "pat@example.com", "pw-2")
	require.NoError(t, err)

	_, err = e.refreshOAuth(session.RefreshToken)
	assertOAuthErrorCode(t, err, "invalid_grant")

	_, err = e.redeem(pendingCode)
	assertOAuthErrorCode(t, err, "invalid_grant")

	_, err = e.issueCode(t, idTok)
	require.ErrorIs(t, err, ErrInvalidToken,
		"the consent step issued a code for an identity token minted before the reset")

	newID, _ := e.login(t, "pat@example.com", "pw-2")
	fresh := e.oauthSession(t, newID)
	_, err = e.refreshOAuth(fresh.RefreshToken)
	require.NoError(t, err, "an OAuth session started after the reset must work")
}

// Refresh tokens are keyed by EMAIL, and the store's jtis outlive a delete. Before
// the epoch, deleting an identity and creating it again under the same email handed
// the new person every refresh token the old one still held.
func TestDeletingAndRecreatingAnEmailDoesNotResurrectItsSessions(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	e.createMember(t, "pat@example.com", "pw-1")
	idTok, pair := e.login(t, "pat@example.com", "pw-1")
	oauth := e.oauthSession(t, idTok)
	before := e.rowEpoch(t, "pat@example.com")

	removed, err := e.admin.DeleteIdentity(ctx, "pat@example.com")
	require.NoError(t, err)
	require.True(t, removed)
	e.createMember(t, "pat@example.com", "pw-new")
	require.NotEqual(t, before, e.rowEpoch(t, "pat@example.com"), "the re-created identity inherited the epoch")

	_, err = e.m.Refresh(ctx, pair.RefreshToken)
	require.ErrorIs(t, err, ErrInvalidToken, "the old identity's tenant refresh token worked for the new one")
	_, err = e.refreshOAuth(oauth.RefreshToken)
	assertOAuthErrorCode(t, err, "invalid_grant")
	_, err = e.m.SelectTenant(ctx, idTok, "acme")
	require.ErrorIs(t, err, ErrInvalidToken)

	_, fresh := e.login(t, "pat@example.com", "pw-new")
	_, err = e.m.Refresh(ctx, fresh.RefreshToken)
	require.NoError(t, err, "the new identity's own session must work")
}

// A DELETED identity — not re-created — ends every session, and the refusal is the
// same invalid-token / invalid_grant answer as any other ended session. The store's
// "record not found" must not leak through: an OAuth client that sees server_error
// retries forever instead of re-authorizing, and the tenant paths would hand a raw
// database error to the API instead of ErrInvalidToken.
func TestDeletingAnIdentityEndsEverySessionAsAnInvalidGrant(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	e.createMember(t, "pat@example.com", "pw-1")
	idTok, pair := e.login(t, "pat@example.com", "pw-1")
	oauth := e.oauthSession(t, idTok)
	pendingCode, err := e.issueCode(t, idTok)
	require.NoError(t, err, "control: before the delete the consent step issues a code")
	_, control := e.login(t, "pat@example.com", "pw-1")
	_, err = e.m.Refresh(ctx, control.RefreshToken)
	require.NoError(t, err, "control: before the delete a refresh token works")
	_, err = e.m.Memberships(ctx, idTok)
	require.NoError(t, err, "control: before the delete the identity token lists memberships")

	removed, err := e.admin.DeleteIdentity(ctx, "pat@example.com")
	require.NoError(t, err)
	require.True(t, removed)

	_, err = e.m.Refresh(ctx, pair.RefreshToken)
	require.ErrorIs(t, err, ErrInvalidToken, "a deleted identity's tenant refresh token")
	_, err = e.m.SelectTenant(ctx, idTok, "acme")
	require.ErrorIs(t, err, ErrInvalidToken, "a deleted identity's identity token at SelectTenant")
	_, err = e.m.Memberships(ctx, idTok)
	require.ErrorIs(t, err, ErrInvalidToken, "a deleted identity's identity token at Memberships")
	_, err = e.issueCode(t, idTok)
	require.ErrorIs(t, err, ErrInvalidToken, "the consent step issued a code for a deleted identity")
	_, err = e.refreshOAuth(oauth.RefreshToken)
	assertOAuthErrorCode(t, err, "invalid_grant")
	_, err = e.redeem(pendingCode)
	assertOAuthErrorCode(t, err, "invalid_grant")
}

// A row that is disabled WITHOUT its epoch changing is refused on every path too. The
// admin mutation rotates the epoch as well, so through it the epoch comparison alone
// would refuse; this row is disabled by a raw UPDATE — the way a pod from before the
// epoch existed disables one during a rolling upgrade — so the ONLY thing that can
// refuse it is the enabled check.
func TestADisabledRowWithAnUnchangedEpochIsRefusedOnEveryPath(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	e.createMember(t, "pat@example.com", "pw-1")
	idTok, pair := e.login(t, "pat@example.com", "pw-1")
	oauth := e.oauthSession(t, idTok)
	pendingCode, err := e.issueCode(t, idTok)
	require.NoError(t, err, "control: before the disable the consent step issues a code")
	_, control := e.login(t, "pat@example.com", "pw-1")
	_, err = e.m.Refresh(ctx, control.RefreshToken)
	require.NoError(t, err, "control: before the disable a refresh token works")
	before := e.rowEpoch(t, "pat@example.com")

	require.NoError(t, e.db().Exec(
		`UPDATE iam_identities SET enabled = ? WHERE email = ?`, false, "pat@example.com").Error)
	require.Equal(t, before, e.rowEpoch(t, "pat@example.com"), "the raw disable must leave the epoch as it was")

	_, err = e.m.Refresh(ctx, pair.RefreshToken)
	require.ErrorIs(t, err, ErrInvalidToken, "a disabled identity's tenant refresh token rotated")
	_, err = e.m.SelectTenant(ctx, idTok, "acme")
	require.ErrorIs(t, err, ErrInvalidToken)
	_, err = e.m.Memberships(ctx, idTok)
	require.ErrorIs(t, err, ErrInvalidToken)
	_, err = e.issueCode(t, idTok)
	require.ErrorIs(t, err, ErrInvalidToken)
	_, err = e.refreshOAuth(oauth.RefreshToken)
	assertOAuthErrorCode(t, err, "invalid_grant")
	_, err = e.redeem(pendingCode)
	assertOAuthErrorCode(t, err, "invalid_grant")
}

// Disabling ends the sessions; re-enabling does not revive them, and enabling alone
// does not change the epoch.
func TestDisableThenEnableDoesNotReviveSessions(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	e.createMember(t, "pat@example.com", "pw-1")
	idTok, pair := e.login(t, "pat@example.com", "pw-1")
	oauth := e.oauthSession(t, idTok)

	_, err := e.admin.SetIdentityEnabled(ctx, "pat@example.com", false)
	require.NoError(t, err)
	afterDisable := e.rowEpoch(t, "pat@example.com")
	_, err = e.admin.SetIdentityEnabled(ctx, "pat@example.com", true)
	require.NoError(t, err)
	require.Equal(t, afterDisable, e.rowEpoch(t, "pat@example.com"), "enabling rotated the epoch")

	_, err = e.m.Refresh(ctx, pair.RefreshToken)
	require.ErrorIs(t, err, ErrInvalidToken, "re-enabling revived a tenant refresh token from before the disable")
	_, err = e.refreshOAuth(oauth.RefreshToken)
	assertOAuthErrorCode(t, err, "invalid_grant")
	_, err = e.m.SelectTenant(ctx, idTok, "acme")
	require.ErrorIs(t, err, ErrInvalidToken)

	_, fresh := e.login(t, "pat@example.com", "pw-1")
	_, err = e.m.Refresh(ctx, fresh.RefreshToken)
	require.NoError(t, err, "a session started after re-enabling must work")
}

// A token with no epoch at all — every token issued before this change — is refused
// on every exchange, even with its jti live in the store. The control is the same
// forged shape carrying the row's epoch, which succeeds: so the refusal is the epoch,
// not something else wrong with the forgery.
func TestATokenWithNoEpochIsRefusedOnEveryPath(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	e.createMember(t, "pat@example.com", "pw-1")
	epoch := e.rowEpoch(t, "pat@example.com")

	_, err := e.m.Refresh(ctx, e.forge(t, tenantRefreshClaims("pat@example.com", epoch), true))
	require.NoError(t, err, "control: the forged tenant refresh shape with the row's epoch is accepted")
	_, err = e.refreshOAuth(e.forge(t, oauthRefreshClaims("pat@example.com", epoch), true))
	require.NoError(t, err, "control: the forged OAuth refresh shape with the row's epoch is accepted")
	_, err = e.m.SelectTenant(ctx, e.forge(t, identityClaims("pat@example.com", epoch), false), "acme")
	require.NoError(t, err, "control: the forged identity shape with the row's epoch is accepted")

	_, err = e.m.Refresh(ctx, e.forge(t, tenantRefreshClaims("pat@example.com", ""), true))
	require.ErrorIs(t, err, ErrInvalidToken)
	_, err = e.refreshOAuth(e.forge(t, oauthRefreshClaims("pat@example.com", ""), true))
	assertOAuthErrorCode(t, err, "invalid_grant")
	noSep := e.forge(t, identityClaims("pat@example.com", ""), false)
	_, err = e.m.SelectTenant(ctx, noSep, "acme")
	require.ErrorIs(t, err, ErrInvalidToken)
	_, err = e.m.Memberships(ctx, noSep)
	require.ErrorIs(t, err, ErrInvalidToken)
	_, err = e.issueCode(t, noSep)
	require.ErrorIs(t, err, ErrInvalidToken)
}

// 🔴 THE EMPTY-EQUALS-EMPTY HOLE. A row still holding the column default, the empty string (inserted
// by a pod from before the column) and a token carrying no epoch compare EQUAL. Both
// empty checks exist so that pair is refused — and a sign-in to such a row fails at
// the mint (the log names the cause; the caller sees the uniform credentials error)
// until an admin resets its password.
func TestAnEmptyStoredEpochIsRefusedEvenAgainstAnEmptyTokenEpoch(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	e.createMember(t, "pat@example.com", "pw-1")
	require.NoError(t, e.db().Exec(
		`UPDATE iam_identities SET session_epoch = '' WHERE email = ?`, "pat@example.com").Error)

	_, err := e.m.Refresh(ctx, e.forge(t, tenantRefreshClaims("pat@example.com", ""), true))
	require.ErrorIs(t, err, ErrInvalidToken)
	_, err = e.refreshOAuth(e.forge(t, oauthRefreshClaims("pat@example.com", ""), true))
	assertOAuthErrorCode(t, err, "invalid_grant")
	_, err = e.m.SelectTenant(ctx, e.forge(t, identityClaims("pat@example.com", ""), false), "acme")
	require.ErrorIs(t, err, ErrInvalidToken)

	// The password is RIGHT here, so the uniform error is the point: a distinct one
	// would confirm the password to whoever sent it.
	_, err = e.m.Login(ctx, "pat@example.com", "pw-1")
	require.ErrorIs(t, err, ErrInvalidCredentials,
		"a sign-in to a row with no epoch must fail at the mint, not hand out a token nothing accepts")
	require.NotErrorIs(t, err, auth.ErrNoSessionEpoch,
		"a sign-in with the right password to a row with no epoch told the caller the password was right")

	// The documented remedy.
	_, err = e.admin.SetPassword(ctx, "pat@example.com", "pw-2")
	require.NoError(t, err)
	_, fresh := e.login(t, "pat@example.com", "pw-2")
	_, err = e.m.Refresh(ctx, fresh.RefreshToken)
	require.NoError(t, err)
}

// A database that fails is not a revoked session. mintScopedGrant must keep telling
// the two apart after moving onto sessionIdentity, or a blip would be reported to an
// OAuth client as grant-revoked and end a valid session.
func TestATransientStoreErrorInTheOAuthGrantIsAServerError(t *testing.T) {
	e := newSessionEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	epoch := e.rowEpoch(t, "pat@example.com")

	sqlDB, err := e.db().DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	_, err = e.m.mintScopedGrant(context.Background(), "pat@example.com", epoch, "acme",
		auth.ScopeReadOnly, auth.ScopeReadOnly, nil, sessClient)
	assertOAuthErrorCode(t, err, "server_error")
}
