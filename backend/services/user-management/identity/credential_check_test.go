// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/credential/credentialtest"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const (
	knownEmail     = "owner@example.com"
	knownPassword  = "correct horse battery staple"
	confidentialID = "grafana"
	clientSecret   = "a-long-random-client-secret"
	publicID       = "mcp"
)

// testCredentialPolicy is small enough to reach the delay in a few attempts.
var testCredentialPolicy = credential.Policy{Free: 3, Base: time.Second, Cap: 8 * time.Second}

type stepClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}
func (c *stepClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// credFixture is a Manager built through NewManager over SQLite with a real
// credential.Checker (in-memory attempt store, stepped clock) and a real issuer, so a
// successful login mints a token exactly as in production.
type credFixture struct {
	mgr     *Manager
	rdbm    *rdb.RdbManager
	store   *credentialtest.Store
	clock   *stepClock
	lookups *atomic.Int32 // SELECTs against the identities table
}

func newCredFixture(t *testing.T) *credFixture {
	t.Helper()
	db := putest.NewSQLiteDB(t, &iam.Identity{}, &iam.Role{}, &iam.Membership{}, &iam.OAuthClient{})
	rdbm := &rdb.RdbManager{Database: db}

	// Count identity lookups at the database, so "a throttled attempt makes no lookup"
	// is observed where the lookup would happen rather than inferred.
	lookups := &atomic.Int32{}
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("count_identity_lookups",
		func(tx *gorm.DB) {
			if tx.Statement.Table == (iam.Identity{}).TableName() {
				lookups.Add(1)
			}
		}))

	sys := core.WithSystemContext(context.Background())
	store := iam.NewStore(rdbm)
	hash, err := bcrypt.GenerateFromPassword([]byte(knownPassword), bcrypt.MinCost)
	require.NoError(t, err)
	require.NoError(t, store.CreateIdentity(sys, &iam.Identity{Email: knownEmail, Enabled: true, PasswordHash: string(hash)}))
	secretHash, err := bcrypt.GenerateFromPassword([]byte(clientSecret), bcrypt.MinCost)
	require.NoError(t, err)
	require.NoError(t, store.CreateOAuthClient(sys, &iam.OAuthClient{ClientId: confidentialID, SecretHash: string(secretHash), Enabled: true}))
	require.NoError(t, store.CreateOAuthClient(sys, &iam.OAuthClient{ClientId: publicID, Enabled: true}))

	clk := &stepClock{now: time.Unix(1_700_000_000, 0)}
	attempts := credentialtest.NewStore()
	// Identities get a schedule small enough to walk; client secrets get the policy the
	// service actually ships, so the token-endpoint tests exercise the real decision.
	checker, err := credential.NewChecker(attempts, map[credential.Kind]credential.Policy{
		credential.KindIdentity:    testCredentialPolicy,
		credential.KindOAuthClient: CredentialPolicies[credential.KindOAuthClient],
	}, credential.WithClock(clk.Now))
	require.NoError(t, err)

	mgr := NewManager(nil, rdbm, nil, nil, 0, 0, "", BootstrapConfig{}, checker)
	key, err := auth.GenerateKeyPair()
	require.NoError(t, err)
	mgr.issuer = auth.NewIssuer(key, "test", time.Minute, time.Hour)
	return &credFixture{mgr: mgr, rdbm: rdbm, store: attempts, clock: clk, lookups: lookups}
}

func (f *credFixture) audit(t *testing.T, op string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, f.rdbm.Database.WithContext(core.WithSystemContext(context.Background())).
		Model(&rdb.AuditEvent{}).Where("operation = ?", op).Count(&n).Error)
	return n
}

func throttledFor(t *testing.T, err error) time.Duration {
	t.Helper()
	var th *credential.ThrottledError
	require.Truef(t, errors.As(err, &th), "want a ThrottledError, got %v", err)
	return th.RetryAfter
}

