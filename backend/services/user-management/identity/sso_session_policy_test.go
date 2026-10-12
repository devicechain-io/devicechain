// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/admin"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/golang-jwt/jwt/v5"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// The session policy: a tenant that requires single sign-on, the session ceiling, and
// signing an identity out everywhere. Each rule is driven through the calls its caller
// makes (selectTenant, refresh, the OAuth authorize and token steps, the MCP authorize
// page's own HTTP handler), and every refusal is paired with a control showing the same
// call working until the rule applies.

const pwMember = "member@example.com"

// auditRows returns the auth audit rows of an operation.
func (e *ssoEnv) auditRows(t *testing.T, op string) []rdb.AuditEvent {
	t.Helper()
	var rows []rdb.AuditEvent
	require.NoError(t, e.db().WithContext(core.WithSystemContext(context.Background())).
		Where("operation = ?", op).Order("id").Find(&rows).Error)
	return rows
}

func (e *ssoEnv) enforce(t *testing.T, tenant string, on bool) {
	t.Helper()
	got, err := e.m.SetTenantSsoEnforced(context.Background(), tenant, on)
	require.NoError(t, err)
	require.Equal(t, on, got.SsoEnforced)
}

// sessionOf returns the sign-in the identity token names and its server-side record.
func (e *ssoEnv) sessionOf(t *testing.T, identityToken string) (auth.SessionID, *sessionRecord) {
	t.Helper()
	claims, err := e.validator.ValidateIdentity(identityToken)
	require.NoError(t, err)
	rec, err := e.m.loadSession(claims.SessionID, claims.Email)
	require.NoError(t, err)
	return claims.SessionID, rec
}

// ---- a tenant that requires single sign-on ------------------------------------------

func TestPasswordSessionRefusedBySsoEnforcedTenant(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.createMember(t, pwMember, "pw-1")
	e.linkedPat(t)

	// Everything a password sign-in holds BEFORE the tenant starts requiring SSO.
	idTok, pair := e.login(t, pwMember, "pw-1")
	oauth := e.oauthSession(t, idTok)
	pendingCode, err := e.issueCode(t, idTok)
	require.NoError(t, err)
	_, err = e.m.Refresh(ctx, pair.RefreshToken)
	require.NoError(t, err, "control: refresh works before enforcement")
	ms, err := e.m.Memberships(ctx, idTok)
	require.NoError(t, err)
	require.Len(t, ms, 1, "control: the tenant is listed before enforcement")

	e.enforce(t, "acme", true)

	_, err = e.m.SelectTenant(ctx, idTok, "acme")
	require.ErrorIs(t, err, ErrInvalidCredentials, "selectTenant entered a tenant that requires single sign-on with a password session")
	_, err = e.m.Refresh(ctx, pair.RefreshToken)
	require.ErrorIs(t, err, ErrInvalidToken, "refresh kept a password session inside an enforced tenant")
	_, err = e.issueCode(t, idTok)
	require.ErrorIs(t, err, errTenantAccessDenied, "the OAuth authorize step issued a code for an enforced tenant")
	_, err = e.redeem(pendingCode)
	assertOAuthErrorCode(t, err, "invalid_grant")
	_, err = e.refreshOAuth(oauth.RefreshToken)
	assertOAuthErrorCode(t, err, "invalid_grant")

	// The password sign-in does not even offer the tenant, on a fresh login or a re-read.
	ms, err = e.m.Memberships(ctx, idTok)
	require.NoError(t, err)
	require.Empty(t, ms)
	fresh, err := e.m.Login(ctx, pwMember, "pw-1")
	require.NoError(t, err)
	require.Empty(t, fresh.Memberships)

	// Controls: a single sign-on session still enters, and turning enforcement off lets the
	// password sign-in back in.
	sso, _ := e.signInOK(t, "acme-idp", "sub-pat", linkedEmail)
	require.Len(t, sso.Memberships, 1)
	_, err = e.m.SelectTenant(ctx, sso.IdentityToken, "acme")
	require.NoError(t, err, "control: a single sign-on session enters the enforced tenant")
	e.enforce(t, "acme", false)
	_, err = e.m.SelectTenant(ctx, idTok, "acme")
	require.NoError(t, err, "control: the password session works again once enforcement is off")
}

// The MCP authorize page is the one browser surface with its own password form. Driven
// through its real HTTP handler over the real manager: the consent step cannot be talked into
// issuing a code for an enforced tenant.
func TestMcpAuthorizePasswordFormCannotReachAnEnforcedTenant(t *testing.T) {
	e := newSsoEnv(t)
	e.createMember(t, pwMember, "pw-1")
	h := AuthorizeHandler(e.m)

	base := url.Values{
		"response_type": {"code"}, "client_id": {sessClient}, "redirect_uri": {sessRedirect},
		"scope": {auth.ScopeReadOnly}, "state": {"st"},
		"code_challenge": {pkceChallenge(sessVerifier)}, "code_challenge_method": {"S256"},
	}
	post := func(form url.Values) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/oauth/authorize", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	form := func(kv map[string]string) url.Values {
		v := url.Values{}
		for k, vals := range base {
			v[k] = vals
		}
		for k, val := range kv {
			v.Set(k, val)
		}
		return v
	}
	landing := func(rec *httptest.ResponseRecorder) *url.URL {
		require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
		u, err := url.Parse(rec.Header().Get("Location"))
		require.NoError(t, err)
		return u
	}

	idTok, _ := e.login(t, pwMember, "pw-1")
	consent := func() *httptest.ResponseRecorder {
		return post(form(map[string]string{"step": "consent", "action": "allow", "identity_token": idTok, "tenant": "acme"}))
	}
	require.NotEmpty(t, landing(consent()).Query().Get("code"), "control: consent issues a code before enforcement")

	e.enforce(t, "acme", true)

	// The login step lists no tenant to choose, and consent for it anyway is denied.
	lr := post(form(map[string]string{"step": "login", "email": pwMember, "password": "pw-1"}))
	require.Equal(t, http.StatusOK, lr.Code)
	require.NotContains(t, lr.Body.String(), `value="acme"`, "the consent page offered a tenant that requires single sign-on")
	u := landing(consent())
	require.Equal(t, "access_denied", u.Query().Get("error"))
	require.Empty(t, u.Query().Get("code"), "a code was issued for an enforced tenant")
}

