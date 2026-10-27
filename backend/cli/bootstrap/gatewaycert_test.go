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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	if err == nil || !strings.Contains(err.Error(), "dci-b") || strings.Contains(err.Error(), "dci-a") {
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

	// Negative control: with no legacy Ingress the same missing certificate fails the run.
	if err := waitForGatewayCertificate(t.Context(), newFakeCertDynamic(), st); err == nil {
		t.Fatal("a Gateway alone with no certificate passed the gate")
	}
	window := *st
	window.LegacyIngress = true
	var err error
	out := captureStdout(t, func() { err = waitForGatewayCertificate(t.Context(), newFakeCertDynamic(), &window) })
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
	out = captureStdout(t, func() { err = waitForGatewayCertificate(t.Context(), newFakeCertDynamic(&foreign), &window) })
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
