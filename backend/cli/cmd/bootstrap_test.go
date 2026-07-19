// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"testing"

	assets "github.com/devicechain-io/dc-deploy"
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
		res, err := resolveCompactMode(changedSet(), "", false, false, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !res.NoTLS || !res.NoMonitoring {
			t.Fatalf("preset not fully applied: %+v", res)
		}
	})

	t.Run("a profile larger than default is rejected", func(t *testing.T) {
		// The reason is the FOOTPRINT CLAIM, not the storage budget: the JetStream
		// reservation sums the whole stream/KV inventory regardless of profile, so
		// the budget holds for `full` too. What does not hold is a published number
		// measured on `default` describing an instance running three more services.
		_, err := resolveCompactMode(changedSet("profile"), "full", false, false, false)
		if err == nil {
			t.Fatal("--compact --profile full was accepted: the published footprint " +
				"would describe fewer services than the instance runs")
		}
		if !strings.Contains(err.Error(), "full") {
			t.Errorf("error %q does not name the offending profile", err)
		}
	})

	t.Run("a profile smaller than default is accepted", func(t *testing.T) {
		// telemetry and ingest-only are strict SUBSETS of default. Refusing them was
		// the first draft's behaviour and it was backwards: asking for the smallest
		// thing the platform ships is the one request a small-footprint preset must
		// not turn down.
		for _, p := range profilesSmallerThanDefault {
			if _, err := resolveCompactMode(changedSet("profile"), p, false, false, false); err != nil {
				t.Errorf("--compact --profile %s was rejected, but it deploys FEWER areas "+
					"than default: %v", p, err)
			}
		}
	})

	t.Run("an explicitly redundant --profile default is fine", func(t *testing.T) {
		if _, err := resolveCompactMode(changedSet("profile"), "default", false, false, false); err != nil {
			t.Fatalf("--compact --profile default was rejected: %v", err)
		}
	})

	t.Run("an explicit --no-tls=false is honoured, not rejected", func(t *testing.T) {
		// Keeping TLS is a DEPENDENCY, not a contradiction: cert-manager stays
		// installed to issue the certificate and every other compact lever still
		// applies. Erroring here would cost real functionality for no benefit.
		res, err := resolveCompactMode(changedSet("no-tls"), "", false, false, false)
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
		res, err := resolveCompactMode(changedSet("no-monitoring"), "", false, false, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.NoMonitoring {
			t.Error("--compact overrode an explicit --no-monitoring=false")
		}
	})
}

// --compact --grafana-sso must not silently do nothing.
//
// grafanaSSOEnabled returns false when monitoring is off, and so does the
// "requested but invalid" warning that would otherwise say so — meaning the SSO
// request is dropped with no output whatsoever. That was tolerable while reaching
// it required typing --no-monitoring alongside --grafana-sso; --compact turns
// monitoring off on the user's behalf, so the silence became reachable by
// accident.
func TestCompactRejectsGrafanaSSOItWouldSilentlySwallow(t *testing.T) {
	_, err := resolveCompactMode(changedSet(), "", false, false, true)
	if err == nil {
		t.Fatal("--compact --grafana-sso was accepted: the monitoring stack Grafana " +
			"lives in is removed, so the SSO request is dropped and nothing says so")
	}
	if !strings.Contains(err.Error(), "--no-monitoring=false") {
		t.Errorf("error %q does not name the escape hatch, which is the only thing "+
			"that turns a refusal into an actionable one", err)
	}

	// Keeping the stack explicitly makes the combination coherent again.
	if _, err := resolveCompactMode(changedSet("no-monitoring"), "", false, false, true); err != nil {
		t.Errorf("--compact --grafana-sso --no-monitoring=false was rejected: %v", err)
	}
}

// Every profile the chart ships must be classified relative to `default`.
//
// resolveCompactMode decides by list membership, so a profile in neither list is
// silently ACCEPTED — including one larger than default, which would quietly widen
// what the published compact number claims to cover. Reading the catalog from the
// chart rather than restating it means adding a profile there fails here until
// someone says which side it falls on.
func TestEveryShippedProfileIsClassifiedForCompact(t *testing.T) {
	raw, err := fs.ReadFile(assets.HelmChart(), "templates/_helpers.tpl")
	if err != nil {
		t.Fatalf("reading the embedded chart helpers: %v", err)
	}
	block := regexp.MustCompile(`(?s)\$profiles := dict(.*?)-\}\}`).FindSubmatch(raw)
	if block == nil {
		t.Fatal("could not find the profile catalog in the chart: this test can no " +
			"longer see what it is classifying, so it must fail rather than pass vacuously")
	}
	// Only the dict KEYS: each starts its own line. Matching every quoted string
	// would also pick up the functional-area names inside each profile's list.
	names := regexp.MustCompile(`(?m)^\s+"([a-z-]+)"\s+`).FindAllSubmatch(block[1], -1)
	if len(names) == 0 {
		t.Fatal("the profile catalog parsed as empty")
	}

	for _, m := range names {
		name := string(m[1])
		if name == "default" {
			continue
		}
		larger := slices.Contains(profilesLargerThanDefault, name)
		smaller := slices.Contains(profilesSmallerThanDefault, name)
		if larger == smaller {
			t.Errorf("the chart ships a %q profile that --compact classifies as neither "+
				"larger nor smaller than default (or as both). An unclassified profile is "+
				"ACCEPTED by default, so one larger than default would silently make the "+
				"published footprint describe fewer services than the instance runs", name)
		}
	}
}
