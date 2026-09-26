// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sqlnull

import (
	"database/sql"
	"math"
	"testing"
)

func ptr[T any](v T) *T { return &v }

func TestTextTrimsAndClearsBlank(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   *string
		want sql.NullString
	}{
		{"nil", nil, sql.NullString{}},
		{"empty", ptr(""), sql.NullString{}},
		{"whitespace", ptr(" \t\n"), sql.NullString{}},
		{"padded", ptr("  Renamed "), sql.NullString{String: "Renamed", Valid: true}},
		{"plain", ptr("x"), sql.NullString{String: "x", Valid: true}},
	} {
		if got := Text(tc.in); got != tc.want {
			t.Errorf("%s: Text = %#v, want %#v", tc.name, got, tc.want)
		}
	}
}

// Secret is stored VERBATIM: surrounding whitespace survives, and a whitespace-only value
// is a value. Only nil and "" mean "no secret".
func TestSecretStoresVerbatim(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   *string
		want sql.NullString
	}{
		{"nil", nil, sql.NullString{}},
		{"empty", ptr(""), sql.NullString{}},
		{"padded", ptr(" a "), sql.NullString{String: " a ", Valid: true}},
		{"tab", ptr("\t"), sql.NullString{String: "\t", Valid: true}},
		{"trailing newline", ptr("pw\n"), sql.NullString{String: "pw\n", Valid: true}},
	} {
		if got := Secret(tc.in); got != tc.want {
			t.Errorf("%s: Secret = %#v, want %#v", tc.name, got, tc.want)
		}
	}
}

func TestInt64FromInt32Widens(t *testing.T) {
	if got, want := Int64FromInt32(ptr(int32(math.MaxInt32))), (sql.NullInt64{Int64: 2147483647, Valid: true}); got != want {
		t.Errorf("MaxInt32: got %#v, want %#v", got, want)
	}
	if got, want := Int64FromInt32(ptr(int32(math.MinInt32))), (sql.NullInt64{Int64: -2147483648, Valid: true}); got != want {
		t.Errorf("MinInt32: got %#v, want %#v", got, want)
	}
	if got := Int64FromInt32(nil); got != (sql.NullInt64{}) {
		t.Errorf("nil: got %#v, want NULL", got)
	}
}
