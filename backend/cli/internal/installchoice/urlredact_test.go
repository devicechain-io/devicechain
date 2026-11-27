// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installchoice

import (
	"strings"
	"testing"
)

// The reproduced command is a print site too. url.Parse misreads a registry written
// without a scheme ("user:pass@registry/org" is an opaque URL with the scheme "user"
// and no userinfo) and a restore source with a fragment and no host, and both used to
// be reproduced whole. Each credential carries the marker "ZX".
func TestCommandTemplatesAURLValuedFlagParseMisreads(t *testing.T) {
	for _, tc := range []struct{ flag, value string }{
		{"registry", "user:dcPwZX@registry.example/org"},
		{"registry", "dcTokZX@registry.example/org"},
		{"registry", "registry.example/org#dcFragZX"},
		{"registry", "registry.example/org%3Ftoken%3DdcTokZX"},
		{"restore-rdb-from", "dc-rdb#dcCanaryZX"},
		{"restore-rdb-from", "dc-rdb?sig=dcSigZX"},
		{"restore-rdb-from", "u:dcPwZX@bucket/archive"},
		{"restore-rdb-from", "s3://u:dcPwZX@bucket/archive"},
	} {
		c := Choices{Given: Presence{tc.flag: true}}
		switch tc.flag {
		case "registry":
			c.Registry = tc.value
		case "restore-rdb-from":
			c.RestoreRdbFrom = tc.value
		}
		for _, shell := range []Shell{POSIX, PowerShell} {
			got, err := (Plan{choices: c}).Command(shell, false)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Template || strings.Contains(strings.ToLower(got.Line+strings.Join(got.Argv, " ")), "zx") {
				t.Errorf("--%s=%s reproduced with its credential: %q", tc.flag, tc.value, got.Line)
			}
		}
	}
}

// The display rule is for URL-valued flags only: an email address has an '@' and is
// not a credential, and a registry or an archive with none is reproduced as given.
func TestCommandReproducesValuesWithoutACredential(t *testing.T) {
	for _, tc := range []struct{ flag, value string }{
		{"registry", "ghcr.io/devicechain-io"},
		{"registry", "localhost:5001"},
		{"restore-rdb-from", "dc-rdb"},
		{"restore-rdb-from", "https://host/a@b/archive"},
		{"restore-rdb-from", "s3://bucket/a%2540b"},
		{"registry", "localhost:5001/org@x"},
		{"alert-email", "ops@example.test"},
	} {
		c := Choices{Given: Presence{tc.flag: true}}
		switch tc.flag {
		case "registry":
			c.Registry = tc.value
		case "restore-rdb-from":
			c.RestoreRdbFrom = tc.value
		case "alert-email":
			c.Alerts.Email = []string{tc.value}
		}
		got, err := (Plan{choices: c}).Command(POSIX, false)
		if err != nil {
			t.Fatal(err)
		}
		if got.Template || !strings.Contains(got.Line, tc.value) {
			t.Errorf("--%s=%s was not reproduced as given: %q (template %v)", tc.flag, tc.value, got.Line, got.Template)
		}
	}
}

// The review screen's rows (DisplayValue) use the same display rule: the forms
// url.Parse misreads were shown whole, credential and all.
func TestDisplayValueRedactsWhatParseMisreads(t *testing.T) {
	for _, tc := range []struct{ flag, value, want string }{
		{"registry", "user:dcPwZX@registry.example/org", "<redacted>@registry.example/org"},
		{"registry", "dcTokZX@registry.example/org", "<redacted>@registry.example/org"},
		{"restore-rdb-from", "dc-rdb#dcTokZX", "dc-rdb#<redacted>"},
		{"restore-rdb-from", "dc-rdb?sig=dcSigZX", "dc-rdb?<redacted>"},
		{"restore-rdb-from", "user:dcPwZX://x@host/archive", "<redacted>@host/archive"},
	} {
		got, ok := DisplayValue(tc.flag, tc.value)
		if !ok || got != tc.want || strings.Contains(strings.ToLower(got), "zx") {
			t.Errorf("DisplayValue(%s, %q) = %q %v, want %q", tc.flag, tc.value, got, ok, tc.want)
		}
	}
	for _, v := range []string{"ghcr.io/devicechain-io", "https://host/a@b/archive", "dc-rdb"} {
		if got, ok := DisplayValue("registry", v); !ok || got != v {
			t.Errorf("DisplayValue(registry, %q) = %q %v, want it as given", v, got, ok)
		}
	}
}
