// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

// applyDeclaration exists so that downstream steps consume the DECLARATION rather
// than the flags that produced it. A version that wrote nothing back would leave
// every field below at its flag value.
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

// 🔴 THE DECLARATION'S UID HAS TO REACH THE RUN, and only the read-back can supply
// it: the API server assigns it, so the spec this step sends does not carry one.
// Every Secret dcctl mints is stamped with it to tell this instance from a previous
// one of the same name, and the writer refuses to mint without it — so dropping it
// here is a bootstrap that stops at the first Secret it tries to write.
func TestTheDeclarationsUIDReachesTheRun(t *testing.T) {
	prev := readInstanceDeclaration
	prevWrite := writeInstanceDeclaration
	t.Cleanup(func() { readInstanceDeclaration = prev; writeInstanceDeclaration = prevWrite })

	writeInstanceDeclaration = func(context.Context, string, string, dcv1beta1.InstanceSpec, string) error {
		return nil
	}
	readInstanceDeclaration = func(context.Context, string, string) (*dcv1beta1.Instance, error) {
		return &dcv1beta1.Instance{
			ObjectMeta: metav1.ObjectMeta{Name: "prod", UID: "7f0d4f2e-0000-4000-8000-000000000001"},
			Spec:       dcv1beta1.InstanceSpec{Provider: "kind", Profile: "default"},
		}, nil
	}

	st := &State{Instance: "prod", Provider: "kind", Profile: "default"}
	if err := stepDeclareInstance(context.Background(), st); err != nil {
		t.Fatalf("declaring: %v", err)
	}
	if st.InstanceUID != "7f0d4f2e-0000-4000-8000-000000000001" {
		t.Errorf("the declaration's UID did not reach the run (%q): every minted Secret would "+
			"be refused for having no owner", st.InstanceUID)
	}
}

// ...and a declaration that comes back without one must stop the run here, where the
// sentence can say what is wrong, rather than several steps later inside a Secret
// write.
func TestADeclarationWithNoUIDStopsTheRun(t *testing.T) {
	prev := readInstanceDeclaration
	prevWrite := writeInstanceDeclaration
	t.Cleanup(func() { readInstanceDeclaration = prev; writeInstanceDeclaration = prevWrite })

	writeInstanceDeclaration = func(context.Context, string, string, dcv1beta1.InstanceSpec, string) error {
		return nil
	}
	readInstanceDeclaration = func(context.Context, string, string) (*dcv1beta1.Instance, error) {
		return &dcv1beta1.Instance{
			ObjectMeta: metav1.ObjectMeta{Name: "prod"},
			Spec:       dcv1beta1.InstanceSpec{Provider: "kind", Profile: "default"},
		}, nil
	}

	err := stepDeclareInstance(context.Background(), &State{Instance: "prod", Provider: "kind", Profile: "default"})
	if err == nil {
		t.Fatal("a declaration with no UID was accepted")
	}
	if !strings.Contains(err.Error(), "no UID") {
		t.Errorf("the refusal says something else: %v", err)
	}
}
