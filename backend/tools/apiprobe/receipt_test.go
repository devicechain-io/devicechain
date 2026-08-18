// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"path/filepath"
	"testing"
)

func canonicalOf(t *testing.T, s string) string {
	t.Helper()
	got, err := canonical(raw(s))
	if err != nil {
		t.Fatalf("canonical(%s): %v", s, err)
	}
	return got
}

// GraphQL makes no promise about key order, so two identical responses can
// arrive with their fields in different orders.
func TestKeyOrderDoesNotCount(t *testing.T) {
	a := canonicalOf(t, `{"token":"t","name":"n"}`)
	b := canonicalOf(t, `{"name":"n","token":"t"}`)
	if a != b {
		t.Errorf("key order changed the comparison:\n  %s\n  %s", a, b)
	}
}

// 🔑 THE jsonb ROUND TRIP. The platform's opaque documents are `String` in the
// schema and `jsonb` in the column, and jsonb stores a parsed value rather than
// bytes: it prints back with its own spacing and key order. A create response
// echoes the caller's exact bytes; the read-back a version later comes from the
// column. Without normalizing inside the string, that difference alone would be
// reported as a field that CHANGED across the upgrade.
func TestAnEmbeddedDocumentIsComparedAsAValueNotAsBytes(t *testing.T) {
	written := canonicalOf(t, `{"metadata":"{\"probe\":\"device\",\"n\":1}"}`)
	// What Postgres hands back: a space after each colon, keys reordered.
	readBack := canonicalOf(t, `{"metadata":"{\"n\": 1, \"probe\": \"device\"}"}`)
	if written != readBack {
		t.Errorf("jsonb's own rendering was reported as a change:\n  written: %s\n  read:    %s", written, readBack)
	}
}

func TestAnEmbeddedArrayIsNormalizedToo(t *testing.T) {
	written := canonicalOf(t, `{"recipients":"[\"a@x.invalid\",\"b@x.invalid\"]"}`)
	readBack := canonicalOf(t, `{"recipients":"[ \"a@x.invalid\", \"b@x.invalid\" ]"}`)
	if written != readBack {
		t.Errorf("an embedded array's spacing was reported as a change:\n  %s\n  %s", written, readBack)
	}
}

// 🔴 THE COUNTERWEIGHT, and the one that matters most: normalizing must not make
// DIFFERENT documents equal. A tool that reports every upgrade as clean is worth
// less than no tool, and this is the exact mechanism that could produce one.
func TestNormalizingStillSeesARealChange(t *testing.T) {
	cases := []struct{ name, written, readBack string }{
		{"a value changed", `{"m":"{\"probe\":\"device\"}"}`, `{"m":"{\"probe\":\"other\"}"}`},
		{"a key was renamed", `{"m":"{\"probe\":\"device\"}"}`, `{"m":"{\"prob\":\"device\"}"}`},
		{"an entry was dropped", `{"m":"{\"a\":1,\"b\":2}"}`, `{"m":"{\"a\":1}"}`},
		{"an entry was added", `{"m":"{\"a\":1}"}`, `{"m":"{\"a\":1,\"b\":2}"}`},
		{"array order changed", `{"m":"[1,2]"}`, `{"m":"[2,1]"}`},
		{"a number became a string", `{"m":"{\"a\":1}"}`, `{"m":"{\"a\":\"1\"}"}`},
		{"an outer field changed", `{"name":"a"}`, `{"name":"b"}`},
		{"a nested token changed", `{"deviceType":{"token":"a"}}`, `{"deviceType":{"token":"b"}}`},
		{"a field became null", `{"name":"a"}`, `{"name":null}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if canonicalOf(t, c.written) == canonicalOf(t, c.readBack) {
				t.Errorf("%s was normalized away", c.name)
			}
		})
	}
}

// A response can carry a genuine JSON ARRAY — a policy's rule set, a bulk
// create's rows — whose elements hold documents of their own. Those need
// normalizing exactly as much as the ones directly under an object key, and it
// is a separate branch of the walk, so a test that only nests inside an object
// leaves half of it unexercised.
func TestAnEmbeddedDocumentInsideAListIsNormalized(t *testing.T) {
	written := canonicalOf(t, `{"rules":[{"m":"{\"a\":1,\"b\":2}"}]}`)
	readBack := canonicalOf(t, `{"rules":[{"m":"{\"b\": 2, \"a\": 1}"}]}`)
	if written != readBack {
		t.Errorf("a document inside a list was compared as bytes:\n  %s\n  %s", written, readBack)
	}
}

// Only objects and arrays are documents. A plain field that happens to hold
// "42" is text, and rewriting it would compare it as a number instead.
//
// 🔑 "1.50" and "1e3" are the load-bearing cases. The obvious ones — "42",
// "true" — survive a broken guard unchanged, because re-encoding them gives
// back the same text; these two do not, and they are what tells a widened guard
// apart from a correct one. A unit, a descriptor or an external id could hold
// either, and neither should be quietly re-rendered.
func TestAScalarStringIsLeftAlone(t *testing.T) {
	for _, v := range []string{`"42"`, `"1.50"`, `"1e3"`, `"true"`, `"null"`, `""`, `"  "`, `"not json"`} {
		written := canonicalOf(t, `{"f":`+v+`}`)
		if want := `{"f":` + v + `}`; written != want {
			t.Errorf("canonical rewrote the scalar %s to %s", want, written)
		}
	}
}

// A string that opens like a document and is not one is stored happily by the
// platform, so it must not become a probe failure.
func TestAStringThatOnlyLooksLikeADocumentSurvives(t *testing.T) {
	got := canonicalOf(t, `{"f":"{not really json"}`)
	if want := `{"f":"{not really json"}`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// A receipt recording nothing would make verify pass instantly, having checked
// nothing — the vacuous green this whole rig exists to refuse.
func TestAnEmptyReceiptIsABrokenInstrument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipt.json")
	if err := writeReceipt(path, Receipt{Instance: "i", Tenant: "apiprobe"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := readReceipt(path)
	assertCode(t, err, exitSetup)
}

func TestAReceiptRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipt.json")
	out := Receipt{
		Instance: "i", Tenant: "apiprobe",
		Identity: Credential{Email: "e@apiprobe.invalid", Password: "p"},
		Entities: []Recorded{{Name: "device", Area: "device-management", Token: "t", Object: raw(`{"token":"t"}`)}},
	}
	if err := writeReceipt(path, out); err != nil {
		t.Fatalf("write: %v", err)
	}
	back, err := readReceipt(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(back.Entities) != 1 || back.Entities[0].Token != "t" || back.Identity.Password != "p" {
		t.Fatalf("the receipt did not survive the round trip: %+v", back)
	}
}
