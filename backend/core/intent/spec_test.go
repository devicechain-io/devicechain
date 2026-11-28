// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package intent

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
)

// validDoc is a valid spec document with every field present; tests derive invalid ones from it
// by one textual replacement, so each case changes exactly one thing.
const validDoc = `{"target":{"groupToken":"quay-lamps","groupVersion":3},"point":{"configKey":"dim"},` +
	`"value":{"valueType":"LONG","value":"40"},"class":"SCHEDULE","ceiling":{"maxDevices":2000},` +
	`"reconcile":{"minIntervalSeconds":60,"maxLinesPerReconcile":500,"maxActuationsPerDevicePerDay":4,"manualDwellSeconds":3600}}`

func mustParse(t testing.TB, doc string) Spec {
	t.Helper()
	s, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse(%s): %v", doc, err)
	}
	return s
}

func with(old, new string) string {
	if strings.Count(validDoc, old) != 1 {
		panic("with: " + old + " must occur exactly once in validDoc")
	}
	return strings.Replace(validDoc, old, new, 1)
}

func TestParseAcceptsTheBoundaries(t *testing.T) {
	for name, doc := range map[string]string{
		"valid":                         validDoc,
		"interval at the floor":         validDoc,
		"interval at the ceiling":       with(`"minIntervalSeconds":60`, `"minIntervalSeconds":86400`),
		"one line":                      with(`"maxLinesPerReconcile":500`, `"maxLinesPerReconcile":1`),
		"max lines":                     with(`"maxLinesPerReconcile":500`, `"maxLinesPerReconcile":10000`),
		"one device":                    with(`"maxDevices":2000`, `"maxDevices":1`),
		"max devices":                   with(`"maxDevices":2000`, `"maxDevices":100000`),
		"max actuations":                with(`"maxActuationsPerDevicePerDay":4`, `"maxActuationsPerDevicePerDay":1440`),
		"dwell zero":                    with(`"manualDwellSeconds":3600`, `"manualDwellSeconds":0`),
		"dwell max":                     with(`"manualDwellSeconds":3600`, `"manualDwellSeconds":86400`),
		"dwell omitted":                 with(`,"manualDwellSeconds":3600`, ``),
		"optimization":                  with(`"SCHEDULE"`, `"OPTIMIZATION"`),
		"group version max int32":       with(`"groupVersion":3`, `"groupVersion":2147483647`),
		"unset":                         with(`{"valueType":"LONG","value":"40"}`, `{"unset":true}`),
		"unset false with a value":      with(`"value":"40"}`, `"value":"40","unset":false}`),
		"negative long":                 with(`"value":"40"`, `"value":"-9223372036854775808"`),
		"double":                        with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"DOUBLE","value":"0.001"}`),
		"double integral":               with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"DOUBLE","value":"-3"}`),
		"double negative zero":          with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"DOUBLE","value":"-0"}`),
		"long zero":                     with(`"value":"40"`, `"value":"0"`),
		"boolean false":                 with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"BOOLEAN","value":"false"}`),
		"json":                          with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"JSON","value":" [1, {\"a\":null}] "}`),
		"string empty":                  with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"STRING","value":""}`),
		"paired surrogate escape":       with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"STRING","value":"😀"}`),
		"escaped backslash before a u":  with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"STRING","value":"\\udc00"}`),
		"token of 128 bytes":            with(`"configKey":"dim"`, `"configKey":"`+strings.Repeat("k", 128)+`"`),
		"token with hyphen, underscore": with(`"configKey":"dim"`, `"configKey":"A9_b-c"`),
	} {
		if _, err := Parse([]byte(doc)); err != nil {
			t.Errorf("%s: Parse refused a valid spec: %v", name, err)
		}
	}
}

