// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	dctest "github.com/devicechain-io/dc-microservice/test"
	"github.com/golang-jwt/jwt/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	audBase     = "https://iot.example.com/api"
	audMCP      = "https://iot.example.com/api/mcp"
	audExternal = "https://reports.example.org"
	// A plane other than user-management: user-management's issuer equals its own area
	// resource, so a mutant that built the own resource from the issuer would survive there.
	audArea = "device-management"
	audOwn  = audBase + "/device-management"
	audOth  = audBase + "/event-management"
)

type obs struct {
	plane   Plane
	class   AudienceClass
	verdict AudienceVerdict
}

// countingRecorder is the counting fake: one entry per Record call.
type countingRecorder struct {
	mu  sync.Mutex
	got []obs
}

func (r *countingRecorder) Record(p Plane, c AudienceClass, v AudienceVerdict) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, obs{p, c, v})
}

func (r *countingRecorder) all() []obs {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]obs(nil), r.got...)
}

func testAudCfg(mode string) config.TokenAudienceConfiguration {
	return config.TokenAudienceConfiguration{
		Enforcement:       mode,
		AreaBaseUrl:       audBase,
		McpResources:      []string{audMCP},
		ExternalResources: []string{audExternal},
	}
}

func newTestPolicy(t *testing.T, mode string) (*PlanePolicy, *countingRecorder) {
	t.Helper()
	rec := &countingRecorder{}
	p, err := NewPlanePolicy(testAudCfg(mode), audArea, rec)
	require.NoError(t, err)
	return p, rec
}

func TestClassifyAudience(t *testing.T) {
	p, _ := newTestPolicy(t, "enforce")
	for _, tc := range []struct {
		name string
		aud  []string
		want AudienceClass
	}{
		{"nil", nil, AudNone},
		{"empty slice", []string{}, AudNone},
		{"own", []string{audOwn}, AudOwnArea},
		{"mcp", []string{audMCP}, AudMCP},
		{"other area", []string{audOth}, AudOtherArea},
		{"external", []string{audExternal}, AudExternal},
		{"foreign", []string{"https://evil.example.net"}, AudUnlisted},
		// M2: a present audience whose member is "" is NOT empty.
		{"[\"\"] is unlisted", []string{""}, AudUnlisted},
		{"[\"\", \"\"] is unlisted", []string{"", ""}, AudUnlisted},
		// RFC 7519 contains semantics (M3: not first-element-only).
		{"own then foreign", []string{audOwn, "https://evil.example.net"}, AudOwnArea},
		{"foreign then mcp", []string{"https://evil.example.net", audMCP}, AudMCP},
		{"foreign then own", []string{"https://evil.example.net", audOwn}, AudOwnArea},
		{"two foreign", []string{"https://a.example.net", "https://b.example.net"}, AudUnlisted},
		{"empty then mcp", []string{"", audMCP}, AudMCP},
		// Deterministic label order: own_area, mcp, other_area, external.
		{"mcp and own", []string{audMCP, audOwn}, AudOwnArea},
		{"other and mcp", []string{audOth, audMCP}, AudMCP},
		{"external and other", []string{audExternal, audOth}, AudOtherArea},
		// Byte-exact, no normalisation (M4 neighbours).
		{"own with trailing slash", []string{audOwn + "/"}, AudUnlisted},
		{"own upper-cased", []string{strings.ToUpper(audOwn)}, AudUnlisted},
		{"own without the separator", []string{audBase + "device-management"}, AudUnlisted},
		{"the base itself", []string{audBase}, AudUnlisted},
		{"mcp with trailing slash", []string{audMCP + "/"}, AudUnlisted},
		{"over-long", []string{strings.Repeat("a", 100000)}, AudUnlisted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, p.Classify(tc.aud))
		})
	}
}

// mcp is not an area: its resource is an MCP resource and an "mcp" area plane has no own
// resource of that name.
func TestClassifyMcpAreaPolicyHasNoOwnResource(t *testing.T) {
	p, err := NewPlanePolicy(testAudCfg("enforce"), "mcp", nil)
	require.NoError(t, err)
	assert.Equal(t, "", p.OwnResource())
	assert.Equal(t, AudMCP, p.Classify([]string{audMCP}))
}