func TestSuperuserBypassesEnforcementAudited(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.createMember(t, "root@example.com", "pw-root")
	e.grantSuperuser(t, "root@example.com")
	e.createMember(t, pwMember, "pw-1")

	idTok, pair := e.login(t, "root@example.com", "pw-root")
	require.Empty(t, e.auditRows(t, rdb.AuditOpSsoEnforcementBypassed), "control: nothing is a bypass while the tenant does not require single sign-on")

	e.enforce(t, "acme", true)

	// The superuser still lists every tenant, enters, refreshes, and consents.
	ia, err := e.m.Login(ctx, "root@example.com", "pw-root")
	require.NoError(t, err)
	require.Len(t, ia.Memberships, 1, "the superuser's password sign-in lists the enforced tenant")
	got, err := e.m.SelectTenant(ctx, idTok, "acme")
	require.NoError(t, err, "the superuser was locked out of an enforced tenant")
	claims, err := e.validator.Validate(got.AccessToken)
	require.NoError(t, err)
	require.True(t, claims.ActingAsSuperuser)
	_, err = e.m.Refresh(ctx, pair.RefreshToken)
	require.NoError(t, err, "refresh locked the superuser out of an enforced tenant")
	_, err = e.issueCode(t, idTok)
	require.NoError(t, err)

	rows := e.auditRows(t, rdb.AuditOpSsoEnforcementBypassed)
	require.NotEmpty(t, rows, "the superuser's entry into an enforced tenant was not audited")
	for _, r := range rows {
		require.Equal(t, "root@example.com", r.Actor)
		require.Equal(t, "acme", r.TenantId)
		require.Equal(t, rdb.AuditCategoryAuth, r.Category)
	}

	// An ordinary member is refused, and a refusal is not a bypass.
	before := len(rows)
	mem, _ := e.login2(t, pwMember, "pw-1")
	_, err = e.m.SelectTenant(ctx, mem, "acme")
	require.ErrorIs(t, err, ErrInvalidCredentials)
	require.Len(t, e.auditRows(t, rdb.AuditOpSsoEnforcementBypassed), before)
}

// login2 signs in without entering a tenant.
func (e *ssoEnv) login2(t *testing.T, email, password string) (string, *IdentityAuth) {
	t.Helper()
	ia, err := e.m.Login(context.Background(), email, password)
	require.NoError(t, err)
	return ia.IdentityToken, ia
}

// Requiring single sign-on for a tenant nobody can sign in to by SSO would lock its people
// out; the call refuses, and the other bad inputs are named.
func TestSetTenantSsoEnforcedRefusesWhatWouldLockATenantOut(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.addTenant(t, "globex")

	_, err := e.m.SetTenantSsoEnforced(ctx, "globex", true)
	require.ErrorIs(t, err, ErrSsoEnforceNoConnection, "no connection serves globex")
	_, err = e.m.SetTenantSsoEnforced(ctx, "nowhere", true)
	require.ErrorIs(t, err, ErrTenantNotFound)
	_, err = e.m.SetTenantSsoEnforced(ctx, "nowhere", false)
	require.ErrorIs(t, err, ErrTenantNotFound)

	// A DISABLED connection is not a way in.
	_, err = e.m.SetIdpConnectionTenants(ctx, "acme-idp", []string{"acme", "globex"})
	require.NoError(t, err)
	e.enforce(t, "globex", true)
	e.enforce(t, "globex", false)
	_, err = e.m.UpdateIdpConnection(ctx, "acme-idp", &IdpConnectionUpdateRequest{Enabled: optBool(false)})
	require.NoError(t, err)
	_, err = e.m.SetTenantSsoEnforced(ctx, "globex", true)
	require.ErrorIs(t, err, ErrSsoEnforceNoConnection)

	// An INSTANCE connection serves no tenant, so it is not one either.
	e.addConnection(t, "ops-idp", iam.IdpKindInstance, "")
	_, err = e.m.SetTenantSsoEnforced(ctx, "globex", true)
	require.ErrorIs(t, err, ErrSsoEnforceNoConnection)

	// Turning it OFF never needs a connection.
	_, err = e.m.SetTenantSsoEnforced(ctx, "globex", false)
	require.NoError(t, err)
}

// The flag is a column of its own: the tenant update path neither writes it nor loses it.
func TestTenantUpdateDoesNotTouchSsoEnforced(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.enforce(t, "acme", true)
	ten, err := e.store.TenantByToken(ctx, "acme")
	require.NoError(t, err)
	require.True(t, ten.SsoEnforced)
	ten.Name.Valid, ten.Name.String = true, "Renamed"
	ten.SsoEnforced = false // a stale copy, as a concurrent editor would hold
	require.NoError(t, e.store.UpdateTenant(ctx, ten))
	again, err := e.store.TenantByToken(ctx, "acme")
	require.NoError(t, err)
	require.True(t, again.SsoEnforced, "an unrelated tenant edit switched single sign-on enforcement off")
}

