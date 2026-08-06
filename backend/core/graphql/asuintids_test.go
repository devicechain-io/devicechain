// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"math"
	"strconv"
	"testing"
)

// AsUintIds turns client-supplied GraphQL IDs into primary keys, so every case
// here is a "wrong row, no error" bug if it regresses. The zero-padded and
// alternate-base cases are the ones the previous per-service copies got wrong.
func TestAsUintIds(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []uint
		err  bool
	}{
		{"empty input", []string{}, []uint{}, false},
		{"plain decimal", []string{"12", "3400"}, []uint{12, 3400}, false},
		{"zero", []string{"0"}, []uint{0}, false},
		// The live defect: base 0 read this as octal and returned 15.
		{"zero padded is decimal, not octal", []string{"017"}, []uint{17}, false},
		{"leading zeros preserved value", []string{"0009"}, []uint{9}, false},
		{"hex rejected", []string{"0x2"}, nil, true},
		{"binary rejected", []string{"0b101"}, nil, true},
		{"underscore separator rejected", []string{"1_0"}, nil, true},
		{"negative rejected", []string{"-1"}, nil, true},
		{"empty string rejected", []string{""}, nil, true},
		{"whitespace rejected", []string{" 12"}, nil, true},
		{"non-numeric rejected", []string{"abc"}, nil, true},
		{"one bad id fails the whole batch", []string{"1", "nope", "3"}, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := AsUintIds(tc.in)
			if tc.err {
				if err == nil {
					t.Fatalf("AsUintIds(%q) = %v, want an error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("AsUintIds(%q) returned %v", tc.in, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("AsUintIds(%q) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("AsUintIds(%q) = %v, want %v", tc.in, got, tc.want)
				}
			}
		})
	}
}

// The truncation CodeQL flagged: a value that does not fit in uint must be an
// error, never a silently narrowed different id. Parsing at strconv.IntSize means
// this holds on a 32-bit build too, where the boundary sits at MaxUint32.
func TestAsUintIdsRejectsValuesWiderThanUint(t *testing.T) {
	tooBig := "1" + strconv.FormatUint(math.MaxUint, 10) // unrepresentable on any build
	if got, err := AsUintIds([]string{tooBig}); err == nil {
		t.Fatalf("AsUintIds(%q) = %v, want an overflow error", tooBig, got)
	}
	// The largest representable id must still be accepted — the counterweight, so
	// "rejects overflow" cannot be satisfied by rejecting everything large.
	max := strconv.FormatUint(math.MaxUint, 10)
	got, err := AsUintIds([]string{max})
	if err != nil {
		t.Fatalf("AsUintIds(%q) returned %v, want it accepted", max, err)
	}
	if len(got) != 1 || got[0] != math.MaxUint {
		t.Fatalf("AsUintIds(%q) = %v, want [%d]", max, got, uint(math.MaxUint))
	}
}