func TestParseRefusesWhatItCannotTakeLiterally(t *testing.T) {
	for name, doc := range map[string]string{
		"unknown top-level field":       with(`"class":"SCHEDULE"`, `"class":"SCHEDULE","owner":"x"`),
		"unknown nested field":          with(`"configKey":"dim"`, `"configKey":"dim","scope":"SHARED"`),
		"case-variant field":            with(`"class":"SCHEDULE"`, `"Class":"SCHEDULE"`),
		"duplicate field":               with(`"class":"SCHEDULE"`, `"class":"OPTIMIZATION","class":"SCHEDULE"`),
		"duplicate nested field":        with(`"groupVersion":3`, `"groupVersion":3,"groupVersion":4`),
		"null field":                    with(`"manualDwellSeconds":3600`, `"manualDwellSeconds":null`),
		"trailing data":                 validDoc + `{}`,
		"top-level array":               `[` + validDoc + `]`,
		"not json":                      `{"target":`,
		"invalid utf-8":                 with(`"configKey":"dim"`, "\"configKey\":\"d\xffm\""),
		"invalid utf-8 in a string":     with(`{"valueType":"LONG","value":"40"}`, "{\"valueType\":\"STRING\",\"value\":\"d\xffm\"}"),
		"unpaired high surrogate":       with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"STRING","value":"\ud83dx"}`),
		"unpaired low surrogate":        with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"STRING","value":"\ude00"}`),
		"high then non-low":             with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"STRING","value":"\ud83dA"}`),
		"high then escaped non-low":     with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"STRING","value":"\ud83dA"}`),
		"high then escaped high":        with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"STRING","value":"\ud83d\ud83d"}`),
		"fractional integer":            with(`"groupVersion":3`, `"groupVersion":3.0`),
		"exponent integer":              with(`"groupVersion":3`, `"groupVersion":3e0`),
		"string integer":                with(`"groupVersion":3`, `"groupVersion":"3"`),
		"oversized integer":             with(`"maxDevices":2000`, `"maxDevices":99999999999999999999`),
		"missing target":                with(`"target":{"groupToken":"quay-lamps","groupVersion":3},`, ``),
		"missing group version":         with(`,"groupVersion":3`, ``),
		"missing class":                 with(`"class":"SCHEDULE",`, ``),
		"missing interval":              with(`"minIntervalSeconds":60,`, ``),
		"missing max lines":             with(`"maxLinesPerReconcile":500,`, ``),
		"missing max actuations":        with(`"maxActuationsPerDevicePerDay":4,`, ``),
		"missing ceiling":               with(`"ceiling":{"maxDevices":2000},`, ``),
		"forbidden class":               with(`"SCHEDULE"`, `"PROTECTION"`),
		"lower-case class":              with(`"SCHEDULE"`, `"schedule"`),
		"interval below the floor":      with(`"minIntervalSeconds":60`, `"minIntervalSeconds":59`),
		"interval above the ceiling":    with(`"minIntervalSeconds":60`, `"minIntervalSeconds":86401`),
		"zero lines":                    with(`"maxLinesPerReconcile":500`, `"maxLinesPerReconcile":0`),
		"too many lines":                with(`"maxLinesPerReconcile":500`, `"maxLinesPerReconcile":10001`),
		"zero devices":                  with(`"maxDevices":2000`, `"maxDevices":0`),
		"too many devices":              with(`"maxDevices":2000`, `"maxDevices":100001`),
		"zero actuations":               with(`"maxActuationsPerDevicePerDay":4`, `"maxActuationsPerDevicePerDay":0`),
		"too many actuations":           with(`"maxActuationsPerDevicePerDay":4`, `"maxActuationsPerDevicePerDay":1441`),
		"negative dwell":                with(`"manualDwellSeconds":3600`, `"manualDwellSeconds":-1`),
		"dwell too long":                with(`"manualDwellSeconds":3600`, `"manualDwellSeconds":86401`),
		"group version zero":            with(`"groupVersion":3`, `"groupVersion":0`),
		"group version over int32":      with(`"groupVersion":3`, `"groupVersion":2147483648`),
		"empty group token":             with(`"groupToken":"quay-lamps"`, `"groupToken":""`),
		"group token with a dot":        with(`"groupToken":"quay-lamps"`, `"groupToken":"quay.lamps"`),
		"config key leading hyphen":     with(`"configKey":"dim"`, `"configKey":"-dim"`),
		"config key too long":           with(`"configKey":"dim"`, `"configKey":"`+strings.Repeat("k", 129)+`"`),
		"config key non-ASCII":          with(`"configKey":"dim"`, `"configKey":"dïm"`),
		"unknown value type":            with(`"valueType":"LONG"`, `"valueType":"INTEGER"`),
		"lower-case value type":         with(`"valueType":"LONG"`, `"valueType":"long"`),
		"value missing":                 with(`,"value":"40"`, ``),
		"value type missing":            with(`"valueType":"LONG",`, ``),
		"value not a string":            with(`"value":"40"`, `"value":40`),
		"unset with a value":            with(`"value":"40"}`, `"value":"40","unset":true}`),
		"unset with only a type":        with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"LONG","unset":true}`),
		"unset with an empty type":      with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"","unset":true}`),
		"unset with an empty value":     with(`{"valueType":"LONG","value":"40"}`, `{"value":"","unset":true}`),
		"unset with both empty":         with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"","value":"","unset":true}`),
		"long with a leading zero":      with(`"value":"40"`, `"value":"040"`),
		"long with a plus sign":         with(`"value":"40"`, `"value":"+40"`),
		"long negative zero":            with(`"value":"40"`, `"value":"-0"`),
		"double with a trailing zero":   with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"DOUBLE","value":"21.50"}`),
		"double integral with a point":  with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"DOUBLE","value":"1.0"}`),
		"double with an exponent":       with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"DOUBLE","value":"1e3"}`),
		"double hex":                    with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"DOUBLE","value":"0x1p2"}`),
		"double with a plus sign":       with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"DOUBLE","value":"+1.5"}`),
		"double with a leading point":   with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"DOUBLE","value":".5"}`),
		"double with underscores":       with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"DOUBLE","value":"1_000"}`),
		"boolean as a digit":            with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"BOOLEAN","value":"1"}`),
		"boolean upper case":            with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"BOOLEAN","value":"TRUE"}`),
		"boolean title case":            with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"BOOLEAN","value":"False"}`),
		"boolean as a letter":           with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"BOOLEAN","value":"F"}`),
		"unset not a boolean":           with(`{"valueType":"LONG","value":"40"}`, `{"unset":"true"}`),
		"empty value object":            with(`{"valueType":"LONG","value":"40"}`, `{}`),
		"long that is not an integer":   with(`"value":"40"`, `"value":"40.0"`),
		"long that overflows":           with(`"value":"40"`, `"value":"9223372036854775808"`),
		"long with a space":             with(`"value":"40"`, `"value":" 40"`),
		"double not finite":             with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"DOUBLE","value":"NaN"}`),
		"double infinite":               with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"DOUBLE","value":"1e400"}`),
		"boolean misspelled":            with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"BOOLEAN","value":"yes"}`),
		"string holding a NUL":          with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"STRING","value":"a\u0000b"}`),
		"json invalid":                  with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"JSON","value":"{"}`),
		"json two values":               with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"JSON","value":"1 2"}`),
		"target not an object":          with(`"target":{"groupToken":"quay-lamps","groupVersion":3}`, `"target":"quay-lamps@3"`),
		"too deep":                      with(`"class":"SCHEDULE"`, `"class":"SCHEDULE","x":[[[[[[[[[[1]]]]]]]]]]`),
		"canonical over the size limit": with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"STRING","value":"`+strings.Repeat(`\n`, 3000)+`"}`),
		"over the size limit":           with(`"value":"40"`, `"value":"`+strings.Repeat("a", MaxSpecBytes)+`"`),
	} {
		_, err := Parse([]byte(doc))
		if err == nil {
			t.Errorf("%s: Parse accepted %s", name, doc[:min(len(doc), 200)])
			continue
		}
		if !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("%s: refusal %v does not wrap ErrInvalidSpec", name, err)
		}
	}
}

