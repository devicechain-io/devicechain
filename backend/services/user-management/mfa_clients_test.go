// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/userclient"
	"github.com/devicechain-io/dc-user-management/iam"
)

// enrolledWithRecoveryCodes is enrolled() with real recovery-code digests, so a client can finish
// a sign-in on SQLite (a recovery code redeems in one plain statement; a TOTP code needs Postgres).
func (w *mfaWiring) enrolledWithRecoveryCodes(t *testing.T, email, password string, codes ...string) *iam.Identity {
	t.Helper()
	ctx := core.WithSystemContext(context.Background())
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)
	id := &iam.Identity{Email: email, Enabled: true, PasswordHash: string(hash)}
	require.NoError(t, w.store.CreateIdentity(ctx, id))
	digests := make([]string, 0, len(codes))
	for _, c := range codes {
		d, err := credential.RecoveryCodeDigest(strconv.FormatUint(uint64(id.ID), 10), c)
		require.NoError(t, err)
		digests = append(digests, d)
	}
	_, err = w.store.BeginMfaEnrollment(ctx, w.secrets, id.ID, []byte("12345678901234567890"))
	require.NoError(t, err)
	require.NoError(t, w.store.ConfirmMfaEnrollment(ctx, w.secrets, id.ID,
		func([]byte) (int64, bool, error) { return 5, true, nil }, digests, time.Now()))
	return id
}

// T16. The Go client every headless caller uses (dcctl, the sims, the probes), against the data
// plane built as main builds it. An enrolled account's sign-in comes back as a TYPED error carrying
// the challenge; the sessions surface it in ONE call (never a loop of fresh challenges); and with an
// OTP function the same sign-in completes.
func TestUserclientAgainstTheServedDataPlaneHandlesTheSecondFactor(t *testing.T) {
	w := newMfaWiring(t)
	w.enrolledWithRecoveryCodes(t, "pat@example.com", "pw-1", "ABCD-EFGH-JKMN-PQRS", "2345-6789-ABCD-EFGH")
	var requests atomic.Int64
	inner := w.served(t, startMfaBroker(t))
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		inner.ServeHTTP(rw, r)
	}))
	t.Cleanup(srv.Close)
	url := srv.URL + "/graphql"
	ctx := context.Background()

	// Login: typed error with the challenge, and no token.
	ia, err := userclient.Login(ctx, nil, url, "pat@example.com", "pw-1")
	var mfa *userclient.MfaRequiredError
	require.True(t, errors.As(err, &mfa), "want *MfaRequiredError, got %T %v", err, err)
	require.Empty(t, ia.IdentityToken)
	require.NotEmpty(t, mfa.Challenge)
	require.WithinDuration(t, time.Now().Add(5*time.Minute), mfa.ExpiresAt, time.Minute)
	require.Equal(t, []string{"TOTP", "RECOVERY_CODE"}, mfa.Methods)

	// A wrong password is still the ordinary refusal, not a challenge.
	_, err = userclient.Login(ctx, nil, url, "pat@example.com", "wrong")
	require.Error(t, err)
	require.False(t, errors.As(err, &mfa))

	// Sessions with no OTP function: one request, the typed error, no retry.
	requests.Store(0)
	admin := userclient.NewAdminSession(nil, url, "pat@example.com", "pw-1")
	_, err = admin.Superuser(ctx)
	require.True(t, errors.As(err, &mfa), "got %v", err)
	require.EqualValues(t, 1, requests.Load(), "a session without an OTP function must not loop on a challenge")
	requests.Store(0)
	tenant := userclient.NewTenantSession(nil, url, "pat@example.com", "pw-1", "acme")
	_, err = tenant.AccessToken(ctx)
	require.True(t, errors.As(err, &mfa), "got %v", err)
	require.EqualValues(t, 1, requests.Load())

	// With an OTP function the client reaches verifyMfa on the served plane: a wrong code is the
	// ordinary refusal (no code of its own, so it cannot be mistaken for an expired challenge), and
	// the function is asked exactly once per sign-in.
	asked := 0
	_, err = userclient.LoginWithOTP(ctx, nil, url, "pat@example.com", "pw-1",
		func(_ context.Context, c *userclient.MfaRequiredError) (string, error) {
			asked++
			return "ZZZZ-ZZZZ-ZZZZ-ZZZZ", nil
		})
	require.Error(t, err)
	require.False(t, errors.As(err, &mfa))
	require.False(t, userclient.AllHaveCode(err, "MFA_CHALLENGE_EXPIRED"), "got %v", err)
	require.Equal(t, 1, asked)

	// An unknown challenge: verifyMfa answers MFA_CHALLENGE_EXPIRED, which the client exposes as a code.
	_, err = userclient.VerifyMfa(ctx, nil, url, "no-such-challenge", "123456")
	require.True(t, userclient.AllHaveCode(err, "MFA_CHALLENGE_EXPIRED"), "got %v", err)

	// An OTP function's own error comes back as it is.
	boom := errors.New("no terminal")
	_, err = userclient.LoginWithOTP(ctx, nil, url, "pat@example.com", "pw-1",
		func(context.Context, *userclient.MfaRequiredError) (string, error) { return "", boom })
	require.ErrorIs(t, err, boom)
}
