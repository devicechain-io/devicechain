// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package upload

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-update-management/model"
	"github.com/stretchr/testify/require"
)

const (
	audBase     = "https://iot.example.com/api"
	audMCP      = "https://iot.example.com/api/mcp"
	audOwn      = audBase + "/update-management"
	audOther    = audBase + "/event-management"
	audExternal = "https://reports.example.org"
)

// obs is one recorded observation.
type obs struct {
	plane   auth.Plane
	class   auth.AudienceClass
	verdict auth.AudienceVerdict
}

// recorder captures every observation the policy makes, so a test can assert it counted
// exactly once, and nothing at all for a token that never authenticated.
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

// withPolicy installs a real PlanePolicy for update-management in the given mode on the
// handler under test and returns the recorder behind it.
func withPolicy(t *testing.T, e *env, mode string) *recorder {
	t.Helper()
	rec := &recorder{}
	p, err := auth.NewPlanePolicy(config.TokenAudienceConfiguration{
		Enforcement: mode, AreaBaseUrl: audBase,
		McpResources: []string{audMCP}, ExternalResources: []string{audExternal},
	}, "update-management", rec)
	require.NoError(t, err)
	e.h.policy = func() *auth.PlanePolicy { return p }
	return rec
}

// audToken mints a tenant access token with ArtifactWrite that carries aud (nil mints a
// first-party, aud-less one).
func (e *env) audToken(aud []string) string {
	e.t.Helper()
	if aud == nil {
		return e.writer("acme")
	}
	tok, err := e.issuer.IssueOAuthAccess("acme", "a@acme.example", nil, []string{string(auth.ArtifactWrite)},
		"", aud, false, "client-1", "j-aud")
	require.NoError(e.t, err)
	return tok.Token
}

// refusedUnread drives the REAL handler with a body that fails the test if it is read, and
// returns the status.
func refusedUnread(t *testing.T, e *env, bearer, artifact string, size int64) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/updates/artifacts/"+artifact+"/content", panicBody{t})
	req.SetPathValue("token", artifact)
	req.ContentLength = size
	req.Header.Set("Authorization", "Bearer "+bearer)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec.Code
}

func TestUploadEnforceRefusesAnAudienceThePlaneDoesNotAccept(t *testing.T) {
	for name, aud := range map[string][]string{
		"external":   {audExternal},
		"other area": {audOther},
		"unlisted":   {"https://evil.example.net"},
		"empty":      {""},
		"mixed":      {audExternal, "https://evil.example.net"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			rec := withPolicy(t, e, "enforce")
			content := []byte("0123456789")
			e.declare("acme", "fw-1", content)

			require.Equal(t, http.StatusUnauthorized, refusedUnread(t, e, e.audToken(aud), "fw-1", int64(len(content))))
			got := rec.all()
			require.Len(t, got, 1, "exactly one observation")
			require.Equal(t, auth.PlaneUpload, got[0].plane)
			require.Equal(t, auth.VerdictRejected, got[0].verdict)
			require.Empty(t, e.objects())
			require.Equal(t, model.StateUploading, e.state("acme", "fw-1").State)
		})
	}
}

func TestUploadObserveAllowsAndCountsExactlyOnce(t *testing.T) {
	e := newEnv(t)
	rec := withPolicy(t, e, "observe")
	content := bytes.Repeat([]byte("fw"), 5000)
	e.declare("acme", "fw-1", content)

	// Served behaviour is unchanged: the upload completes end to end.
	status(t, e.put(e.audToken([]string{audExternal}), "fw-1", bytes.NewReader(content)), http.StatusOK)
	require.Equal(t, model.StateAvailable, e.state("acme", "fw-1").State)
	require.Equal(t, []obs{{auth.PlaneUpload, auth.AudExternal, auth.VerdictWouldReject}}, rec.all())
}

// The counterweight: what the plane accepts passes in BOTH modes, and is counted accepted.
func TestUploadAcceptedAudiencesPassInBothModes(t *testing.T) {
	for _, mode := range []string{"enforce", "observe"} {
		for name, tc := range map[string]struct {
			aud   []string
			class auth.AudienceClass
		}{
			"no audience": {nil, auth.AudNone},
			"own area":    {[]string{audOwn}, auth.AudOwnArea},
			"mcp":         {[]string{audMCP}, auth.AudMCP},
			"contains":    {[]string{audExternal, audMCP}, auth.AudMCP},
		} {
			t.Run(mode+"/"+name, func(t *testing.T) {
				e := newEnv(t)
				rec := withPolicy(t, e, mode)
				content := bytes.Repeat([]byte("ok"), 3000)
				e.declare("acme", "fw-1", content)

				status(t, e.put(e.audToken(tc.aud), "fw-1", bytes.NewReader(content)), http.StatusOK)
				require.Equal(t, model.StateAvailable, e.state("acme", "fw-1").State)
				require.Equal(t, []obs{{auth.PlaneUpload, tc.class, auth.VerdictAccepted}}, rec.all())
			})
		}
	}
}

// The check runs only after the signature verifies, so a forged token cannot drive the counters.
func TestUploadForgedTokenIsNeverCounted(t *testing.T) {
	e := newEnv(t)
	rec := withPolicy(t, e, "enforce")
	e.declare("acme", "fw-1", []byte("0123456789"))

	otherKey, err := auth.GenerateKeyPair()
	require.NoError(t, err)
	forged, err := auth.NewIssuer(otherKey, "https://as.example.com", time.Hour, time.Hour).
		IssueOAuthAccess("acme", "a@acme.example", nil, []string{string(auth.ArtifactWrite)}, "",
			[]string{audExternal}, false, "client-1", "j-forged")
	require.NoError(t, err)

	require.Equal(t, http.StatusUnauthorized, refusedUnread(t, e, forged.Token, "fw-1", 10))
	require.Empty(t, rec.all())
}

// A handler registered without a policy source is the CLOSED policy: it refuses an
// audience-bound token loudly and still accepts a first-party one.
func TestUploadWithoutAPolicySourceFailsClosed(t *testing.T) {
	e := newEnv(t) // newEnv registers a nil policy source
	content := []byte("0123456789")
	e.declare("acme", "fw-1", content)
	require.Equal(t, http.StatusUnauthorized, refusedUnread(t, e, e.audToken([]string{audMCP}), "fw-1", int64(len(content))))
	status(t, e.put(e.audToken(nil), "fw-1", bytes.NewReader(content)), http.StatusOK)
}

// A source that exists but returns nil is the closed policy too, not "skip admission".
func TestUploadNilReturningSourceFailsClosed(t *testing.T) {
	e := newEnv(t)
	e.h.policy = func() *auth.PlanePolicy { return nil }
	content := []byte("0123456789")
	e.declare("acme", "fw-1", content)
	require.Equal(t, http.StatusUnauthorized, refusedUnread(t, e, e.audToken([]string{audMCP}), "fw-1", int64(len(content))))
}