// ---- the session ceiling ------------------------------------------------------------

// atAge points the manager's clock d after the sign-in's anchor.
func (e *ssoEnv) atAge(rec *sessionRecord, d time.Duration) {
	at := rec.Anchor.Add(d)
	e.m.now = func() time.Time { return at }
}

func TestRefreshRefusedPastCeiling(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.linkedPat(t)
	got, _ := e.signInOK(t, "acme-idp", "sub-pat", linkedEmail)
	_, rec := e.sessionOf(t, got.IdentityToken)
	require.Equal(t, iam.DefaultSessionMaxAgeSeconds, e.reloadConn(t, "acme-idp").SessionMaxAgeSeconds)
	ceiling := 12 * time.Hour

	// One second inside the ceiling everything works; at the ceiling it is over.
	e.atAge(rec, ceiling-time.Second)
	pair, err := e.m.SelectTenant(ctx, got.IdentityToken, "acme")
	require.NoError(t, err, "control: selectTenant one second inside the ceiling")
	oauth := e.oauthSession(t, got.IdentityToken)
	pending, err := e.issueCode(t, got.IdentityToken)
	require.NoError(t, err, "control")
	_, err = e.m.Memberships(ctx, got.IdentityToken)
	require.NoError(t, err, "control")
	rotated, err := e.m.Refresh(ctx, pair.RefreshToken)
	require.NoError(t, err, "control: refresh inside the ceiling")

	e.atAge(rec, ceiling)
	_, err = e.m.SelectTenant(ctx, got.IdentityToken, "acme")
	require.ErrorIs(t, err, ErrInvalidToken, "selectTenant admitted a session at its ceiling")
	_, err = e.m.Refresh(ctx, rotated.RefreshToken)
	require.ErrorIs(t, err, ErrInvalidToken, "refresh admitted a session at its ceiling")
	_, err = e.m.Memberships(ctx, got.IdentityToken)
	require.ErrorIs(t, err, ErrInvalidToken, "the membership list admitted a session at its ceiling")
	_, err = e.issueCode(t, got.IdentityToken)
	require.ErrorIs(t, err, ErrInvalidToken, "the OAuth authorize step admitted a session at its ceiling")
	_, err = e.redeem(pending)
	assertOAuthErrorCode(t, err, "invalid_grant")
	_, err = e.refreshOAuth(oauth.RefreshToken)
	assertOAuthErrorCode(t, err, "invalid_grant")
}

// A refresh moves the pair along, never the ceiling: a chain refreshed late in the session
// still ends when the sign-in's ceiling does.
func TestRefreshNeverExtendsTheCeiling(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.linkedPat(t)
	got, _ := e.signInOK(t, "acme-idp", "sub-pat", linkedEmail)
	_, rec := e.sessionOf(t, got.IdentityToken)
	pair, err := e.m.SelectTenant(ctx, got.IdentityToken, "acme")
	require.NoError(t, err)

	for _, age := range []time.Duration{3 * time.Hour, 6 * time.Hour, 11 * time.Hour} {
		e.atAge(rec, age)
		pair, err = e.m.Refresh(ctx, pair.RefreshToken)
		require.NoError(t, err, "control: refresh at %s", age)
	}
	e.atAge(rec, 12*time.Hour+time.Second)
	_, err = e.m.Refresh(ctx, pair.RefreshToken)
	require.ErrorIs(t, err, ErrInvalidToken, "a refresh near the end of the session extended its ceiling")
}

// The ceiling counts from OUR callback, whatever the provider says about when the person
// authenticated: a backdated auth_time does not shorten it, a future one does not stretch
// it, and a missing one changes nothing.
func TestCeilingAnchoredOnCallbackNotAuthTime(t *testing.T) {
	for name, authTime := range map[string]func() any{
		"backdated": func() any { return time.Now().Add(-30 * time.Hour).Unix() },
		"future":    func() any { return time.Now().Add(30 * time.Hour).Unix() },
		"absent":    func() any { return nil },
	} {
		t.Run(name, func(t *testing.T) {
			e := newSsoEnv(t)
			ctx := context.Background()
			e.linkedPat(t)
			e.op.mutate = func(c jwt.MapClaims) {
				if v := authTime(); v != nil {
					c["auth_time"] = v
				}
			}
			before := time.Now()
			got, _ := e.signInOK(t, "acme-idp", "sub-pat", linkedEmail)
			after := time.Now()
			_, rec := e.sessionOf(t, got.IdentityToken)
			require.False(t, rec.Anchor.Before(before.Add(-time.Second)) || rec.Anchor.After(after.Add(time.Second)),
				"the anchor %s is not our callback time (between %s and %s)", rec.Anchor, before, after)

			e.atAge(rec, 11*time.Hour+59*time.Minute)
			_, err := e.m.SelectTenant(ctx, got.IdentityToken, "acme")
			require.NoError(t, err, "the provider's auth_time shortened the session")
			e.atAge(rec, 12*time.Hour+time.Minute)
			_, err = e.m.SelectTenant(ctx, got.IdentityToken, "acme")
			require.ErrorIs(t, err, ErrInvalidToken, "the provider's auth_time stretched the session")
		})
	}
}

