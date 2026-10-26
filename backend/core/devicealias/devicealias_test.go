// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package devicealias

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeIsTheOneCanonicalSpelling(t *testing.T) {
	cases := []struct{ scheme, in, want string }{
		{SchemeDevEUI, "0123456789abcdef", "0123456789ABCDEF"},
		{SchemeDevEUI, "0123456789ABCDEF", "0123456789ABCDEF"},
		{SchemeDevEUI, " 70b3d57ed0000001 ", "70B3D57ED0000001"},
		{SchemeTTSDeviceID, "Sensor-01", "sensor-01"},
		{"custom.acme", " Serial-ABC ", "Serial-ABC"},
	}
	for _, c := range cases {
		got, err := Normalize(c.scheme, c.in)
		if err != nil || got != c.want {
			t.Errorf("Normalize(%q, %q) = %q, %v; want %q", c.scheme, c.in, got, err, c.want)
		}
	}
}

func TestNormalizeRefusesWhatIsNotTheScheme(t *testing.T) {
	bad := []struct{ scheme, in string }{
		{SchemeDevEUI, ""},
		{SchemeDevEUI, "0123456789abcde"},   // 15 digits
		{SchemeDevEUI, "0123456789abcdef0"}, // 17 digits
		{SchemeDevEUI, "01:23:45:67:89:ab:cd:ef"},
		{SchemeDevEUI, "0123456789abcdeg"},
		{SchemeTTSDeviceID, "-lead"},
		{SchemeTTSDeviceID, "trail-"},
		{SchemeTTSDeviceID, "has space"},
		{SchemeTTSDeviceID, strings.Repeat("a", 37)},
		{"custom.acme", ""},
		{"custom.acme", "tab\there"},
		{"custom.acme", strings.Repeat("x", MaxValueLen+1)},
		{"custom.Acme", "x"}, // upper case in the scheme name
		{"custom.", "x"},
		{"lorawan.other", "x"},
		{"", "x"},
	}
	for _, c := range bad {
		if got, err := Normalize(c.scheme, c.in); err == nil || !errors.Is(err, ErrInvalid) {
			t.Errorf("Normalize(%q, %q) = %q, %v; want an ErrInvalid refusal", c.scheme, clip(c.in), got, err)
		}
	}
}

func TestACustomValueKeepsItsCase(t *testing.T) {
	a, _ := Normalize("custom.x", "AbC")
	b, _ := Normalize("custom.x", "abc")
	if a == b {
		t.Fatalf("a custom value was case folded: %q == %q", a, b)
	}
}
