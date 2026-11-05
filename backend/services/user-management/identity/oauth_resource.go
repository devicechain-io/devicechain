// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"errors"
	"strings"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/rs/zerolog/log"
)

// The authorization server's resource rule (RFC 8707): which `resource` values it will bind a
// token to. The allowlist is the instance's FIXED token-audience block, the same document every
// API plane reads, so what is issued for and what a plane accepts cannot drift apart.
//
// In OBSERVE mode (the v1.2 default) a request that would be refused is counted, logged and
// served exactly as before. In ENFORCE mode it is refused: invalid_target at the authorization
// and token endpoints, invalid_grant (and the refresh token burned) at a refresh.

// OAuthGrant is the grant a resource check ran for. It is a metric label, so the set is closed.
type OAuthGrant string

const (
	GrantAuthorizationCode OAuthGrant = "authorization_code"
	GrantRefreshToken      OAuthGrant = "refresh_token"
	GrantClientCredentials OAuthGrant = "client_credentials"
)

// OAuthResourceMetrics counts the authorization server's resource decisions:
//
//	devicechain_<subsystem>_oauth_resource_checks_total{grant, resource, verdict}
//	devicechain_<subsystem>_oauth_unscoped_client_credentials_total{verdict}
//
// Every label is a member of a closed set (OAuthGrant, auth.ResourceClass, the three
// auth.AudienceVerdict values): the raw resource string is NEVER a label, because the sender
// chooses it. Both counters are recorded only after the sender authenticated, so an
// unauthenticated sender cannot move them.
type OAuthResourceMetrics struct {
	checks   *prometheus.CounterVec
	unscoped *prometheus.CounterVec
}

// NewOAuthResourceMetrics registers the counters on reg (nil leaves them unregistered).
// Registering twice on one registry panics, so build it once.
func NewOAuthResourceMetrics(reg prometheus.Registerer, subsystem string) *OAuthResourceMetrics {
	f := promauto.With(reg)
	return &OAuthResourceMetrics{
		checks: f.NewCounterVec(prometheus.CounterOpts{
			Namespace: "devicechain", Subsystem: subsystem, Name: "oauth_resource_checks_total",
			Help: "Token requests by grant, the class of the requested resource (none, area, mcp, " +
				"external, unlisted, malformed, multiple) and the verdict (accepted, would_reject in " +
				"observe mode, rejected in enforce mode). Recorded after the client authenticated.",
		}, []string{"grant", "resource", "verdict"}),
		unscoped: f.NewCounterVec(prometheus.CounterOpts{
			Namespace: "devicechain", Subsystem: subsystem, Name: "oauth_unscoped_client_credentials_total",
			Help: "client_credentials requests that named a resource without a scope (deprecated: the " +
				"next major release refuses them). Recorded after the client authenticated. The request " +
				"still succeeds in this release, so the verdict is always would_reject.",
		}, []string{"verdict"}),
	}
}

func (m *OAuthResourceMetrics) check(g OAuthGrant, c auth.ResourceClass, v auth.AudienceVerdict) {
	if m == nil {
		return
	}
	m.checks.WithLabelValues(string(g), string(c), string(v)).Inc()
}

func (m *OAuthResourceMetrics) unscopedWouldReject() {
	if m == nil {
		return
	}
	m.unscoped.WithLabelValues(string(auth.VerdictWouldReject)).Inc()
}

// CheckCounter returns the checks series for one label set. It exists for tests that read a
// count back; production code never calls it.
func (m *OAuthResourceMetrics) CheckCounter(g OAuthGrant, c auth.ResourceClass, v auth.AudienceVerdict) prometheus.Counter {
	return m.checks.WithLabelValues(string(g), string(c), string(v))
}

// UnscopedCounter is CheckCounter's twin for the rule 3 counter.
func (m *OAuthResourceMetrics) UnscopedCounter(v auth.AudienceVerdict) prometheus.Counter {
	return m.unscoped.WithLabelValues(string(v))
}

// ResourceCheck is the outcome of judging one requested resource.
type ResourceCheck struct {
	// Checked is false when no check ran (the request did not reach it, or it carried nothing
	// to judge). A zero ResourceCheck is "nothing to record".
	Checked bool
	Class   auth.ResourceClass
	Verdict auth.AudienceVerdict
	// Resource is what was requested, for the log line only (truncated and quoted there).
	Resource string
	// Desc is the client-facing reason when the verdict is rejected.
	Desc string
}