// A connection's own ceiling replaces the default, applies to sessions already open, and
// leaves password sessions alone.
func TestConnectionCeilingOverridesAndAppliesToOpenSessions(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.linkedPat(t)
	e.createMember(t, pwMember, "pw-1")
	got, _ := e.signInOK(t, "acme-idp", "sub-pat", linkedEmail)
	_, rec := e.sessionOf(t, got.IdentityToken)
	pwTok, _ := e.login2(t, pwMember, "pw-1")

	d, err := e.m.SetIdpSessionMaxAge(ctx, "acme-idp", 3600)
	require.NoError(t, err)
	require.Equal(t, 3600, d.Connection.SessionMaxAgeSeconds)

	e.atAge(rec, 59*time.Minute)
	_, err = e.m.SelectTenant(ctx, got.IdentityToken, "acme")
	require.NoError(t, err, "control: inside the connection's own ceiling")
	e.atAge(rec, 61*time.Minute)
	_, err = e.m.SelectTenant(ctx, got.IdentityToken, "acme")
	require.ErrorIs(t, err, ErrInvalidToken, "a session already open outlived its connection's new ceiling")
	_, err = e.m.SelectTenant(ctx, pwTok, "acme")
	require.NoError(t, err, "the SSO ceiling ended a PASSWORD session")

	// A new sign-in gets a fresh anchor and a fresh hour.
	e.m.now = nil
	again, _ := e.signInOK(t, "acme-idp", "sub-pat", linkedEmail)
	_, err = e.m.SelectTenant(ctx, again.IdentityToken, "acme")
	require.NoError(t, err)
}

func TestSessionCeilingBoundsAreValidated(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	set := func(s int) error {
		_, err := e.m.SetIdpSessionMaxAge(ctx, "acme-idp", s)
		return err
	}
	for _, bad := range []int{-1, 0, 1, 899, 604801, 1 << 30} {
		require.ErrorIs(t, set(bad), ErrIdpInvalid, "%d seconds was accepted", bad)
		require.Equal(t, iam.DefaultSessionMaxAgeSeconds, e.reloadConn(t, "acme-idp").SessionMaxAgeSeconds, "a refused ceiling was stored")
	}
	for _, ok := range []int{900, 3600, 604800} {
		require.NoError(t, set(ok), "%d seconds was refused", ok)
		require.Equal(t, ok, e.reloadConn(t, "acme-idp").SessionMaxAgeSeconds)
	}
	_, err := e.m.SetIdpSessionMaxAge(ctx, "nope", 3600)
	require.ErrorIs(t, err, ErrIdpNotFound)

	// The sign-in record lives only as long as the refresh lifetime, so a ceiling beyond it
	// would promise sessions the store cannot keep.
	e.m.refreshTTL = 24 * time.Hour
	require.NoError(t, set(86400))
	require.ErrorIs(t, set(86401), ErrIdpInvalid)
	require.Equal(t, 24*time.Hour, e.m.MaxSessionMaxAge())
	e.m.refreshTTL = 30 * 24 * time.Hour
	require.Equal(t, 7*24*time.Hour, e.m.MaxSessionMaxAge(), "a long refresh lifetime must not raise the cap above seven days")

	// The store refuses an out-of-range value on its own.
	conn := e.reloadConn(t, "acme-idp")
	require.Error(t, e.store.SetIdpSessionMaxAge(ctx, conn, 60))
}

// ---- sign out everywhere ------------------------------------------------------------

func (e *ssoEnv) sessionRecordsFor(t *testing.T, email string) []string {
	t.Helper()
	w, err := e.m.refreshKV.Watch(sessionIndexPattern(email), nats.IgnoreDeletes(), nats.MetaOnly())
	require.NoError(t, err)
	defer func() { _ = w.Stop() }()
	var keys []string
	for {
		select {
		case en := <-w.Updates():
			if en == nil {
				return keys
			}
			keys = append(keys, en.Key())
		case <-time.After(5 * time.Second):
			t.Fatal("the index listing never finished")
		}
	}
}

