// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/require"
)

// realAuthorize is the REAL Manager serving the authorize page, with only the client lookup
// replaced (it needs an OAuth client row these tests have no use for).
type realAuthorize struct{ *Manager }

func (realAuthorize) ResolveAuthorizeClient(context.Context, string, string) (*iam.OAuthClient, error) {
	return validClient(), nil
}

func authorizeForm(step string, extra url.Values) url.Values {
	f := validParams()
	f.Set("step", step)
	for k, v := range extra {
		f[k] = v
	}
	return f
}

var challengeField = regexp.MustCompile(`name="challenge" value="([^"]+)"`)

// signInToMfaStep drives the login step of the page for an enrolled account and returns the
// challenge the rendered form carries.
func signInToMfaStep(t *testing.T, svc AuthorizeService, email, password string) string {
	t.Helper()
	rec := postAuthorize(svc, authorizeForm("login", url.Values{"email": {email}, "password": {password}}))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, `name="step" value="mfa"`, "an enrolled account's password step must ask for the code")
	require.NotContains(t, body, `name="identity_token"`, "no token before the second factor")
	m := challengeField.FindStringSubmatch(body)
	require.NotNil(t, m, "the form must carry the challenge")
	return m[1]
}

// T15 / T16 (page). The MCP authorize page, served by the REAL Manager, takes an enrolled
// account through the password step, asks for the code, and only a correct one reaches consent.
// A wrong code keeps the same form; the challenge never appears in an address.
func TestTheAuthorizePageCompletesASecondFactorSignIn(t *testing.T) {
	e := newMfaEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	_, recovery := e.enroll(t, "pat@example.com", "pw-1")
	svc := realAuthorize{e.m}

	challenge := signInToMfaStep(t, svc, "pat@example.com", "pw-1")

	// The step carries the challenge in the body only: a GET of it, even with the challenge in
	// the address, shows the sign-in form and does not spend or honour it.
	q := validParams()
	q.Set("step", "mfa")
	q.Set("challenge", challenge)
	q.Set("code", recovery[0])
	got := getAuthorize(svc, q)
	require.Equal(t, http.StatusOK, got.Code)
	require.Contains(t, got.Body.String(), `name="step" value="login"`)
	require.NotContains(t, got.Body.String(), "identity_token")
	require.NotContains(t, got.Body.String(), challenge, "the challenge is never reflected from an address")

	// A wrong code: the same form again, the challenge still usable.
	rec := postAuthorize(svc, authorizeForm("mfa", url.Values{"challenge": {challenge}, "code": {"ABCD-EFGH-JKMN-PQRS"}}))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "That code is not valid")
	require.Contains(t, rec.Body.String(), `name="step" value="mfa"`)
	require.NotContains(t, rec.Body.String(), "identity_token")

	// The right code: the consent page, carrying an identity token.
	rec = postAuthorize(svc, authorizeForm("mfa", url.Values{"challenge": {challenge}, "code": {recovery[0]}}))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `name="identity_token"`)
	require.Contains(t, rec.Body.String(), `name="step" value="consent"`)
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
}

// T15. One challenge signs in once; a spent one sends the person back to the password step.
func TestTheAuthorizePageSendsASpentChallengeBackToSignIn(t *testing.T) {
	e := newMfaEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	_, recovery := e.enroll(t, "pat@example.com", "pw-1")
	svc := realAuthorize{e.m}
	challenge := signInToMfaStep(t, svc, "pat@example.com", "pw-1")

	ok := postAuthorize(svc, authorizeForm("mfa", url.Values{"challenge": {challenge}, "code": {recovery[0]}}))
	require.Contains(t, ok.Body.String(), `name="identity_token"`)

	again := postAuthorize(svc, authorizeForm("mfa", url.Values{"challenge": {challenge}, "code": {recovery[1]}}))
	require.Contains(t, again.Body.String(), "Your sign-in expired")
	require.Contains(t, again.Body.String(), `name="step" value="login"`)
	require.NotContains(t, again.Body.String(), "identity_token")

	// A made-up challenge is the same answer: nothing distinguishes it from a spent one.
	bogus := postAuthorize(svc, authorizeForm("mfa", url.Values{"challenge": {"nope"}, "code": {recovery[2]}}))
	require.Contains(t, bogus.Body.String(), "Your sign-in expired")
}

// A request a browser marks cross-site is refused before it spends an attempt.
func TestTheAuthorizeMfaStepRefusesACrossSitePost(t *testing.T) {
	e := newMfaEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	_, recovery := e.enroll(t, "pat@example.com", "pw-1")
	svc := realAuthorize{e.m}
	challenge := signInToMfaStep(t, svc, "pat@example.com", "pw-1")

	form := authorizeForm("mfa", url.Values{"challenge": {challenge}, "code": {recovery[0]}})
	req := httptest.NewRequest(http.MethodPost, AuthorizePath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	AuthorizeHandler(svc).ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.NotContains(t, rec.Body.String(), "identity_token")

	// The control: the same request from the same site works, and the cross-site one spent nothing.
	ok := postAuthorize(svc, authorizeForm("mfa", url.Values{"challenge": {challenge}, "code": {recovery[0]}}))
	require.Contains(t, ok.Body.String(), `name="identity_token"`)
}

// The page's messages for the refusals verifyMfa can answer with, through a fake service.
func TestTheAuthorizeMfaStepMessages(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		contains string
		step     string
	}{
		{"invalid", ErrMfaInvalidCode, "That code is not valid", "mfa"},
		{"rate limited", ErrMfaRateLimited, "Too many attempts", "mfa"},
		{"unavailable", ErrMfaUnavailable, "could not be checked", "mfa"},
		{"credential unavailable", credential.ErrUnavailable, "could not be checked", "mfa"},
		{"expired", ErrMfaChallengeExpired, "Your sign-in expired", "login"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc := &fakeAuthorizeSvc{client: validClient(), mfaErr: c.err}
			rec := postAuthorize(svc, authorizeForm("mfa", url.Values{"challenge": {"ch"}, "code": {"123456"}}))
			require.Equal(t, http.StatusOK, rec.Code)
			require.Contains(t, rec.Body.String(), c.contains)
			require.Contains(t, rec.Body.String(), `name="step" value="`+c.step+`"`)
			require.Equal(t, "ch", svc.mfaGot[0])
			require.Equal(t, "123456", svc.mfaGot[1])
		})
	}
	// An account challenged at the password step gets the code form, not an error line.
	svc := &fakeAuthorizeSvc{client: validClient(), loginErr: &MfaChallengeError{Challenge: "<c&h>"}}
	rec := postAuthorize(svc, authorizeForm("login", url.Values{"email": {"a@b.c"}, "password": {"x"}}))
	require.Contains(t, rec.Body.String(), `name="step" value="mfa"`)
	require.NotContains(t, rec.Body.String(), "<c&h>", "the challenge is escaped into its attribute")
	require.NotContains(t, rec.Body.String(), "Invalid email or password")
}
