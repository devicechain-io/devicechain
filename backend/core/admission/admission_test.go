// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admission

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 9, 18, 0, 0, 0, time.UTC)

func f64(v float64) *float64 { return &v }

func lightingPolicy() Policy {
	return Policy{
		Version: "city-a/2026-11",
		Rules: map[string]Rule{
			"lighting.dim": {
				MaxTargets: 3,
				MaxWindow:  12 * time.Hour,
				MinLead:    10 * time.Minute,
				// The lighting floor: never below 30 %.
				Bounds: map[string]Bound{"level": {Min: f64(30), Max: f64(100)}},
			},
			"lighting.report": {MaxTargets: 100, MaxWindow: time.Hour},
		},
	}
}

func dimProposal() Proposal {
	return Proposal{
		Submitter:  "cms:city-a",
		Tenant:     "city-a",
		Action:     "lighting.dim",
		Targets:    []string{"lamp-2", "lamp-1"},
		Parameters: json.RawMessage(`{"level": 40, "ramp": {"seconds": 5}}`),
		NotBefore:  t0.Add(time.Hour),
		NotAfter:   t0.Add(3 * time.Hour),
	}
}

func keys(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func issuerAndVerifier(t *testing.T) (*Issuer, *Verifier) {
	t.Helper()
	pub, priv := keys(t)
	iss, err := NewIssuer("k1", priv)
	if err != nil {
		t.Fatal(err)
	}
	ver, err := NewVerifier(map[string]ed25519.PublicKey{"k1": pub})
	if err != nil {
		t.Fatal(err)
	}
	return iss, ver
}

func mustDigest(t *testing.T, p Proposal) string {
	t.Helper()
	d, err := p.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// ---- canonical parameters ----

func TestCanonicalJSONIsOneSpelling(t *testing.T) {
	a, err := canonicalJSON([]byte(` { "b" : [1, {"y":true,"x":null}], "a":"<x>" } `))
	if err != nil {
		t.Fatal(err)
	}
	bs := `\`
	want := `{"a":"` + bs + `u003cx` + bs + `u003e","b":[1,{"x":null,"y":true}]}`
	if string(a) != want {
		t.Fatalf("canonical = %s, want %s", a, want)
	}
	for _, empty := range [][]byte{nil, []byte(""), []byte("  "), []byte("null")} {
		c, err := canonicalJSON(empty)
		if err != nil || string(c) != "null" {
			t.Fatalf("canonical(%q) = %s, %v; want null", empty, c, err)
		}
	}
}

func TestCanonicalJSONKeepsNumberLiterals(t *testing.T) {
	one, _ := canonicalJSON([]byte(`{"v":1}`))
	oneDot, _ := canonicalJSON([]byte(`{"v":1.0}`))
	if string(one) == string(oneDot) {
		t.Fatal("1 and 1.0 must stay different spellings")
	}
	big, _ := canonicalJSON([]byte(`12345678901234567890123`))
	if string(big) != "12345678901234567890123" {
		t.Fatalf("a large literal lost precision: %s", big)
	}
}

func TestCanonicalJSONRefusals(t *testing.T) {
	deep := strings.Repeat("[", maxParameterDepth+2) + strings.Repeat("]", maxParameterDepth+2)
	for name, doc := range map[string]string{
		"duplicate key":        `{"level":10,"level":100}`,
		"nested duplicate key": `{"a":{"k":1,"k":1}}`,
		"trailing data":        `{"a":1} {"a":2}`,
		"invalid utf8":         "{\"a\":\"\xff\"}",
		"too deep":             deep,
		"syntax":               `{"a":}`,
		"unterminated":         `{"a":1`,
		"too large":            `"` + strings.Repeat("x", MaxParameterBytes) + `"`,
	} {
		if _, err := canonicalJSON([]byte(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Exactly at the depth limit is fine.
	ok := strings.Repeat("[", maxParameterDepth+1) + strings.Repeat("]", maxParameterDepth+1)
	if _, err := canonicalJSON([]byte(ok)); err != nil {
		t.Errorf("depth at the limit refused: %v", err)
	}
}

// ---- proposal digest ----

func TestDigestIgnoresSpellingButNotIntent(t *testing.T) {
	base := dimProposal()
	same := base
	same.Targets = []string{"lamp-1", "lamp-2"}
	same.Parameters = json.RawMessage(`{"ramp":{"seconds":5},"level":40}`)
	same.NotBefore = base.NotBefore.In(time.FixedZone("CET", 3600))
	if mustDigest(t, base) != mustDigest(t, same) {
		t.Fatal("target order, key order or time zone changed the digest")
	}
	changes := map[string]func(*Proposal){
		"submitter":  func(p *Proposal) { p.Submitter = "cms:other" },
		"tenant":     func(p *Proposal) { p.Tenant = "city-b" },
		"action":     func(p *Proposal) { p.Action = "lighting.report" },
		"target":     func(p *Proposal) { p.Targets = []string{"lamp-1", "lamp-3"} },
		"parameters": func(p *Proposal) { p.Parameters = json.RawMessage(`{"level":41,"ramp":{"seconds":5}}`) },
		"notBefore":  func(p *Proposal) { p.NotBefore = p.NotBefore.Add(time.Nanosecond) },
		"notAfter":   func(p *Proposal) { p.NotAfter = p.NotAfter.Add(time.Second) },
	}
	for name, change := range changes {
		p := dimProposal()
		change(&p)
		if mustDigest(t, p) == mustDigest(t, base) {
			t.Errorf("changing %s did not change the digest", name)
		}
	}
	if d := mustDigest(t, base); !strings.HasPrefix(d, "sha256:") || len(d) != len("sha256:")+64 {
		t.Errorf("digest %q is not sha256:<64 hex>", d)
	}
}

func TestMalformedProposalsAreRefused(t *testing.T) {
	cases := map[string]func(*Proposal){
		"no submitter":     func(p *Proposal) { p.Submitter = "" },
		"no tenant":        func(p *Proposal) { p.Tenant = "" },
		"no action":        func(p *Proposal) { p.Action = "" },
		"long action":      func(p *Proposal) { p.Action = strings.Repeat("a", MaxNameBytes+1) },
		"bad utf8 tenant":  func(p *Proposal) { p.Tenant = "\xff" },
		"no targets":       func(p *Proposal) { p.Targets = nil },
		"empty target":     func(p *Proposal) { p.Targets = []string{"lamp-1", ""} },
		"duplicate target": func(p *Proposal) { p.Targets = []string{"lamp-1", "lamp-1"} },
		"too many targets": func(p *Proposal) { p.Targets = make([]string, MaxTargets+1) },
		"duplicate key":    func(p *Proposal) { p.Parameters = json.RawMessage(`{"level":10,"level":100}`) },
		"no notBefore":     func(p *Proposal) { p.NotBefore = time.Time{} },
		"no notAfter":      func(p *Proposal) { p.NotAfter = time.Time{} },
		"empty window":     func(p *Proposal) { p.NotAfter = p.NotBefore },
		"inverted window":  func(p *Proposal) { p.NotAfter = p.NotBefore.Add(-time.Second) },
	}
	for name, change := range cases {
		p := dimProposal()
		change(&p)
		if _, err := p.Digest(); !errors.Is(err, ErrMalformedProposal) {
			t.Errorf("%s: digest error = %v, want ErrMalformedProposal", name, err)
		}
		if _, err := Admit(t0, p, lightingPolicy()); !errors.Is(err, ErrMalformedProposal) {
			t.Errorf("%s: admit error = %v, want ErrMalformedProposal", name, err)
		}
	}
	// The caller's slice is not reordered by digesting.
	p := dimProposal()
	_, _ = p.Digest()
	if p.Targets[0] != "lamp-2" {
		t.Error("Digest sorted the caller's target slice in place")
	}
}

// ---- policy ----

func TestPolicyValidation(t *testing.T) {
	if err := lightingPolicy().Validate(); err != nil {
		t.Fatalf("the reference policy is invalid: %v", err)
	}
	bad := map[string]func(*Policy){
		"no version":    func(p *Policy) { p.Version = "" },
		"no rules":      func(p *Policy) { p.Rules = nil },
		"empty action":  func(p *Policy) { p.Rules[""] = Rule{MaxTargets: 1, MaxWindow: time.Hour} },
		"zero targets":  func(p *Policy) { p.Rules["x"] = Rule{MaxTargets: 0, MaxWindow: time.Hour} },
		"huge targets":  func(p *Policy) { p.Rules["x"] = Rule{MaxTargets: MaxTargets + 1, MaxWindow: time.Hour} },
		"zero window":   func(p *Policy) { p.Rules["x"] = Rule{MaxTargets: 1} },
		"negative lead": func(p *Policy) { p.Rules["x"] = Rule{MaxTargets: 1, MaxWindow: time.Hour, MinLead: -1} },
		"empty bound key": func(p *Policy) {
			p.Rules["x"] = Rule{MaxTargets: 1, MaxWindow: time.Hour, Bounds: map[string]Bound{"": {Min: f64(1)}}}
		},
		"empty bound": func(p *Policy) {
			p.Rules["x"] = Rule{MaxTargets: 1, MaxWindow: time.Hour, Bounds: map[string]Bound{"a": {}}}
		},
		"inverted bound": func(p *Policy) {
			p.Rules["x"] = Rule{MaxTargets: 1, MaxWindow: time.Hour, Bounds: map[string]Bound{"a": {Min: f64(2), Max: f64(1)}}}
		},
		"nan bound": func(p *Policy) {
			nan := 0.0
			nan = nan / nan
			p.Rules["x"] = Rule{MaxTargets: 1, MaxWindow: time.Hour, Bounds: map[string]Bound{"a": {Min: &nan}}}
		},
	}
	for name, change := range bad {
		p := lightingPolicy()
		change(&p)
		if err := p.Validate(); !errors.Is(err, ErrInvalidPolicy) {
			t.Errorf("%s: Validate = %v, want ErrInvalidPolicy", name, err)
		}
		if _, err := Admit(t0, dimProposal(), p); !errors.Is(err, ErrInvalidPolicy) {
			t.Errorf("%s: Admit = %v, want ErrInvalidPolicy", name, err)
		}
	}
}

func TestPolicyDigestTracksContent(t *testing.T) {
	a, err := lightingPolicy().Digest()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := lightingPolicy().Digest()
	if a != b {
		t.Fatal("the same policy gave two digests")
	}
	p := lightingPolicy()
	r := p.Rules["lighting.dim"]
	r.Bounds = map[string]Bound{"level": {Min: f64(20), Max: f64(100)}}
	p.Rules["lighting.dim"] = r
	c, _ := p.Digest()
	if c == a {
		t.Fatal("lowering the floor did not change the policy digest")
	}
}

// ---- admission ----

func reasonCodes(d Decision) []ReasonCode {
	var out []ReasonCode
	for _, r := range d.Reasons {
		out = append(out, r.Code)
	}
	return out
}

func TestAdmitAdmitsAProposalInsideThePolicy(t *testing.T) {
	d, err := Admit(t0, dimProposal(), lightingPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if d.Verdict != Admitted || len(d.Reasons) != 0 {
		t.Fatalf("decision = %+v, want admitted with no reasons", d)
	}
}

func TestAdmitRefusals(t *testing.T) {
	cases := []struct {
		name   string
		now    time.Time
		change func(*Proposal)
		want   []ReasonCode
	}{
		{"unknown action fails closed", t0, func(p *Proposal) { p.Action = "lighting.off" }, []ReasonCode{ReasonUnknownAction}},
		{"too many targets", t0, func(p *Proposal) { p.Targets = []string{"a", "b", "c", "d"} }, []ReasonCode{ReasonTooManyTargets}},
		{"window too long", t0, func(p *Proposal) { p.NotAfter = p.NotBefore.Add(12*time.Hour + time.Second) }, []ReasonCode{ReasonWindowTooLong}},
		{"window elapsed", t0.Add(3 * time.Hour), func(p *Proposal) {}, []ReasonCode{ReasonWindowElapsed, ReasonInsufficientLead}},
		{"lead one second short", t0.Add(50*time.Minute + time.Second), func(p *Proposal) {}, []ReasonCode{ReasonInsufficientLead}},
		{"below the floor", t0, func(p *Proposal) { p.Parameters = json.RawMessage(`{"level":29.9}`) }, []ReasonCode{ReasonParameterOutOfBounds}},
		{"above the ceiling", t0, func(p *Proposal) { p.Parameters = json.RawMessage(`{"level":100.5}`) }, []ReasonCode{ReasonParameterOutOfBounds}},
		{"out of float range", t0, func(p *Proposal) { p.Parameters = json.RawMessage(`{"level":1e400}`) }, []ReasonCode{ReasonParameterOutOfBounds}},
		{"bound missing", t0, func(p *Proposal) { p.Parameters = json.RawMessage(`{"ramp":1}`) }, []ReasonCode{ReasonParameterMissing}},
		{"quoted number", t0, func(p *Proposal) { p.Parameters = json.RawMessage(`{"level":"40"}`) }, []ReasonCode{ReasonParameterNotNumeric}},
		{"null level", t0, func(p *Proposal) { p.Parameters = json.RawMessage(`{"level":null}`) }, []ReasonCode{ReasonParameterNotNumeric}},
		{"not an object", t0, func(p *Proposal) { p.Parameters = json.RawMessage(`[40]`) }, []ReasonCode{ReasonParametersNotObject}},
		{"no parameters", t0, func(p *Proposal) { p.Parameters = nil }, []ReasonCode{ReasonParametersNotObject}},
		{"every reason at once", t0.Add(2 * time.Hour), func(p *Proposal) {
			p.Targets = []string{"a", "b", "c", "d"}
			p.NotAfter = p.NotBefore.Add(13 * time.Hour)
			p.Parameters = json.RawMessage(`{"level":5}`)
		}, []ReasonCode{ReasonTooManyTargets, ReasonWindowTooLong, ReasonInsufficientLead, ReasonParameterOutOfBounds}},
	}
	for _, c := range cases {
		p := dimProposal()
		c.change(&p)
		d, err := Admit(c.now, p, lightingPolicy())
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got := reasonCodes(d)
		if d.Verdict != Refused || strings.Join(codes(got), ",") != strings.Join(codes(c.want), ",") {
			t.Errorf("%s: %s %v, want REFUSED %v", c.name, d.Verdict, got, c.want)
		}
	}
}

func codes(cs []ReasonCode) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = string(c)
	}
	return out
}

func TestAdmitBoundaries(t *testing.T) {
	// Exactly at the floor, the ceiling, the longest window and the shortest lead: admitted.
	p := dimProposal()
	p.Parameters = json.RawMessage(`{"level":30}`)
	p.Targets = []string{"a", "b", "c"}
	p.NotAfter = p.NotBefore.Add(12 * time.Hour)
	d, err := Admit(p.NotBefore.Add(-10*time.Minute), p, lightingPolicy())
	if err != nil || d.Verdict != Admitted {
		t.Fatalf("at every boundary: %+v %v, want admitted", d, err)
	}
	p.Parameters = json.RawMessage(`{"level":100}`)
	if d, _ := Admit(t0, p, lightingPolicy()); d.Verdict != Admitted {
		t.Fatalf("at the ceiling: %+v", d)
	}
	// A rule with no lead admits a window that is already open, until it ends.
	open := dimProposal()
	open.Action = "lighting.report"
	open.Parameters = nil
	open.NotBefore, open.NotAfter = t0.Add(-time.Minute), t0.Add(time.Minute)
	if d, _ := Admit(t0, open, lightingPolicy()); d.Verdict != Admitted {
		t.Fatalf("open window with no lead: %+v", d)
	}
	if d, _ := Admit(open.NotAfter, open, lightingPolicy()); d.Verdict != Refused {
		t.Fatalf("at NotAfter the window has ended: %+v", d)
	}
}

// ---- receipts ----

func TestIssueAndVerifyRoundTrip(t *testing.T) {
	iss, ver := issuerAndVerifier(t)
	p := dimProposal()
	r, err := iss.Issue(t0, p, lightingPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if r.Verdict != Admitted || r.PolicyVersion != "city-a/2026-11" || r.KeyID != "k1" || r.ProposalDigest != mustDigest(t, p) {
		t.Fatalf("receipt = %+v", r)
	}
	// Through JSON and a parser that keeps another zone, it still verifies.
	b, _ := json.Marshal(r)
	var back Receipt
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	back.IssuedAt = back.IssuedAt.In(time.FixedZone("X", -5*3600))
	if err := ver.Verify(back, p); err != nil {
		t.Fatalf("round-tripped receipt: %v", err)
	}
	if back.ID() != r.ID() || !strings.HasPrefix(r.ID(), "sha256:") {
		t.Fatal("the receipt id moved through a round trip")
	}
	if err := ver.Authorizes(back, p, p.NotBefore); err != nil {
		t.Fatalf("at NotBefore: %v", err)
	}
	if err := ver.Authorizes(back, p, p.NotAfter.Add(-time.Nanosecond)); err != nil {
		t.Fatalf("just before NotAfter: %v", err)
	}
	if err := ver.Authorizes(back, p, p.NotAfter); !errors.Is(err, ErrOutsideWindow) {
		t.Fatalf("at NotAfter = %v, want ErrOutsideWindow", err)
	}
	if err := ver.Authorizes(back, p, p.NotBefore.Add(-time.Nanosecond)); !errors.Is(err, ErrOutsideWindow) {
		t.Fatalf("before NotBefore = %v, want ErrOutsideWindow", err)
	}
}

func TestEverySignedFieldIsSigned(t *testing.T) {
	iss, ver := issuerAndVerifier(t)
	p := dimProposal()
	r, err := iss.Issue(t0, p, lightingPolicy())
	if err != nil {
		t.Fatal(err)
	}
	tamper := map[string]func(*Receipt){
		"v":              func(r *Receipt) { r.V = 2 },
		"proposalDigest": func(r *Receipt) { r.ProposalDigest = "sha256:00" },
		"submitter":      func(r *Receipt) { r.Submitter = "x" },
		"tenant":         func(r *Receipt) { r.Tenant = "x" },
		"action":         func(r *Receipt) { r.Action = "x" },
		"notBefore":      func(r *Receipt) { r.NotBefore = r.NotBefore.Add(-time.Hour) },
		"notAfter":       func(r *Receipt) { r.NotAfter = r.NotAfter.Add(time.Hour) },
		"verdict":        func(r *Receipt) { r.Verdict = Refused },
		"reasons":        func(r *Receipt) { r.Reasons = []Reason{{ReasonUnknownAction, "x"}} },
		"policyVersion":  func(r *Receipt) { r.PolicyVersion = "x" },
		"policyDigest":   func(r *Receipt) { r.PolicyDigest = "x" },
		"issuedAt":       func(r *Receipt) { r.IssuedAt = r.IssuedAt.Add(time.Second) },
		"signature":      func(r *Receipt) { r.Signature = append([]byte(nil), r.Signature...); r.Signature[0] ^= 1 },
	}
	for name, change := range tamper {
		c := r
		change(&c)
		if err := ver.VerifySignature(c); !errors.Is(err, ErrBadSignature) {
			t.Errorf("tampered %s: %v, want ErrBadSignature", name, err)
		}
	}
	c := r
	c.KeyID = "k2"
	if err := ver.VerifySignature(c); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("unknown key: %v", err)
	}
}

func TestAReceiptBelongsToOneProposal(t *testing.T) {
	iss, ver := issuerAndVerifier(t)
	p := dimProposal()
	r, _ := iss.Issue(t0, p, lightingPolicy())
	other := dimProposal()
	other.Targets = []string{"lamp-1", "lamp-3"}
	if err := ver.Authorizes(r, other, other.NotBefore); !errors.Is(err, ErrProposalMismatch) {
		t.Fatalf("receipt reused for another target set: %v", err)
	}
	louder := dimProposal()
	louder.Parameters = json.RawMessage(`{"level":100,"ramp":{"seconds":5}}`)
	if err := ver.Authorizes(r, louder, louder.NotBefore); !errors.Is(err, ErrProposalMismatch) {
		t.Fatalf("receipt reused for other parameters: %v", err)
	}
	malformedP := dimProposal()
	malformedP.Targets = nil
	if err := ver.Verify(r, malformedP); !errors.Is(err, ErrMalformedProposal) {
		t.Fatalf("malformed proposal: %v", err)
	}
}

func TestReadableCopiesMustAgreeWithTheProposal(t *testing.T) {
	// A receipt whose signed readable copy of the window disagrees with the proposal it is
	// shown with is refused, even though the digest matches: built here by signing a
	// deliberately inconsistent receipt with the real key.
	pub, priv := keys(t)
	ver, _ := NewVerifier(map[string]ed25519.PublicKey{"k1": pub})
	p := dimProposal()
	r := Receipt{V: 1, ProposalDigest: mustDigest(t, p), Submitter: p.Submitter, Tenant: p.Tenant,
		Action: p.Action, NotBefore: p.NotBefore, NotAfter: p.NotAfter.Add(24 * time.Hour),
		Verdict: Admitted, PolicyVersion: "v", PolicyDigest: "d", IssuedAt: t0, KeyID: "k1"}
	b, _ := r.signingBytes()
	r.Signature = ed25519.Sign(priv, b)
	if err := ver.VerifySignature(r); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := ver.Verify(r, p); !errors.Is(err, ErrProposalMismatch) {
		t.Fatalf("inconsistent readable window: %v, want ErrProposalMismatch", err)
	}
}

func TestARefusalIsAReceiptButNeverAuthorizes(t *testing.T) {
	iss, ver := issuerAndVerifier(t)
	p := dimProposal()
	p.Parameters = json.RawMessage(`{"level":10}`)
	r, err := iss.Issue(t0, p, lightingPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if r.Verdict != Refused || len(r.Reasons) != 1 || r.Reasons[0].Code != ReasonParameterOutOfBounds {
		t.Fatalf("receipt = %+v", r)
	}
	if err := ver.Verify(r, p); err != nil {
		t.Fatalf("a refusal is still a genuine receipt: %v", err)
	}
	if err := ver.Authorizes(r, p, p.NotBefore); !errors.Is(err, ErrNotAdmitted) {
		t.Fatalf("refusal authorizes: %v", err)
	}
}

func TestIssueRefusesWhatCannotBeBound(t *testing.T) {
	iss, _ := issuerAndVerifier(t)
	p := dimProposal()
	p.Targets = nil
	if _, err := iss.Issue(t0, p, lightingPolicy()); !errors.Is(err, ErrMalformedProposal) {
		t.Fatalf("malformed: %v", err)
	}
	if _, err := iss.Issue(t0, dimProposal(), Policy{}); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("invalid policy: %v", err)
	}
}

func TestKeyConstruction(t *testing.T) {
	pub, priv := keys(t)
	if _, err := NewIssuer("", priv); err == nil {
		t.Error("empty key id accepted")
	}
	if _, err := NewIssuer("k", priv[:10]); err == nil {
		t.Error("short private key accepted")
	}
	if _, err := NewVerifier(nil); err == nil {
		t.Error("verifier with no keys accepted")
	}
	if _, err := NewVerifier(map[string]ed25519.PublicKey{"": pub}); err == nil {
		t.Error("empty key id accepted")
	}
	if _, err := NewVerifier(map[string]ed25519.PublicKey{"k": pub[:5]}); err == nil {
		t.Error("short public key accepted")
	}
	// The verifier keeps its own copy: a caller mutating its slice afterwards changes nothing.
	iss, _ := NewIssuer("k", priv)
	own := append(ed25519.PublicKey(nil), pub...)
	ver, _ := NewVerifier(map[string]ed25519.PublicKey{"k": own})
	own[0] ^= 1
	r, _ := iss.Issue(t0, dimProposal(), lightingPolicy())
	if err := ver.VerifySignature(r); err != nil {
		t.Fatalf("the verifier aliased the caller's key: %v", err)
	}
}

func TestBoundsCompareTheLiteralExactly(t *testing.T) {
	for lit, want := range map[string]Verdict{
		"29.999999999999999999":      Refused, // rounds to 30 through float64
		"30.000000000000000000":      Admitted,
		"100.00000000000000000001":   Refused, // rounds to 100 through float64
		"3e1":                        Admitted,
		"3E+1":                       Admitted,
		"2.9999999999999999999e1":    Refused,
		"1e401":                      Refused, // exponent beyond the comparison limit
		"1e-401":                     Refused, // likewise, refused rather than built
		"0.000000000000000000000001": Refused,
	} {
		p := dimProposal()
		p.Parameters = json.RawMessage(`{"level":` + lit + `}`)
		d, err := Admit(t0, p, lightingPolicy())
		if err != nil || d.Verdict != want {
			t.Errorf("level %s: %+v %v, want %s", lit, d, err, want)
		}
	}
}

func TestUnpairedSurrogatesAreRefused(t *testing.T) {
	bs := `\`
	refused := []string{
		`"` + bs + `ud800"`,
		`"` + bs + `udc00"`,
		`"` + bs + `uD800x"`,
		`"` + bs + `ud800` + bs + `u0041"`,
		`{"k` + bs + `udbff":1}`,
	}
	for _, doc := range refused {
		if _, err := canonicalJSON([]byte(doc)); err == nil {
			t.Errorf("%s accepted", doc)
		}
	}
	accepted := []string{
		`"` + bs + `ud83d` + bs + `ude00"`, // a valid pair
		`"` + bs + `ufffd"`,
		`"` + bs + bs + `ud800"`, // an escaped backslash, then plain text
		`"` + bs + `n` + bs + `u0041"`,
	}
	for _, doc := range accepted {
		if _, err := canonicalJSON([]byte(doc)); err != nil {
			t.Errorf("%s refused: %v", doc, err)
		}
	}
}
