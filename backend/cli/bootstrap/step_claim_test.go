// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"testing"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

// 🔴 THE DECLARATION RECORDS THE RESOLVED VALUE; THE STATE HOLDS THE REQUEST, and
// for Grafana SSO those are two different facts that must not be collapsed.
// InstanceSpecFrom writes grafanaSSOEnabled(st) — what this run will actually do —
// while st.GrafanaSSO is what the operator asked for. stepRenderConfig compares
// them to say "SSO was requested and could not be switched on", which is the only
// way an operator learns that --grafana-sso did nothing.
//
// Writing the resolved value back over the request makes the two equal, which
// silences that warning by erasing its input rather than by fixing anything. The
// run still has no SSO; the operator is no longer told.
func TestTheDeclarationDoesNotOverwriteTheGrafanaSSORequest(t *testing.T) {
	// Requested, with monitoring on, but an http issuer on a non-loopback host —
	// so it resolves to OFF and the warning is the whole point of the round trip.
	st := &State{
		Profile:     "default",
		GrafanaSSO:  true,
		NoTLS:       true,
		IngressHost: "dc.example.com",
	}
	if grafanaSSOEnabled(st) {
		t.Fatal("this fixture resolves SSO ON, so it is not the requested-but-invalid case " +
			"this test is about")
	}
	if !grafanaSSORequestedButInvalid(st) {
		t.Fatal("this fixture does not trip the warn condition, so nothing below can detect it " +
			"being silenced")
	}

	spec := InstanceSpecFrom(st, ClusterBinding{}, "local")
	if spec.GrafanaSSO {
		t.Fatal("the declaration recorded SSO as enabled when it is not, so it is recording the " +
			"request rather than what this run does")
	}

	applyDeclaration(st, spec)

	if !st.GrafanaSSO {
		t.Error("the read-back declaration overwrote the request with the resolved value")
	}
	if !grafanaSSORequestedButInvalid(st) {
		t.Error("the warning that SSO was asked for and could not be enabled can no longer fire: " +
			"the operator will be told nothing and get no SSO")
	}
}

// The counterweight, and it is doing real work: applyDeclaration exists so that
// downstream steps consume the DECLARATION rather than the flags that produced it.
// A version that wrote nothing back would satisfy the test above perfectly.
func TestTheReadBackDeclarationIsWhatTheRestOfTheRunUses(t *testing.T) {
	st := &State{Profile: "default", IngressHost: "flag.example.com"}

	applyDeclaration(st, dcv1beta1.InstanceSpec{
		Profile:              "full",
		HA:                   true,
		Compact:              true,
		Monitoring:           false,
		CNPG:                 false,
		Host:                 "declared.example.com",
		TLS:                  false,
		ExtraFunctionalAreas: []string{"lwm2m-ingest"},
	})

	for _, tc := range []struct {
		field    string
		got, was any
	}{
		{"profile", st.Profile, "full"},
		{"ha", st.HA, true},
		{"compact", st.Compact, true},
		{"monitoring (inverted onto NoMonitoring)", st.NoMonitoring, true},
		{"cnpg (inverted onto NoCNPG)", st.NoCNPG, true},
		{"host", st.IngressHost, "declared.example.com"},
		{"tls (inverted onto NoTLS)", st.NoTLS, true},
	} {
		if tc.got != tc.was {
			t.Errorf("%s did not reach the run from the declaration: %v, want %v", tc.field, tc.got, tc.was)
		}
	}
	if len(st.EnableAreas) != 1 || st.EnableAreas[0] != "lwm2m-ingest" {
		t.Errorf("the declared area request did not reach the run: %v", st.EnableAreas)
	}
}