func TestOwnResourceIsBaseSlashArea(t *testing.T) {
	p, _ := newTestPolicy(t, "enforce")
	assert.Equal(t, audOwn, p.OwnResource())
}

// A made-up area (a test Microservice) and an empty one get no own-area resource: a token
// bound to "an area" is then never this one's, and an empty own never matches "".
func TestNonRosterAreaHasNoOwnResource(t *testing.T) {
	for _, area := range []string{"log-level-test", "", "test-area"} {
		p, err := NewPlanePolicy(testAudCfg("enforce"), area, nil)
		require.NoError(t, err)
		assert.Equal(t, "", p.OwnResource(), area)
		assert.Equal(t, AudUnlisted, p.Classify([]string{""}), "[\"\"] must not match an empty own")
		assert.Equal(t, AudOtherArea, p.Classify([]string{audOwn}), "every real area is 'other' to a made-up one")
	}
}

// With no base there are no area entries at all: nothing but the MCP/external lists can match.
func TestNoBaseMeansNoAreaEntries(t *testing.T) {
	p, err := NewPlanePolicy(config.TokenAudienceConfiguration{McpResources: []string{audMCP}}, audArea, nil)
	require.NoError(t, err)
	assert.Equal(t, AudUnlisted, p.Classify([]string{audOwn}))
	assert.Equal(t, AudMCP, p.Classify([]string{audMCP}))
}

func TestNewPlanePolicyRefusesAnInvalidBlock(t *testing.T) {
	_, err := NewPlanePolicy(config.TokenAudienceConfiguration{Enforcement: "bogus"}, audArea, nil)
	assert.Error(t, err)
	_, err = NewPlanePolicy(config.TokenAudienceConfiguration{AreaBaseUrl: audBase + "/"}, audArea, nil)
	assert.Error(t, err)
}

// The 6x2 grid of class x mode. Enforce refuses exactly {other_area, external, unlisted};
// observe refuses nothing; every call records exactly one observation with the right verdict.
func TestAdmitGrid(t *testing.T) {
	auds := map[AudienceClass][]string{
		AudNone:      nil,
		AudOwnArea:   {audOwn},
		AudMCP:       {audMCP},
		AudOtherArea: {audOth},
		AudExternal:  {audExternal},
		AudUnlisted:  {"https://evil.example.net"},
	}
	for _, mode := range []string{"enforce", "observe"} {
		for class, aud := range auds {
			t.Run(mode+"/"+string(class), func(t *testing.T) {
				p, rec := newTestPolicy(t, mode)
				err := p.Admit(&Claims{Tenant: "t", ClientId: "c", RegisteredClaims: regClaims(aud)}, PlaneGraphQL)

				wantAccepted := class == AudNone || class == AudOwnArea || class == AudMCP
				var wantVerdict AudienceVerdict
				switch {
				case wantAccepted:
					wantVerdict = VerdictAccepted
					assert.NoError(t, err)
				case mode == "enforce":
					wantVerdict = VerdictRejected
					assert.ErrorIs(t, err, ErrAudienceRefused)
				default:
					wantVerdict = VerdictWouldReject
					assert.NoError(t, err, "observe never refuses")
				}
				assert.Equal(t, []obs{{PlaneGraphQL, class, wantVerdict}}, rec.all(), "exactly one observation")
			})
		}
	}
}

func TestAdmitPlaneLabelIsRecorded(t *testing.T) {
	p, rec := newTestPolicy(t, "observe")
	for _, pl := range []Plane{PlaneGraphQL, PlaneGraphQLWS, PlaneAdmin, PlaneSettings, PlaneUpload, PlaneBranding, PlaneUserinfo} {
		require.NoError(t, p.Admit(&Claims{}, pl))
	}
	var got []Plane
	for _, o := range rec.all() {
		got = append(got, o.plane)
	}
	assert.Equal(t, []Plane{PlaneGraphQL, PlaneGraphQLWS, PlaneAdmin, PlaneSettings, PlaneUpload, PlaneBranding, PlaneUserinfo}, got)
}