// Login throttles after the free attempts, refuses even the CORRECT password during
// the delay without evaluating it, and resets on success.
func TestLoginThrottlesAndResets(t *testing.T) {
	f := newCredFixture(t)
	ctx := context.Background()

	for i := 1; i <= testCredentialPolicy.Free; i++ {
		_, err := f.mgr.Login(ctx, knownEmail, "wrong")
		require.ErrorIs(t, err, ErrInvalidCredentials, "attempt %d", i)
	}
	require.Equal(t, int64(testCredentialPolicy.Free), f.audit(t, rdb.AuditOpLoginFailed))

	_, err := f.mgr.Login(ctx, knownEmail, knownPassword)
	require.Equal(t, time.Second, throttledFor(t, err))
	require.Equal(t, int64(0), f.audit(t, rdb.AuditOpLogin), "a throttled correct password must not sign in")

	f.clock.Advance(time.Second)
	res, err := f.mgr.Login(ctx, knownEmail, knownPassword)
	require.NoError(t, err)
	require.NotEmpty(t, res.IdentityToken)
	require.Equal(t, int64(1), f.audit(t, rdb.AuditOpLogin))

	// Reset: the next failures are evaluated again at once.
	for i := 1; i < testCredentialPolicy.Free; i++ {
		_, err := f.mgr.Login(ctx, knownEmail, "wrong")
		require.ErrorIs(t, err, ErrInvalidCredentials)
	}
}

// 🔴 A THROTTLED LOGIN WRITES NO AUDIT ROW AND MAKES NO LOOKUP. The positive control
// comes first: the same counters DO move for an admitted attempt, so their staying
// still is a value, not an absence a broken fixture would also produce.
func TestThrottledLoginWritesNothingAndLooksNothingUp(t *testing.T) {
	f := newCredFixture(t)
	ctx := context.Background()

	_, err := f.mgr.Login(ctx, knownEmail, "wrong")
	require.ErrorIs(t, err, ErrInvalidCredentials)
	require.Equal(t, int64(1), f.audit(t, rdb.AuditOpLoginFailed), "control: an admitted failure writes one row")
	require.Equal(t, int32(1), f.lookups.Load(), "control: an admitted attempt looks the identity up")

	for i := 1; i < testCredentialPolicy.Free; i++ {
		_, _ = f.mgr.Login(ctx, knownEmail, "wrong")
	}
	rows := f.audit(t, rdb.AuditOpLoginFailed)
	lookups := f.lookups.Load()

	_, err = f.mgr.Login(ctx, knownEmail, "wrong")
	_ = throttledFor(t, err)
	assert.Equal(t, rows, f.audit(t, rdb.AuditOpLoginFailed), "a throttled attempt must not write an audit row")
	assert.Equal(t, lookups, f.lookups.Load(), "a throttled attempt must not look the identity up")
}

// An unknown email is throttled on exactly the same schedule as a real one, so the
// throttle is not an existence oracle — and both write the same audit rows.
func TestLoginUnknownAndKnownEmailsBehaveIdentically(t *testing.T) {
	type step struct {
		kind  string
		after time.Duration
	}
	run := func(t *testing.T, email string) ([]step, int64) {
		f := newCredFixture(t)
		var steps []step
		for i := 0; i < 7; i++ {
			_, err := f.mgr.Login(context.Background(), email, "wrong")
			var th *credential.ThrottledError
			switch {
			case errors.As(err, &th):
				steps = append(steps, step{"throttled", th.RetryAfter})
				f.clock.Advance(th.RetryAfter)
			case errors.Is(err, ErrInvalidCredentials):
				steps = append(steps, step{"invalid", 0})
			default:
				t.Fatalf("unexpected: %v", err)
			}
		}
		return steps, f.audit(t, rdb.AuditOpLoginFailed)
	}
	var known, unknown []step
	var knownRows, unknownRows int64
	// Separate subtests, because each fixture is a named in-memory database per test.
	t.Run("known", func(t *testing.T) { known, knownRows = run(t, knownEmail) })
	t.Run("unknown", func(t *testing.T) { unknown, unknownRows = run(t, "nobody@example.com") })
	require.Equal(t, known, unknown)
	require.Equal(t, knownRows, unknownRows)
	require.Contains(t, known, step{"throttled", 2 * time.Second}, "the sequence must actually escalate")
}

