// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"container/list"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/rs/zerolog/log"
)

// Plane names the API surface a token was presented to. It is a metric label, so the set is
// closed and small.
type Plane string

const (
	PlaneGraphQL   Plane = "graphql"
	PlaneGraphQLWS Plane = "graphql_ws"
	PlaneAdmin     Plane = "admin"
	PlaneSettings  Plane = "settings"
	PlaneUpload    Plane = "upload"
	PlaneBranding  Plane = "branding"
	PlaneUserinfo  Plane = "userinfo"
)

// AudienceClass is what a token's `aud` is, relative to the plane it was presented to. The
// set is closed (it is a metric label).
type AudienceClass string

const (
	AudNone      AudienceClass = "none"       // len(aud) == 0
	AudOwnArea   AudienceClass = "own_area"   // aud contains this area's resource
	AudMCP       AudienceClass = "mcp"        // aud contains a registered MCP resource
	AudOtherArea AudienceClass = "other_area" // aud contains another platform area's resource
	AudExternal  AudienceClass = "external"   // aud contains an operator-declared external resource
	AudUnlisted  AudienceClass = "unlisted"   // anything else, including aud == [""]
)

// AudienceVerdict is what a plane did with a classified token.
type AudienceVerdict string

const (
	VerdictAccepted    AudienceVerdict = "accepted"
	VerdictWouldReject AudienceVerdict = "would_reject"
	VerdictRejected    AudienceVerdict = "rejected"
)

// ErrAudienceRefused is returned by PlanePolicy.Admit when the token's audience is not
// accepted by this plane in enforce mode. A caller writes the same 401 it writes for an
// invalid token, so a client cannot tell the two apart; the reason goes to the log and the
// metric only.
var ErrAudienceRefused = errors.New("auth: token audience is not accepted by this plane")

// Recorder receives exactly one observation per Admit call.
type Recorder interface {
	Record(plane Plane, class AudienceClass, verdict AudienceVerdict)
}

const (
	// warnInterval is the shortest gap between two Warn lines for one (plane, audience,
	// client_id) key.
	warnInterval = time.Minute
	// warnKeys bounds the limiter's key set. Once full, eviction only lets an extra line
	// through; it never suppresses one.
	warnKeys = 256
	// maxLoggedAud caps how much of an attacker-influenced `aud` reaches a log line.
	maxLoggedAud = 256
)

// PlanePolicy is rule 2: an API plane accepts a token only if its `aud` is absent, this
// area's resource, or a registered MCP resource.
//
// It is immutable once built and safe for concurrent use. A nil *PlanePolicy behaves as the
// CLOSED policy (see NewClosedPlanePolicy): a call site that forgot its wiring refuses
// loudly instead of admitting every audience.
type PlanePolicy struct {
	mode config.TokenAudienceMode
	area string
	// own is this area's resource identifier, or "" when the area is not a platform area
	// or no base is configured. "" never matches anything.
	own    string
	mcp    map[string]struct{}
	ext    map[string]struct{}
	others map[string]struct{} // other platform areas' resources
	rec    Recorder
	lim    *warnLimiter
}

// NewPlanePolicy builds the policy for one area from the instance's token-audience block.
// It validates the block again, so a hand-built configuration cannot install a policy the
// loader would have refused.
//
// An area that is not a platform area (including empty) gets a policy with NO own-area
// resource: a token bound to some area is then never "this area's". That is not an error,
// because test Microservices use made-up area names and a break-glass build uses a struct
// literal; a production area is always in the roster, which a chart test pins.
func NewPlanePolicy(cfg config.TokenAudienceConfiguration, area string, rec Recorder) (*PlanePolicy, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	p := &PlanePolicy{
		mode:   cfg.EffectiveEnforcement(),
		area:   area,
		mcp:    map[string]struct{}{},
		ext:    map[string]struct{}{},
		others: map[string]struct{}{},
		rec:    rec,
		lim:    newWarnLimiter(time.Now),
	}
	if config.IsAreaAudience(area) {
		p.own = config.AreaResource(cfg.AreaBaseUrl, area)
	}
	for _, a := range config.AreaAudiences() {
		if a == area {
			continue
		}
		if r := config.AreaResource(cfg.AreaBaseUrl, a); r != "" {
			p.others[r] = struct{}{}
		}
	}
	for _, r := range cfg.McpResources {
		if r != "" {
			p.mcp[r] = struct{}{}
		}
	}
	for _, r := range cfg.ExternalResources {
		if r != "" {
			p.ext[r] = struct{}{}
		}
	}
	return p, nil
}

