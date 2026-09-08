// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"database/sql"
	"strings"
	"testing"
	"time"
)

// The required fold has one job and one anti-job: keep an ABSENT field's stored
// value, and refuse a CLEAR that the column cannot hold. Both directions are
// asserted for every scalar, because the fold is written per type and a copy that
// forgot the !Set branch would silently zero the column on every update that did not
// name the field — which is the full-replace defect this whole arc removes, arriving
// through the helper built to prevent it.

func TestApplyToRequired_AbsentKeepsTheStoredValue(t *testing.T) {
	if got, err := (OptionalString{}).ApplyToRequired("dataType", "DOUBLE"); err != nil || got != "DOUBLE" {
		t.Errorf("string: got %q, %v; want DOUBLE, nil", got, err)
	}
	if got, err := (OptionalBool{}).ApplyToRequired("enabled", true); err != nil || !got {
		t.Errorf("bool: got %v, %v; want true, nil", got, err)
	}
	if got, err := (OptionalInt32{}).ApplyToRequired("shedPriority", 7); err != nil || got != 7 {
		t.Errorf("int32: got %v, %v; want 7, nil", got, err)
	}
	if got, err := (OptionalFloat64{}).ApplyToRequired("rate", 2.5); err != nil || got != 2.5 {
		t.Errorf("float64: got %v, %v; want 2.5, nil", got, err)
	}
}

func TestApplyToRequired_AValueSetsIt(t *testing.T) {
	if got, err := OptionalStringOf("INT64").ApplyToRequired("dataType", "DOUBLE"); err != nil || got != "INT64" {
		t.Errorf("string: got %q, %v", got, err)
	}
	if got, err := OptionalBoolOf(false).ApplyToRequired("enabled", true); err != nil || got {
		t.Errorf("bool: got %v, %v; want false, nil", got, err)
	}
	if got, err := OptionalInt32Of(0).ApplyToRequired("shedPriority", 7); err != nil || got != 0 {
		t.Errorf("int32: got %v, %v; want 0, nil", got, err)
	}
	if got, err := OptionalFloat64Of(0).ApplyToRequired("rate", 2.5); err != nil || got != 0 {
		t.Errorf("float64: got %v, %v; want 0, nil", got, err)
	}
}

// 🔴 THE POINT OF THE WHOLE FILE. ApplyToValue writes false / "" / 0 here and returns
// success; every one of those is a legal value the create path would also have
// accepted, so nothing downstream can tell the caller they cleared a required field.
func TestApplyToRequired_AnExplicitNullIsRefused(t *testing.T) {
	if _, err := ClearedString().ApplyToRequired("dataType", "DOUBLE"); err == nil {
		t.Error("string: a null was accepted")
	}
	if _, err := ClearedBool().ApplyToRequired("enabled", true); err == nil {
		t.Error("bool: a null was accepted, which silently disables the entity")
	}
	if _, err := ClearedInt32().ApplyToRequired("shedPriority", 7); err == nil {
		t.Error("int32: a null was accepted")
	}
	if _, err := ClearedFloat64().ApplyToRequired("rate", 2.5); err == nil {
		t.Error("float64: a null was accepted")
	}
}

// A whitespace-only string is a null spelled differently. Accepting it would store a
// value that matches nothing and that no reader can distinguish from a real one.
func TestApplyToRequired_ABlankStringIsRefused(t *testing.T) {
	for _, blank := range []string{"", " ", "\t", "\n  "} {
		if _, err := OptionalStringOf(blank).ApplyToRequired("dataType", "DOUBLE"); err == nil {
			t.Errorf("the blank value %q was accepted for a required field", blank)
		}
	}
}

// 🔴 WHAT IS ACCEPTED IS STORED VERBATIM, AND THIS IS THE TEST THAT SAYS SO.
//
// An earlier version trimmed. No create path on this platform trims, so an update that
// did made RESTATING A FIELD change it: a provisioning profile created with the legal
// secret " s3cret " and then updated by a client re-sending what it read back was left
// holding "s3cret", the whole fleet stopped authenticating, and the edit returned 200.
// Clients that restate are not hypothetical — the simulator's convergence paths send a
// full restatement on every pass.
//
// The property is stated as a ROUND TRIP rather than as "does not call TrimSpace",
// because the round trip is what callers depend on and it stays true however the fold
// is rewritten.
func TestApplyToRequired_RestatingAStoredValueIsANoOp(t *testing.T) {
	// Values a create path accepts today and stores exactly. The padded ones are the
	// point; the bare one is the control that keeps this from passing vacuously.
	for _, stored := range []string{" s3cret ", "s3cret", "key with spaces", "\ttabbed\t", "a b"} {
		t.Run(stored, func(t *testing.T) {
			got, err := OptionalStringOf(stored).ApplyToRequired("provisionSecret", stored)
			if err != nil {
				t.Fatalf("restating the stored value was refused: %v", err)
			}
			if got != stored {
				t.Fatalf("restating %q stored %q — an update rewrote a value the caller did "+
					"not mean to change, which for a credential means the fleet presenting the "+
					"created one stops authenticating", stored, got)
			}
		})
	}
}

