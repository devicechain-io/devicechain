// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	hBase     = "https://iot.example.com/api"
	hMCP      = "https://iot.example.com/api/mcp"
	hOwn      = hBase + "/device-management"
	hOther    = hBase + "/event-management"
	hExternal = "https://reports.example.org"
	hFoo      = "https://evil.example.net"
	hMetric   = "devicechain_devicemanagement_token_audience_checks_total"
)

// audHarness is a matched issuer/validator, a gate with the configured policy installed, and
// the registry the policy's counter lives on.
type audHarness struct {
	t    *testing.T
	key  *rsa.PrivateKey
	iss  *auth.Issuer
	val  *auth.Validator
	gate *core.ReadinessGate
	reg  *prometheus.Registry
	rec  *auth.PromAudienceRecorder
}

func newAudHarness(t *testing.T, mode string) *audHarness {
	t.Helper()
	key, err := auth.GenerateKeyPair()
	require.NoError(t, err)
	h := &audHarness{
		t: t, key: key,
		iss: auth.NewIssuer(key, "test", time.Minute, time.Hour),
		val: auth.NewValidator(&key.PublicKey),
		reg: prometheus.NewRegistry(),
	}
	h.rec = auth.NewPromAudienceRecorder(h.reg, "devicemanagement")
	h.gate = core.NewReadinessGate()
	h.gate.MarkReady(h.val)
	policy, err := auth.NewPlanePolicy(config.TokenAudienceConfiguration{
		Enforcement:       mode,
		AreaBaseUrl:       hBase,
		McpResources:      []string{hMCP},
		ExternalResources: []string{hExternal},
	}, "device-management", h.rec)
	require.NoError(t, err)
	h.gate.SetAudiencePolicy(policy)
	return h
}

// tokenFor mints a tenant access token carrying aud (nil mints a first-party, aud-less one).
func (h *audHarness) tokenFor(aud []string) string {
	h.t.Helper()
	var tok auth.IssuedToken
	var err error
	if aud == nil {
		tok, err = h.iss.IssueTenantAccess("acme", "a@acme.example", nil, []string{string(auth.AuthorityAll)}, false, "j-"+fmt.Sprint(len(aud)))
	} else {
		tok, err = h.iss.IssueOAuthAccess("acme", "a@acme.example", nil, []string{string(auth.AuthorityAll)}, "", aud, false, "client-1", "j-aud")
	}
	require.NoError(h.t, err)
	return tok.Token
}

