// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package calcfield

import (
	"math"
	"testing"
)

func TestFormatStored(t *testing.T) {
	cases := []struct {
		t    Type
		v    float64
		want string
	}{
		{Double, 21.5, "21.5"},
		{Double, 230.12345679, "230.12345679"},
		{Double, -0.0, "0"},
		{Double, 1e11, "1e+11"},
		{Integer, 5, "5"},
		{Integer, -12, "-12"},
		{Integer, 999999999999, "999999999999"},
		{Integer, 1e11, "100000000000"},
		{Integer, 2.5, "2.5"},
		{Integer, math.Ldexp(1, 60), "1.152921504606847e+18"},
		{Boolean, 1, "1"},
		{Boolean, 0, "0"},
		{Boolean, 0.5, "0.5"},
		{Boolean, 2, "2"},
	}
	for _, c := range cases {
		if got := FormatStored(c.t, c.v); got != c.want {
			t.Errorf("FormatStored(%s, %v) = %q, want %q", c.t, c.v, got, c.want)
		}
	}
}

// What FormatStored renders parses back, as its type, to the stored value — or refuses to,
// for a value that is not one of its type, so a formula reading it skips as invalid_input.
func TestFormatStoredParsesAsTheStoredValue(t *testing.T) {
	env, err := NewEnv([]Input{{Key: "d", Type: Double}, {Key: "i", Type: Integer}, {Key: "b", Type: Boolean}})
	if err != nil {
		t.Fatal(err)
	}
	f, err := env.Compile(Definition{Key: "out", Expression: "d + double(i) + (b ? 1.0 : 0.0)", Type: Double})
	if err != nil {
		t.Fatal(err)
	}
	s := env.Sample()
	defer s.Release()
	for _, c := range []struct {
		d, i, b float64
		want    string
		skip    SkipReason
	}{
		{1.25, 3, 1, "5.25", SkipNone},
		{1e11, 1e11, 0, "2e+11", SkipNone},
		{0.1, 2.5, 1, "", SkipInvalidInput},
		{0.1, 2, 0.5, "", SkipInvalidInput},
	} {
		s.Reset()
		_, _ = s.Set("d", FormatStored(Double, c.d))
		_, _ = s.Set("i", FormatStored(Integer, c.i))
		_, _ = s.Set("b", FormatStored(Boolean, c.b))
		got := f.Eval(s)
		if got.Value != c.want || got.Skip != c.skip {
			t.Errorf("%+v: got %+v", c, got)
		}
	}
}
