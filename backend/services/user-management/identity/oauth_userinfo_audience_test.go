// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/stretchr/testify/require"
)

const (
	uiBase     = "https://iot.example.com/api"
	uiMCP      = "https://iot.example.com/api/mcp"
	uiOwn      = uiBase + "/user-management"
	uiOther    = uiBase + "/event-management"
	uiExternal = "https://reports.example.org"
)

type uiObs struct {
	plane   auth.Plane
	class   auth.AudienceClass
	verdict auth.AudienceVerdict
}

type uiRecorder struct {
	mu   sync.Mutex
	seen []uiObs
}

func (r *uiRecorder) Record(p auth.Plane, c auth.AudienceClass, v auth.AudienceVerdict) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, uiObs{p, c, v})
}

func (r *uiRecorder) all() []uiObs {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]uiObs(nil), r.seen...)
}

// userinfoRig is the REAL userinfo handler over a real validator and a real PlanePolicy for
// user-management.
type userinfoRig struct {
	t   *testing.T
	h   http.Handler
	iss *auth.Issuer
	rec *uiRecorder
}

func newUserinfoRig(t *testing.T, mode string) *userinfoRig {
	t.Helper()
	key, err := auth.GenerateKeyPair()
	require.NoError(t, err)
	rec := &uiRecorder{}
	p, err := auth.NewPlanePolicy(config.TokenAudienceConfiguration{
		Enforcement: mode, AreaBaseUrl: uiBase,
		McpResources: []string{uiMCP}, ExternalResources: []string{uiExternal},
	}, "user-management", rec)
	require.NoError(t, err)
	v := auth.NewValidator(&key.PublicKey)
	return &userinfoRig{t: t, rec: rec,
		iss: auth.NewIssuer(key, "https://as.example.com", time.Hour, time.Hour),
		h:   UserinfoHandler(v.Validate, func() *auth.PlanePolicy { return p })}
}

func (r *userinfoRig) token(aud []string) string {
	r.t.Helper()
	if aud == nil {
		tok, err := r.iss.IssueTenantAccess("acme", "a@acme.example", nil, []string{string(auth.AuthorityAll)}, false, "j-1")
		require.NoError(r.t, err)
		return tok.Token
	}
	tok, err := r.iss.IssueOAuthAccess("acme", "a@acme.example", nil, []string{string(auth.AuthorityAll)}, "", aud, false, "client-1", "j-aud")
	require.NoError(r.t, err)
	return tok.Token
}

func (r *userinfoRig) get(bearer string) *httptest.ResponseRecorder {
	return getUserinfo(r.h, bearer)
}

func TestUserinfoEnforceRefusesAnExternalAudience(t *testing.T) {
	for name, tc := range map[string]struct {
		aud   []string
		class auth.AudienceClass
	}{
		"external":   {[]string{uiExternal}, auth.AudExternal},
		"other area": {[]string{uiOther}, auth.AudOtherArea},
		"unlisted":   {[]string{"https://evil.example.net"}, auth.AudUnlisted},
		"empty":      {[]string{""}, auth.AudUnlisted},
	} {
		t.Run(name, func(t *testing.T) {
			r := newUserinfoRig(t, "enforce")
			w := r.get(r.token(tc.aud))
			require.Equal(t, http.StatusUnauthorized, w.Code)
			require.NotContains(t, w.Body.String(), "a@acme.example", "a refusal discloses no identity")
			require.Equal(t, `Bearer error="invalid_token"`, w.Header().Get("WWW-Authenticate"),
				"indistinguishable from an invalid token")
			require.Equal(t, []uiObs{{auth.PlaneUserinfo, tc.class, auth.VerdictRejected}}, r.rec.all())
		})
	}
}

func TestUserinfoObserveAllowsAndCountsExactlyOnce(t *testing.T) {
	r := newUserinfoRig(t, "observe")
	w := r.get(r.token([]string{uiExternal}))
	require.Equal(t, http.StatusOK, w.Code, "observe changes nothing about what is served")
	require.Contains(t, w.Body.String(), "a@acme.example")
	require.Equal(t, []uiObs{{auth.PlaneUserinfo, auth.AudExternal, auth.VerdictWouldReject}}, r.rec.all())
}

// The counterweight: Grafana's aud-less token, a user-management-bound token and an MCP token
// all pass in both modes.
func TestUserinfoAcceptedAudiencesPassInBothModes(t *testing.T) {
	for _, mode := range []string{"enforce", "observe"} {
		for name, tc := range map[string]struct {
			aud   []string
			class auth.AudienceClass
		}{
			"no audience (Grafana)": {nil, auth.AudNone},
			"user-management":       {[]string{uiOwn}, auth.AudOwnArea},
			"mcp":                   {[]string{uiMCP}, auth.AudMCP},
		} {
			t.Run(mode+"/"+name, func(t *testing.T) {
				r := newUserinfoRig(t, mode)
				w := r.get(r.token(tc.aud))
				require.Equal(t, http.StatusOK, w.Code)
				require.Contains(t, w.Body.String(), "a@acme.example")
				require.Equal(t, []uiObs{{auth.PlaneUserinfo, tc.class, auth.VerdictAccepted}}, r.rec.all())
			})
		}
	}
}

// The check runs only after the signature verifies: a forged token is never counted.
func TestUserinfoForgedTokenIsNeverCounted(t *testing.T) {
	r := newUserinfoRig(t, "enforce")
	otherKey, err := auth.GenerateKeyPair()
	require.NoError(t, err)
	forged, err := auth.NewIssuer(otherKey, "https://as.example.com", time.Hour, time.Hour).
		IssueOAuthAccess("acme", "a@acme.example", nil, []string{string(auth.AuthorityAll)}, "", []string{uiExternal}, false, "client-1", "j-f")
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, r.get(forged.Token).Code)
	require.Empty(t, r.rec.all())
}

// No policy source is the CLOSED policy: audience-bound tokens refused, aud-less accepted.
func TestUserinfoWithoutAPolicySourceFailsClosed(t *testing.T) {
	key, err := auth.GenerateKeyPair()
	require.NoError(t, err)
	iss := auth.NewIssuer(key, "https://as.example.com", time.Hour, time.Hour)
	h := UserinfoHandler(auth.NewValidator(&key.PublicKey).Validate, nil)
	bound, err := iss.IssueOAuthAccess("acme", "a@acme.example", nil, []string{string(auth.AuthorityAll)}, "", []string{uiMCP}, false, "c", "j")
	require.NoError(t, err)
	plain, err := iss.IssueTenantAccess("acme", "a@acme.example", nil, []string{string(auth.AuthorityAll)}, false, "j2")
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, getUserinfo(h, bound.Token).Code)
	require.Equal(t, http.StatusOK, getUserinfo(h, plain.Token).Code)
}