// Canonical, SpecDigest, IntendedValue, InstantBucket, SameSubject and Diff each validate, so a
// spec built in code without Parse cannot reach a digest unchecked.
// A null is refused by name, not merely because it is not the type a field expects, so the
// author is told to omit the field rather than that it has the wrong type.
func TestParseNamesANull(t *testing.T) {
	for _, doc := range []string{
		with(`"manualDwellSeconds":3600`, `"manualDwellSeconds":null`),
		with(`"value":"40"}`, `"value":"40","unset":null}`),
		with(`"class":"SCHEDULE"`, `"class":null`),
	} {
		_, err := Parse([]byte(doc))
		if !errors.Is(err, ErrInvalidSpec) || !strings.Contains(err.Error(), "null is not a value") {
			t.Errorf("Parse(%s) = %v; want the null refused by name", doc, err)
		}
	}
}

func TestEveryEntryPointValidates(t *testing.T) {
	bad := mustParse(t, validDoc)
	bad.Reconcile.MinIntervalSeconds = 59
	good := mustParse(t, validDoc)
	if _, err := Canonical(bad); err == nil {
		t.Error("Canonical accepted an invalid spec")
	}
	if _, err := SpecDigest(bad); err == nil {
		t.Error("SpecDigest accepted an invalid spec")
	}
	if _, err := IntendedValue(bad, 1, epoch); err == nil {
		t.Error("IntendedValue accepted an invalid spec")
	}
	if _, err := InstantBucket(bad, epoch); err == nil {
		t.Error("InstantBucket accepted an invalid spec")
	}
	if _, err := SameSubject(bad, good); err == nil {
		t.Error("SameSubject accepted an invalid first spec")
	}
	if _, err := SameSubject(good, bad); err == nil {
		t.Error("SameSubject accepted an invalid second spec")
	}
	if _, err := Diff(bad, nil, epoch); err == nil {
		t.Error("Diff accepted an invalid spec")
	}
	forbidden := good
	forbidden.Class = "PROTECTION"
	if err := forbidden.Validate(); err == nil {
		t.Error("Validate accepted a forbidden class")
	}
	halfUnset := good
	halfUnset.Value.Unset = true
	if err := halfUnset.Validate(); err == nil {
		t.Error("Validate accepted unset together with a value")
	}
}

