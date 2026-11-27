// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installtui"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ktesting "k8s.io/client-go/testing"
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

// 🔴 THE INGRESS CHARTS FOLLOW THE CLUSTER'S FRONT-END PLAN, as the install resolves it
// from the cluster's IngressClasses: Traefik always, and ingress-nginx while the cluster
// runs it (it is kept, and its chart downloaded) unless --remove-legacy-ingress removes
// it. When the classes cannot be read, the ingress-nginx row says it was not checked.
func TestTheIngressChartsFollowTheClustersFrontEnd(t *testing.T) {
	nginx := &networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: "nginx"},
		Spec: networkingv1.IngressClassSpec{Controller: "k8s.io/ingress-nginx"}}
	for _, tc := range []struct {
		name         string
		classes      []*networkingv1.IngressClass
		listErr      error
		removeLegacy bool
		nginx        installflow.CheckVerdict // "" when the row must be absent
	}{
		{name: "a cluster without ingress-nginx"},
		{name: "a cluster that runs ingress-nginx", classes: []*networkingv1.IngressClass{nginx}, nginx: installflow.CheckPass},
		{name: "--remove-legacy-ingress", classes: []*networkingv1.IngressClass{nginx}, removeLegacy: true},
		{name: "the classes cannot be read", listErr: apierrors.NewForbidden(schema.GroupResource{Group: "networking.k8s.io", Resource: "ingressclasses"}, "", errors.New("no")),
			nginx: installflow.CheckNotChecked},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCloudFakes(t)
			for _, c := range tc.classes {
				if _, err := f.kube.NetworkingV1().IngressClasses().Create(context.Background(), c, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			f.kube.ClearActions()
			if tc.listErr != nil {
				f.kube.PrependReactor("list", "ingressclasses", func(ktesting.Action) (bool, runtime.Object, error) {
					return true, nil, tc.listErr
				})
			}
			target := prodTarget
			target.RemoveLegacyIngress = tc.removeLegacy
			var rows []installflow.PreflightObservation
			_, _ = cloudDiagnosticsRunner(target, f.probes(testServer)).RunPreflight(context.Background(),
				func(o installflow.PreflightObservation) {
					if o.Observation == installflow.ObserveCheck {
						rows = append(rows, o)
					}
				})
			assertReadOnly(t, f)
			if r := row(t, rows, "Traefik chart repository"); r.Verdict != installflow.CheckPass {
				t.Errorf("Traefik reads %s %q", r.Verdict, r.Text)
			}
			var got installflow.CheckVerdict
			for _, o := range rows {
				if strings.HasPrefix(o.Text.String(), "ingress-nginx chart repository") {
					got = o.Verdict
				}
			}
			if got != tc.nginx {
				t.Errorf("the ingress-nginx row reads %q, want %q", got, tc.nginx)
			}
		})
	}
}

// --remove-legacy-ingress on the command line reaches the checks: the plan it makes
// removes ingress-nginx, so its chart is not asked for.
func TestTheRemoveLegacyIngressFlagReachesTheChecks(t *testing.T) {
	saveChoiceFlags(t)
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"local", "--kube-context", "prod", "--version", "v1.12.0"}, false},
		{[]string{"local", "--kube-context", "prod", "--version", "v1.12.0", "--remove-legacy-ingress"}, true},
	} {
		saveChoiceFlags(t)
		prepared, err := parseChoices(t, tc.args).Prepare(Version, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := cloudTargetFor(prepared).RemoveLegacyIngress; got != tc.want {
			t.Errorf("%v: RemoveLegacyIngress %v", tc.args, got)
		}
	}
}