// The email is normalized BEFORE it keys the throttle, so case and whitespace
// variants of one address share one count.
func TestLoginThrottleKeysOnTheNormalizedEmail(t *testing.T) {
	f := newCredFixture(t)
	for _, e := range []string{knownEmail, strings.ToUpper(knownEmail), "  " + knownEmail + " "} {
		_, err := f.mgr.Login(context.Background(), e, "wrong")
		require.ErrorIs(t, err, ErrInvalidCredentials)
	}
	_, err := f.mgr.Login(context.Background(), "Owner@Example.com", knownPassword)
	_ = throttledFor(t, err)
}

// 🔴 AN UNREACHABLE ATTEMPT STORE FAILS CLOSED — the correct password does not sign in.
func TestLoginFailsClosedWhenTheAttemptStoreIsDown(t *testing.T) {
	f := newCredFixture(t)
	f.store.Fail = errors.New("nats: connection closed")
	_, err := f.mgr.Login(context.Background(), knownEmail, knownPassword)
	var ue *credential.UnavailableError
	require.Truef(t, errors.As(err, &ue), "got %v", err)
	assert.Equal(t, int64(0), f.audit(t, rdb.AuditOpLogin))
	assert.Equal(t, int32(0), f.lookups.Load())
}

// Login under the per-request budget the GraphQL layer installs (1): the one sign-in
// runs all the way to an issued token, and a second sign-in on the SAME request is
// refused with the budget's error before anything is looked up — while a fresh request
// is evaluated again.
func TestLoginWithinARequestBudget(t *testing.T) {
	f := newCredFixture(t)
	req := credential.WithRequestBudget(context.Background(), 1)

	res, err := f.mgr.Login(req, knownEmail, knownPassword)
	require.NoError(t, err)
	require.NotEmpty(t, res.IdentityToken)
	lookups := f.lookups.Load()

	_, err = f.mgr.Login(req, knownEmail, knownPassword)
	var be *credential.RequestBudgetError
	require.Truef(t, errors.As(err, &be), "got %v", err)
	assert.Equal(t, lookups, f.lookups.Load(), "a refused sign-in looks nothing up")
	assert.Equal(t, int64(1), f.audit(t, rdb.AuditOpLogin), "and writes no audit row")
	assert.Equal(t, int64(0), f.audit(t, rdb.AuditOpLoginFailed))

	_, err = f.mgr.Login(credential.WithRequestBudget(context.Background(), 1), knownEmail, knownPassword)
	require.NoError(t, err, "a new request has its own budget")
}

// A Manager built without a checker refuses to compare at all rather than falling
// back to an unthrottled compare.
func TestLoginWithoutACheckerFailsLoudly(t *testing.T) {
	f := newCredFixture(t)
	bare := NewManager(nil, f.rdbm, nil, nil, 0, 0, "", BootstrapConfig{}, nil)
	_, err := bare.Login(context.Background(), knownEmail, knownPassword)
	require.ErrorIs(t, err, errNoCredentialChecker)
	require.ErrorIs(t, bare.AuthenticateClient(context.Background(), confidentialID, clientSecret, true), errNoCredentialChecker)
}

// ── the OAuth token endpoint ─────────────────────────────────────────────────────

func postToken(t *testing.T, f *credFixture, clientID, secret string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"rt"}}
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if secret != "" {
		req.SetBasicAuth(clientID, secret)
	} else {
		form.Set("client_id", clientID)
		req = httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	rec := httptest.NewRecorder()
	h := TokenHandler(f.mgr.AuthenticateClient,
		func(context.Context, string, string, string, string) (*OAuthTokens, error) {
			return nil, errors.New("unused")
		},
		func(context.Context, string, string, string) (*OAuthTokens, error) {
			return &OAuthTokens{AccessToken: "at", ExpiresIn: 60}, nil
		})
	h.ServeHTTP(rec, req)
	return rec
}

