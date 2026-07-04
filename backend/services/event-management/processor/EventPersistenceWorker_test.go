// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"errors"
	"testing"
)

// A non-numeric measurement/location value is a deterministic persistence failure
// (it can never be stored in the numeric column), so parseNullableFloat64 wraps it
// as ErrDeterministic — the signal the worker uses to dead-letter on the first
// failure rather than NAK-retry to the delivery cap (ADR-024). A nil or numeric
// value is not an error.
func TestParseNullableFloat64Deterministic(t *testing.T) {
	bad := "not-a-number"
	if _, err := parseNullableFloat64(&bad); err == nil || !errors.Is(err, ErrDeterministic) {
		t.Fatalf("expected a non-numeric value to be a deterministic error, got: %v", err)
	}

	good := "42.5"
	if v, err := parseNullableFloat64(&good); err != nil || v == nil || *v != 42.5 {
		t.Fatalf("expected a numeric value to parse, got v=%v err=%v", v, err)
	}

	if v, err := parseNullableFloat64(nil); err != nil || v != nil {
		t.Fatalf("expected nil for a nil value, got v=%v err=%v", v, err)
	}
}
