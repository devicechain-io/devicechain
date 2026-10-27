// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

// ---------------------------------------------------------------------------
// The Gateway's certificate is a precondition of the steps that make the Gateway the only front end
// ---------------------------------------------------------------------------

func readyGatewayCert(ns string) []gatewayCertVerdict {
	return []gatewayCertVerdict{{Namespace: ns, Gateway: true, Expected: true, Ready: true}}
}

// stubGatewayCertificates swaps the cluster read for a fixed answer and records who was asked about.
func stubGatewayCertificates(t *testing.T, vs []gatewayCertVerdict, err error) *[]string {
	t.Helper()
	prev := readGatewayCertificates
	t.Cleanup(func() { readGatewayCertificates = prev })
	var asked []string
	readGatewayCertificates = func(_ context.Context, _, instance string) ([]gatewayCertVerdict, error) {
		asked = append(asked, instance)
		return vs, err
	}
	return &asked
}

// The caller: settleFrontEnd, with --drop-legacy-ingress, against a cluster whose Gateway
// certificate is or is not Ready. Each refusal has its control: the same run with a Ready certificate.
func TestDropLegacyIngressIsRefusedUntilTheGatewayCertificateIsReady(t *testing.T) {
	prev := readFrontEndFacts
	t.Cleanup(func() { readFrontEndFacts = prev })
	readFrontEndFacts = func(context.Context, string, string) (frontEndFacts, error) {
		return frontEndFacts{clusterFrontEnd: clusterFrontEnd{GatewayClass: true, NginxClass: true},
			InstanceHasIngress: true, InstanceHasGateway: true, InstanceGatewaySecret: gatewayCutoverSecret}, nil
	}
	run := func(drop bool) (*State, error) {
		st := &State{Instance: "prod", DropLegacyIngress: drop}
		return st, settleFrontEnd(t.Context(), st)
	}
	ns := InstanceNamespace("prod")

	for name, vs := range map[string][]gatewayCertVerdict{
		"not Ready":           {{Namespace: ns, Gateway: true, Expected: true, Why: "Certificate gw is not Ready: waiting"}},
		"not the Gateway's":   {{Namespace: ns, Gateway: true, Expected: true, Why: "Certificate x is not controlled by the Gateway"}},
		"no Gateway to go to": {{Namespace: ns}},
	} {
		stubGatewayCertificates(t, vs, nil)
		st, err := run(true)
		if err == nil || !strings.Contains(err.Error(), "Nothing has been changed") {
			t.Errorf("%s: --drop-legacy-ingress was not refused: %v", name, err)
		}
		if st.IngressController != "" {
			t.Errorf("%s: the refusal still settled a front end (%q): something would have rendered", name, st.IngressController)
		}
	}

	// Controls: Ready lets it through, and the same cluster without the flag is never asked.
	asked := stubGatewayCertificates(t, readyGatewayCert(ns), nil)
	if st, err := run(true); err != nil || st.LegacyIngress || st.IngressController != frontEndGateway {
		t.Fatalf("a Ready certificate was refused or mis-settled: %v legacy=%v", err, st.LegacyIngress)
	}
	if len(*asked) != 1 || (*asked)[0] != "prod" {
		t.Errorf("the drop read the certificate of %v, want exactly this instance", *asked)
	}
	asked = stubGatewayCertificates(t, []gatewayCertVerdict{{Namespace: ns, Gateway: true, Expected: true}}, nil)
	if _, err := run(false); err != nil || len(*asked) != 0 {
		t.Errorf("without the flag the upgrade read the certificate (%v) or failed: %v", *asked, err)
	}
	// A Gateway whose Secret the operator supplies has no certificate to wait for.
	stubGatewayCertificates(t, []gatewayCertVerdict{{Namespace: ns, Gateway: true}}, nil)
	if _, err := run(true); err != nil {
		t.Errorf("a Gateway with no cert-manager issuer was refused: %v", err)
	}
	// An unreadable cluster stops a real run.
	stubGatewayCertificates(t, nil, errors.New("connection refused"))
	if _, err := run(true); err == nil {
		t.Error("a certificate that could not be read let the drop through")
	}
}