// NewClosedPlanePolicy is the policy a gate starts with: enforce mode, no own-area resource
// and no MCP resources, so it accepts only tokens that carry no audience. A production path
// that forgot to install the configured policy therefore breaks the MCP pass-through loudly
// (refused) instead of silently admitting every audience.
//
// It returns ONE shared instance, so every caller (a gate with no policy, a nil policy at a
// call site) shares one warning limiter; a fresh copy per call would never rate limit. It has
// no recorder, so its refusals are logged, not counted.
func NewClosedPlanePolicy() *PlanePolicy { return closedPolicy }

// Mode is the policy's effective mode.
func (p *PlanePolicy) Mode() config.TokenAudienceMode {
	if p == nil {
		return config.TokenAudienceEnforce
	}
	return p.mode
}

// OwnResource is this area's resource identifier, or "" when it has none.
func (p *PlanePolicy) OwnResource() string {
	if p == nil {
		return ""
	}
	return p.own
}

// Classify returns the class of aud. "Empty" means len(aud) == 0: aud == [""] is a present
// audience whose member is the empty string, so it is unlisted. Several members use RFC 7519
// section 4.1.3 "contains" semantics, and when several classes match the first in the order
// own_area, mcp, other_area, external wins, so the label is deterministic. Matching is
// byte-exact, with no normalisation.
func (p *PlanePolicy) Classify(aud []string) AudienceClass {
	if len(aud) == 0 {
		return AudNone
	}
	if p == nil {
		return AudUnlisted
	}
	var own, mcp, other, ext bool
	for _, m := range aud {
		if m == "" {
			continue
		}
		if p.own != "" && m == p.own {
			own = true
		}
		if _, ok := p.mcp[m]; ok {
			mcp = true
		}
		if _, ok := p.others[m]; ok {
			other = true
		}
		if _, ok := p.ext[m]; ok {
			ext = true
		}
	}
	switch {
	case own:
		return AudOwnArea
	case mcp:
		return AudMCP
	case other:
		return AudOtherArea
	case ext:
		return AudExternal
	}
	return AudUnlisted
}

// accepted is the accept set of a plane: no audience, this area, or a registered MCP resource.
func (c AudienceClass) accepted() bool {
	return c == AudNone || c == AudOwnArea || c == AudMCP
}

// Admit applies the rule to a token that has ALREADY passed signature, expiry and type
// verification, so a forged token can never move the counters. It returns nil when the
// request may proceed: always in observe mode, and in enforce mode only for an accepted
// class. It records exactly one observation per call.
//
// A nil policy is the closed policy.
func (p *PlanePolicy) Admit(c *Claims, plane Plane) error {
	if p == nil {
		p = closedPolicy
	}
	if c == nil {
		return ErrAudienceRefused
	}
	class := p.Classify(c.Audience)
	verdict := VerdictAccepted
	var err error
	if !class.accepted() {
		if p.mode == config.TokenAudienceEnforce {
			verdict = VerdictRejected
			err = ErrAudienceRefused
		} else {
			verdict = VerdictWouldReject
		}
	}
	if p.rec != nil {
		p.rec.Record(plane, class, verdict)
	}
	if verdict != VerdictAccepted {
		p.warn(c, plane, class, verdict)
	}
	return err
}

// closedPolicy is the shared closed policy (see NewClosedPlanePolicy); it also serves a nil
// *PlanePolicy.
var closedPolicy = &PlanePolicy{
	mode:   config.TokenAudienceEnforce,
	mcp:    map[string]struct{}{},
	ext:    map[string]struct{}{},
	others: map[string]struct{}{},
	lim:    newWarnLimiter(time.Now),
}