func TestRevokeSessionsEndsAllChains(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	// Pat has a password AND an SSO link, so both kinds of sign-in exist; the member is
	// another person with a password sign-in.
	e.linkedPat(t)
	_, err := e.admin.SetPassword(ctx, linkedEmail, "pw-pat")
	require.NoError(t, err)
	e.createMember(t, pwMember, "pw-1")

	type signIn struct {
		idTok string
		pair  *TokenPair
		oauth *OAuthTokens
		code  string
	}
	enter := func(idTok string) signIn {
		pair, err := e.m.SelectTenant(ctx, idTok, "acme")
		require.NoError(t, err)
		code, err := e.issueCode(t, idTok)
		require.NoError(t, err)
		return signIn{idTok: idTok, pair: pair, oauth: e.oauthSession(t, idTok), code: code}
	}
	laptopID, _ := e.login2(t, linkedEmail, "pw-pat")
	phoneID, _ := e.login2(t, linkedEmail, "pw-pat")
	ssoGot, _ := e.signInOK(t, "acme-idp", "sub-pat", linkedEmail)
	pat := []signIn{enter(laptopID), enter(phoneID), enter(ssoGot.IdentityToken)}
	memID, _ := e.login2(t, pwMember, "pw-1")
	other := enter(memID)
	require.Len(t, e.sessionRecordsFor(t, linkedEmail), 3, "each sign-in is indexed under its owner")
	require.Len(t, e.sessionRecordsFor(t, pwMember), 1)
	patEpoch := e.rowEpoch(t, linkedEmail)
	var patSids []auth.SessionID
	for _, s := range pat {
		sid, _ := e.sessionOf(t, s.idTok)
		patSids = append(patSids, sid)
	}
	otherSid, _ := e.sessionOf(t, other.idTok)

	n, err := e.m.RevokeSessions(ctx, "  "+strings.ToUpper(linkedEmail)+" ")
	require.NoError(t, err, "the email is matched like a sign-in's")
	require.Equal(t, 3, n)

	for i, s := range pat {
		_, err = e.m.Refresh(ctx, s.pair.RefreshToken)
		require.ErrorIs(t, err, ErrInvalidToken, "sign-in %d: refresh survived revokeSessions", i)
		_, err = e.m.SelectTenant(ctx, s.idTok, "acme")
		require.ErrorIs(t, err, ErrInvalidToken, "sign-in %d: selectTenant survived", i)
		_, err = e.m.Memberships(ctx, s.idTok)
		require.ErrorIs(t, err, ErrInvalidToken, "sign-in %d: the membership list survived", i)
		_, err = e.issueCode(t, s.idTok)
		require.ErrorIs(t, err, ErrInvalidToken, "sign-in %d: authorize survived", i)
		_, err = e.redeem(s.code)
		assertOAuthErrorCode(t, err, "invalid_grant")
		_, err = e.refreshOAuth(s.oauth.RefreshToken)
		assertOAuthErrorCode(t, err, "invalid_grant")
	}

	// The records themselves are gone, which the epoch alone would not do: this is what
	// ends them by name.
	for i, sid := range patSids {
		_, err := e.m.loadSession(sid, linkedEmail)
		require.ErrorIs(t, err, errSessionEnded, "sign-in %d: its record is still there", i)
	}
	require.Empty(t, e.sessionRecordsFor(t, linkedEmail), "the index entries were left behind")
	require.NotEqual(t, patEpoch, e.rowEpoch(t, linkedEmail), "the epoch was not rotated")

	// Another identity is untouched, down to its OAuth grant.
	_, err = e.m.loadSession(otherSid, pwMember)
	require.NoError(t, err)
	_, err = e.m.Refresh(ctx, other.pair.RefreshToken)
	require.NoError(t, err, "revoking one identity ended another's session")
	_, err = e.m.SelectTenant(ctx, other.idTok, "acme")
	require.NoError(t, err)
	_, err = e.refreshOAuth(other.oauth.RefreshToken)
	require.NoError(t, err)
	require.Len(t, e.sessionRecordsFor(t, pwMember), 1)

	// The person can sign in again, by either method, and the revoke is audited.
	again, _ := e.login2(t, linkedEmail, "pw-pat")
	_, err = e.m.SelectTenant(ctx, again, "acme")
	require.NoError(t, err)
	sso, _ := e.signInOK(t, "acme-idp", "sub-pat", linkedEmail)
	_, err = e.m.SelectTenant(ctx, sso.IdentityToken, "acme")
	require.NoError(t, err)
	rows := e.auditRows(t, rdb.AuditOpSessionsRevoked)
	require.Len(t, rows, 1)
	require.Equal(t, linkedEmail, rows[0].EntityLabel)

	// Repeating it ends nothing more, and an unknown identity is named.
	n, err = e.m.RevokeSessions(ctx, linkedEmail)
	require.NoError(t, err)
	require.Equal(t, 2, n, "only the two new sign-ins remained to end")
	_, err = e.m.RevokeSessions(ctx, "ghost@example.com")
	require.ErrorIs(t, err, ErrIdentityNotFound)
}

// A sign-in the index does not list (one that predates it) is still ended: the epoch is the
// part that cannot be missed.
func TestRevokeSessionsEndsAnUnindexedSignInByEpoch(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.createMember(t, pwMember, "pw-1")
	idTok, pair := e.login(t, pwMember, "pw-1")
	sid, _ := e.sessionOf(t, idTok)
	require.NoError(t, e.m.refreshKV.Delete(sessionIndexKey(pwMember, sid)))

	n, err := e.m.RevokeSessions(ctx, pwMember)
	require.NoError(t, err)
	require.Zero(t, n, "nothing was indexed to end by name")
	_, err = e.m.Refresh(ctx, pair.RefreshToken)
	require.ErrorIs(t, err, ErrInvalidToken, "an unindexed sign-in survived revokeSessions")
	_, err = e.m.SelectTenant(ctx, idTok, "acme")
	require.ErrorIs(t, err, ErrInvalidToken)
}

// A sign-in's index entry follows its record out: logout removes it.
func TestLogoutRemovesTheSessionIndexEntry(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.createMember(t, pwMember, "pw-1")
	_, a := e.login(t, pwMember, "pw-1")
	_, b := e.login(t, pwMember, "pw-1")
	require.Len(t, e.sessionRecordsFor(t, pwMember), 2)
	require.NoError(t, e.m.Logout(ctx, a.RefreshToken))
	keys := e.sessionRecordsFor(t, pwMember)
	require.Len(t, keys, 1)
	bClaims, err := e.validator.ValidateRefresh(b.RefreshToken)
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(keys[0], "."+string(bClaims.SessionID)), "logout removed the wrong entry: %v", keys)
}

