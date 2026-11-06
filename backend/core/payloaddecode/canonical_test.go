// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package payloaddecode

import (
	"fmt"
	"github.com/devicechain-io/dc-microservice/calcfield"
	"strings"
	"testing"
)

const ignoreSection = `"sections":[{"name":"s","ignore":true}]`

func docWithTable(table string) string {
	return `{"kind":"CEL","envVersion":1,"input":"FRAME",` + ignoreSection + `,"table":` + table + `}`
}

// The measure is the rendering Postgres jsonb gives: ", " and ": " separators and numbers written
// out in full.
func TestCanonicalSizeMatchesJsonbRendering(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int
	}{
		{`[0,0,0]`, len(`[0, 0, 0]`)},
		{`{"a":1,"b":[true,null,"x"]}`, len(`{"a": 1, "b": [true, null, "x"]}`)},
		{`{"table":[1e5]}`, len(`{"table": [100000]}`)}, // upper bound: 1e5 renders as 100000 (6), counted 1+5+2=8
	} {
		got, err := CanonicalSize([]byte(c.in))
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if got < c.want || got > c.want+3 {
			t.Errorf("%s: size %d, want about %d", c.in, got, c.want)
		}
	}
}

// 🔴 The cap is on the stored rendering. The document is 20 bytes; jsonb writes 1e100000 as a
// 100,001-digit literal, so every read of it would return about 100 KB per number.
func TestHugeExponentIsRefusedAtParse(t *testing.T) {
	for _, n := range []string{"1e100000", "1E+99999", "1e-100000", "0.5e50000", "1e99999999999"} {
		js := docWithTable(`{"x":[` + n + `]}`)
		if len(js) > MaxDocumentBytes {
			t.Fatal("test document should be small")
		}
		if _, err := ParseDocument([]byte(js)); err == nil {
			t.Errorf("%s: a document that renders over the cap parsed", n)
		}
	}
	// A small exponent is a small rendering and stays legal.
	if _, err := ParseDocument([]byte(docWithTable(`{"v":1,"x":[1e3,2.5e-2]}`))); err != nil {
		t.Errorf("small exponents refused: %v", err)
	}
}

// Many modest numbers add up: the sum is what is measured.
func TestManySmallExponentsAreMeasuredInTotal(t *testing.T) {
	js := docWithTable(`{"x":[` + strings.TrimSuffix(strings.Repeat("1e400,", 200), ",") + `]}`) // ~1.2 KB in, ~80 KB out
	if _, err := ParseDocument([]byte(js)); err == nil {
		t.Fatal("a document that renders to 80 KB parsed")
	}
}

// The separators jsonb adds count against the cap: a document that is under it as sent and over it
// as stored is refused, so it can never be saved and then fail to publish.
func TestCapIsMeasuredOnTheStoredForm(t *testing.T) {
	prefix := `{"kind":"CEL","envVersion":1,"input":"FRAME",` + ignoreSection + `,"table":{"v":1,"x":[`
	suffix := `]}}`
	// Elements "0" cost 1 byte each plus "," as sent (2 stored).
	n := (MaxDocumentBytes - len(prefix) - len(suffix)) // exactly fills the cap as sent
	body := strings.TrimSuffix(strings.Repeat("0,", n/2), ",")
	js := prefix + body + suffix
	if len(js) > MaxDocumentBytes {
		t.Fatalf("setup: %d bytes as sent", len(js))
	}
	if _, err := ParseDocument([]byte(js)); err == nil {
		t.Fatalf("a %d-byte document that renders over %d parsed", len(js), MaxDocumentBytes)
	}
	// The same document, trimmed until it fits when stored, is accepted: the cap is not tighter
	// than it has to be.
	for k := n / 2; k > 0; k -= 50 {
		js = prefix + strings.TrimSuffix(strings.Repeat("0,", k), ",") + suffix
		sz, err := CanonicalSize([]byte(js))
		if err != nil {
			t.Fatal(err)
		}
		if sz <= MaxDocumentBytes {
			if _, err := ParseDocument([]byte(js)); err != nil {
				t.Fatalf("document of stored size %d refused: %v", sz, err)
			}
			return
		}
	}
	t.Fatal("no fitting size found")
}

func TestTableMustBeAnObject(t *testing.T) {
	for _, tb := range []string{`[1,2]`, `"x"`, `7`, `null`, `true`} {
		if _, err := ParseDocument([]byte(docWithTable(tb))); err == nil {
			t.Errorf("table %s accepted", tb)
		}
	}
	if _, err := ParseDocument([]byte(docWithTable(`{"v":1,"sections":[]}`))); err != nil {
		t.Errorf("an object table refused: %v", err)
	}
}

// Nesting is not limited here: the size cap bounds it, and a decoder whose advisory table nests
// deeply was legal before the stored form was measured.
func TestDeepNestingIsMeasuredNotRefused(t *testing.T) {
	deep := strings.Repeat("[", 200) + strings.Repeat("]", 200)
	if _, err := ParseDocument([]byte(docWithTable(`{"x":` + deep + `}`))); err != nil {
		t.Fatalf("a deeply nested table was refused: %v", err)
	}
}

// CanonicalSize fails closed on anything that is not exactly one complete JSON value.
func TestCanonicalSizeRefusesIncompleteAndMultipleDocuments(t *testing.T) {
	for _, in := range []string{``, `[`, `{"x":1`, `{"x":`, `{} {}`, `1 2`} {
		if sz, err := CanonicalSize([]byte(in)); err == nil && sz <= MaxDocumentBytes {
			t.Errorf("%q measured as %d", in, sz)
		}
	}
	if _, err := CanonicalSize([]byte(`{"x":[1,2]}`)); err != nil {
		t.Errorf("a complete document refused: %v", err)
	}
}

// An explicit empty frameHex is an empty-payload uplink, distinct from a vector that names no
// frame at all (which is a missing input).
func TestEmptyFrameVectorIsAnEmptyFrameNotAMissingOne(t *testing.T) {
	doc := func(input string) string {
		return fmt.Sprintf(`{"kind":"CEL","envVersion":1,"input":"FRAME",
		 "sections":[{"name":"s","outputs":[{"key":"n","expr":"size(frame)"}]}],
		 "vectors":[{"name":"v","input":%s,"expect":{"n":0}}]}`, input)
	}
	m := tm("n", calcfield.Integer)
	for _, in := range []string{`{"frameHex":""}`, `{"frameBase64":""}`} {
		res := mustCompile(t, doc(in), m).RunVectors()
		if len(res) != 1 || !res[0].OK {
			t.Errorf("%s: %+v", in, res)
		}
	}
	// Negative control: no frame member at all is still a missing input.
	res := mustCompile(t, doc(`{"fPort":1}`), m).RunVectors()
	if len(res) != 1 || res[0].OK {
		t.Errorf("a vector with no frame passed: %+v", res)
	}
}
