// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import "testing"

// changedSet builds a Changed-style predicate from the flags the user set explicitly.
func changedSet(names ...string) func(string) bool {
	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}
	return func(n string) bool { return set[n] }
}

func TestResolveDevMode(t *testing.T) {
	t.Run("bare --dev applies the full preset", func(t *testing.T) {
		res, err := resolveDevMode(changedSet(), "", false, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !res.Build || res.Host != "localhost" || !res.NoTLS || !res.Yes {
			t.Fatalf("preset not fully applied: %+v", res)
		}
	})

	t.Run("redundant but consistent explicit flags are accepted", func(t *testing.T) {
		// --dev --host localhost --no-tls --build: all agree with the preset.
		res, err := resolveDevMode(changedSet("host", "no-tls", "build"), "localhost", true, true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Host != "localhost" {
			t.Fatalf("host = %q, want localhost", res.Host)
		}
	})

	t.Run("a conflicting --host is rejected, not silently overridden", func(t *testing.T) {
		_, err := resolveDevMode(changedSet("host"), "prod.example.com", false, false)
		if err == nil {
			t.Fatal("expected an error for --dev --host prod.example.com")
		}
	})

	t.Run("--no-tls=false conflicts with the http preset", func(t *testing.T) {
		if _, err := resolveDevMode(changedSet("no-tls"), "", false, false); err == nil {
			t.Fatal("expected an error for --dev --no-tls=false")
		}
	})

	t.Run("--build=false conflicts with build-from-source", func(t *testing.T) {
		if _, err := resolveDevMode(changedSet("build"), "", false, false); err == nil {
			t.Fatal("expected an error for --dev --build=false")
		}
	})

	t.Run("an unset conflicting-looking value is fine (only explicit flags conflict)", func(t *testing.T) {
		// host defaults to "" (not localhost) but was NOT set by the user, so no conflict.
		if _, err := resolveDevMode(changedSet(), "", false, false); err != nil {
			t.Fatalf("unset host must not conflict: %v", err)
		}
	})
}