// identityWithAud signs an identity-tier token that carries aud. The issuer never mints one
// (identity tokens carry no audience), so this is the test-only path the spec calls for.
func (h *audHarness) identityWithAud(aud []string) string {
	h.t.Helper()
	claims := &auth.Claims{
		Username: "root@example.com", Email: "root@example.com", TokenType: auth.TokenTypeIdentity,
		Authorities: []string{string(auth.AuthorityAll)},
		RegisteredClaims: jwt.RegisteredClaims{
			Subject: "root@example.com", Audience: jwt.ClaimStrings(aud), ID: "j-id",
			IssuedAt: jwt.NewNumericDate(time.Now()), ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = h.iss.Kid()
	s, err := tok.SignedString(h.key)
	require.NoError(h.t, err)
	return s
}

// signedAccess signs, with the real key, an access token that carries aud and expires at exp.
func (h *audHarness) signedAccess(aud []string, exp time.Time) string {
	h.t.Helper()
	claims := &auth.Claims{
		Tenant: "acme", Username: "a@acme.example", Email: "a@acme.example", TokenType: auth.TokenTypeAccess,
		Authorities: []string{string(auth.AuthorityAll)},
		RegisteredClaims: jwt.RegisteredClaims{
			Subject: "a@acme.example", Audience: jwt.ClaimStrings(aud), ID: "j-exp",
			IssuedAt: jwt.NewNumericDate(time.Now().Add(-time.Hour)), ExpiresAt: jwt.NewNumericDate(exp),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = h.iss.Kid()
	s, err := tok.SignedString(h.key)
	require.NoError(h.t, err)
	return s
}

// series is how many label sets the counter holds; zero means nothing was ever recorded.
func (h *audHarness) series() int {
	n, err := testutil.GatherAndCount(h.reg, hMetric)
	require.NoError(h.t, err)
	return n
}

// count reads one label set without disturbing the others' values.
func (h *audHarness) count(plane auth.Plane, class auth.AudienceClass, verdict auth.AudienceVerdict) float64 {
	return testutil.ToFloat64(h.rec.CounterFor(plane, class, verdict))
}

func gqlPost(t *testing.T, url, token string) (int, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"query": "query{whoAmI}"})
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.String()
}

type audCase struct {
	name  string
	aud   []string
	class auth.AudienceClass
}

var audCases = []audCase{
	{"none", nil, auth.AudNone},
	{"own_area", []string{hOwn}, auth.AudOwnArea},
	{"mcp", []string{hMCP}, auth.AudMCP},
	{"other_area", []string{hOther}, auth.AudOtherArea},
	{"external", []string{hExternal}, auth.AudExternal},
	{"unlisted", []string{hFoo}, auth.AudUnlisted},
	{"blank member", []string{""}, auth.AudUnlisted},
	{"multi: foreign then mcp", []string{hFoo, hMCP}, auth.AudMCP},
	{"multi: own then foreign", []string{hOwn, hFoo}, auth.AudOwnArea},
	{"multi: two foreign", []string{hFoo, hOther}, auth.AudOtherArea},
}

func accepted(c auth.AudienceClass) bool {
	return c == auth.AudNone || c == auth.AudOwnArea || c == auth.AudMCP
}

// The HTTP data plane through the REAL handler, in both modes, for every class. Enforce
// refuses exactly the non-accepted classes with a 401 and counts rejected; observe serves
// every one with GraphQL data and counts would_reject exactly once. Accepted classes are the
// counterweight: 200 in both modes, counted accepted.
func TestDataPlaneAudienceRule(t *testing.T) {
	for _, mode := range []string{"enforce", "observe"} {
		for _, tc := range audCases {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				h := newAudHarness(t, mode)
				srv := httptest.NewServer(NewHttpHandler(MustParseSchema(
					`schema { query: Query } type Query { whoAmI: String! }`, &echoRoot{}),
					map[ContextKey]interface{}{}, h.gate))
				defer srv.Close()

				code, body := gqlPost(t, srv.URL, h.tokenFor(tc.aud))

				var verdict auth.AudienceVerdict
				switch {
				case accepted(tc.class):
					verdict = auth.VerdictAccepted
					require.Equal(t, http.StatusOK, code, body)
				case mode == "enforce":
					verdict = auth.VerdictRejected
					require.Equal(t, http.StatusUnauthorized, code, body)
					assert.Contains(t, body, "invalid or expired token", "indistinguishable from a bad token")
				default:
					verdict = auth.VerdictWouldReject
					require.Equal(t, http.StatusOK, code, "observe must not change served behaviour: %s", body)
					assert.Contains(t, body, `"whoAmI"`, "observe serves the real GraphQL answer")
				}
				assert.Equal(t, 1, h.series(), "exactly one series")
				assert.Equal(t, 1.0, h.count(auth.PlaneGraphQL, tc.class, verdict), "counted exactly once, with the right labels")
			})
		}
	}
}

// What this checks: in observe mode, attaching the recorder (counting plus the warning) does not
// change what the caller receives, for any class. It compares an observe policy WITH a recorder
// against an observe policy WITHOUT one; it is not a diff against pre-PR behaviour. That claim
// rests on TestDataPlaneAudienceRule/observe, which asserts 200 with real GraphQL data for every
// class.
func TestObserveModeRecorderDoesNotChangeTheResponse(t *testing.T) {
	h := newAudHarness(t, "observe")
	bare := newAudHarness(t, "observe")
	bare.gate = core.NewReadinessGate()
	bare.gate.MarkReady(h.val)
	bare.gate.SetAudiencePolicy(nil)
	// A policy-less gate is CLOSED, so compare against an explicit accept-everything baseline:
	// the handler built over a gate whose policy admits all (observe, but with no recorder).
	open, err := auth.NewPlanePolicy(config.TokenAudienceConfiguration{Enforcement: "observe"}, "device-management", nil)
	require.NoError(t, err)
	bare.gate.SetAudiencePolicy(open)

	schema := MustParseSchema(`schema { query: Query } type Query { whoAmI: String! }`, &echoRoot{})
	withPolicy := httptest.NewServer(NewHttpHandler(schema, map[ContextKey]interface{}{}, h.gate))
	baseline := httptest.NewServer(NewHttpHandler(schema, map[ContextKey]interface{}{}, bare.gate))
	defer withPolicy.Close()
	defer baseline.Close()
	for _, tc := range audCases {
		tok := h.tokenFor(tc.aud)
		c1, b1 := gqlPost(t, withPolicy.URL, tok)
		c2, b2 := gqlPost(t, baseline.URL, tok)
		assert.Equal(t, c2, c1, tc.name)
		assert.Equal(t, b2, b1, tc.name)
		assert.Equal(t, http.StatusOK, c1, tc.name)
	}
}