func TestDefaultsAndExactness(t *testing.T) {
	s := mustParse(t, with(`,"manualDwellSeconds":3600`, ``))
	if s.Reconcile.ManualDwellSeconds != DefaultManualDwellSeconds {
		t.Errorf("manualDwellSeconds defaulted to %d, want %d", s.Reconcile.ManualDwellSeconds, DefaultManualDwellSeconds)
	}
	explicitZero := mustParse(t, with(`"manualDwellSeconds":3600`, `"manualDwellSeconds":0`))
	if explicitZero.Reconcile.ManualDwellSeconds != 0 {
		t.Error("an explicit manualDwellSeconds of 0 was replaced by the default")
	}
	a := mustParse(t, with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"STRING","value":"40"}`))
	b := mustParse(t, with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"STRING","value":"040"}`))
	da, _ := SpecDigest(a)
	db, _ := SpecDigest(b)
	if da == db {
		t.Error(`STRING "40" and "040" must be different specs: the value text is exact`)
	}
}

// storedSpelling repeats device-management's normalizeAttributeValue (model/api_attributes.go),
// which every configuration write passes through: the text a device's stored row holds for a
// written value, or ok=false where device-management refuses the write. Package core cannot
// import a service, so the rule is restated here and the cases below pin the two together.
func storedSpelling(valueType, text string) (string, bool) {
	switch valueType {
	case ValueTypeLong, ValueTypeDouble:
		f, err := strconv.ParseFloat(text, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return "", false
		}
		if valueType == ValueTypeDouble {
			return strconv.FormatFloat(f, 'f', -1, 64), true
		}
		if i, err := strconv.ParseInt(text, 10, 64); err == nil {
			return strconv.FormatInt(i, 10), true
		}
		if f != math.Trunc(f) || f < math.MinInt64 || f >= math.MaxInt64 {
			return "", false
		}
		return strconv.FormatInt(int64(f), 10), true
	case ValueTypeBoolean:
		b, err := strconv.ParseBool(text)
		if err != nil {
			return "", false
		}
		return strconv.FormatBool(b), true
	}
	return text, true
}