// The caller: the install's cluster variables, with --remove-legacy-ingress, re-check EVERY instance.
func TestRemoveLegacyIngressChecksEveryInstanceOnTheCluster(t *testing.T) {
	cls := nginxClass()
	run := func(remove bool) ([]string, error) {
		st := installState(ClusterBinding{KubeContext: "gke_p_z_c"}, "local", InstallOptions{RemoveLegacyIngress: remove})
		return installClusterVars(t.Context(), st, fakeTypedWith(&cls))
	}
	waiting := []gatewayCertVerdict{
		{Namespace: "dci-a", Gateway: true, Expected: true, Ready: true},
		{Namespace: "dci-b", Gateway: true, Expected: true, Why: "no Certificate issues into Secret devicechain-tls-gateway"},
	}
	asked := stubGatewayCertificates(t, waiting, nil)
	_, err := run(true)
	if err == nil || !strings.Contains(err.Error(), "dci-b") || strings.Contains(err.Error(), "dci-a") || !strings.Contains(err.Error(), "does not change how they are served") {
		t.Fatalf("removal went ahead past an instance with no certificate, or named the wrong one: %v", err)
	}
	if len(*asked) != 1 || (*asked)[0] != "" {
		t.Errorf("the removal read %v, want every instance (the empty name)", *asked)
	}
	// Controls: the same cluster ready (an instance with no Gateway is not this check's), and no flag.
	stubGatewayCertificates(t, []gatewayCertVerdict{waiting[0], {Namespace: "dci-c"}}, nil)
	if vars, err := run(true); err != nil || !slices.Contains(vars, "allow_ingress_nginx_removal=true") {
		t.Errorf("a ready cluster was refused: %v %v", err, vars)
	}
	asked = stubGatewayCertificates(t, waiting, nil)
	if _, err := run(false); err != nil || len(*asked) != 0 {
		t.Errorf("without the flag the install read the certificates (%v) or failed: %v", *asked, err)
	}
}

// The caller: waitForGatewayCertificate while the Ingress objects still render.
func TestTheCertificateGateOnlyWarnsDuringTheWindow(t *testing.T) {
	prevT, prevL, prevP := gatewayCertTimeout, gatewayCertLegacyTimeout, gatewayCertPoll
	gatewayCertTimeout, gatewayCertLegacyTimeout, gatewayCertPoll = 50*time.Millisecond, 50*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { gatewayCertTimeout, gatewayCertLegacyTimeout, gatewayCertPoll = prevT, prevL, prevP })

	st := compactState(false)
	st.Instance = "prod"
	st.IngressController, st.GatewayTLSSecret = frontEndGateway, "devicechain-tls-gateway"
	st.Install = &InstallRecord{Settings: InstallSettings{CertManager: true}}
	gw := gwObj("dci-prod", map[string]string{"cert-manager.io/issuer": "dc"}, "devicechain-tls-gateway")

	// Negative control: with no legacy Ingress the same missing certificate fails the run.
	if err := waitForGatewayCertificate(t.Context(), newFakeCertDynamic(gw), st); err == nil {
		t.Fatal("a Gateway alone with no certificate passed the gate")
	}
	window := *st
	window.LegacyIngress = true
	var err error
	out := captureStdout(t, func() { err = waitForGatewayCertificate(t.Context(), newFakeCertDynamic(gw), &window) })
	if err != nil {
		t.Fatalf("the window failed the upgrade for a certificate it does not need yet: %v", err)
	}
	for _, want := range []string{"warning", "no Certificate issues into Secret devicechain-tls-gateway", "--drop-legacy-ingress", "get certificate"} {
		if !strings.Contains(out, want) {
			t.Errorf("the warning does not say %q:\n%s", want, out)
		}
	}
	// A Certificate the Ingress owns is still named for what it is.
	foreign := cert("devicechain-tls", "devicechain-tls-gateway", false, "True")
	out = captureStdout(t, func() { err = waitForGatewayCertificate(t.Context(), newFakeCertDynamic(gw, &foreign), &window) })
	if err != nil || !strings.Contains(out, "not controlled by the Gateway") {
		t.Errorf("a foreign Certificate in the window: err=%v out=%s", err, out)
	}
}