// Refused reports whether the request must be refused: an enforce-mode verdict.
func (c ResourceCheck) Refused() bool { return c.Verdict == auth.VerdictRejected }

// resourcePolicy is rule 1 for the authorization server.
type resourcePolicy struct {
	allow   *auth.ResourceAllowlist
	metrics *OAuthResourceMetrics
	lim     *auth.WarnLimiter
}

func newResourcePolicy(cfg config.TokenAudienceConfiguration, metrics *OAuthResourceMetrics) (*resourcePolicy, error) {
	allow, err := auth.NewResourceAllowlist(cfg)
	if err != nil {
		return nil, err
	}
	return &resourcePolicy{allow: allow, metrics: metrics, lim: auth.NewWarnLimiter(nil)}, nil
}

// defaultResourcePolicy is what a Manager that was never given the instance's block uses: the
// release default (observe) over an empty allowlist. main installs the real one; a Manager
// built as a struct literal by a test runs this, which counts nothing and refuses nothing.
var defaultResourcePolicy = func() *resourcePolicy {
	p, err := newResourcePolicy(config.TokenAudienceConfiguration{}, nil)
	if err != nil {
		panic(err) // an empty block always validates
	}
	return p
}()

// SetTokenAudience installs the instance's token-audience block as the authorization server's
// resource allowlist, with the counters decisions are recorded on (nil counts nothing). It
// fails on a block the loader would have refused.
func (m *Manager) SetTokenAudience(cfg config.TokenAudienceConfiguration, metrics *OAuthResourceMetrics) error {
	p, err := newResourcePolicy(cfg, metrics)
	if err != nil {
		return err
	}
	m.resources = p
	if cfg.EffectiveUnscopedClientCredentials() == config.TokenAudienceEnforce {
		log.Warn().Msg("infrastructure.tokenAudience.unscopedClientCredentials is enforce, but this release only " +
			"deprecates a client_credentials request that names a resource without a scope: it is counted and " +
			"logged, never refused. The next major release refuses it.")
	}
	return nil
}

// ResourceEnforcement is the mode the authorization server's resource rule runs in.
func (m *Manager) ResourceEnforcement() config.TokenAudienceMode {
	return m.resourcePolicy().allow.Mode()
}

func (m *Manager) resourcePolicy() *resourcePolicy {
	if m.resources != nil {
		return m.resources
	}
	return defaultResourcePolicy
}

func (p *resourcePolicy) enforcing() bool { return p.allow.Mode() == config.TokenAudienceEnforce }

// verdictFor is the verdict for a class: accepted when it is issuable, otherwise rejected in
// enforce mode and would_reject in observe mode.
func (p *resourcePolicy) verdictFor(c auth.ResourceClass) auth.AudienceVerdict {
	switch {
	case c.Issuable():
		return auth.VerdictAccepted
	case p.enforcing():
		return auth.VerdictRejected
	}
	return auth.VerdictWouldReject
}

// check judges one requested resource that arrived `count` times (count counts NON-empty
// values: an empty `resource=` is an absent one). More than one is multiple whatever it says;
// then syntax; then membership. This is the ONE function both the request validation and the
// mint-time re-assert run, so the mint covers multiplicity and syntax as well as membership.
func (p *resourcePolicy) check(resource string, count int) ResourceCheck {
	rc := ResourceCheck{Checked: true, Resource: resource}
	switch {
	case count > 1:
		rc.Class = auth.ResMultiple
		rc.Desc = "at most one resource may be requested"
	case resource == "":
		rc.Class = auth.ResNone
	default:
		if err := ValidateResource(resource); err != nil {
			rc.Class = auth.ResMalformed
			var oe *oauthError
			if errors.As(err, &oe) {
				rc.Desc = oe.Desc
			}
		} else {
			rc.Class = p.allow.Classify(resource)
		}
	}
	if rc.Desc == "" {
		rc.Desc = "resource is not served by this instance"
	}
	rc.Verdict = p.verdictFor(rc.Class)
	return rc
}