// A sign-in whose index entry cannot be written is not started: a session sign-out could
// not find is one it could not end.
func TestSignInIsRefusedWhenItsIndexEntryCannotBeWritten(t *testing.T) {
	e := newSsoEnv(t)
	e.createMember(t, pwMember, "pw-1")
	inner := e.m.refreshKV
	e.m.refreshKV = &indexWriteFails{KeyValue: inner}
	_, err := e.m.Login(context.Background(), pwMember, "pw-1")
	require.Error(t, err)
	// The record written before the failed index put is not left behind.
	_, err = inner.Keys()
	require.ErrorIs(t, err, nats.ErrNoKeysFound, "a half-started sign-in was left in the store")
}

type indexWriteFails struct{ nats.KeyValue }

func (f *indexWriteFails) Put(key string, value []byte) (uint64, error) {
	if strings.HasPrefix(key, sessionIndexPrefix) {
		return 0, nats.ErrTimeout
	}
	return f.KeyValue.Put(key, value)
}

func TestClearPasswordBumpsEpoch(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.linkedPat(t)
	_, err := e.admin.SetPassword(ctx, linkedEmail, "pw-pat")
	require.NoError(t, err)
	e.createMember(t, pwMember, "pw-1")
	patID, patPair := e.login(t, linkedEmail, "pw-pat")
	_, memPair := e.login(t, pwMember, "pw-1")
	ssoGot, _ := e.signInOK(t, "acme-idp", "sub-pat", linkedEmail)
	epoch := e.rowEpoch(t, linkedEmail)

	id, err := e.m.ClearPassword(ctx, linkedEmail)
	require.NoError(t, err)
	require.Empty(t, id.PasswordHash)
	row, err := e.store.IdentityByEmail(ctx, linkedEmail)
	require.NoError(t, err)
	require.Empty(t, row.PasswordHash, "the stored hash was not cleared")
	require.NotEqual(t, epoch, e.rowEpoch(t, linkedEmail), "clearing the password did not rotate the epoch")
	require.Empty(t, e.sessionRecordsFor(t, linkedEmail), "clearing the password left the identity's sign-in records behind")

	// Every session of the old password ends, and so does the SSO one (all of the
	// identity's sessions), while another identity keeps its own.
	_, err = e.m.Refresh(ctx, patPair.RefreshToken)
	require.ErrorIs(t, err, ErrInvalidToken)
	_, err = e.m.SelectTenant(ctx, patID, "acme")
	require.ErrorIs(t, err, ErrInvalidToken)
	_, err = e.m.SelectTenant(ctx, ssoGot.IdentityToken, "acme")
	require.ErrorIs(t, err, ErrInvalidToken)
	_, err = e.m.Refresh(ctx, memPair.RefreshToken)
	require.NoError(t, err, "clearing one identity's password ended another's session")

	// The password no longer signs in -- for the right one or an empty one -- the failure is
	// the uniform one, and a single sign-on still does.
	for _, pw := range []string{"pw-pat", ""} {
		_, err = e.m.Login(ctx, linkedEmail, pw)
		require.ErrorIs(t, err, ErrInvalidCredentials, "password %q signed in after clearPassword", pw)
	}
	again, _ := e.signInOK(t, "acme-idp", "sub-pat", linkedEmail)
	_, err = e.m.SelectTenant(ctx, again.IdentityToken, "acme")
	require.NoError(t, err, "the identity can no longer sign in by single sign-on either")

	rows := e.auditRows(t, rdb.AuditOpPasswordCleared)
	require.Len(t, rows, 1)
	require.Equal(t, linkedEmail, rows[0].EntityLabel)
	_, err = e.m.ClearPassword(ctx, "ghost@example.com")
	require.ErrorIs(t, err, ErrIdentityNotFound)
}

// Clearing the password of an identity with no other way in is refused, and the password is
// left as it was: an unlinked identity, one linked only through a disabled connection, and
// a superuser linked only through a TENANT connection (which never reaches a system role).
func TestClearPasswordRefusedWithNoOtherWayIn(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.createMember(t, pwMember, "pw-1")
	hashOf := func(email string) string {
		id, err := e.store.IdentityByEmail(ctx, email)
		require.NoError(t, err)
		return id.PasswordHash
	}
	refused := func(email, why string) {
		t.Helper()
		before, epoch := hashOf(email), e.rowEpoch(t, email)
		_, err := e.m.ClearPassword(ctx, email)
		require.ErrorIs(t, err, ErrClearPasswordNoOtherWayIn, why)
		require.Equal(t, before, hashOf(email), "a refused clearPassword changed the password (%s)", why)
		require.Equal(t, epoch, e.rowEpoch(t, email), "a refused clearPassword rotated the epoch (%s)", why)
	}
	refused(pwMember, "not linked anywhere")

	e.addConnection(t, "globex-idp", iam.IdpKindTenant, "", "acme")
	require.NoError(t, e.m.LinkIdentity(ctx, pwMember, "globex-idp", "sub-member"))
	_, err := e.m.UpdateIdpConnection(ctx, "globex-idp", &IdpConnectionUpdateRequest{Enabled: optBool(false)})
	require.NoError(t, err)
	refused(pwMember, "linked only through a disabled connection")

	e.createMember(t, "root@example.com", "pw-root")
	require.NoError(t, e.m.LinkIdentity(ctx, "root@example.com", "acme-idp", "sub-root"))
	e.grantSuperuser(t, "root@example.com")
	refused("root@example.com", "a superuser linked only through a tenant connection")

	// Controls: an enabled tenant link for a member, an instance link for a superuser.
	_, err = e.m.UpdateIdpConnection(ctx, "globex-idp", &IdpConnectionUpdateRequest{Enabled: optBool(true)})
	require.NoError(t, err)
	_, err = e.m.ClearPassword(ctx, pwMember)
	require.NoError(t, err)
	e.addConnection(t, "ops-idp", iam.IdpKindInstance, "")
	require.NoError(t, e.m.LinkIdentity(ctx, "root@example.com", "ops-idp", "sub-root"))
	_, err = e.m.ClearPassword(ctx, "root@example.com")
	require.NoError(t, err)
}

