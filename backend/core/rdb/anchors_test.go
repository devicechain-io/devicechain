// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"reflect"
	"testing"
)

func TestAnchorsRoundTripThroughTheArrayLiteral(t *testing.T) {
	for _, in := range []Anchors{
		{},
		{"customer:acme-corp"},
		{"customer:acme-corp", "group:north"},
		{`odd "quoted"`, `back\slash`, "with,comma", "with space", "NULL"},
	} {
		v, err := in.Value()
		if err != nil {
			t.Fatal(err)
		}
		var out Anchors
		if err := out.Scan(v); err != nil {
			t.Fatalf("Scan(%q): %v", v, err)
		}
		if !reflect.DeepEqual(out, in) {
			t.Fatalf("round trip of %q: got %q via %q", in, out, v)
		}
	}
	// nil is NULL, and NULL scans back as nil.
	if v, _ := Anchors(nil).Value(); v != nil {
		t.Fatalf("nil anchors render as %v, want NULL", v)
	}
	var out Anchors = Anchors{"x"}
	if err := out.Scan(nil); err != nil || out != nil {
		t.Fatalf("NULL scans as %v, %v", out, err)
	}
}

// The server's own text form: unquoted where it can be, quoted where it must.
func TestAnchorsScanTheServersTextForm(t *testing.T) {
	var a Anchors
	if err := a.Scan([]byte(`{customer:acme-corp,"group:north side"}`)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, Anchors{"customer:acme-corp", "group:north side"}) {
		t.Fatalf("got %q", a)
	}
	for _, bad := range []string{"", "customer:acme", "{NULL}", "{a,NULL}", `{"open}`, "{{a}}", "{a}x"} {
		if err := a.Scan(bad); err == nil {
			t.Errorf("Scan(%q) was accepted as %q", bad, a)
		}
	}
}
