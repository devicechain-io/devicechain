// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"strings"
	"testing"
)

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

func TestResolveCompactMode(t *testing.T) {
	t.Run("bare --compact turns off TLS and monitoring", func(t *testing.T) {
		res, err := resolveCompactMode(changedSet(), "", false, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !res.NoTLS || !res.NoMonitoring {
			t.Fatalf("preset not fully applied: %+v", res)
		}
	})

	t.Run("--profile full is rejected rather than resized", func(t *testing.T) {
		// full adds the three areas that reach outside the instance, which the
		// compact storage budget was not sized against. There is no resolution that
		// honours both flags, so picking one silently is the wrong answer.
		_, err := resolveCompactMode(changedSet("profile"), "full", false, false)
		if err == nil {
			t.Fatal("--compact --profile full was accepted: the instance is sized for a " +
				"smaller set of areas than it deploys")
		}
		if !strings.Contains(err.Error(), "full") {
			t.Errorf("error %q does not name the offending profile", err)
		}
	})

	t.Run("an explicitly redundant --profile default is fine", func(t *testing.T) {
		if _, err := resolveCompactMode(changedSet("profile"), "default", false, false); err != nil {
			t.Fatalf("--compact --profile default was rejected: %v", err)
		}
	})

	t.Run("an explicit --no-tls=false is honoured, not rejected", func(t *testing.T) {
		// Keeping TLS is a DEPENDENCY, not a contradiction: cert-manager stays
		// installed to issue the certificate and every other compact lever still
		// applies. Erroring here would cost real functionality for no benefit.
		res, err := resolveCompactMode(changedSet("no-tls"), "", false, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.NoTLS {
			t.Error("--compact overrode an explicit --no-tls=false and disabled TLS anyway")
		}
		if !res.NoMonitoring {
			t.Error("keeping TLS also kept the monitoring stack: the two levers are " +
				"independent and only the one the user named should have changed")
		}
	})

	t.Run("an explicit --no-monitoring=false is honoured", func(t *testing.T) {
		res, err := resolveCompactMode(changedSet("no-monitoring"), "", false, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.NoMonitoring {
			t.Error("--compact overrode an explicit --no-monitoring=false")
		}
	})
}