// A tenant administrator is an ordinary member as far as the requirement goes: only a
// superuser is exempt, so a password sign-in holding full authority IN the tenant is refused
// there and its entry is not a bypass.
func TestTenantAdminIsNotExemptFromEnforcement(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.tenantRole(t, "tenant-admin", "*")
	_, err := e.admin.CreateIdentity(ctx, admin.CreateIdentityInput{Email: pwMember, Password: "pw-1", Enabled: true})
	require.NoError(t, err)
	_, err = e.admin.AddMembership(ctx, pwMember, "acme", []string{"tenant-admin"})
	require.NoError(t, err)
	idTok, pair := e.login(t, pwMember, "pw-1")
	claims, err := e.validator.Validate(pair.AccessToken)
	require.NoError(t, err)
	require.Contains(t, claims.Authorities, string(auth.AuthorityAll), "control: the member holds full authority in acme")

	e.enforce(t, "acme", true)

	_, err = e.m.SelectTenant(ctx, idTok, "acme")
	require.ErrorIs(t, err, ErrInvalidCredentials, "a tenant administrator's password session entered an enforced tenant")
	_, err = e.m.Refresh(ctx, pair.RefreshToken)
	require.ErrorIs(t, err, ErrInvalidToken)
	_, err = e.issueCode(t, idTok)
	require.ErrorIs(t, err, errTenantAccessDenied)
	ms, err := e.m.Memberships(ctx, idTok)
	require.NoError(t, err)
	require.Empty(t, ms, "a tenant administrator's password sign-in listed an enforced tenant")
	require.Empty(t, e.auditRows(t, rdb.AuditOpSsoEnforcementBypassed), "a tenant administrator was recorded as bypassing")
}

// A single sign-on with no membership for the per-tenant rules to look at (an operator
// through an instance connection) still ends at its ceiling: the membership list refuses it
// rather than answering an empty list for a session that is over.
func TestSsoSessionWithNoMembershipEndsAtCeiling(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.addConnection(t, "ops-idp", iam.IdpKindInstance, "")
	e.ssoIdentity(t, "op@ops.example", "ops-idp", "sub-op")
	got, _ := e.signInOK(t, "ops-idp", "sub-op", "op@ops.example")
	_, rec := e.sessionOf(t, got.IdentityToken)

	e.atAge(rec, 12*time.Hour-time.Second)
	ms, err := e.m.Memberships(ctx, got.IdentityToken)
	require.NoError(t, err, "control: inside the ceiling")
	require.Empty(t, ms)
	e.atAge(rec, 12*time.Hour)
	_, err = e.m.Memberships(ctx, got.IdentityToken)
	require.ErrorIs(t, err, ErrInvalidToken, "a membership-less single sign-on outlived its ceiling")
}

// A ceiling stored while the refresh lifetime was long is held to a shorter lifetime
// configured later, rather than outliving it.
func TestCeilingHeldToALoweredRefreshLifetime(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.linkedPat(t)
	_, err := e.m.SetIdpSessionMaxAge(ctx, "acme-idp", 3*24*3600)
	require.NoError(t, err)
	got, _ := e.signInOK(t, "acme-idp", "sub-pat", linkedEmail)
	_, rec := e.sessionOf(t, got.IdentityToken)

	e.atAge(rec, 25*time.Hour)
	_, err = e.m.SelectTenant(ctx, got.IdentityToken, "acme")
	require.NoError(t, err, "control: inside the stored three-day ceiling")
	e.m.refreshTTL = 24 * time.Hour
	_, err = e.m.SelectTenant(ctx, got.IdentityToken, "acme")
	require.ErrorIs(t, err, ErrInvalidToken, "a stored ceiling outlived the refresh lifetime it was capped by")
	e.atAge(rec, 23*time.Hour)
	_, err = e.m.SelectTenant(ctx, got.IdentityToken, "acme")
	require.NoError(t, err, "control: inside the lowered cap")
}