// A nil policy is the closed policy, and so is a fresh closed one: enforce mode, accepting
// only aud-less tokens. This is what makes a forgotten wiring loud.
func TestNilAndClosedPolicyAcceptOnlyAudLessTokens(t *testing.T) {
	for name, p := range map[string]*PlanePolicy{"nil": nil, "closed": NewClosedPlanePolicy()} {
		t.Run(name, func(t *testing.T) {
			assert.NoError(t, p.Admit(&Claims{}, PlaneGraphQL), "an aud-less (first-party) token passes")
			for _, aud := range [][]string{{audMCP}, {audOwn}, {audExternal}, {""}} {
				assert.ErrorIs(t, p.Admit(&Claims{RegisteredClaims: regClaims(aud)}, PlaneGraphQL), ErrAudienceRefused, "%v", aud)
			}
			assert.Equal(t, config.TokenAudienceEnforce, p.Mode())
		})
	}
}

func TestAdmitNilClaimsFailsClosed(t *testing.T) {
	p, rec := newTestPolicy(t, "observe")
	assert.ErrorIs(t, p.Admit(nil, PlaneGraphQL), ErrAudienceRefused)
	assert.Empty(t, rec.all())
}

// The Prometheus recorder: the metric name, label set and one increment per call.
func TestPromAudienceRecorder(t *testing.T) {
	reg := prometheus.NewRegistry()
	rec := NewPromAudienceRecorder(reg, "devicemanagement")
	p, err := NewPlanePolicy(testAudCfg("observe"), audArea, rec)
	require.NoError(t, err)

	require.NoError(t, p.Admit(&Claims{RegisteredClaims: regClaims([]string{audExternal})}, PlaneAdmin))
	require.NoError(t, p.Admit(&Claims{RegisteredClaims: regClaims([]string{audExternal})}, PlaneAdmin))
	require.NoError(t, p.Admit(&Claims{}, PlaneGraphQL))

	n, err := testutil.GatherAndCount(reg, "devicechain_devicemanagement_token_audience_checks_total")
	require.NoError(t, err)
	assert.Equal(t, 2, n, "two series exist")

	got := func(plane, aud, verdict string) float64 {
		return testutil.ToFloat64(rec.checks.WithLabelValues(plane, aud, verdict))
	}
	assert.Equal(t, 2.0, got("admin", "external", "would_reject"))
	assert.Equal(t, 1.0, got("graphql", "none", "accepted"))
	assert.Equal(t, 0.0, got("admin", "external", "rejected"), "M20: the two verdicts must not be swapped")
}

// --- log limiter and log content ------------------------------------------------------------

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func TestWarnLimiterIsKeyedPerClient(t *testing.T) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	l := newWarnLimiter(c.now)

	// Client A floods; client B's first line must still be emitted (M35).
	n, ok := l.allow("p\x00unlisted\x00A")
	assert.True(t, ok)
	assert.Equal(t, 0, n)
	for i := 0; i < 5; i++ {
		_, ok = l.allow("p\x00unlisted\x00A")
		assert.False(t, ok, "A is limited to one per minute")
	}
	_, ok = l.allow("p\x00unlisted\x00B")
	assert.True(t, ok, "B is not suppressed by A's flood")

	c.t = c.t.Add(warnInterval)
	n, ok = l.allow("p\x00unlisted\x00A")
	assert.True(t, ok)
	assert.Equal(t, 5, n, "the line reports what it held back")
}

func TestWarnLimiterKeySetIsBounded(t *testing.T) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	l := newWarnLimiter(c.now)
	for i := 0; i < warnKeys*3; i++ {
		_, ok := l.allow(string(rune('a'+i%26)) + strings.Repeat("x", i))
		assert.True(t, ok)
	}
	assert.LessOrEqual(t, l.ll.Len(), warnKeys)
	assert.LessOrEqual(t, len(l.idx), warnKeys)
}

// captureLog captures the process-wide log through the package's shared sink (installed from
// TestMain); a test never swaps the global logger itself.
func captureLog(t *testing.T) *dctest.LogSink {
	t.Helper()
	return logSink.Capture(t)
}