// checkBound judges the audience a stored code or refresh token is bound to against the
// CURRENT allowlist. A binding the AS never mints (more than one member) is judged like the
// same request would be.
func (p *resourcePolicy) checkBound(aud []string) ResourceCheck {
	if len(aud) == 0 {
		return p.check("", 0)
	}
	rc := p.check(aud[0], len(aud))
	if aud[0] == "" && rc.Class == auth.ResNone {
		// A PRESENT audience whose member is the empty string is not "no audience".
		rc.Class = auth.ResMalformed
		rc.Verdict = p.verdictFor(rc.Class)
	}
	return rc
}

// checkRequested judges the `resource` a token request (authorization_code or refresh_token)
// carries against the audience its grant is bound to. A resource is only ever CHECKED against
// the grant, never added to it: absent is unchanged, equal to the bound audience is accepted,
// and anything else (a different value, more than one, or any value on an unbound grant) is a
// mismatch. The returned check is zero (nothing to record) for the first two.
func (p *resourcePolicy) checkRequested(resources, bound []string) ResourceCheck {
	var vals []string
	for _, r := range resources {
		if r != "" {
			vals = append(vals, r)
		}
	}
	switch {
	case len(vals) == 0:
		return ResourceCheck{}
	case len(vals) == 1 && len(bound) == 1 && bound[0] == vals[0]:
		return ResourceCheck{}
	}
	rc := p.check(vals[0], len(vals))
	rc.Desc = "resource does not match the resource this grant is bound to"
	if rc.Class == auth.ResMultiple {
		rc.Desc = "at most one resource may be requested"
	}
	// A mismatch is a mismatch whatever the value is: even a listed resource is refused
	// here, because the token endpoint never adds an audience the grant was not given.
	rc.Verdict = auth.VerdictWouldReject
	if p.enforcing() {
		rc.Verdict = auth.VerdictRejected
	}
	return rc
}

// record counts one decision and, when it is not an acceptance, writes the rate-limited
// warning. It is called only for an authenticated sender; clientID and tenant are therefore
// trustworthy log fields, and the limiter is keyed on (grant, class, client_id), never on
// the resource, which the sender chooses.
func (p *resourcePolicy) record(g OAuthGrant, clientID, tenant string, rc ResourceCheck) {
	p.recordAs(g, clientID, tenant, true, rc)
}

// recordAs is record with the sender's authentication stated. The authorization endpoint's
// entry request is PRE-LOGIN by nature: the client_id and redirect URI are public values, so
// anyone who knows them can send it. It is therefore a request-volume signal, and its warning
// says so (sender_authenticated=false) instead of presenting the client_id as established.
func (p *resourcePolicy) recordAs(g OAuthGrant, clientID, tenant string, authenticated bool, rc ResourceCheck) {
	if !rc.Checked {
		return
	}
	p.metrics.check(g, rc.Class, rc.Verdict)
	if rc.Verdict == auth.VerdictAccepted {
		return
	}
	suppressed, ok := p.lim.Allow(string(g) + "\x00" + string(rc.Class) + "\x00" + clientID)
	if !ok {
		return
	}
	log.Warn().
		Str("grant", string(g)).
		Str("resource_class", string(rc.Class)).
		Str("verdict", string(rc.Verdict)).
		Str("client_id", clientID).
		Str("tenant", tenant).
		Bool("sender_authenticated", authenticated).
		Str("resource", auth.QuoteLogValue(rc.Resource)).
		Int("suppressed", suppressed).
		Msg("OAuth resource is not one this instance serves")
}

// recordUnscoped counts a client_credentials request that named a resource without a scope,
// and writes the rate-limited warning. Rule 3 is deprecated in this release: the request is
// served exactly as before, in every mode.
func (p *resourcePolicy) recordUnscoped(clientID, tenant string) {
	p.metrics.unscopedWouldReject()
	suppressed, ok := p.lim.Allow("unscoped\x00" + clientID)
	if !ok {
		return
	}
	log.Warn().
		Str("client_id", clientID).
		Str("tenant", tenant).
		Int("suppressed", suppressed).
		Msg("A client_credentials request named a resource without a scope; this is deprecated and the next major release refuses it")
}

// nonEmptyCount counts the values that are not the empty string.
func nonEmptyCount(vals []string) int {
	n := 0
	for _, v := range vals {
		if v != "" {
			n++
		}
	}
	return n
}

// unscopedRequest is rule 3's condition: exactly one resource and a blank scope. Whitespace
// counts as blank.
func unscopedRequest(resources []string, scope string) bool {
	return len(resources) == 1 && strings.TrimSpace(scope) == ""
}