// cert-manager's HTTP-01 solver Ingress is neither the instance's nor the cluster's.
func TestAnACMESolverIngressIsNotTheInstancesOrTheClusters(t *testing.T) {
	cls := nginxClass()
	own := InstanceNamespace("prod")
	className := cls.Name
	solver := networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Namespace: own, Name: "cm-acme-http-solver-abcde",
		Labels: map[string]string{acmeSolverLabel: "true"}}, Spec: networkingv1.IngressSpec{IngressClassName: &className}}
	real := networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Namespace: own, Name: "devicechain-web"},
		Spec: networkingv1.IngressSpec{IngressClassName: &className}}
	classes := []networkingv1.IngressClass{cls}

	f := frontEndFactsFrom(classes, []networkingv1.Ingress{solver}, true, nil, "prod", true)
	if f.InstanceHasIngress || len(f.NginxIngresses) != 0 {
		t.Errorf("a solver Ingress counted: instance=%v cluster=%v", f.InstanceHasIngress, f.NginxIngresses)
	}
	if _, err := planClusterFrontEnd(f.clusterFrontEnd, false, true); err != nil {
		t.Errorf("a solver Ingress blocked the controller's removal: %v", err)
	}
	// Negative control: a real Ingress beside the solver is counted, and the solver still is not.
	f = frontEndFactsFrom(classes, []networkingv1.Ingress{solver, real}, true, nil, "prod", true)
	if !f.InstanceHasIngress || len(f.NginxIngresses) != 1 || !strings.HasSuffix(f.NginxIngresses[0], "/devicechain-web") {
		t.Errorf("a real Ingress beside a solver: instance=%v cluster=%v", f.InstanceHasIngress, f.NginxIngresses)
	}
	if _, err := planClusterFrontEnd(f.clusterFrontEnd, false, true); err == nil {
		t.Error("removal went ahead with a real Ingress left")
	}
}

// The ca.crt hint names the Secret that is actually serving.
func TestTheServingSecretFollowsTheFrontEnd(t *testing.T) {
	for name, tc := range map[string]struct {
		st   State
		want string
	}{
		"nginx":                  {State{IngressController: frontEndNginx}, defaultTLSSecret},
		"the window":             {State{IngressController: frontEndGateway, LegacyIngress: true, GatewayTLSSecret: gatewayCutoverSecret}, defaultTLSSecret},
		"the Gateway alone":      {State{IngressController: frontEndGateway, GatewayTLSSecret: gatewayCutoverSecret}, gatewayCutoverSecret},
		"a new Gateway instance": {State{IngressController: frontEndGateway}, defaultTLSSecret},
	} {
		if got := servingTLSSecret(&tc.st); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}

// gwObj is an instance's Gateway: annotated as given, https listener naming secret (none if empty).
func gwObj(ns string, annotations map[string]string, secret string) *unstructured.Unstructured {
	listeners := []interface{}{map[string]interface{}{"name": "http", "protocol": "HTTP", "port": int64(80)}}
	if secret != "" {
		listeners = append(listeners, map[string]interface{}{"name": "https", "protocol": "HTTPS", "port": int64(443),
			"tls": map[string]interface{}{"certificateRefs": []interface{}{map[string]interface{}{"name": secret}}}})
	}
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "gateway.networking.k8s.io/v1", "kind": "Gateway",
		"metadata": map[string]interface{}{"name": instanceGatewayName, "namespace": ns},
		"spec":     map[string]interface{}{"listeners": listeners},
	}}
	if annotations != nil {
		u.SetAnnotations(annotations)
	}
	return u
}

// certIn is cert() in another namespace.
func certIn(ns, name, secret string, owned bool, ready string) *unstructured.Unstructured {
	c := cert(name, secret, owned, ready)
	c.SetNamespace(ns)
	return &c
}