// The inverse of TestSetTenantSsoEnforcedRefusesWhatWouldLockATenantOut: while a tenant
// requires single sign-on, the last enabled connection serving it can be neither disabled,
// deleted, nor told to stop serving it. Each refusal leaves the connection as it was, and
// each is paired with the same change succeeding once it would strand nobody.
func TestConnectionChangeCannotStrandAnEnforcedTenant(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.addTenant(t, "globex")
	_, err := e.m.SetIdpConnectionTenants(ctx, "acme-idp", []string{"acme", "globex"})
	require.NoError(t, err)
	e.enforce(t, "acme", true)

	disable := func(token string) error {
		_, err := e.m.UpdateIdpConnection(ctx, token, &IdpConnectionUpdateRequest{Enabled: optBool(false)})
		return err
	}
	require.ErrorIs(t, disable("acme-idp"), ErrSsoEnforcedTenantStranded)
	require.True(t, e.reloadConn(t, "acme-idp").Enabled, "a refused disable was written")
	_, err = e.m.DeleteIdpConnection(ctx, "acme-idp")
	require.ErrorIs(t, err, ErrSsoEnforcedTenantStranded)
	require.Equal(t, "acme-idp", e.reloadConn(t, "acme-idp").Token, "a refused delete was carried out")
	_, err = e.m.SetIdpConnectionTenants(ctx, "acme-idp", []string{"globex"})
	require.ErrorIs(t, err, ErrSsoEnforcedTenantStranded)
	served, err := e.store.ConnectionTenants(ctx, e.reloadConn(t, "acme-idp").ID)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"acme", "globex"}, served, "a refused tenant change was written")
	// The error names the tenant, so the operator knows which requirement to lift.
	require.Contains(t, disable("acme-idp").Error(), "acme")

	// Dropping a tenant that does NOT require it is fine.
	_, err = e.m.SetIdpConnectionTenants(ctx, "acme-idp", []string{"acme"})
	require.NoError(t, err, "control: dropping an unenforced tenant")

	// A second enabled connection serving acme makes the first one free to go; the second
	// is then the last, and refused in turn. A DISABLED one does not count.
	e.addConnection(t, "acme-idp-2", iam.IdpKindTenant, "", "acme")
	require.NoError(t, disable("acme-idp-2"))
	require.ErrorIs(t, disable("acme-idp"), ErrSsoEnforcedTenantStranded, "a disabled connection counted as a way in")
	_, err = e.m.UpdateIdpConnection(ctx, "acme-idp-2", &IdpConnectionUpdateRequest{Enabled: optBool(true)})
	require.NoError(t, err)
	require.NoError(t, disable("acme-idp"), "control: another enabled connection still serves acme")
	ok, err := e.m.DeleteIdpConnection(ctx, "acme-idp")
	require.NoError(t, err, "control: a disabled connection strands nobody")
	require.True(t, ok)
	_, err = e.m.DeleteIdpConnection(ctx, "acme-idp-2")
	require.ErrorIs(t, err, ErrSsoEnforcedTenantStranded)

	// Lifting the requirement first is the way through.
	e.enforce(t, "acme", false)
	ok, err = e.m.DeleteIdpConnection(ctx, "acme-idp-2")
	require.NoError(t, err)
	require.True(t, ok)
}

// A revoke whose record cleanup fails has still ended every session (the epoch rotated), so
// it is still audited; so is a cleared password.
func TestRevokeAndClearAreAuditedEvenWhenCleanupFails(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.linkedPat(t)
	_, err := e.admin.SetPassword(ctx, linkedEmail, "pw-pat")
	require.NoError(t, err)
	e.login(t, linkedEmail, "pw-pat")
	e.m.refreshKV = &watchFails{KeyValue: e.m.refreshKV}

	epoch := e.rowEpoch(t, linkedEmail)
	_, err = e.m.RevokeSessions(ctx, linkedEmail)
	require.Error(t, err, "control: the cleanup failed")
	require.NotEqual(t, epoch, e.rowEpoch(t, linkedEmail))
	require.Len(t, e.auditRows(t, rdb.AuditOpSessionsRevoked), 1, "a revoke that ended every session went unaudited")

	_, err = e.m.ClearPassword(ctx, linkedEmail)
	require.Error(t, err, "control: the cleanup failed")
	row, err := e.store.IdentityByEmail(ctx, linkedEmail)
	require.NoError(t, err)
	require.Empty(t, row.PasswordHash)
	require.Len(t, e.auditRows(t, rdb.AuditOpPasswordCleared), 1, "a cleared password went unaudited")
}

type watchFails struct{ nats.KeyValue }

func (f *watchFails) Watch(string, ...nats.WatchOpt) (nats.KeyWatcher, error) {
	return nil, nats.ErrTimeout
}

// ---- audit --------------------------------------------------------------------------

func TestSsoBreakGlassIsAuditedWithTheConnection(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.addConnection(t, "ops-idp", iam.IdpKindInstance, "")
	e.ssoIdentity(t, "root@ops.example", "", "", "acme")
	e.grantSuperuser(t, "root@ops.example")
	require.NoError(t, e.m.LinkIdentity(ctx, "root@ops.example", "ops-idp", "sub-root"))
	e.ssoIdentity(t, "member@ops.example", "", "", "acme")
	require.NoError(t, e.m.LinkIdentity(ctx, "member@ops.example", "ops-idp", "sub-member"))

	// An ordinary member is refused and leaves no break-glass row.
	m, _ := e.signInOK(t, "ops-idp", "sub-member", "member@ops.example")
	_, err := e.m.SelectTenant(ctx, m.IdentityToken, "acme")
	require.ErrorIs(t, err, ErrInvalidCredentials)
	require.Empty(t, e.auditRows(t, rdb.AuditOpSsoBreakGlass))

	root, _ := e.signInOK(t, "ops-idp", "sub-root", "root@ops.example")
	_, err = e.m.SelectTenant(ctx, root.IdentityToken, "acme")
	require.NoError(t, err)
	rows := e.auditRows(t, rdb.AuditOpSsoBreakGlass)
	require.Len(t, rows, 1)
	require.Equal(t, "root@ops.example", rows[0].Actor)
	require.Equal(t, "acme", rows[0].TenantId)
	require.Equal(t, "ops-idp", rows[0].EntityLabel, "the audit row does not name the connection")
}

func TestMfaAcknowledgementAuditRowNamesTheConnection(t *testing.T) {
	e := newSsoEnv(t)
	e.addConnection(t, "ops-idp", iam.IdpKindInstance, "") // created with mfaEnforcedByIdp
	rows := e.auditRows(t, rdb.AuditOpIdpMfaAcknowledged)
	require.Len(t, rows, 1)
	require.Equal(t, "ops-idp", rows[0].EntityLabel)
}
