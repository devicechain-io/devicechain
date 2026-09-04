// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"
)

// normalizeAttributeValue enforces the facet-selector storage-form invariant (ADR-061 G3
// §3.4) by REFUSING a present value its declared type cannot hold: the ea.value::numeric cast
// is always safe because a LONG/DOUBLE row can only exist if its text parsed, a boolean is
// canonicalized to 'true'/'false', and STRING/JSON stay verbatim.
//
// 🔴 IT USED TO COERCE THESE TO UNSET, which was a successful write that stored nothing —
// the caller was told "saved" and the facet it was authoring matched nothing. The two range
// cases below are the ones no client-side pattern can catch, because they are well-formed
// text that the PARSER rejects (or silently rounds), not malformed text.
func TestNormalizeAttributeValue(t *testing.T) {
	strp := func(s string) *string { return &s }

	// A present value its type cannot hold is refused, not stored as unset.
	for _, bad := range []struct {
		vt, v, why string
	}{
		{string(AttributeValueLong), "abc", "not a number at all"},
		{string(AttributeValueDouble), "1.2.3", "not a number at all"},
		{string(AttributeValueDouble), "NaN", "not finite"},
		{string(AttributeValueBoolean), "maybe", "not a boolean"},
		// 🔑 Both of these match any reasonable client-side numeric pattern.
		{string(AttributeValueDouble), "1e400", "overflows float64 to +Inf"},
		{string(AttributeValueLong), "12345678901234567890", "overflows int64; the float " +
			"path would have stored 12345678901234567168, a different number"},
		{string(AttributeValueLong), "3.5", "not a whole number"},
	} {
		got, err := normalizeAttributeValue(bad.vt, strp(bad.v))
		if err == nil {
			t.Errorf("normalizeAttributeValue(%s, %q) = %v with no error; want refused (%s)",
				bad.vt, bad.v, got, bad.why)
		}
		if got != nil {
			t.Errorf("normalizeAttributeValue(%s, %q) returned a value alongside its refusal", bad.vt, bad.v)
		}
	}

	// A boolean is canonicalized to 'true'/'false' regardless of the accepted spelling.
	for _, c := range []struct{ in, want string }{
		{"true", "true"}, {"True", "true"}, {"1", "true"},
		{"false", "false"}, {"FALSE", "false"}, {"0", "false"},
	} {
		got, err := normalizeAttributeValue(string(AttributeValueBoolean), strp(c.in))
		if err != nil || got == nil || *got != c.want {
			t.Errorf("normalizeAttributeValue(BOOLEAN, %q) = %v, %v; want %q", c.in, got, err, c.want)
		}
	}

	// Numeric text is canonicalized to plain decimal Postgres accepts: a hex float (which Go
	// parses but PG rejects) becomes decimal, an exponent is expanded, a LONG keeps precision.
	for _, c := range []struct{ vt, in, want string }{
		{string(AttributeValueDouble), "0x1p2", "4"},                               // hex float → decimal
		{string(AttributeValueDouble), "1e3", "1000"},                              // exponent expanded
		{string(AttributeValueDouble), "72.50", "72.5"},                            // trailing zero trimmed
		{string(AttributeValueLong), "3000", "3000"},                               // integer verbatim
		{string(AttributeValueLong), "9223372036854775807", "9223372036854775807"}, // int64 precision kept
		{string(AttributeValueLong), "3.0", "3"},                                   // integral spelling still accepted
	} {
		got, err := normalizeAttributeValue(c.vt, strp(c.in))
		if err != nil || got == nil || *got != c.want {
			t.Errorf("normalizeAttributeValue(%s, %q) = %v, %v; want %q", c.vt, c.in, got, err, c.want)
		}
	}
	// 🔴 A NIL VALUE STAYS LEGAL. Clearing a numeric attribute is a different act from
	// writing text that does not parse, and it is how the removal fact is still emitted.
	if got, err := normalizeAttributeValue(string(AttributeValueDouble), nil); got != nil || err != nil {
		t.Errorf("nil numeric value should stay nil and be accepted: got %v, %v", got, err)
	}
	if got, err := normalizeAttributeValue(string(AttributeValueString), strp("arid")); err != nil || got == nil || *got != "arid" {
		t.Errorf("STRING verbatim: got %v, %v", got, err)
	}
}

// AttributeScope.Valid accepts only the known scope vocabulary (ADR-012).
func TestAttributeScopeValid(t *testing.T) {
	valid := []AttributeScope{AttributeScopeClient, AttributeScopeServer, AttributeScopeShared}
	for _, s := range valid {
		if !s.Valid() {
			t.Errorf("expected scope %q to be valid", s)
		}
	}

	invalid := []AttributeScope{"", "client", "PUBLIC", "SHARED ", "OTHER"}
	for _, s := range invalid {
		if s.Valid() {
			t.Errorf("expected scope %q to be invalid", s)
		}
	}
}

// AttributeValueType.Valid accepts only the known value-type vocabulary.
func TestAttributeValueTypeValid(t *testing.T) {
	valid := []AttributeValueType{
		AttributeValueString, AttributeValueLong, AttributeValueDouble,
		AttributeValueBoolean, AttributeValueJson,
	}
	for _, vt := range valid {
		if !vt.Valid() {
			t.Errorf("expected value type %q to be valid", vt)
		}
	}

	invalid := []AttributeValueType{"", "string", "INT", "FLOAT", "OBJECT"}
	for _, vt := range invalid {
		if vt.Valid() {
			t.Errorf("expected value type %q to be invalid", vt)
		}
	}
}
