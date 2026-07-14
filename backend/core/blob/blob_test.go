// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package blob

import "testing"

func TestBuildKeyHappyPath(t *testing.T) {
	got, err := buildKey("inst1", Key{Tenant: "acme", Purpose: "branding-logo", ID: "abc123.png"})
	if err != nil {
		t.Fatalf("buildKey: %v", err)
	}
	want := "inst1/acme/branding-logo/abc123.png"
	if got != want {
		t.Fatalf("buildKey = %q, want %q", got, want)
	}
}

func TestBuildKeyInstanceScope(t *testing.T) {
	// An empty tenant maps to the reserved instance slot, never a blank segment.
	got, err := buildKey("inst1", Key{Purpose: "export", ID: "id1"})
	if err != nil {
		t.Fatalf("buildKey: %v", err)
	}
	want := "inst1/" + instanceScopeSegment + "/export/id1"
	if got != want {
		t.Fatalf("buildKey = %q, want %q", got, want)
	}
}

func TestBuildKeyRejectsTraversalAndBadSegments(t *testing.T) {
	cases := map[string]Key{
		"dotdot tenant":     {Tenant: "..", Purpose: "p", ID: "i"},
		"dot id":            {Tenant: "t", Purpose: "p", ID: "."},
		"slash in purpose":  {Tenant: "t", Purpose: "a/b", ID: "i"},
		"backslash in id":   {Tenant: "t", Purpose: "p", ID: "a\\b"},
		"empty purpose":     {Tenant: "t", Purpose: "", ID: "i"},
		"empty id":          {Tenant: "t", Purpose: "p", ID: ""},
		"space in tenant":   {Tenant: "a b", Purpose: "p", ID: "i"},
		"null byte":         {Tenant: "t", Purpose: "p", ID: "a\x00b"},
		"traversal segment": {Tenant: "t", Purpose: "..", ID: "i"},
	}
	for name, k := range cases {
		if _, err := buildKey("inst1", k); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func TestBuildKeyRejectsReservedAndLeadingDotID(t *testing.T) {
	cases := map[string]Key{
		"reserved dcmeta suffix": {Tenant: "t", Purpose: "p", ID: "obj.dcmeta"},
		"leading dot id":         {Tenant: "t", Purpose: "p", ID: ".hidden"},
		"temp-prefix id":         {Tenant: "t", Purpose: "p", ID: ".put-x"},
		"leading dot tenant":     {Tenant: ".acme", Purpose: "p", ID: "i"},
	}
	for name, k := range cases {
		if _, err := buildKey("inst1", k); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
	// A normal dotted id (uuid.png) is still fine.
	if _, err := buildKey("inst1", Key{Tenant: "t", Purpose: "p", ID: "abc.png"}); err != nil {
		t.Fatalf("dotted id must pass: %v", err)
	}
}

func TestBuildKeyRejectsBadInstanceID(t *testing.T) {
	for _, id := range []string{"", "..", "a/b", "in stance"} {
		if _, err := buildKey(id, Key{Tenant: "t", Purpose: "p", ID: "i"}); err == nil {
			t.Errorf("instanceID %q: expected error, got nil", id)
		}
	}
}

func TestValidateSegmentLength(t *testing.T) {
	long := make([]byte, maxSegmentLen+1)
	for i := range long {
		long[i] = 'a'
	}
	if err := validateSegment("id", string(long)); err == nil {
		t.Fatal("over-length segment must be rejected")
	}
	ok := long[:maxSegmentLen]
	if err := validateSegment("id", string(ok)); err != nil {
		t.Fatalf("max-length segment must pass: %v", err)
	}
}

func TestRefRoundTrip(t *testing.T) {
	r := Ref{Backend: "filesystem", Key: "inst1/acme/branding-logo/abc.png"}
	got, err := ParseRef(r.String())
	if err != nil {
		t.Fatalf("ParseRef: %v", err)
	}
	if got != r {
		t.Fatalf("round trip = %+v, want %+v", got, r)
	}
}

func TestParseRefRejectsMalformed(t *testing.T) {
	for _, s := range []string{
		"",
		"acme/key",              // no scheme
		"blob://",               // no backend/key
		"blob://filesystem",     // no key
		"blob:///key",           // empty backend
		"blob://filesystem/",    // empty key
		"http://filesystem/key", // wrong scheme
	} {
		if _, err := ParseRef(s); err == nil {
			t.Errorf("ParseRef(%q): expected error, got nil", s)
		}
	}
}
