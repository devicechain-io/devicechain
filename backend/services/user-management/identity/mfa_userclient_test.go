// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/userclient"
	"github.com/stretchr/testify/require"
)

// managerOverGraphQLShape serves the REAL Manager's login and verifyMfa in the GraphQL response
// envelope the service answers with (errors carry the error's own Extensions(), exactly as
// graphql-go copies them), so the Go client is driven by what the Manager actually produces and
// not by a hand-written stub of it. The served schema itself is exercised in the main package.
func managerOverGraphQLShape(t *testing.T, m *Manager, requests *atomic.Int64) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		str := func(k string) string { s, _ := body.Variables[k].(string); return s }
		var (
			ia  *IdentityAuth
			err error
			op  string
		)
		switch {
		case strings.Contains(body.Query, "verifyMfa("):
			op = "verifyMfa"
			ia, err = m.VerifyMfa(context.Background(), str("challenge"), str("code"), "192.0.2.1")
		case strings.Contains(body.Query, "login("):
			op = "login"
			ia, err = m.Login(context.Background(), str("email"), str("password"))
		default:
			t.Errorf("unexpected operation %q", body.Query)
		}
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			e := map[string]any{"message": err.Error()}
			var coded interface{ Extensions() map[string]any }
			if errors.As(err, &coded) {
				e["extensions"] = coded.Extensions()
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": []any{e}, "data": nil})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{op: map[string]any{
			"identityToken": ia.IdentityToken, "expiresAt": ia.ExpiresAt.UTC().Format(time.RFC3339),
			"superuser": ia.Superuser, "memberships": []any{},
		}}})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// T16. The Go client against the real Manager: the typed error, one request and no loop for a
// session with no way to answer a challenge, and a completed sign-in (a token the validator accepts,
// amr recording the second factor) when the client can.
func TestUserclientSignsInThroughTheSecondFactor(t *testing.T) {
	e := newMfaEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	_, recovery := e.enroll(t, "pat@example.com", "pw-1")
	var requests atomic.Int64
	url := managerOverGraphQLShape(t, e.m, &requests)
	ctx := context.Background()

	_, err := userclient.Login(ctx, nil, url, "pat@example.com", "pw-1")
	var mfa *userclient.MfaRequiredError
	require.True(t, errors.As(err, &mfa), "got %T %v", err, err)
	require.NotEmpty(t, mfa.Challenge)
	require.Equal(t, []string{"TOTP", "RECOVERY_CODE"}, mfa.Methods)
	require.False(t, mfa.ExpiresAt.IsZero())

	// No OTP function: the session fails in one call. A loop would mint challenge after challenge.
	challengesBefore := e.auditCount(t, "mfa_challenged")
	requests.Store(0)
	sess := userclient.NewAdminSession(nil, url, "pat@example.com", "pw-1")
	_, err = sess.Superuser(ctx)
	require.True(t, errors.As(err, &mfa), "got %v", err)
	require.EqualValues(t, 1, requests.Load())
	require.Equal(t, challengesBefore+1, e.auditCount(t, "mfa_challenged"), "exactly one challenge was minted")

	// With one: the sign-in completes and the token records the second factor.
	var asked int
	ia, err := userclient.LoginWithOTP(ctx, nil, url, "pat@example.com", "pw-1",
		func(_ context.Context, c *userclient.MfaRequiredError) (string, error) {
			asked++
			require.NotEmpty(t, c.Challenge)
			return recovery[0], nil
		})
	require.NoError(t, err)
	require.Equal(t, 1, asked)
	require.Equal(t, []string{"pwd", "mfa"}, e.claims(t, ia.IdentityToken).Amr)

	// The same code twice: the second sign-in is refused (single use), and the client says so
	// without a code of its own that could be mistaken for an expired challenge.
	_, err = userclient.LoginWithOTP(ctx, nil, url, "pat@example.com", "pw-1",
		func(context.Context, *userclient.MfaRequiredError) (string, error) { return recovery[0], nil })
	require.Error(t, err)
	require.False(t, errors.As(err, &mfa))

	// A session with an OTP function signs in.
	sess = userclient.NewAdminSession(nil, url, "pat@example.com", "pw-1").
		WithOTP(func(context.Context, *userclient.MfaRequiredError) (string, error) { return recovery[1], nil })
	_, err = sess.Superuser(ctx)
	require.NoError(t, err)
}