// M22: a forged token never reaches the rule, so it cannot move a counter. Three forgeries:
// a signature by another key, an expired token, and garbage, each carrying an other_area aud.
func TestForgedAndExpiredTokensAreNotCounted(t *testing.T) {
	for _, mode := range []string{"enforce", "observe"} {
		t.Run(mode, func(t *testing.T) {
			h := newAudHarness(t, mode)
			srv := httptest.NewServer(NewHttpHandler(MustParseSchema(
				`schema { query: Query } type Query { whoAmI: String! }`, &echoRoot{}),
				map[ContextKey]interface{}{}, h.gate))
			defer srv.Close()

			otherKey, err := auth.GenerateKeyPair()
			require.NoError(t, err)
			forger := auth.NewIssuer(otherKey, "test", time.Minute, time.Hour)
			forged, err := forger.IssueOAuthAccess("acme", "a@acme.example", nil, []string{"*"}, "", []string{hOther}, false, "c", "j")
			require.NoError(t, err)

			expired := h.signedAccess([]string{hOther}, time.Now().Add(-time.Minute))

			for name, tok := range map[string]string{"forged": forged.Token, "expired": expired, "garbage": "a.b.c"} {
				code, _ := gqlPost(t, srv.URL, tok)
				assert.Equal(t, http.StatusUnauthorized, code, name)
			}
			assert.Equal(t, 0, h.series(), "no counter series moved")
		})
	}
}

// M23: the plane label comes from the handler, not a constant. The data handler counts under
// graphql, the admin handler under admin, and the settings mount under settings.
func TestPlaneLabelFollowsTheHandler(t *testing.T) {
	h := newAudHarness(t, "observe")
	schema := MustParseSchema(`schema { query: Query } type Query { whoAmI: String! }`, &echoRoot{})
	providers := map[ContextKey]interface{}{}
	data := httptest.NewServer(NewHttpHandler(schema, providers, h.gate))
	admin := httptest.NewServer(NewAdminHttpHandler(schema, providers, h.gate))
	settings := httptest.NewServer(NewAdminHttpHandler(schema, providers, h.gate, WithPlane(auth.PlaneSettings)))
	defer data.Close()
	defer admin.Close()
	defer settings.Close()

	// An identity token is refused by the data plane before the audience rule, so use the
	// right token on each plane.
	idTok := h.identityWithAud([]string{hExternal})
	accessTok := h.tokenFor([]string{hExternal})
	for _, c := range []struct {
		srv   *httptest.Server
		token string
	}{{data, accessTok}, {admin, idTok}, {settings, idTok}} {
		code, body := gqlPost(t, c.srv.URL, c.token)
		require.Equal(t, http.StatusOK, code, body)
	}
	assert.Equal(t, 3, h.series())
	assert.Equal(t, 1.0, h.count(auth.PlaneGraphQL, auth.AudExternal, auth.VerdictWouldReject))
	assert.Equal(t, 1.0, h.count(auth.PlaneAdmin, auth.AudExternal, auth.VerdictWouldReject))
	assert.Equal(t, 1.0, h.count(auth.PlaneSettings, auth.AudExternal, auth.VerdictWouldReject))
}

