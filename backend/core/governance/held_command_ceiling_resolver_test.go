// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestResolveHeldCommandCeiling pins the wire fold: a usable ceiling passes through;
// null or non-positive inherits the SERVICE's default, never unlimited. The default is
// a parameter here (unlike resolveShedPriority's package const) because how large a
// held backlog is tolerable is the enforcing service's number, not this package's.
func TestResolveHeldCommandCeiling(t *testing.T) {
	const def = 5000
	cases := []struct {
		name string
		in   *int32
		want int
	}{
		{"null inherits the service default", nil, def},
		{"a usable ceiling passes through", i32(250), 250},
		{"one is a legal (if tiny) ceiling", i32(1), 1},
		{"a large ceiling passes through", i32(1_000_000), 1_000_000},
		{"zero is not a ceiling → inherit", i32(0), def},
		{"negative is not a ceiling → inherit", i32(-5), def},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveHeldCommandCeiling(c.in, def); got != c.want {
				t.Errorf("resolveHeldCommandCeiling(%v, %d) = %d, want %d", c.in, def, got, c.want)
			}
		})
	}
	// The counterweight to every case above: NO input resolves to "no bound". A zero
	// would be exactly that — command-delivery reading it as "hold as many as you like"
	// or "hold none" are both wrong, and only the first is silent.
	for _, in := range []*int32{nil, i32(0), i32(-1)} {
		if got := resolveHeldCommandCeiling(in, def); got <= 0 {
			t.Fatalf("resolveHeldCommandCeiling(%v, %d) = %d — a HELD ceiling must never resolve to a non-positive value", in, def, got)
		}
	}
}

// TestHeldCommandCeilingQueryAndResponseAgree is the seam a unit test cannot otherwise
// reach: svcclient.Client is concrete, so nothing in fetchHeldCommandCeiling is
// injectable. A field name that does not match the schema, or a json tag that does not
// match the field, both fail SILENTLY — the query errors (every tenant sits at the
// service default behind a WARN) or the response decodes to nil (every operator
// override is ignored, and the tenant still looks correctly bounded).
func TestHeldCommandCeilingQueryAndResponseAgree(t *testing.T) {
	if !strings.Contains(heldCommandCeilingQuery, "heldCommandCeiling") {
		t.Fatalf("the query does not select heldCommandCeiling: %q", heldCommandCeilingQuery)
	}
	if !strings.Contains(heldCommandCeilingQuery, "tenantGovernance") {
		t.Fatalf("the query does not read tenantGovernance: %q", heldCommandCeilingQuery)
	}

	// A real response body, decoded through the type the fetch decodes into.
	var out heldCommandCeilingResponse
	if err := json.Unmarshal([]byte(`{"tenantGovernance":{"heldCommandCeiling":750}}`), &out); err != nil {
		t.Fatalf("decoding a well-formed response failed: %v", err)
	}
	if out.TenantGovernance.HeldCommandCeiling == nil {
		t.Fatal("heldCommandCeiling decoded to nil from a response that carries it — the json tag does not match the wire field")
	}
	if got := resolveHeldCommandCeiling(out.TenantGovernance.HeldCommandCeiling, 5000); got != 750 {
		t.Errorf("decoded ceiling folded to %d, want 750", got)
	}

	// A null on the wire is the ordinary "neither the tenant nor its tier declares one"
	// case, and must decode to nil rather than to zero.
	var null heldCommandCeilingResponse
	if err := json.Unmarshal([]byte(`{"tenantGovernance":{"heldCommandCeiling":null}}`), &null); err != nil {
		t.Fatalf("decoding a null response failed: %v", err)
	}
	if null.TenantGovernance.HeldCommandCeiling != nil {
		t.Fatal("a null heldCommandCeiling must decode to nil (inherit), not to a value")
	}
}

// TestNewHeldCommandCeilingResolverFloorsANonPositiveDefault pins the direction a
// mis-configured service fails in. A zero or negative configured default would mean
// "this tenant may hold nothing" for every tenant the cache has not warmed — an
// instance-wide refusal produced by a missing Helm value, which is the inversion of
// fail-open the whole resolver exists to avoid.
//
// It inspects the constructed resolver's default rather than calling Resolve: resolving
// would fire a background refresh against a nil client.
func TestNewHeldCommandCeilingResolverFloorsANonPositiveDefault(t *testing.T) {
	for _, def := range []int{0, -1} {
		r := NewHeldCommandCeilingResolver(nil, "http://um.invalid/graphql", def)
		if r.def != DefaultHeldCommandCeiling {
			t.Errorf("a configured default of %d became %d, want the floor %d", def, r.def, DefaultHeldCommandCeiling)
		}
	}
	// A usable default is honoured verbatim — the counterweight, without which the
	// flooring above could be "always use the package const" and still pass.
	r := NewHeldCommandCeilingResolver(nil, "http://um.invalid/graphql", 250)
	if r.def != 250 {
		t.Errorf("a configured default of 250 became %d — the service's own number must win", r.def)
	}
	if DefaultHeldCommandCeiling <= 0 {
		t.Fatal("the package floor is not a live ceiling — a HELD backlog would be unbounded or refused outright")
	}
}
