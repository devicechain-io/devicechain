// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package semantic_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/semantic"
)

func TestHostileInputIsRefused(t *testing.T) {
	r := reg(t)
	ctx := semantic.TenantContext{
		EnabledPacks:   []string{"brick"},
		CustomPrefixes: map[string]string{"ex": "https://example.org/ns#"},
	}
	for name, in := range map[string]string{
		"NUL":                       "ex:A\x00B",
		"NUL hiding a newline":      "ex:A\x00\nB",
		"NUL in a pack term":        "brick:Lumi\x00naire",
		"NUL alone":                 "\x00",
		"newline":                   "ex:A\nB",
		"tab":                       "ex:A\tB",
		"invalid UTF-8":             "ex:A\xff",
		"C1 control":                "ex:A\u0085B",
		"bidirectional override":    "ex:A\u202eB",
		"zero-width space":          "ex:A\u200bB",
		"no-break space":            "ex:A\u00a0B",
		"replacement character":     "ex:A\ufffdB",
		"second fragment delimiter": "ex:A#B",
		"full IRI with a NUL":       "https://example.org/ns#A\x00B",
		"full IRI, second fragment": "https://example.org/ns#A#B",
		"angle bracket":             "ex:A>B",
		"backslash":                 "ex:A\\B",
	} {
		_, err := r.Validate(in, semantic.KindClass, ctx)
		if !errors.Is(err, semantic.ErrMalformed) {
			t.Errorf("%s: Validate(%q) = %v, want ErrMalformed", name, in, err)
		}
		if _, err := r.Expand(in, ctx); !errors.Is(err, semantic.ErrMalformed) {
			t.Errorf("%s: Expand(%q) = %v, want ErrMalformed", name, in, err)
		}
	}
	// The same characters in a namespace.
	for name, ns := range map[string]string{
		"NUL":                       "https://example.org/\x00",
		"no host":                   "https://@/",
		"userinfo":                  "https://user:pass@example.org/ns#",
		"fragment before the end":   "https://example.org/a#b/",
		"two fragment delimiters":   "https://example.org/a##",
		"bidirectional override":    "https://example.org/\u202e",
		"urn with nothing after it": "urn:",
	} {
		c := semantic.TenantContext{CustomPrefixes: map[string]string{"a": ns}}
		if err := r.ValidateContext(c); !errors.Is(err, semantic.ErrMalformed) {
			t.Errorf("namespace %s: ValidateContext = %v, want ErrMalformed", name, err)
		}
	}
}

// TestReturnedIRIsValidateAgain pins the storage contract: Validate returns the
// expanded IRI, callers store it, and what was stored must be acceptable input
// later. A namespace at its limit plus a local part is longer than a CURIE may
// be, so full IRIs get their own, larger, limit.
func TestReturnedIRIsValidateAgain(t *testing.T) {
	r := reg(t)
	ns := "https://example.org/" + strings.Repeat("a", semantic.MaxCURIEBytes-len("https://example.org/"))
	if len(ns) != semantic.MaxCURIEBytes {
		t.Fatalf("test setup: namespace is %d bytes", len(ns))
	}
	ctx := semantic.TenantContext{CustomPrefixes: map[string]string{"ex": ns}}
	got, err := r.Validate("ex:T", semantic.KindClass, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) <= semantic.MaxCURIEBytes {
		t.Fatalf("test setup: expansion is %d bytes, not over the CURIE limit", len(got))
	}
	again, err := r.Validate(string(got), semantic.KindClass, ctx)
	if err != nil || again != got {
		t.Errorf("a returned IRI must validate again unchanged: %q, %v", again, err)
	}
	// The longest possible expansion -- a maximal CURIE over a maximal namespace --
	// is still within the full-IRI limit.
	long := "ex:" + strings.Repeat("b", semantic.MaxCURIEBytes-len("ex:"))
	got, err = r.Validate(long, semantic.KindClass, ctx)
	if err != nil || len(got) > semantic.MaxIRIBytes {
		t.Fatalf("maximal expansion: %d bytes, %v", len(got), err)
	}
	if _, err := r.Validate(string(got), semantic.KindClass, ctx); err != nil {
		t.Errorf("the maximal expansion does not validate again: %v", err)
	}
	// A full IRI past its limit is refused.
	over := ns + strings.Repeat("c", semantic.MaxIRIBytes)
	if _, err := r.Validate(over, semantic.KindClass, ctx); !errors.Is(err, semantic.ErrMalformed) {
		t.Errorf("over-long full IRI: %v", err)
	}
}

