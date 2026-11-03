// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package attr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

type call struct {
	query string
	vars  map[string]any
}

// fakeClient answers each call with the next canned response, a JSON object as it would
// sit under the envelope's "data" key, so the tests parse the shape the code really reads.
type fakeClient struct {
	responses []string
	errs      []error
	calls     []call
}

func (f *fakeClient) Query(_ context.Context, _ string, query string, vars map[string]any, out any) error {
	i := len(f.calls)
	f.calls = append(f.calls, call{query: query, vars: vars})
	if i < len(f.errs) && f.errs[i] != nil {
		return f.errs[i]
	}
	if i >= len(f.responses) {
		return fmt.Errorf("call %d has no canned response; an unbounded loop looks like this", i+1)
	}
	return json.Unmarshal([]byte(f.responses[i]), out)
}

func facets(total int, kv ...string) string {
	rows := []string{}
	for i := 0; i+1 < len(kv); i += 2 {
		rows = append(rows, fmt.Sprintf(`{"key":%q,"valueType":%q}`, kv[i], kv[i+1]))
	}
	return fmt.Sprintf(`{"facetKeys":{"results":[%s],"pagination":{"totalRecords":%d}}}`, strings.Join(rows, ","), total)
}

const stored = `{"setEntityAttribute":{"scope":"SHARED","attrKey":"floor","valueType":"LONG","value":"3","lastUpdated":"t"}}`

func target() Target { return Target{Endpoint: "http://dm/graphql", Kind: "device", Token: "d1"} }

func request(t *testing.T, c call) map[string]any {
	t.Helper()
	r, ok := c.vars["request"].(map[string]any)
	if !ok {
		t.Fatalf("the set call carried no request object: %v", c.vars)
	}
	return r
}

