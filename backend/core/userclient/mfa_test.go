// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package userclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// mfaStub answers login with MFA_CHALLENGE (counting how many it minted) and verifyMfa with a token
// for the right code, MFA_CHALLENGE_EXPIRED for a challenge it did not issue, and a plain refusal
// for a wrong code.
type mfaStub struct {
	logins, verifies atomic.Int64
	loginExt         string // extensions JSON for the login error; "" means the standard challenge
}

func (s *mfaStub) handler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.Contains(body.Query, "verifyMfa("):
		s.verifies.Add(1)
		switch {
		case body.Variables["challenge"] != "chal-1":
			fmt.Fprint(w, `{"errors":[{"message":"expired","extensions":{"code":"MFA_CHALLENGE_EXPIRED"}}]}`)
		case body.Variables["code"] != "123456":
			fmt.Fprint(w, `{"errors":[{"message":"invalid code"}]}`)
		default:
			writeData(w, fmt.Sprintf(`{"verifyMfa":{"identityToken":"id-mfa","expiresAt":%q,"superuser":true,"memberships":[]}}`, farFuture()))
		}
	case strings.Contains(body.Query, "login("):
		s.logins.Add(1)
		ext := s.loginExt
		if ext == "" {
			ext = fmt.Sprintf(`{"code":"MFA_CHALLENGE","challenge":"chal-1","expiresAt":%q,"methods":["TOTP","RECOVERY_CODE"]}`,
				time.Now().Add(5*time.Minute).UTC().Format(time.RFC3339))
		}
		fmt.Fprintf(w, `{"errors":[{"message":"second factor","extensions":%s}]}`, ext)
	}
}

func TestLoginReturnsATypedErrorForAnMFAChallenge(t *testing.T) {
	s := &mfaStub{}
	srv := httptest.NewServer(http.HandlerFunc(s.handler))
	defer srv.Close()

	_, err := Login(context.Background(), nil, srv.URL, "a@b.c", "pw")
	var mfa *MfaRequiredError
	if !errors.As(err, &mfa) {
		t.Fatalf("want *MfaRequiredError, got %T %v", err, err)
	}
	if mfa.Challenge != "chal-1" || len(mfa.Methods) != 2 || mfa.ExpiresAt.IsZero() {
		t.Errorf("challenge not carried: %+v", mfa)
	}
}

// A challenge error that carries no challenge is not one a caller could act on: it stays the
// ordinary GraphQLError rather than becoming an MfaRequiredError with nothing to verify.
func TestAnMFAChallengeWithoutAHandleIsNotTyped(t *testing.T) {
	s := &mfaStub{loginExt: `{"code":"MFA_CHALLENGE"}`}
	srv := httptest.NewServer(http.HandlerFunc(s.handler))
	defer srv.Close()
	_, err := Login(context.Background(), nil, srv.URL, "a@b.c", "pw")
	var mfa *MfaRequiredError
	if errors.As(err, &mfa) {
		t.Fatalf("a challenge with no handle must not be typed: %+v", mfa)
	}
	if !AllHaveCode(err, "MFA_CHALLENGE") {
		t.Errorf("want the GraphQLError, got %T %v", err, err)
	}
}

func TestLoginWithOTPCompletesTheSignIn(t *testing.T) {
	s := &mfaStub{}
	srv := httptest.NewServer(http.HandlerFunc(s.handler))
	defer srv.Close()

	ia, err := LoginWithOTP(context.Background(), nil, srv.URL, "a@b.c", "pw",
		func(_ context.Context, c *MfaRequiredError) (string, error) {
			if c.Challenge != "chal-1" {
				t.Errorf("OTP func got challenge %q", c.Challenge)
			}
			return "123456", nil
		})
	if err != nil || ia.IdentityToken != "id-mfa" {
		t.Fatalf("got %+v, %v", ia, err)
	}

	// A nil OTP function is Login: the typed error, and verifyMfa is never called.
	before := s.verifies.Load()
	_, err = LoginWithOTP(context.Background(), nil, srv.URL, "a@b.c", "pw", nil)
	var mfa *MfaRequiredError
	if !errors.As(err, &mfa) || s.verifies.Load() != before {
		t.Fatalf("nil OTP func: %v (verifies %d -> %d)", err, before, s.verifies.Load())
	}

	// The OTP function's own error is returned as it is, and nothing is verified.
	boom := errors.New("no terminal")
	_, err = LoginWithOTP(context.Background(), nil, srv.URL, "a@b.c", "pw",
		func(context.Context, *MfaRequiredError) (string, error) { return "", boom })
	if !errors.Is(err, boom) || s.verifies.Load() != before {
		t.Fatalf("OTP func error: %v", err)
	}

	// A wrong code is a refusal, not a typed challenge.
	_, err = LoginWithOTP(context.Background(), nil, srv.URL, "a@b.c", "pw",
		func(context.Context, *MfaRequiredError) (string, error) { return "000000", nil })
	if err == nil || errors.As(err, &mfa) {
		t.Fatalf("wrong code: %v", err)
	}
}

// T16: a session with no OTP function fails in ONE call; it never signs in again to see if the
// challenge went away. A session with one signs in.
func TestSessionsDoNotLoopOnAChallenge(t *testing.T) {
	s := &mfaStub{}
	srv := httptest.NewServer(http.HandlerFunc(s.handler))
	defer srv.Close()

	var mfa *MfaRequiredError
	admin := NewAdminSession(nil, srv.URL, "a@b.c", "pw")
	if _, err := admin.Superuser(context.Background()); !errors.As(err, &mfa) {
		t.Fatalf("admin: %v", err)
	}
	if s.logins.Load() != 1 {
		t.Errorf("admin session made %d logins, want 1", s.logins.Load())
	}
	tenant := NewTenantSession(nil, srv.URL, "a@b.c", "pw", "acme")
	if _, err := tenant.AccessToken(context.Background()); !errors.As(err, &mfa) {
		t.Fatalf("tenant: %v", err)
	}
	if s.logins.Load() != 2 {
		t.Errorf("tenant session made %d logins in total, want 2", s.logins.Load())
	}

	ok := NewAdminSession(nil, srv.URL, "a@b.c", "pw").
		WithOTP(func(context.Context, *MfaRequiredError) (string, error) { return "123456", nil })
	if su, err := ok.Superuser(context.Background()); err != nil || !su {
		t.Fatalf("with an OTP function: %v %v", su, err)
	}
}