func (p *PlanePolicy) warn(c *Claims, plane Plane, class AudienceClass, verdict AudienceVerdict) {
	suppressed, ok := p.lim.allow(string(plane) + "\x00" + string(class) + "\x00" + c.ClientId)
	if !ok {
		return
	}
	log.Warn().
		Str("area", p.area).
		Str("plane", string(plane)).
		Str("audience", string(class)).
		Str("verdict", string(verdict)).
		Str("tenant", c.Tenant).
		Str("client_id", c.ClientId).
		Str("sub_kind", subKind(c)).
		Str("aud", quoteAud(c.Audience)).
		Int("aud_count", len(c.Audience)).
		Int("suppressed", suppressed).
		Msg("Token audience is not accepted by this plane")
}

// subKind classifies the principal without ever logging it: sub is an email for people.
func subKind(c *Claims) string {
	switch {
	case c.TokenType == TokenTypeService:
		return "service"
	case c.ClientId != "" && c.Email == "":
		return "service-account"
	default:
		return "person"
	}
}

// quoteAud renders an attacker-influenced audience for a log line: truncated to
// maxLoggedAud bytes on a rune boundary and quoted, so it cannot inject lines or fields.
func quoteAud(aud []string) string {
	return QuoteLogValue(strings.Join(aud, " "))
}

// QuoteLogValue renders an attacker-influenced string for a log line: truncated to 256 bytes
// on a rune boundary and quoted, so it cannot inject lines or fields.
func QuoteLogValue(s string) string {
	if len(s) > maxLoggedAud {
		cut := maxLoggedAud
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut]
	}
	return strconv.Quote(s)
}

// warnLimiter lets one line through per key per warnInterval, over a bounded set of keys. It
// is keyed on (plane, audience, client_id) and never on the `aud` value, which an attacker
// chooses; keying on the client means one noisy caller cannot suppress another client's
// diagnostic line.
type warnLimiter struct {
	now func() time.Time
	mu  sync.Mutex
	ll  *list.List // front = most recently used; elements are *warnEntry
	idx map[string]*list.Element
}

type warnEntry struct {
	key        string
	last       time.Time
	suppressed int
}

func newWarnLimiter(now func() time.Time) *warnLimiter {
	return &warnLimiter{now: now, ll: list.New(), idx: map[string]*list.Element{}}
}

// allow reports whether a line for key may be written now, and how many were held back since
// the last one.
func (l *warnLimiter) allow(key string) (suppressed int, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if el, found := l.idx[key]; found {
		e := el.Value.(*warnEntry)
		l.ll.MoveToFront(el)
		if now.Sub(e.last) < warnInterval {
			e.suppressed++
			return 0, false
		}
		suppressed = e.suppressed
		e.suppressed = 0
		e.last = now
		return suppressed, true
	}
	l.idx[key] = l.ll.PushFront(&warnEntry{key: key, last: now})
	if l.ll.Len() > warnKeys {
		oldest := l.ll.Back()
		l.ll.Remove(oldest)
		delete(l.idx, oldest.Value.(*warnEntry).key)
	}
	return 0, true
}

// WarnLimiter is the rate limiter behind the audience warnings, exported so the
// authorization server rate limits its own warnings the same way: one line per key per
// minute, over a bounded key set (256, least recently used evicted, and eviction only ever
// lets an extra line through). Key it on a bounded, authenticated identity such as
// (grant, class, client_id), never on a value the sender chooses.
type WarnLimiter struct{ l *warnLimiter }

// NewWarnLimiter builds a limiter over the given clock (nil is time.Now).
func NewWarnLimiter(now func() time.Time) *WarnLimiter {
	if now == nil {
		now = time.Now
	}
	return &WarnLimiter{l: newWarnLimiter(now)}
}

// Allow reports whether a line for key may be written now, and how many were held back
// since the last one.
func (w *WarnLimiter) Allow(key string) (suppressed int, ok bool) { return w.l.allow(key) }
