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
	"github.com/devicechain-io/dcctl/bootstrap"
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
		res, err := resolveDevMode(changedSet(), "", false, false, false, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !res.Build || res.Host != "localhost" || !res.NoTLS || !res.Yes {
			t.Fatalf("preset not fully applied: %+v", res)
		}
	})

	t.Run("redundant but consistent explicit flags are accepted", func(t *testing.T) {
		// --dev --host localhost --no-tls --build: all agree with the preset.
		res, err := resolveDevMode(changedSet("host", "no-tls", "build"), "localhost", true, true, false, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Host != "localhost" {
			t.Fatalf("host = %q, want localhost", res.Host)
		}
	})

	t.Run("a conflicting --host is rejected, not silently overridden", func(t *testing.T) {
		_, err := resolveDevMode(changedSet("host"), "prod.example.com", false, false, false, false)
		if err == nil {
			t.Fatal("expected an error for --dev --host prod.example.com")
		}
	})

	t.Run("--no-tls=false conflicts with the http preset", func(t *testing.T) {
		if _, err := resolveDevMode(changedSet("no-tls"), "", false, false, false, false); err == nil {
			t.Fatal("expected an error for --dev --no-tls=false")
		}
	})

	t.Run("--build=false conflicts with build-from-source", func(t *testing.T) {
		if _, err := resolveDevMode(changedSet("build"), "", false, false, false, false); err == nil {
			t.Fatal("expected an error for --dev --build=false")
		}
	})

	t.Run("an unset conflicting-looking value is fine (only explicit flags conflict)", func(t *testing.T) {
		// host defaults to "" (not localhost) but was NOT set by the user, so no conflict.
		if _, err := resolveDevMode(changedSet(), "", false, false, false, false); err != nil {
			t.Fatalf("unset host must not conflict: %v", err)
		}
	})
}

func TestResolveCompactMode(t *testing.T) {
	t.Run("bare --compact turns off TLS and monitoring", func(t *testing.T) {
		res := resolveCompactMode(changedSet(), false, false)
		if !res.NoTLS || !res.NoMonitoring {
			t.Fatalf("preset not fully applied: %+v", res)
		}
	})

	t.Run("an explicit --no-tls=false is honoured, not rejected", func(t *testing.T) {
		// Keeping TLS is a DEPENDENCY, not a contradiction: cert-manager stays
		// installed to issue the certificate and every other compact lever still
		// applies. Erroring here would cost real functionality for no benefit.
		res := resolveCompactMode(changedSet("no-tls"), false, false)
		if res.NoTLS {
			t.Error("--compact overrode an explicit --no-tls=false and disabled TLS anyway")
		}
		if !res.NoMonitoring {
			t.Error("keeping TLS also kept the monitoring stack: the two levers are " +
				"independent and only the one the user named should have changed")
		}
	})

	t.Run("an explicit --no-monitoring=false is honoured", func(t *testing.T) {
		res := resolveCompactMode(changedSet("no-monitoring"), false, false)
		if res.NoMonitoring {
			t.Error("--compact overrode an explicit --no-monitoring=false")
		}
	})
}

func TestFollowClusterShape(t *testing.T) {
	compact := func(profile string) *bootstrap.State {
		return &bootstrap.State{Compact: true, Profile: profile}
	}

	t.Run("a profile larger than default is rejected on a compact cluster", func(t *testing.T) {
		// The reason is the FOOTPRINT CLAIM, not the storage budget: the JetStream
		// reservation sums the whole stream/KV inventory regardless of profile, so
		// the budget holds for `full` too. What does not hold is a published number
		// measured on `default` describing an instance running three more services.
		err := followClusterShape(changedSet("profile"), compact("full"))
		if err == nil {
			t.Fatal("--profile full was accepted on a compact cluster: the published footprint " +
				"would describe fewer services than the instance runs")
		}
		if !strings.Contains(err.Error(), "full") {
			t.Errorf("error %q does not name the offending profile", err)
		}
		if err := followClusterShape(changedSet("profile"), &bootstrap.State{Profile: "full"}); err != nil {
			t.Errorf("--profile full was rejected on a cluster installed without --compact: %v", err)
		}
	})

	t.Run("a profile smaller than default is accepted", func(t *testing.T) {
		// telemetry and ingest-only are strict SUBSETS of default. Refusing them was
		// the first draft's behaviour and it was backwards: asking for the smallest
		// thing the platform ships is the one request a small-footprint preset must
		// not turn down.
		for _, p := range append([]string{"default", ""}, profilesSmallerThanDefault...) {
			if err := followClusterShape(changedSet("profile"), compact(p)); err != nil {
				t.Errorf("--profile %q was rejected on a compact cluster, but it deploys no more "+
					"areas than default: %v", p, err)
			}
		}
	})

	withoutCertManager := func(noTLS bool) *bootstrap.State {
		return &bootstrap.State{NoTLS: noTLS, Install: &bootstrap.InstallRecord{
			Settings: bootstrap.InstallSettings{CertManager: false},
		}}
	}

	t.Run("--no-tls=false is refused on a cluster installed without cert-manager", func(t *testing.T) {
		if err := followClusterShape(changedSet("no-tls"), withoutCertManager(false)); err == nil {
			t.Fatal("TLS was accepted on a cluster with nothing to issue the certificate")
		}
	})

	t.Run("plain HTTP is settled on a cluster installed without cert-manager", func(t *testing.T) {
		st := withoutCertManager(false)
		if err := followClusterShape(changedSet(), st); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !st.NoTLS {
			t.Error("an instance on a cluster with no cert-manager was left serving TLS")
		}
	})

	t.Run("TLS is left alone on a cluster installed with cert-manager", func(t *testing.T) {
		st := &bootstrap.State{Install: &bootstrap.InstallRecord{
			Settings: bootstrap.InstallSettings{CertManager: true},
		}}
		if err := followClusterShape(changedSet("no-tls"), st); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if st.NoTLS {
			t.Error("TLS was switched off on a cluster whose cert-manager can issue the certificate")
		}
	})
}

// Every profile the chart ships must be classified relative to `default`.
//
// followClusterShape decides by list membership, so a profile in neither list is
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