// valueSpellings are typed value texts in the spellings device-management accepts and rewrites,
// and in the ones it stores unchanged.
var valueSpellings = []struct{ valueType, text string }{
	{"BOOLEAN", "true"}, {"BOOLEAN", "false"}, {"BOOLEAN", "TRUE"}, {"BOOLEAN", "True"}, {"BOOLEAN", "1"},
	{"BOOLEAN", "0"}, {"BOOLEAN", "t"}, {"BOOLEAN", "F"}, {"BOOLEAN", "FALSE"}, {"BOOLEAN", "yes"},
	{"LONG", "40"}, {"LONG", "040"}, {"LONG", "+40"}, {"LONG", "-0"}, {"LONG", "0"}, {"LONG", "-40"},
	{"LONG", "40.0"}, {"LONG", "4e1"}, {"LONG", "9223372036854775807"}, {"LONG", "-9223372036854775808"},
	{"LONG", "0x28"}, {"LONG", "4_0"},
	{"DOUBLE", "21.5"}, {"DOUBLE", "21.50"}, {"DOUBLE", "1.0"}, {"DOUBLE", "1"}, {"DOUBLE", "1e3"},
	{"DOUBLE", "1E3"}, {"DOUBLE", "1000"}, {"DOUBLE", "0x1p2"}, {"DOUBLE", "4"}, {"DOUBLE", "+1.5"},
	{"DOUBLE", ".5"}, {"DOUBLE", "0.5"}, {"DOUBLE", "-0"}, {"DOUBLE", "-0.0"}, {"DOUBLE", "1e-7"},
	{"DOUBLE", "0.0000001"}, {"DOUBLE", "1_000"}, {"DOUBLE", "Inf"}, {"DOUBLE", "1e400"},
	{"DOUBLE", "0.1"}, {"DOUBLE", "0.30000000000000004"}, {"DOUBLE", "123456789012345678901234567890"},
	{"STRING", " 40 "}, {"JSON", " [1] "},
}

// An intended value is digested as written and compared with the digest of what a device's row
// holds, so a spelling device-management would rewrite on the way in would read as DRIFTED
// forever. Validate accepts a value text only when it is exactly what device-management stores.
func TestValueTextIsTheSpellingDeviceManagementStores(t *testing.T) {
	accepted := 0
	for _, tc := range valueSpellings {
		s := mustParse(t, validDoc)
		text := tc.text
		s.Value = Value{ValueType: tc.valueType, Value: &text}
		err := s.Validate()
		stored, storable := storedSpelling(tc.valueType, tc.text)
		switch {
		case err == nil && !storable:
			t.Errorf("%s %q accepted; device-management refuses it", tc.valueType, tc.text)
		case err == nil && stored != tc.text:
			t.Errorf("%s %q accepted; device-management stores it as %q, so it would never be IN_SYNC",
				tc.valueType, tc.text, stored)
		case err != nil && storable && stored == tc.text:
			t.Errorf("%s %q refused (%v); device-management stores exactly that text", tc.valueType, tc.text, err)
		case err != nil && !errors.Is(err, ErrInvalidSpec):
			t.Errorf("%s %q refused without ErrInvalidSpec: %v", tc.valueType, tc.text, err)
		}
		if err == nil {
			accepted++
		}
	}
	if accepted < 10 {
		t.Errorf("only %d spellings accepted; the table no longer exercises acceptance", accepted)
	}
}