// 🔴 A CONFIDENTIAL CLIENT CANNOT BE LOCKED OUT BY SOMEONE WHO KNOWS ITS client_id.
// Its client_id is public, so a throttle keyed on it would let anyone hold every
// sign-in through the client (Grafana SSO) at 429. Far more wrong secrets than any
// schedule's free allowance, then the correct one, which must succeed at once — and
// the attempt store is never touched, since there is no count to keep.
func TestConfidentialClientCannotBeLockedOut(t *testing.T) {
	f := newCredFixture(t)
	for i := 0; i < 10*testCredentialPolicy.Free; i++ {
		rec := postToken(t, f, confidentialID, "wrong")
		require.Equal(t, http.StatusUnauthorized, rec.Code, "attempt %d: %s", i, rec.Body)
		assert.Contains(t, rec.Body.String(), `"error":"invalid_client"`)
	}
	require.Equal(t, http.StatusOK, postToken(t, f, confidentialID, clientSecret).Code)
	assert.Empty(t, f.store.Ops(), "a client-secret check must not read or write the attempt store")

	// Control: a password check on the same fixture does use the store.
	_, _ = f.mgr.Login(context.Background(), knownEmail, "wrong")
	assert.NotEmpty(t, f.store.Ops())
}

// An unknown client_id is refused as invalid_client however often it is tried, exactly
// as a confidential client's wrong secret is — and it still pays the dummy compare,
// which core/credential's TestUnknownPrincipalPaysARealCompare pins.
func TestTokenEndpointRefusesAnUnknownClient(t *testing.T) {
	f := newCredFixture(t)
	for i := 0; i < 2*testCredentialPolicy.Free; i++ {
		require.Equal(t, http.StatusUnauthorized, postToken(t, f, "no-such-client", "guess").Code)
	}
}

// A KNOWN PUBLIC CLIENT never reaches the checker: far more requests than any free
// allowance all succeed.
func TestPublicClientIsNeverChecked(t *testing.T) {
	f := newCredFixture(t)
	for i := 0; i < 5*testCredentialPolicy.Free; i++ {
		require.Equal(t, http.StatusOK, postToken(t, f, publicID, "").Code, "request %d", i)
	}
}

// Client authentication does not depend on the attempt store, so a broker outage does
// not stop a confidential client — the regression the store would otherwise add.
func TestTokenEndpointDoesNotNeedTheAttemptStore(t *testing.T) {
	f := newCredFixture(t)
	f.store.Fail = errors.New("nats: connection closed")
	require.Equal(t, http.StatusOK, postToken(t, f, confidentialID, clientSecret).Code)
	require.Equal(t, http.StatusUnauthorized, postToken(t, f, confidentialID, "wrong").Code)
}

// The shipped policy for client secrets is Unthrottled. Pinned on its own because the
// fixture above borrows it, so a change to it would change what those tests test.
func TestClientSecretsAreUnthrottled(t *testing.T) {
	assert.Equal(t, credential.Policy{Unthrottled: true}, CredentialPolicies[credential.KindOAuthClient])
}

// ── the authorize form ───────────────────────────────────────────────────────────

// The authorize login form tells a throttled user to wait, and an unavailable check
// that sign-in is down — never "invalid email or password" for either.
func TestAuthorizeFormReportsThrottleAndUnavailable(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want string
	}{
		{"throttled", &credential.ThrottledError{RetryAfter: 4 * time.Second}, "Too many attempts. Try again in 4 seconds."},
		{"unavailable", &credential.UnavailableError{Err: credential.ErrUnavailable}, "Sign-in is temporarily unavailable. Try again shortly."},
		{"wrong password", ErrInvalidCredentials, "Invalid email or password."},
	} {
		t.Run(c.name, func(t *testing.T) {
			form := validParams()
			form.Set("step", "login")
			form.Set("email", "a@b.c")
			form.Set("password", "p")
			rec := postAuthorize(&fakeAuthorizeSvc{client: validClient(), loginErr: c.err}, form)
			require.Equal(t, http.StatusOK, rec.Code)
			assert.Contains(t, rec.Body.String(), c.want)
		})
	}

	// And through the REAL Login: a throttled manager renders the wait message.
	f := newCredFixture(t)
	for i := 0; i < testCredentialPolicy.Free; i++ {
		_, _ = f.mgr.Login(context.Background(), knownEmail, "wrong")
	}
	_, err := f.mgr.Login(context.Background(), knownEmail, knownPassword)
	assert.Equal(t, "Too many attempts. Try again in 1 second.", authorizeLoginError(err))
}
