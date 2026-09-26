// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"database/sql"
	"errors"
	"math"
	"testing"
)

func i32(v int32) *int32 { return &v }

// The column folds over all three states, asserted by VALUE. The absent rows are the ones
// that matter: the spelling these replace — NullStrOf(o.ApplyTo(NullStr(current))) and its
// int32 twin — sent the stored value back through a normalizer on the absent branch, and
// these rows are what that spelling fails.
func TestApplyToNullString(t *testing.T) {
	padded := sql.NullString{String: " padded ", Valid: true}
	emptyStored := sql.NullString{String: "", Valid: true}
	for _, tc := range []struct {
		name    string
		o       OptionalString
		current sql.NullString
		want    sql.NullString
	}{
		{"absent keeps an untrimmed value byte for byte", OptionalString{}, padded, padded},
		{"absent keeps a stored empty string (not NULL)", OptionalString{}, emptyStored, emptyStored},
		{"absent keeps NULL", OptionalString{}, sql.NullString{}, sql.NullString{}},
		{"value is trimmed", OptionalStringOf("  Renamed "), padded, sql.NullString{String: "Renamed", Valid: true}},
		{"whitespace clears", OptionalStringOf("   "), padded, sql.NullString{}},
		{"empty clears", OptionalStringOf(""), padded, sql.NullString{}},
		{"null clears", ClearedString(), padded, sql.NullString{}},
	} {
		if got := tc.o.ApplyToNullString(tc.current); got != tc.want {
			t.Errorf("%s: got %#v, want %#v", tc.name, got, tc.want)
		}
	}
}

func TestApplyToNullSecret(t *testing.T) {
	stored := sql.NullString{String: " old ", Valid: true}
	for _, tc := range []struct {
		name string
		o    OptionalString
		want sql.NullString
	}{
		{"absent keeps", OptionalString{}, stored},
		{"a value is stored exactly as sent", OptionalStringOf(" s3cret\t"), sql.NullString{String: " s3cret\t", Valid: true}},
		{"a whitespace-only value is a value", OptionalStringOf("   "), sql.NullString{String: "   ", Valid: true}},
		{"empty clears", OptionalStringOf(""), sql.NullString{}},
		{"null clears", ClearedString(), sql.NullString{}},
	} {
		if got := tc.o.ApplyToNullSecret(stored); got != tc.want {
			t.Errorf("%s: got %#v, want %#v", tc.name, got, tc.want)
		}
	}
}

func TestApplyToNullInt64(t *testing.T) {
	wide := sql.NullInt64{Int64: math.MaxInt32 + 1, Valid: true}
	for _, tc := range []struct {
		name string
		o    OptionalInt32
		want sql.NullInt64
	}{
		{"absent keeps a value wider than 32 bits", OptionalInt32{}, wide},
		{"value sets", OptionalInt32Of(7), sql.NullInt64{Int64: 7, Valid: true}},
		{"null clears", ClearedInt32(), sql.NullInt64{}},
	} {
		if got := tc.o.ApplyToNullInt64(wide); got != tc.want {
			t.Errorf("%s: got %#v, want %#v", tc.name, got, tc.want)
		}
	}
}

func TestApplyToIntPtr(t *testing.T) {
	v := 1 << 40
	current := &v
	if got := (OptionalInt32{}).ApplyToIntPtr(current); got != current || *got != 1<<40 {
		t.Errorf("absent: got %v (%d), want the SAME pointer holding %d", got, *got, 1<<40)
	}
	if got := ClearedInt32().ApplyToIntPtr(current); got != nil {
		t.Errorf("null: got %d, want nil (inherit), never zero", *got)
	}
	got := OptionalInt32Of(9).ApplyToIntPtr(current)
	if got == nil || *got != 9 || got == current {
		t.Errorf("value: got %v, want a fresh pointer to 9", got)
	}
	if *current != 1<<40 {
		t.Errorf("setting a value wrote through the stored pointer: it now holds %d", *current)
	}
}

// The read half REFUSES a value it cannot represent; a wrapped number is a plausible wrong
// answer.
func TestNullInt32RefusesOutOfRange(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      sql.NullInt64
		want    *int32
		refused bool
	}{
		{"NULL", sql.NullInt64{}, nil, false},
		{"MaxInt32", sql.NullInt64{Int64: math.MaxInt32, Valid: true}, i32(math.MaxInt32), false},
		{"MinInt32", sql.NullInt64{Int64: math.MinInt32, Valid: true}, i32(math.MinInt32), false},
		{"MaxInt32+1", sql.NullInt64{Int64: math.MaxInt32 + 1, Valid: true}, nil, true},
		{"MinInt32-1", sql.NullInt64{Int64: math.MinInt32 - 1, Valid: true}, nil, true},
	} {
		got, err := NullInt32("throttleSeconds", tc.in)
		if tc.refused != errors.Is(err, ErrStoredIntOutOfRange) {
			t.Errorf("%s: err = %v, refused want %v", tc.name, err, tc.refused)
		}
		if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestIntPtrInt32RefusesOutOfRange(t *testing.T) {
	if got, err := IntPtrInt32("ingestBurst", nil); got != nil || err != nil {
		t.Errorf("nil: got (%v, %v), want (nil, nil) — inherit, never zero", got, err)
	}
	max := math.MaxInt32
	if got, err := IntPtrInt32("ingestBurst", &max); err != nil || got == nil || *got != math.MaxInt32 {
		t.Errorf("MaxInt32: got (%v, %v)", got, err)
	}
	wide := math.MaxInt32 + 1
	if got, err := IntPtrInt32("ingestBurst", &wide); got != nil || !errors.Is(err, ErrStoredIntOutOfRange) {
		t.Errorf("MaxInt32+1: got (%v, %v), want a refusal", got, err)
	}
}

func TestNullStrNonEmpty(t *testing.T) {
	if got := NullStrNonEmpty(sql.NullString{}); got != nil {
		t.Errorf("NULL: got %q, want nil", *got)
	}
	if got := NullStrNonEmpty(sql.NullString{String: "", Valid: true}); got != nil {
		t.Errorf("stored empty string: got %q, want nil", *got)
	}
	if got := NullStrNonEmpty(sql.NullString{String: " Ada", Valid: true}); got == nil || *got != " Ada" {
		t.Errorf("a value: got %v, want \" Ada\" exactly", got)
	}
}
