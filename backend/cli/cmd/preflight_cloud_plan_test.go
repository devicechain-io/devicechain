// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installtui"
)

// The chart registries the advisory checks ask are the ones THIS plan installs from: a
// component the plan leaves out (monitoring under Compact or --no-monitoring, the
// CloudNativePG operator under --no-cnpg, cert-manager under Compact without TLS) is not
// asked, and so cannot WARN. Each plan the screen makes (Replan) carries its own list.
func TestTheRegistriesFollowThePlan(t *testing.T) {
	saveChoiceFlags(t)
	old := cloudProbesFor
	t.Cleanup(func() { cloudProbesFor = old })
	f := newCloudFakes(t)
	cloudProbesFor = f.probes(testServer)
	provider, err := bootstrap.Get("local")
	if err != nil {
		t.Fatal(err)
	}
	c := parseChoices(t, []string{"local", "--kube-context", "prod", "--version", "v1.12.0"})
	prepared, err := c.Prepare(Version, nil)
	if err != nil {
		t.Fatal(err)
	}
	w := installWizardFor(provider, c, prepared)

	asked := func(t *testing.T, r installflow.PreflightRunner) map[string]bool {
		t.Helper()
		if r == nil {
			t.Fatal("the plan has no advisory checks")
		}
		got := map[string]bool{}
		_, _ = r.RunPreflight(context.Background(), func(o installflow.PreflightObservation) {
			if o.Observation != installflow.ObserveCheck {
				return
			}
			for _, name := range []string{"cert-manager chart repository", "CloudNativePG chart registry",
				"monitoring chart repository", "Traefik chart repository", "ingress-nginx chart repository"} {
				if strings.HasPrefix(o.Text.String(), name) {
					got[name] = true
				}
			}
		})
		return got
	}
	yes, no := true, false
	for _, tc := range []struct {
		name  string
		edits *installtui.Edits // nil: the command line's own plan
		want  []string
		never []string
	}{
		{"standard", nil,
			[]string{"cert-manager chart repository", "CloudNativePG chart registry", "monitoring chart repository", "Traefik chart repository"},
			[]string{"ingress-nginx chart repository"}},
		{"compact", &installtui.Edits{Compact: &yes},
			[]string{"CloudNativePG chart registry", "Traefik chart repository"},
			[]string{"monitoring chart repository", "cert-manager chart repository"}},
		{"compact, monitoring kept", &installtui.Edits{Compact: &yes, Toggles: map[string]bool{"no-monitoring": false}},
			[]string{"monitoring chart repository"}, []string{"cert-manager chart repository"}},
		{"compact, TLS kept", &installtui.Edits{Compact: &yes, Toggles: map[string]bool{"no-tls": false}},
			[]string{"cert-manager chart repository"}, []string{"monitoring chart repository"}},
		{"--no-monitoring", &installtui.Edits{Toggles: map[string]bool{"no-monitoring": true}},
			[]string{"cert-manager chart repository", "CloudNativePG chart registry"}, []string{"monitoring chart repository"}},
		{"--no-cnpg", &installtui.Edits{Toggles: map[string]bool{"no-cnpg": true}},
			[]string{"monitoring chart repository"}, []string{"CloudNativePG chart registry"}},
		{"back to standard", &installtui.Edits{Compact: &no},
			[]string{"monitoring chart repository", "cert-manager chart repository"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := w.Advisory
			if tc.edits != nil {
				re, err := w.Replan(*tc.edits)
				if err != nil {
					t.Fatal(err)
				}
				r = re.Advisory
			}
			got := asked(t, r)
			for _, name := range tc.want {
				if !got[name] {
					t.Errorf("the %s is not asked (asked %v)", name, got)
				}
			}
			for _, name := range tc.never {
				if got[name] {
					t.Errorf("the %s is asked, and this plan does not install from it", name)
				}
			}
		})
	}
}
