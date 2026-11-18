// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package deadletter

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/devicechain-io/dc-microservice/svcclient"
	coretest "github.com/devicechain-io/dc-microservice/test"
)

var tenantGateLogs *coretest.LogSink

func TestMain(m *testing.M) {
	tenantGateLogs = coretest.InstallLogSink()
	os.Exit(m.Run())
}

// stubDeletedTenants makes the tenant-deletion gate answer "deleted" for the given tenants, and
// counts how often it is BUILT, which must be once per producer however many sinks it hands out.
func stubDeletedTenants(t *testing.T, deleted ...string) *int {
	t.Helper()
	builds := 0
	prev := tenantLifecycleGate
	tenantLifecycleGate = func(*svcclient.Peers, *governance.SafetyGates) func(string) bool {
		builds++
		return func(tenant string) bool {
			for _, d := range deleted {
				if tenant == d {
					return true
				}
			}
			return false
		}
	}
	t.Cleanup(func() { tenantLifecycleGate = prev })
	return &builds
}

// No letter is written for a deleted tenant, through any of a producer's sinks and any of their
// write calls, and withholding it is not a loss. The same sink still letters a live tenant —
// the negative control that shows the writer could have been reached.
func TestASinkWritesNoLetterForADeletedTenant(t *testing.T) {
	builds := stubDeletedTenants(t, "acme")
	gone := tenantCtx() // "acme"
	live := core.WithTenant(context.Background(), "globex")
	for _, tc := range []struct {
		name  string
		write func(*Sink, context.Context) error
	}{
		{"Write", func(s *Sink, ctx context.Context) error { return s.Write(ctx, good()) }},
		{"WriteFor", func(s *Sink, ctx context.Context) error { return s.WriteFor(ctx, consumed(), armEnvelope()) }},
		{"WriteForPart", func(s *Sink, ctx context.Context) error {
			return s.WriteForPart(ctx, consumed(), "a1", armEnvelope())
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, reg := testProducer(t, "device-management")
			for _, sink := range []struct {
				name string
				make func(Writer) *Sink
			}{
				{"NewSink", p.NewSink},
				{"NewIndexSink", func(w Writer) *Sink {
					return p.NewIndexSink(w, func(error) { t.Error("an index sink counted a withheld letter as lost") })
				}},
			} {
				w := &fakeWriter{}
				s := sink.make(w)
				if err := tc.write(s, gone); err != nil {
					t.Errorf("%s: withholding a deleted tenant's letter returned %v; want nil", sink.name, err)
				}
				if w.calls != 0 {
					t.Errorf("%s: a deleted tenant's letter reached the writer (%d calls)", sink.name, w.calls)
				}
				if err := tc.write(s, live); err != nil || len(w.got) != 1 {
					t.Errorf("%s: a live tenant's letter was not written (err %v, %d written)", sink.name, err, len(w.got))
				}
			}
			if got := lostCount(t, reg, "device-management"); got != 0 {
				t.Errorf("dead_letter_lost_total = %v; want 0: a withheld letter is not a lost one", got)
			}
		})
	}
	if *builds != 3 {
		t.Errorf("the tenant-deletion gate was built %d times for 3 producers; want once each", *builds)
	}
}

// A sink asks nothing until it has a letter to write: the gate is built on the first letter,
// not when the producer is, because the producer is built before the instance configuration
// the gate reads has been loaded.
func TestTheGateIsBuiltOnTheFirstLetterNotWithTheProducer(t *testing.T) {
	builds := stubDeletedTenants(t)
	p, _ := testProducer(t, "device-management")
	s := p.NewSink(&fakeWriter{})
	if *builds != 0 {
		t.Fatalf("the gate was built %d times before any letter; want 0", *builds)
	}
	_ = s.Write(tenantCtx(), good())
	_ = s.Write(tenantCtx(), good())
	if *builds != 1 {
		t.Fatalf("the gate was built %d times over two letters; want 1", *builds)
	}
}

// A gate handed in before the first letter is the one asked, and one handed in after is
// refused: the gate is built once, so a sink that has already relied on an answer cannot see a
// different one.
func TestAGateHandedInIsUsedOnlyBeforeTheFirstLetter(t *testing.T) {
	builds := stubDeletedTenants(t)
	p, _ := testProducer(t, "device-management")
	if !p.UseTenantGate(func(tenant string) bool { return tenant == "acme" }) {
		t.Fatal("a gate handed in before any letter was refused")
	}
	if !p.TenantDeleted("acme") || p.TenantDeleted("globex") {
		t.Error("TenantDeleted did not answer from the gate handed in")
	}
	if *builds != 0 {
		t.Errorf("the configured gate was built %d times beside the one handed in; want 0", *builds)
	}
	if p.UseTenantGate(func(string) bool { return false }) {
		t.Error("a gate handed in after the first answer was accepted")
	}
	if !p.TenantDeleted("acme") {
		t.Error("a late gate replaced the answer a sink had already relied on")
	}
}

// A ceiling is repository policy, not a runtime prerequisite for the lazy gate.
func TestTenantGateAcceptsAnAreaWithoutACeiling(t *testing.T) {
	const area = "synthetic-deadletter-area"
	if _, ok := auth.ServiceCeiling(area); ok {
		t.Fatal("test needs an area absent from the ceiling table")
	}
	// Exercise the real gate factory, replacing only the lookup closure so construction
	// can be checked without starting a background request to user-management.
	previousFactory := tenantLifecycleGate
	tenantLifecycleGate = func(peers *svcclient.Peers, gates *governance.SafetyGates) func(string) bool {
		gate := previousFactory(peers, gates)
		if gate == nil {
			return nil
		}
		return func(string) bool { return false }
	}
	t.Cleanup(func() { tenantLifecycleGate = previousFactory })
	configured := config.InfrastructureConfiguration{}
	configured.UserManagement = config.UserManagementConfiguration{Hostname: "um", Port: 8080}
	configured.ServiceAuth.Secret = "secret"
	for _, tc := range []struct {
		name    string
		missing string
	}{
		{"configured", ""},
		{"no secret", "secret"},
		{"no hostname", "hostname"},
		{"no port", "port"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := testProducer(t, area)
			infra := configured
			switch tc.missing {
			case "secret":
				infra.ServiceAuth.Secret = ""
			case "hostname":
				infra.UserManagement.Hostname = ""
			case "port":
				infra.UserManagement.Port = 0
			}
			p.ms.InstanceConfiguration.Infrastructure = infra
			logs := tenantGateLogs.Capture(t)
			if p.TenantDeleted("acme") {
				t.Fatal("construction-only lookup or unconfigured gate must fail open")
			}
			wantGate := tc.missing == ""
			if (p.gate != nil) != wantGate {
				t.Fatalf("gate present = %v, want %v", p.gate != nil, wantGate)
			}
			if !wantGate && (!strings.Contains(logs.String(), `"level":"warn"`) ||
				!strings.Contains(logs.String(), "user-management endpoint or service secret not configured")) {
				t.Fatalf("missing configuration did not warn: %s", logs.String())
			}
		})
	}
}