// Two spellings device-management stores as one value are one value: SameSubject must not see a
// competing intent where there is none. With only stored spellings admitted, the case cannot be
// written, and the refusal says which spelling to use.
func TestNonCanonicalSpellingNamesTheStoredOne(t *testing.T) {
	for _, tc := range []struct{ valueType, text, want string }{
		{"BOOLEAN", "TRUE", `"true"`}, {"LONG", "040", `"40"`}, {"DOUBLE", "1e3", `"1000"`}, {"DOUBLE", "0x1p2", `"4"`},
	} {
		s := mustParse(t, validDoc)
		text := tc.text
		s.Value = Value{ValueType: tc.valueType, Value: &text}
		err := s.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s %q: Validate = %v; want a refusal naming %s", tc.valueType, tc.text, err, tc.want)
		}
	}
}

// The token grammar is repeated here because package core is not pure. It must give core's
// answer for every input core's own tests use, and for every single byte.
func TestTokenGrammarAgreesWithCore(t *testing.T) {
	if MaxTokenLen != core.MaxTokenLen {
		t.Fatalf("MaxTokenLen = %d, core.MaxTokenLen = %d", MaxTokenLen, core.MaxTokenLen)
	}
	cases := []string{"", "a", "Z", "0", "9", "_", "-", "a_", "a-", "_a", "-a", "a-b_c", "ABC-def_123",
		"dev.1", "dev*", "dev>", "a b", " a", "a ", "a\n", "a\x00", "a/b", "a+b", "a#b", "café", "é", "a\xff", "\xff", "aİ",
		strings.Repeat("a", 128), strings.Repeat("a", 129)}
	for b := 0; b < 256; b++ {
		cases = append(cases, string([]byte{byte(b)}), "a"+string([]byte{byte(b)}))
	}
	for _, tok := range cases {
		if got, want := validTokenGrammar(tok), core.ValidateToken(tok) == nil; got != want {
			t.Errorf("validTokenGrammar(%q) = %v, core.ValidateToken accepts: %v", tok, got, want)
		}
	}
}

// hex4 reads exactly four hex digits, so whatever follows them, including digits that would
// spell MaxInt32, MaxInt32+1 or MaxUint32 as one number, it never yields more than 0xFFFF and
// never wraps to a negative rune.
func TestHex4ReadsExactlyFourDigits(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want rune
	}{
		{"0000", 0}, {"0041", 'A'}, {"d83d", 0xD83D}, {"DC00", 0xDC00}, {"fFfF", 0xFFFF},
		{"7fffffff", 0x7FFF}, // MaxInt32
		{"80000000", 0x8000}, // MaxInt32 + 1
		{"ffffffff", 0xFFFF}, // MaxUint32
		{"0010ffff", 0x0010},
	} {
		got, ok := hex4([]byte(tc.in), 0)
		if !ok || got != tc.want || got < 0 || got > 0xFFFF {
			t.Errorf("hex4(%q) = %#x, %v; want %#x", tc.in, got, ok, tc.want)
		}
	}
	for _, in := range []string{"", "fff", "+fff", "-001", " fff", "0x12", "fffg", "ff\"f", "\u00e9ff"} {
		if got, ok := hex4([]byte(in), 0); ok {
			t.Errorf("hex4(%q) = %#x; want refused", in, got)
		}
	}
	if _, ok := hex4([]byte("ffff"), -1); ok {
		t.Error("hex4 accepted a negative offset")
	}
	if _, ok := hex4([]byte("ffff"), 1); ok {
		t.Error("hex4 read past the end of the document")
	}
	// Eight escaped hex digits spelling MaxUint32 are a \uFFFF escape followed by text, so the
	// document parses, and the value holds U+FFFF then "ffff": nothing wrapped.
	s, err := Parse([]byte(with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"STRING","value":"\uffffffff"}`)))
	if err != nil || *s.Value.Value != "\uffff"+"ffff" {
		t.Errorf("Parse of \uffffffff = %+v, %v", s.Value, err)
	}
}