// The admin plane (M7): an identity token with an injected aud is refused in enforce mode and
// counted in observe mode; a plain identity token passes in both.
func TestAdminPlaneAudienceRule(t *testing.T) {
	schema := MustParseSchema(`schema { query: Query } type Query { whoAmI: String! }`, &echoRoot{})
	for _, mode := range []string{"enforce", "observe"} {
		t.Run(mode, func(t *testing.T) {
			h := newAudHarness(t, mode)
			srv := httptest.NewServer(NewAdminHttpHandler(schema, map[ContextKey]interface{}{}, h.gate))
			defer srv.Close()

			// Counterweight: an aud-less identity token passes, counted accepted/none.
			code, body := gqlPost(t, srv.URL, h.identityWithAud(nil))
			require.Equal(t, http.StatusOK, code, body)
			assert.Equal(t, 1.0, h.count(auth.PlaneAdmin, auth.AudNone, auth.VerdictAccepted))

			// Negative control: an anomalous aud on an identity token.
			code, body = gqlPost(t, srv.URL, h.identityWithAud([]string{hOther}))
			if mode == "enforce" {
				require.Equal(t, http.StatusUnauthorized, code, body)
				assert.Equal(t, 1.0, h.count(auth.PlaneAdmin, auth.AudOtherArea, auth.VerdictRejected))
			} else {
				require.Equal(t, http.StatusOK, code, body)
				assert.Equal(t, 1.0, h.count(auth.PlaneAdmin, auth.AudOtherArea, auth.VerdictWouldReject))
			}
		})
	}
}

// M9: a handler over a gate that was never given a policy refuses an MCP-bound token (the
// closed default), and still admits an aud-less one.
func TestClosedDefaultRefusesAnMcpAudience(t *testing.T) {
	h := newAudHarness(t, "observe")
	gate := core.NewReadinessGate() // no policy installed
	gate.MarkReady(h.val)
	srv := httptest.NewServer(NewHttpHandler(MustParseSchema(
		`schema { query: Query } type Query { whoAmI: String! }`, &echoRoot{}),
		map[ContextKey]interface{}{}, gate))
	defer srv.Close()

	code, _ := gqlPost(t, srv.URL, h.tokenFor([]string{hMCP}))
	assert.Equal(t, http.StatusUnauthorized, code, "an unwired policy must break the pass-through loudly")
	code, _ = gqlPost(t, srv.URL, h.tokenFor(nil))
	assert.Equal(t, http.StatusOK, code, "first-party tokens are unaffected")
}

// --- WebSocket ------------------------------------------------------------------------------

// wsInit performs connection_init with token and reports whether the server acked it, or the
// close reason when it did not.
func wsInit(t *testing.T, url, token string) (acked bool, closeText string) {
	t.Helper()
	dialer := websocket.Dialer{Subprotocols: []string{wsSubprotocol}}
	conn, _, err := dialer.Dial("ws"+strings.TrimPrefix(url, "http"), nil)
	require.NoError(t, err)
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	params, _ := json.Marshal(map[string]string{"Authorization": "Bearer " + token})
	writeMsg(t, conn, wsMessage{Type: msgConnectionInit, Payload: params})
	var msg wsMessage
	if err := conn.ReadJSON(&msg); err != nil {
		return false, err.Error()
	}
	return msg.Type == msgConnectionAck, ""
}

// M6: the same pair through the subscription path's real authenticate. A separate test, so a
// mutant deleting only the WS call dies here.
func TestSubscriptionAudienceRule(t *testing.T) {
	for _, mode := range []string{"enforce", "observe"} {
		for _, tc := range audCases {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				h := newAudHarness(t, mode)
				srv := httptest.NewServer(NewSubscriptionHandler(MustParseSchema(scopeSubSDL, &scopeSubRoot{}),
					map[ContextKey]interface{}{}, h.gate))
				defer srv.Close()

				acked, closed := wsInit(t, srv.URL, h.tokenFor(tc.aud))

				var verdict auth.AudienceVerdict
				switch {
				case accepted(tc.class):
					verdict = auth.VerdictAccepted
					assert.True(t, acked, closed)
				case mode == "enforce":
					verdict = auth.VerdictRejected
					assert.False(t, acked)
					assert.Contains(t, closed, "invalid or expired token", "the same text as an invalid token")
				default:
					verdict = auth.VerdictWouldReject
					assert.True(t, acked, "observe must not refuse a connection: %s", closed)
				}
				assert.Equal(t, 1, h.series())
				assert.Equal(t, 1.0, h.count(auth.PlaneGraphQLWS, tc.class, verdict), "once per connection init")
			})
		}
	}
}