func TestWarnLineContentAndInjectionSafety(t *testing.T) {
	buf := captureLog(t)
	p, _ := newTestPolicy(t, "observe")
	evil := "https://evil.example.net\"}\n{\"level\":\"fatal\",\"msg\":\"forged"
	long := strings.Repeat("é", 400)
	require.NoError(t, p.Admit(&Claims{
		Tenant: "acme", ClientId: "client-1", Email: "person@example.com", Username: "person@example.com",
		TokenType: TokenTypeAccess, RegisteredClaims: regClaims([]string{evil, long}),
	}, PlaneGraphQL))

	out := buf.String()
	assert.Equal(t, 1, strings.Count(out, "\n"), "one line, whatever the aud holds: %q", out)
	assert.Contains(t, out, `"plane":"graphql"`)
	assert.Contains(t, out, `"audience":"unlisted"`)
	assert.Contains(t, out, `"verdict":"would_reject"`)
	assert.Contains(t, out, `"tenant":"acme"`)
	assert.Contains(t, out, `"client_id":"client-1"`)
	assert.Contains(t, out, `"sub_kind":"person"`)
	assert.Contains(t, out, `"aud_count":2`)
	assert.Contains(t, out, `"area":"device-management"`)
	assert.NotContains(t, out, "person@example.com", "never the email")
	assert.Less(t, len(out), 1500, "aud is truncated")
}

func TestSubKind(t *testing.T) {
	assert.Equal(t, "service", subKind(&Claims{TokenType: TokenTypeService}))
	assert.Equal(t, "service-account", subKind(&Claims{TokenType: TokenTypeAccess, ClientId: "c"}))
	assert.Equal(t, "person", subKind(&Claims{TokenType: TokenTypeAccess, ClientId: "c", Email: "a@b.c"}))
	assert.Equal(t, "person", subKind(&Claims{TokenType: TokenTypeAccess}))
}

// Accepted tokens log nothing; a would-reject logs once per client per minute through Admit.
func TestAdmitWarnsOnlyForNonAcceptedAndRateLimits(t *testing.T) {
	buf := captureLog(t)
	p, _ := newTestPolicy(t, "observe")
	clk := &clock{t: time.Unix(5_000_000, 0)}
	p.lim = newWarnLimiter(clk.now)

	require.NoError(t, p.Admit(&Claims{RegisteredClaims: regClaims([]string{audMCP})}, PlaneGraphQL))
	assert.Empty(t, buf.String())

	bad := &Claims{ClientId: "A", RegisteredClaims: regClaims([]string{audExternal})}
	for i := 0; i < 4; i++ {
		require.NoError(t, p.Admit(bad, PlaneGraphQL))
	}
	assert.Equal(t, 1, strings.Count(buf.String(), "\n"))
	clk.t = clk.t.Add(2 * warnInterval)
	require.NoError(t, p.Admit(bad, PlaneGraphQL))
	assert.Equal(t, 2, strings.Count(buf.String(), "\n"))
	assert.Contains(t, buf.String(), `"suppressed":3`)
}

func regClaims(aud []string) jwt.RegisteredClaims {
	return jwt.RegisteredClaims{Audience: jwt.ClaimStrings(aud)}
}

// M35 through Admit itself: client A floods and is rate limited, client B's first line is
// still written. A limiter keyed without client_id would suppress B.
func TestAdmitWarnIsRateLimitedPerClient(t *testing.T) {
	buf := captureLog(t)
	p, _ := newTestPolicy(t, "observe")
	clk := &clock{t: time.Unix(7_000_000, 0)}
	p.lim = newWarnLimiter(clk.now)

	a := &Claims{ClientId: "client-A", RegisteredClaims: regClaims([]string{audExternal})}
	b := &Claims{ClientId: "client-B", RegisteredClaims: regClaims([]string{audExternal})}
	for i := 0; i < 5; i++ {
		require.NoError(t, p.Admit(a, PlaneGraphQL))
	}
	require.NoError(t, p.Admit(b, PlaneGraphQL))
	out := buf.String()
	assert.Equal(t, 2, strings.Count(out, "\n"), "one line for A, one for B: %s", out)
	assert.Contains(t, out, `"client_id":"client-B"`)
}