// A SHARED write with no --type takes the declared type, and the request carries the
// scope and type the selector reads. Asserting the VALUES on the wire is the point:
// SHARED/LONG is what makes the value match; STRING would save and match nothing.
func TestSetTakesTheDeclaredTypeAtSharedScope(t *testing.T) {
	f := &fakeClient{responses: []string{facets(2, "zone", "STRING", "floor", "LONG"), stored}}
	got, err := Set(context.Background(), f, SetOptions{Target: target(), Key: "floor", Value: "3"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Key != "floor" || got.ValueType != "LONG" {
		t.Errorf("unexpected stored row %+v", got)
	}
	r := request(t, f.calls[1])
	want := map[string]any{"entityType": "device", "entity": "d1", "scope": "SHARED",
		"attrKey": "floor", "valueType": "LONG", "value": "3"}
	for k, v := range want {
		if r[k] != v {
			t.Errorf("request %s = %v, want %v", k, r[k], v)
		}
	}
	if m := f.calls[0].vars["criteria"].(map[string]any)["memberType"]; m != "device" {
		t.Errorf("declarations were looked up for %v, not the entity's family", m)
	}
}

// Negative control: a type that contradicts the declaration is refused and nothing is
// written, since the write would succeed and never match.
func TestSetRefusesATypeThatContradictsTheDeclaration(t *testing.T) {
	f := &fakeClient{responses: []string{facets(1, "floor", "LONG")}}
	_, err := Set(context.Background(), f, SetOptions{Target: target(), Key: "floor", Value: "3", ValueType: "string"})
	if err == nil || !strings.Contains(err.Error(), "declared as LONG") {
		t.Fatalf("want a refusal naming the declared type, got %v", err)
	}
	if len(f.calls) != 1 {
		t.Errorf("a refused write still reached the mutation: %d calls", len(f.calls))
	}
}

func TestSetOfAnUndeclaredKeyNeedsAType(t *testing.T) {
	f := &fakeClient{responses: []string{facets(0)}}
	_, err := Set(context.Background(), f, SetOptions{Target: target(), Key: "adhoc", Value: "x"})
	if err == nil || !strings.Contains(err.Error(), "--type is required") {
		t.Fatalf("want --type required, got %v", err)
	}
	f = &fakeClient{responses: []string{facets(0), stored}}
	if _, err := Set(context.Background(), f, SetOptions{Target: target(), Key: "adhoc", Value: "x", ValueType: "string"}); err != nil {
		t.Fatal(err)
	}
	if got := request(t, f.calls[1])["valueType"]; got != "STRING" {
		t.Errorf("valueType = %v, want STRING (upper-cased)", got)
	}
}

// Other scopes skip the declaration lookup entirely and require --type.
func TestSetAtAnotherScopeNeedsNoLookup(t *testing.T) {
	f := &fakeClient{responses: []string{stored}}
	if _, err := Set(context.Background(), f, SetOptions{Target: target(), Key: "k", Value: "v", Scope: "server", ValueType: "STRING"}); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || request(t, f.calls[0])["scope"] != "SERVER" {
		t.Errorf("unexpected calls %+v", f.calls)
	}
	f = &fakeClient{}
	if _, err := Set(context.Background(), f, SetOptions{Target: target(), Key: "k", Value: "v", Scope: "SERVER"}); err == nil {
		t.Error("a non-SHARED write with no --type must be refused")
	}
}

// The declaration may sit past the first page; the lookup must page rather than conclude
// "undeclared" from page one.
func TestDeclaredTypeLookupPages(t *testing.T) {
	f := &fakeClient{responses: []string{facets(2, "zone", "STRING"), facets(2, "floor", "LONG"), stored}}
	if _, err := Set(context.Background(), f, SetOptions{Target: target(), Key: "floor", Value: "3"}); err != nil {
		t.Fatal(err)
	}
	if got := request(t, f.calls[2])["valueType"]; got != "LONG" {
		t.Errorf("valueType = %v, want LONG from page two", got)
	}
}

func TestInputsAreValidatedBeforeAnyCall(t *testing.T) {
	cases := map[string]SetOptions{
		"kind":  {Target: Target{Endpoint: "e", Kind: "group", Token: "t"}, Key: "k"},
		"token": {Target: Target{Endpoint: "e", Kind: "device", Token: " "}, Key: "k"},
		"key":   {Target: target(), Key: ""},
		"scope": {Target: target(), Key: "k", Scope: "OTHER"},
		"type":  {Target: target(), Key: "k", ValueType: "INT"},
	}
	for name, o := range cases {
		f := &fakeClient{}
		if _, err := Set(context.Background(), f, o); err == nil {
			t.Errorf("%s: want an error", name)
		}
		if len(f.calls) != 0 {
			t.Errorf("%s: reached the server with invalid input", name)
		}
	}
}

func TestServerErrorsAreSurfacedWithContext(t *testing.T) {
	f := &fakeClient{responses: []string{facets(0)}, errs: []error{nil, fmt.Errorf("forbidden")}}
	_, err := Set(context.Background(), f, SetOptions{Target: target(), Key: "k", Value: "v", ValueType: "STRING"})
	if err == nil || !strings.Contains(err.Error(), "forbidden") || !strings.Contains(err.Error(), `device "d1"`) {
		t.Fatalf("want the server's refusal with the entity named, got %v", err)
	}
}

func TestGetPagesAndNarrows(t *testing.T) {
	row := func(k string) string {
		return fmt.Sprintf(`{"scope":"SHARED","attrKey":%q,"valueType":"STRING","value":"v","lastUpdated":null}`, k)
	}
	f := &fakeClient{responses: []string{
		`{"entityAttributes":{"results":[` + row("a") + `],"pagination":{"totalRecords":2}}}`,
		`{"entityAttributes":{"results":[` + row("b") + `],"pagination":{"totalRecords":2}}}`,
	}}
	got, err := Get(context.Background(), f, GetOptions{Target: target(), Key: "a", Scope: "shared"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || len(f.calls) != 2 {
		t.Fatalf("got %d rows in %d calls, want 2 in 2", len(got), len(f.calls))
	}
	c := f.calls[0].vars["criteria"].(map[string]any)
	if c["scope"] != "SHARED" || c["entityType"] != "device" || c["entity"] != "d1" {
		t.Errorf("criteria %v", c)
	}
	if keys, _ := c["attrKeys"].([]string); len(keys) != 1 || keys[0] != "a" {
		t.Errorf("attrKeys %v", c["attrKeys"])
	}

	// With no key and no scope, neither narrowing is sent: omitted means everything.
	f = &fakeClient{responses: []string{`{"entityAttributes":{"results":[],"pagination":{"totalRecords":0}}}`}}
	if _, err := Get(context.Background(), f, GetOptions{Target: target()}); err != nil {
		t.Fatal(err)
	}
	c = f.calls[0].vars["criteria"].(map[string]any)
	if _, ok := c["scope"]; ok {
		t.Error("scope sent on an unnarrowed read")
	}
	if _, ok := c["attrKeys"]; ok {
		t.Error("attrKeys sent on an unnarrowed read")
	}
}

func TestUnset(t *testing.T) {
	for _, deleted := range []bool{true, false} {
		f := &fakeClient{responses: []string{fmt.Sprintf(`{"deleteEntityAttribute":%t}`, deleted)}}
		got, err := Unset(context.Background(), f, UnsetOptions{Target: target(), Key: "floor"})
		if err != nil || got != deleted {
			t.Fatalf("deleted=%v: got %v, %v", deleted, got, err)
		}
		v := f.calls[0].vars
		if v["scope"] != "SHARED" || v["attrKey"] != "floor" || v["entityType"] != "device" || v["entity"] != "d1" {
			t.Errorf("vars %v", v)
		}
	}
	if _, err := Unset(context.Background(), &fakeClient{}, UnsetOptions{Target: target()}); err == nil {
		t.Error("an empty key must be refused")
	}
}

func TestPrint(t *testing.T) {
	v := "arid"
	attrs := []Attribute{{Scope: "SHARED", Key: "climate", ValueType: "STRING", Value: &v}}
	var table bytes.Buffer
	if err := Print(&table, attrs, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(table.String(), "SCOPE") || !strings.Contains(table.String(), "climate") || !strings.Contains(table.String(), "arid") {
		t.Errorf("table: %q", table.String())
	}
	var js bytes.Buffer
	if err := Print(&js, attrs, true); err != nil {
		t.Fatal(err)
	}
	var back []Attribute
	if err := json.Unmarshal(js.Bytes(), &back); err != nil || len(back) != 1 || *back[0].Value != "arid" {
		t.Errorf("json: %v %q", err, js.String())
	}
	js.Reset()
	_ = Print(&js, nil, true)
	if strings.TrimSpace(js.String()) != "[]" {
		t.Errorf("empty json = %q, want []", js.String())
	}
}