// T1: the REAL read, over a fake cluster. The verdict is what every caller above acts on, and the
// mutants `v.Expected = false` / dropping the ClusterIssuer arm each turn a row below red.
func TestTheGatewayCertificateReadSeesWhatTheClusterHolds(t *testing.T) {
	const secret = "devicechain-tls-gateway"
	read := func(t *testing.T, instance string, objs ...*unstructured.Unstructured) []gatewayCertVerdict {
		t.Helper()
		dyn := newFakeCertDynamic(objs...)
		vs, err := gatewayCertificatesFrom(t.Context(), dyn, instance)
		if err != nil {
			t.Fatal(err)
		}
		return vs
	}
	one := func(t *testing.T, vs []gatewayCertVerdict) gatewayCertVerdict {
		t.Helper()
		if len(vs) != 1 {
			t.Fatalf("got %d verdicts, want 1: %+v", len(vs), vs)
		}
		return vs[0]
	}

	for name, ann := range map[string]map[string]string{
		"Issuer":        {"cert-manager.io/issuer": "dc"},
		"ClusterIssuer": {"cert-manager.io/cluster-issuer": "letsencrypt"},
		"tls-acme":      {"kubernetes.io/tls-acme": "true"},
	} {
		t.Run(name+" expects a Certificate, Ready when the Gateway controls one", func(t *testing.T) {
			v := one(t, read(t, "prod", gwObj("dci-prod", ann, secret), certIn("dci-prod", "gw", secret, true, "True")))
			if !v.Gateway || !v.Expected || !v.Ready {
				t.Errorf("verdict %+v, want Gateway, Expected, Ready", v)
			}
			// Control: the same Gateway, no Certificate yet, is expected and NOT ready.
			v = one(t, read(t, "prod", gwObj("dci-prod", ann, secret)))
			if !v.Expected || v.Ready || v.Why == "" {
				t.Errorf("no Certificate: verdict %+v, want Expected, not Ready, with a reason", v)
			}
		})
	}
	t.Run("a Certificate with no controlling owner is not the Gateway's", func(t *testing.T) {
		v := one(t, read(t, "prod", gwObj("dci-prod", map[string]string{"cert-manager.io/issuer": "dc"}, secret),
			certIn("dci-prod", "devicechain-tls", secret, false, "True")))
		if !v.Expected || v.Ready || !strings.Contains(v.Why, "not controlled by the Gateway") {
			t.Errorf("verdict %+v", v)
		}
	})
	t.Run("an operator's Secret (no issuer annotation) expects nothing", func(t *testing.T) {
		v := one(t, read(t, "prod", gwObj("dci-prod", map[string]string{"unrelated": "x", "kubernetes.io/tls-acme": "false"}, secret)))
		if !v.Gateway || v.Expected {
			t.Errorf("verdict %+v, want a Gateway that expects no Certificate", v)
		}
	})
	t.Run("an issuer with no https Secret expects nothing", func(t *testing.T) {
		if v := one(t, read(t, "prod", gwObj("dci-prod", map[string]string{"cert-manager.io/issuer": "dc"}, ""))); v.Expected {
			t.Errorf("verdict %+v", v)
		}
	})
	t.Run("no Gateway object is an instance with no Gateway", func(t *testing.T) {
		if v := one(t, read(t, "prod")); v.Gateway || v.Namespace != "dci-prod" {
			t.Errorf("verdict %+v", v)
		}
	})
	t.Run("across the cluster: every Gateway of the name, each its own namespace", func(t *testing.T) {
		vs := read(t, "",
			gwObj("dci-a", map[string]string{"cert-manager.io/issuer": "dc"}, secret), certIn("dci-a", "gw", secret, true, "True"),
			gwObj("dci-b", map[string]string{"cert-manager.io/issuer": "dc"}, secret),
			gwObj("dci-c", nil, secret))
		got := map[string]gatewayCertVerdict{}
		for _, v := range vs {
			got[v.Namespace] = v
		}
		if len(got) != 3 || !got["dci-a"].Ready || got["dci-b"].Ready || !got["dci-b"].Expected || got["dci-c"].Expected {
			t.Errorf("verdicts %+v", vs)
		}
	})
	t.Run("the Gateway API absent (NoMatch) is no Gateways, not an error", func(t *testing.T) {
		for name, absent := range map[string]error{
			"NoMatch":  &meta.NoResourceMatchError{PartialResource: gatewayGVR},
			"NotFound": apierrors.NewNotFound(gatewayGVR.GroupResource(), "devicechain"),
		} {
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
				map[schema.GroupVersionResource]string{certificateGVR: "CertificateList", gatewayGVR: "GatewayList"})
			dyn.PrependReactor("*", "gateways", func(clienttesting.Action) (bool, runtime.Object, error) { return true, nil, absent })
			_ = name
			for _, instance := range []string{"prod", ""} {
				vs, err := gatewayCertificatesFrom(t.Context(), dyn, instance)
				if err != nil {
					t.Fatalf("instance %q: %v", instance, err)
				}
				if instance == "" && len(vs) != 0 {
					t.Errorf("cluster-wide: %+v, want none", vs)
				}
				if instance != "" && (len(vs) != 1 || vs[0].Gateway) {
					t.Errorf("instance: %+v, want one verdict with no Gateway", vs)
				}
			}
		}
	})
}