func TestSubscriptionForgedTokenIsNotCounted(t *testing.T) {
	h := newAudHarness(t, "observe")
	srv := httptest.NewServer(NewSubscriptionHandler(MustParseSchema(scopeSubSDL, &scopeSubRoot{}),
		map[ContextKey]interface{}{}, h.gate))
	defer srv.Close()
	otherKey, err := auth.GenerateKeyPair()
	require.NoError(t, err)
	forged, err := auth.NewIssuer(otherKey, "test", time.Minute, time.Hour).
		IssueOAuthAccess("acme", "a@acme.example", nil, []string{"*"}, "", []string{hOther}, false, "c", "j")
	require.NoError(t, err)
	acked, _ := wsInit(t, srv.URL, forged.Token)
	assert.False(t, acked)
	assert.Equal(t, 0, h.series())
}

// --- the caller, not only the function -----------------------------------------------------

const callerSDL = `
	schema { query: Query subscription: Subscription }
	type Query { whoAmI: String! }
	type Subscription { gatedStream: String! }
`

type callerRoot struct{ echoRoot }

func (*callerRoot) GatedStream(ctx context.Context) (<-chan string, error) { return oneValue("x"), nil }

func writeInstanceDoc(t *testing.T, enforcement string) string {
	t.Helper()
	extra := ""
	if enforcement != "" {
		extra = fmt.Sprintf(`, "enforcement": %q`, enforcement)
	}
	doc := fmt.Sprintf(`{"infrastructure": {
		"nats": {"hostname": "n", "port": 4222},
		"userManagement": {"hostname": "u", "port": 8080},
		"tokenAudience": {"areaBaseUrl": %q, "mcpResources": [%q]%s}}}`, hBase, hMCP, extra)
	path := filepath.Join(t.TempDir(), "instance")
	require.NoError(t, os.WriteFile(path, []byte(doc), 0o600))
	return path
}

// Drive the whole production path: LoadInstanceConfigurationFrom installs the policy on the
// gate, GraphQLManager.ExecuteInitialize registers /graphql on the real mux, and a request
// through that mux is judged by the policy read from the gate. A handler built by hand would
// not show that the registered route reads it (M10, and "Admit not wired on the route").
func TestPolicyIsReadFromTheGateOnTheRegisteredRoute(t *testing.T) {
	for _, tc := range []struct {
		enforcement string
		wantOther   int
	}{
		{"enforce", http.StatusUnauthorized},
		{"observe", http.StatusOK},
		{"", http.StatusUnauthorized}, // the release default is enforce (from v1.3)
	} {
		t.Run("enforcement="+tc.enforcement, func(t *testing.T) {
			// Loading the instance document sets the process-wide log level; put it back so
			// the tests that follow still see the level they expect.
			prevLevel := zerolog.GlobalLevel()
			t.Cleanup(func() { zerolog.SetGlobalLevel(prevLevel) })
			h := newAudHarness(t, "observe")
			ms := &core.Microservice{InstanceId: "test", FunctionalArea: "device-management", Readiness: core.NewReadinessGate()}
			ms.UseMetricsRegistry(prometheus.NewRegistry())
			require.NoError(t, ms.LoadInstanceConfigurationFrom(writeInstanceDoc(t, tc.enforcement)))
			ms.Readiness.MarkReady(h.val)

			gql := &GraphQLManager{
				Microservice: ms,
				Schema:       MustParseSchema(callerSDL, &callerRoot{}),
				Gate:         ms.Readiness,
			}
			require.NoError(t, gql.ExecuteInitialize(context.Background()))
			srv := httptest.NewServer(ms.Mux())
			defer srv.Close()

			// Accepted classes pass over the registered route.
			for _, aud := range [][]string{nil, {hMCP}, {hOwn}} {
				code, body := gqlPost(t, srv.URL+"/graphql", h.tokenFor(aud))
				assert.Equal(t, http.StatusOK, code, "%v: %s", aud, body)
			}
			// Another area's audience is the negative control.
			code, body := gqlPost(t, srv.URL+"/graphql", h.tokenFor([]string{hOther}))
			assert.Equal(t, tc.wantOther, code, body)

			// And the subscription path on the same mux.
			acked, closed := wsInit(t, srv.URL+"/graphql", h.tokenFor([]string{hOther}))
			assert.Equal(t, tc.wantOther == http.StatusOK, acked, closed)
			acked, closed = wsInit(t, srv.URL+"/graphql", h.tokenFor([]string{hMCP}))
			assert.True(t, acked, closed)
		})
	}
}