// The counterweight to the round trip: a DIFFERENT value must still take, or the fold
// above would satisfy its test by ignoring the request entirely.
func TestApplyToRequired_ADifferentValueStillReplaces(t *testing.T) {
	got, err := OptionalStringOf(" rotated ").ApplyToRequired("provisionSecret", " s3cret ")
	if err != nil {
		t.Fatalf("a replacement was refused: %v", err)
	}
	if got != " rotated " {
		t.Fatalf("got %q, want the replacement stored verbatim", got)
	}
}

// The refusals must name the SCHEMA field, or a caller sending five fields is told
// only that one of them was wrong.
func TestApplyToRequired_TheRefusalNamesTheField(t *testing.T) {
	_, err := ClearedString().ApplyToRequired("credentialType", "ACCESS_TOKEN")
	if err == nil || !strings.Contains(err.Error(), "credentialType") {
		t.Fatalf("error %v does not name the field", err)
	}
	_, err = OptionalStringOf(" ").ApplyToRequired("credentialType", "ACCESS_TOKEN")
	if err == nil || !strings.Contains(err.Error(), "credentialType") {
		t.Fatalf("blank error %v does not name the field", err)
	}
}

// ─── the nullable-timestamp fold ───────────────────────────────────────────

func TestApplyToNullTime(t *testing.T) {
	seeded := sql.NullTime{Time: time.Date(2031, 3, 4, 5, 6, 7, 0, time.UTC), Valid: true}

	// ABSENT keeps. This is the state the pre-conversion shape could not express: it
	// read `if request.ExpiresAt != nil`, so a caller who said nothing cleared it.
	got, err := (OptionalString{}).ApplyToNullTime("expiresAt", seeded)
	if err != nil || !got.Valid || !got.Time.Equal(seeded.Time) {
		t.Fatalf("absent: got %+v, %v", got, err)
	}

	// NULL clears.
	got, err = ClearedString().ApplyToNullTime("expiresAt", seeded)
	if err != nil || got.Valid {
		t.Fatalf("null: got %+v, %v; want an invalid NullTime", got, err)
	}

	// An empty string clears too — what a form sends for "no expiry".
	got, err = OptionalStringOf("").ApplyToNullTime("expiresAt", seeded)
	if err != nil || got.Valid {
		t.Fatalf("empty: got %+v, %v; want an invalid NullTime", got, err)
	}

	// A value sets.
	got, err = OptionalStringOf("2032-01-02T03:04:05Z").ApplyToNullTime("expiresAt", seeded)
	if err != nil || !got.Valid || !got.Time.Equal(time.Date(2032, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("value: got %+v, %v", got, err)
	}
}

// A malformed timestamp is an ERROR, not a clear. The distinction matters because the
// clear is silent and successful: a caller who mistyped a date would otherwise be told
// their credential now never expires.
func TestApplyToNullTime_AMalformedValueIsRefused(t *testing.T) {
	seeded := sql.NullTime{Time: time.Now(), Valid: true}
	for _, bad := range []string{"tomorrow", "2032-01-02", "2032-01-02 03:04:05"} {
		got, err := OptionalStringOf(bad).ApplyToNullTime("expiresAt", seeded)
		if err == nil {
			t.Errorf("%q was accepted as a timestamp (folded to %+v)", bad, got)
			continue
		}
		if !strings.Contains(err.Error(), "expiresAt") {
			t.Errorf("the error for %q does not name the field: %v", bad, err)
		}
	}
}

// ─── the read half ─────────────────────────────────────────────────────────

// NullFloat64 must report a cleared column as ABSENT rather than as 0, or a partial
// update that leaves the field alone folds the cleared column back in as a real zero.
func TestNullFloat64(t *testing.T) {
	if got := NullFloat64(sql.NullFloat64{}); got != nil {
		t.Errorf("an invalid NullFloat64 read back as %v, not nil", *got)
	}
	if got := NullFloat64(sql.NullFloat64{Float64: 0, Valid: true}); got == nil || *got != 0 {
		t.Errorf("a valid zero read back as %v, not a pointer to 0", got)
	}
	if got := NullFloat64(sql.NullFloat64{Float64: 4.5, Valid: true}); got == nil || *got != 4.5 {
		t.Errorf("a valid 4.5 read back as %v", got)
	}
}
