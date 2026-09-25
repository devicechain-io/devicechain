// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// A refresh reads everything it needs BEFORE it consumes the token, so what happens to the
// token depends on why a refresh failed:
//
//   - a store error leaves it unconsumed, and the same token works once the store is back;
//   - a definite denial still burns it, so it cannot come back to life.
//
// These run on the real paths (sessionEnv: SQLite store, real RSA issuer and validator,
// refresh tokens in a real JetStream KV bucket), so the claim is the revision-checked
// delete production uses.

// breakIdentityStore makes every identity read fail with a database error, as a database
// that has gone away does, and returns the repair.
func (e *sessionEnv) breakIdentityStore(t *testing.T) (repair func()) {
	t.Helper()
	// SQLite ":memory:" gives every pooled connection its own empty database, so a second
	// connection would fail for the wrong reason and the repair could land elsewhere.
	sqlDB, err := e.db().DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, e.db().Exec("ALTER TABLE iam_identities RENAME TO iam_identities_away").Error)
	return func() {
		require.NoError(t, e.db().Exec("ALTER TABLE iam_identities_away RENAME TO iam_identities").Error)
	}
}

// 🔴 THE DEFECT. The token used to be claimed first, so a database blip while the grant was
// re-checked burned it: the refresh failed as "invalid or expired token", and retrying the
// same token after the blip failed the same way. The user was signed out by an outage they
// could not see.
func TestARefreshThatHitsAStoreErrorDoesNotConsumeTheToken(t *testing.T) {
	e := newSessionEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	_, pair := e.login(t, "pat@example.com", "pw-1")

	repair := e.breakIdentityStore(t)
	_, err := e.m.Refresh(context.Background(), pair.RefreshToken)
	require.ErrorIs(t, err, ErrRefreshUnavailable,
		"a store error must be reported as retryable, not as an invalid token")
	require.NotErrorIs(t, err, ErrInvalidToken)
	// The GraphQL mutation returns this text to the caller verbatim.
	require.NotContains(t, strings.ToLower(err.Error()), "iam_identities",
		"the refresh error leaks the database's own error text to the caller")
	repair()

	_, err = e.m.Refresh(context.Background(), pair.RefreshToken)
	require.NoError(t, err, "the same refresh token must still work once the store is back")
}

// The same on the OAuth refresh grant: a server_error, the cause not in the description,
// and the same token redeemable afterwards.
func TestAnOAuthRefreshThatHitsAStoreErrorDoesNotConsumeTheToken(t *testing.T) {
	e := newSessionEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	idTok, _ := e.login(t, "pat@example.com", "pw-1")
	toks := e.oauthSession(t, idTok)

	repair := e.breakIdentityStore(t)
	_, err := e.refreshOAuth(toks.RefreshToken)
	var oe *oauthError
	require.True(t, errors.As(err, &oe), "want an OAuth error, got %v", err)
	require.Equal(t, "server_error", oe.Code, "a store error must not read as a revoked grant")
	require.NotContains(t, strings.ToLower(oe.Desc), "iam_identities",
		"the token endpoint leaks the database's own error text to the client")
	repair()

	_, err = e.refreshOAuth(toks.RefreshToken)
	require.NoError(t, err, "the same OAuth refresh token must still work once the store is back")
}

// The counterweight to both: reading first must not let a DENIED refresh leave its token
// alive. The membership is removed, the refresh is denied; the membership is restored, and
// the same token is still refused. Without the burn, the token would revive as soon as the
// membership came back.
func TestADeniedRefreshStillBurnsTheToken(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	e.createMember(t, "pat@example.com", "pw-1")
	_, pair := e.login(t, "pat@example.com", "pw-1")

	_, err := e.admin.RemoveMembership(ctx, "pat@example.com", "acme")
	require.NoError(t, err)
	_, err = e.m.Refresh(ctx, pair.RefreshToken)
	require.ErrorIs(t, err, ErrInvalidToken, "a refresh by a non-member must be denied")

	_, err = e.admin.AddMembership(ctx, "pat@example.com", "acme", nil)
	require.NoError(t, err)
	_, err = e.m.Refresh(ctx, pair.RefreshToken)
	require.ErrorIs(t, err, ErrInvalidToken,
		"a token whose refresh was DENIED came back to life when the membership was restored")

	// And the membership really is usable again: a fresh sign-in refreshes fine, so the
	// refusal above is the burned token, not a membership that did not come back.
	_, fresh := e.login(t, "pat@example.com", "pw-1")
	_, err = e.m.Refresh(ctx, fresh.RefreshToken)
	require.NoError(t, err)
}

// The OAuth grant burns on a denial the same way.
func TestADeniedOAuthRefreshStillBurnsTheToken(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	e.createMember(t, "pat@example.com", "pw-1")
	idTok, _ := e.login(t, "pat@example.com", "pw-1")
	toks := e.oauthSession(t, idTok)

	_, err := e.admin.SetMembershipEnabled(ctx, "pat@example.com", "acme", false)
	require.NoError(t, err)
	_, err = e.refreshOAuth(toks.RefreshToken)
	var oe *oauthError
	require.True(t, errors.As(err, &oe), "want an OAuth error, got %v", err)
	require.Equal(t, "invalid_grant", oe.Code)

	_, err = e.admin.SetMembershipEnabled(ctx, "pat@example.com", "acme", true)
	require.NoError(t, err)
	_, err = e.refreshOAuth(toks.RefreshToken)
	require.True(t, errors.As(err, &oe), "want an OAuth error, got %v", err)
	require.Equal(t, "invalid_grant", oe.Code,
		"an OAuth token whose refresh was DENIED came back to life when the membership was re-enabled")
}

// Reading first widens the window between reading the token and claiming it, so the
// single-use rotation has to hold under contention: of N concurrent refreshes of one
// token, exactly one mints a pair and every other one is refused as an invalid token.
func TestConcurrentRefreshesMintExactlyOnce(t *testing.T) {
	e := newSessionEnv(t)
	// The store is SQLite ":memory:", where every pooled connection opens its OWN empty
	// database; concurrent reads would each get a fresh connection and find no tables.
	// One connection keeps them on the database this test seeded.
	sqlDB, err := e.db().DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	e.createMember(t, "pat@example.com", "pw-1")
	_, pair := e.login(t, "pat@example.com", "pw-1")

	const n = 10
	var wg sync.WaitGroup
	errs := make([]error, n)
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = e.m.Refresh(context.Background(), pair.RefreshToken)
		}()
	}
	close(start)
	wg.Wait()

	wins := 0
	for _, err := range errs {
		if err == nil {
			wins++
			continue
		}
		require.ErrorIs(t, err, ErrInvalidToken, "a losing concurrent refresh must be an invalid token")
	}
	require.Equal(t, 1, wins, "exactly one concurrent refresh of one token may mint a pair")
}
