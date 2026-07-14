// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"
)

// normalizeAttributeValue enforces the facet-selector storage-form invariant (ADR-061 G3
// §3.4) by COERCION (the write contract does not reject): non-numeric text under a numeric
// type is coerced to unset (nil) so the ea.value::numeric cast is always safe, a boolean is
// canonicalized to 'true'/'false' (an unparseable one → nil), and STRING/JSON stay verbatim.
func TestNormalizeAttributeValue(t *testing.T) {
	strp := func(s string) *string { return &s }

	// Non-numeric text under a numeric type, and a bad boolean, coerce to unset (nil).
	for _, bad := range []struct {
		vt, v string
	}{
		{string(AttributeValueLong), "abc"},
		{string(AttributeValueDouble), "1.2.3"},
		{string(AttributeValueDouble), "NaN"},
		{string(AttributeValueBoolean), "maybe"},
	} {
		if got := normalizeAttributeValue(bad.vt, strp(bad.v)); got != nil {
			t.Errorf("normalizeAttributeValue(%s, %q) = %q, want nil (coerced to unset)", bad.vt, bad.v, *got)
		}
	}

	// A boolean is canonicalized to 'true'/'false' regardless of the accepted spelling.
	for _, c := range []struct{ in, want string }{
		{"true", "true"}, {"True", "true"}, {"1", "true"},
		{"false", "false"}, {"FALSE", "false"}, {"0", "false"},
	} {
		got := normalizeAttributeValue(string(AttributeValueBoolean), strp(c.in))
		if got == nil || *got != c.want {
			t.Errorf("normalizeAttributeValue(BOOLEAN, %q) = %v; want %q", c.in, got, c.want)
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
	} {
		if got := normalizeAttributeValue(c.vt, strp(c.in)); got == nil || *got != c.want {
			t.Errorf("normalizeAttributeValue(%s, %q) = %v; want %q", c.vt, c.in, got, c.want)
		}
	}
	if got := normalizeAttributeValue(string(AttributeValueDouble), nil); got != nil {
		t.Errorf("nil numeric value should stay nil: got %v", got)
	}
	if got := normalizeAttributeValue(string(AttributeValueString), strp("arid")); got == nil || *got != "arid" {
		t.Errorf("STRING verbatim: got %v", got)
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