// TestRegistryHandsOutCopies: the registry promises to be immutable and safe
// for concurrent use, which holds only if nothing a caller can reach is shared
// with what Validate reads.
func TestRegistryHandsOutCopies(t *testing.T) {
	r := reg(t)
	brickOnly := ctxWith("brick")
	lum := semantic.IRI(brickNS + "Luminaire")

	isaBefore := r.IsA(lum, brickNS+"Equipment")
	p, ok := r.Pack("brick")
	if !ok {
		t.Fatal("no brick pack")
	}
	delete(p.Terms, lum)
	p.Terms[brickNS+"Planted"] = semantic.Term{IRI: brickNS + "Planted", Kind: semantic.KindClass}
	p.Namespace = "https://hijacked.example/"
	for _, q := range r.Packs() {
		q.Terms = nil
		q.Prefix = "hijacked"
	}
	if _, err := r.Validate("brick:Luminaire", semantic.KindClass, brickOnly); err != nil {
		t.Errorf("mutating a returned pack changed Validate: %v", err)
	}
	if _, err := r.Validate("brick:Planted", semantic.KindClass, brickOnly); !errors.Is(err, semantic.ErrUnknownTerm) {
		t.Errorf("a term planted in a returned pack became valid: %v", err)
	}

	term, _ := r.Term(lum)
	if len(term.Parents) == 0 {
		t.Fatal("test setup: Luminaire has no parents")
	}
	term.Parents[0] = "https://evil.example/Parent"
	again, _ := r.Term(lum)
	if again.Parents[0] == "https://evil.example/Parent" {
		t.Error("mutating a returned term's Parents changed the registry")
	}
	if got := r.IsA(lum, brickNS+"Equipment"); got != isaBefore {
		t.Errorf("IsA changed after a caller mutated a returned term: %v -> %v", isaBefore, got)
	}
	res, err := r.Resolve("brick:Luminaire", semantic.KindClass, brickOnly)
	if err != nil {
		t.Fatal(err)
	}
	res.Term.Parents[0] = "https://evil.example/Parent"
	if again2, _ := r.Term(lum); again2.Parents[0] == "https://evil.example/Parent" {
		t.Error("mutating a Resolution's term changed the registry")
	}

	// A pack handed to NewRegistry is copied too.
	const ns = "https://x.example/ns#"
	src := &semantic.Pack{ID: "x", Prefix: "x", Namespace: ns, Terms: map[semantic.IRI]semantic.Term{
		ns + "A": {IRI: ns + "A", Kind: semantic.KindClass, Parents: []semantic.IRI{ns + "B"}},
		ns + "B": {IRI: ns + "B", Kind: semantic.KindClass},
	}}
	r2, err := semantic.NewRegistry([]*semantic.Pack{src}, nil)
	if err != nil {
		t.Fatal(err)
	}
	delete(src.Terms, ns+"A")
	src.Terms[ns+"B"] = semantic.Term{IRI: ns + "B", Kind: semantic.KindPredicate}
	if _, err := r2.Validate("x:A", semantic.KindClass, ctxWith("x")); err != nil {
		t.Errorf("mutating the source pack after NewRegistry changed Validate: %v", err)
	}
	if !r2.IsA(ns+"A", ns+"B") {
		t.Error("mutating the source pack after NewRegistry changed IsA")
	}
}
