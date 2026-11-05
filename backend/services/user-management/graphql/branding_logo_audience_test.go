// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"bytes"
	"io"
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
	audBase     = "https://iot.example.com/api"
	audMCP      = "https://iot.example.com/api/mcp"
	audOwn      = audBase + "/user-management"
	audOther    = audBase + "/event-management"
	audExternal = "https://reports.example.org"
)

type obs struct {
	plane   auth.Plane
	class   auth.AudienceClass
	verdict auth.AudienceVerdict
}

// recorder captures every observation, so a test can assert one per request and none for a
// token that never authenticated.
type recorder struct {
	mu   sync.Mutex
	seen []obs
}

func (r *recorder) Record(p auth.Plane, c auth.AudienceClass, v auth.AudienceVerdict) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, obs{p, c, v})
}

func (r *recorder) all() []obs {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]obs(nil), r.seen...)
}

func audPolicy(t *testing.T, mode string) (PolicySource, *recorder) {
	t.Helper()
	rec := &recorder{}
	p, err := auth.NewPlanePolicy(config.TokenAudienceConfiguration{
		Enforcement: mode, AreaBaseUrl: audBase,
		McpResources: []string{audMCP}, ExternalResources: []string{audExternal},
	}, "user-management", rec)
	require.NoError(t, err)
	return func() *auth.PlanePolicy { return p }, rec
}

func logoToken(t *testing.T, iss *auth.Issuer, aud []string) string {
	t.Helper()
	if aud == nil {
		tok, err := iss.IssueTenantAccess("acme", "a@b.c", nil, []string{string(auth.BrandingWrite)}, false, "j-logo")
		require.NoError(t, err)
		return tok.Token
	}
	tok, err := iss.IssueOAuthAccess("acme", "a@b.c", nil, []string{string(auth.BrandingWrite)}, "", aud, false, "client-1", "j-aud")
	require.NoError(t, err)
	return tok.Token
}

var tinyPNG = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 2<<10)...)

// panicBody fails the test if the handler reads the body.
type panicBody struct{ t *testing.T }

func (b panicBody) Read([]byte) (int, error) {
	b.t.Error("the request body was read before the request was admitted")
	return 0, io.EOF
}

// The refusal precedes the body: the REAL handler, driven directly so the body is the
// handler's to read (over a socket the client would read it).
func TestBrandingEnforceRefusalDoesNotReadTheBody(t *testing.T) {
	key, err := auth.GenerateKeyPair()
	require.NoError(t, err)
	src, rec := audPolicy(t, "enforce")
	h := &BrandingLogoHandler{validator: auth.NewValidator(&key.PublicKey), policy: src}
	iss := auth.NewIssuer(key, "https://as.example.com", time.Hour, time.Hour)

	req := httptest.NewRequest(http.MethodPost, brandingLogoPath, io.NopCloser(panicBody{t}))
	req.Header.Set("Authorization", "Bearer "+logoToken(t, iss, []string{audExternal}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Equal(t, []obs{{auth.PlaneBranding, auth.AudExternal, auth.VerdictRejected}}, rec.all())
}

func do(t *testing.T, ts *httptest.Server, method, bearer string, body io.Reader) int {
	t.Helper()
	req, err := http.NewRequest(method, ts.URL+brandingLogoPath, body)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	return resp.StatusCode
}

func TestBrandingEnforceRefusesAnAudienceThePlaneDoesNotAccept(t *testing.T) {
	for name, aud := range map[string][]string{
		"external":   {audExternal},
		"other area": {audOther},
		"unlisted":   {"https://evil.example.net"},
		"empty":      {""},
	} {
		for _, method := range []string{http.MethodPost, http.MethodGet} {
			t.Run(name+"/"+method, func(t *testing.T) {
				src, rec := audPolicy(t, "enforce")
				ts, iss := logoServerWithPolicy(t, src)
				var body io.Reader
				if method == http.MethodPost {
					body = bytes.NewReader(tinyPNG)
				}
				require.Equal(t, http.StatusUnauthorized, do(t, ts, method, logoToken(t, iss, aud), body))
				require.Equal(t, []obs{{auth.PlaneBranding, classOf(t, src, aud), auth.VerdictRejected}}, rec.all())
			})
		}
	}
}

func classOf(t *testing.T, src PolicySource, aud []string) auth.AudienceClass {
	t.Helper()
	return src().Classify(aud)
}

func TestBrandingObserveAllowsAndCountsExactlyOnce(t *testing.T) {
	src, rec := audPolicy(t, "observe")
	ts, iss := logoServerWithPolicy(t, src)
	// Served behaviour is unchanged: the upload is stored.
	require.Equal(t, http.StatusOK, do(t, ts, http.MethodPost, logoToken(t, iss, []string{audExternal}), bytes.NewReader(tinyPNG)))
	require.Equal(t, []obs{{auth.PlaneBranding, auth.AudExternal, auth.VerdictWouldReject}}, rec.all())
}

// The counterweight: what the plane accepts passes in BOTH modes.
func TestBrandingAcceptedAudiencesPassInBothModes(t *testing.T) {
	for _, mode := range []string{"enforce", "observe"} {
		for name, tc := range map[string]struct {
			aud   []string
			class auth.AudienceClass
		}{
			"no audience": {nil, auth.AudNone},
			"own area":    {[]string{audOwn}, auth.AudOwnArea},
			"mcp":         {[]string{audMCP}, auth.AudMCP},
		} {
			t.Run(mode+"/"+name, func(t *testing.T) {
				src, rec := audPolicy(t, mode)
				ts, iss := logoServerWithPolicy(t, src)
				require.Equal(t, http.StatusOK, do(t, ts, http.MethodPost, logoToken(t, iss, tc.aud), bytes.NewReader(tinyPNG)))
				require.Equal(t, []obs{{auth.PlaneBranding, tc.class, auth.VerdictAccepted}}, rec.all())
			})
		}
	}
}

// The check runs only after the signature verifies: a forged token is never counted.
func TestBrandingForgedTokenIsNeverCounted(t *testing.T) {
	src, rec := audPolicy(t, "enforce")
	ts, _ := logoServerWithPolicy(t, src)
	otherKey, err := auth.GenerateKeyPair()
	require.NoError(t, err)
	forged := logoToken(t, auth.NewIssuer(otherKey, "https://as.example.com", time.Hour, time.Hour), []string{audExternal})
	require.Equal(t, http.StatusUnauthorized, do(t, ts, http.MethodPost, forged, bytes.NewReader(tinyPNG)))
	require.Empty(t, rec.all())
}

// A registration with no policy source is the CLOSED policy: audience-bound tokens are
// refused, first-party ones still pass.
func TestBrandingWithoutAPolicySourceFailsClosed(t *testing.T) {
	ts, iss := logoServerWithPolicy(t, nil)
	require.Equal(t, http.StatusUnauthorized, do(t, ts, http.MethodPost, logoToken(t, iss, []string{audMCP}), bytes.NewReader(tinyPNG)))
	require.Equal(t, http.StatusOK, do(t, ts, http.MethodPost, logoToken(t, iss, nil), bytes.NewReader(tinyPNG)))
}
