// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

// ---------------------------------------------------------------------------
// The caller wiring: a flag that never reaches the State renders green and does nothing
// ---------------------------------------------------------------------------

// hydrateUpgrade drives the REAL hydrateUpgradeState with the options given, over a declaration
// carrying the annotations given and a cluster that serves the instance's host from ingress-nginx
// with Traefik installed beside it.
func hydrateUpgrade(t *testing.T, opts UpgradeOptions, annotations map[string]string, facts frontEndFacts) *State {
	t.Helper()
	provider, err := Get("local")
	if err != nil {
		t.Fatal(err)
	}
	creds, err := existingCredentials()
	if err != nil {
		t.Fatal(err)
	}
	deployed := &config.InstanceConfiguration{}
	deployed.Infrastructure.Nats.Tls.Enabled = true
	deployed.Infrastructure.Nats.Tls.Ca = "-----RUNNING CA-----"
	deployed.Infrastructure.Nats.Auth.CalloutIssuerSeed = creds.IssuerSeed
	deployed.Infrastructure.Nats.Auth.Password = creds.ServicePassword
	deployed.Infrastructure.Nats.Auth.SysPassword = creds.SysPassword

	prevDecl, prevDeployed, prevFacts := readInstanceDeclaration, lookupDeployedInstance, readFrontEndFacts
	t.Cleanup(func() {
		readInstanceDeclaration, lookupDeployedInstance, readFrontEndFacts = prevDecl, prevDeployed, prevFacts
	})
	readInstanceDeclaration = func(context.Context, string, string) (*dcv1beta1.Instance, error) {
		inst := atVersion(t, "ghcr.io/devicechain-io", "v0.17.0")
		inst.Annotations = annotations
		return inst, nil
	}
	lookupDeployedInstance = func(context.Context, string, string) (*config.InstanceConfiguration, error) {
		return deployed, nil
	}
	readFrontEndFacts = func(context.Context, string, string) (frontEndFacts, error) { return facts, nil }
	stubOperatorCheck(t, nil)

	written := aWritableState()
	written.Instance = "prod"
	c := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: "kube-system", UID: types.UID(testClusterUID)}})
	writeInstallThenBootstrapSecrets(t, c, written)
	settleStringDataLikeAnAPIServer(t, c)
	if err := writeInstalled(context.Background(), c, aCompleteInstall(), installClock); err != nil {
		t.Fatal(err)
	}
	opts.Instance = "prod"
	st, err := hydrateUpgradeState(context.Background(), c, provider,
		ClusterBinding{KubeContext: "gke_p_z_c", Cluster: "devicechain"}, opts)
	if err != nil {
		t.Fatalf("hydrating: %v", err)
	}
	return st
}

var bothControllers = frontEndFacts{
	clusterFrontEnd:    clusterFrontEnd{GatewayClass: true, NginxClass: true},
	InstanceHasIngress: true,
}

// The flags reach the State the upgrade acts on, through the real hydration, and change what is
// settled. Each assertion is one the corresponding assignment in hydrateUpgradeState (and in
// applyIngressControllerChoice) has to satisfy: removing either line fails here.
func TestHydrationCarriesTheFrontEndFlagsOntoTheState(t *testing.T) {
	t.Run("no flag: the instance moves to the Gateway with its Ingress objects beside it", func(t *testing.T) {
		st := hydrateUpgrade(t, UpgradeOptions{}, nil, bothControllers)
		if st.IngressController != frontEndGateway || !st.LegacyIngress {
			t.Errorf("settled %s legacy=%v", st.IngressController, st.LegacyIngress)
		}
	})
	t.Run("--ingress-controller nginx keeps the Ingress objects, and is to be recorded", func(t *testing.T) {
		st := hydrateUpgrade(t, UpgradeOptions{Options: Options{IngressController: frontEndNginx}}, nil, bothControllers)
		if st.IngressController != frontEndNginx || st.LegacyIngress {
			t.Errorf("settled %s legacy=%v", st.IngressController, st.LegacyIngress)
		}
		if !st.IngressControllerChange || st.IngressControllerOverride != frontEndNginx {
			t.Errorf("the override is not marked for recording: change=%v override=%q", st.IngressControllerChange, st.IngressControllerOverride)
		}
	})
	t.Run("--drop-legacy-ingress stops rendering the Ingress objects", func(t *testing.T) {
		stubGatewayCertificates(t, readyGatewayCert(InstanceNamespace("prod")), nil)
		st := hydrateUpgrade(t, UpgradeOptions{Options: Options{DropLegacyIngress: true}}, nil, bothControllers)
		if st.IngressController != frontEndGateway || st.LegacyIngress || !st.DropLegacyIngress {
			t.Errorf("settled %s legacy=%v drop=%v", st.IngressController, st.LegacyIngress, st.DropLegacyIngress)
		}
	})
	t.Run("a recorded nginx survives a plain upgrade", func(t *testing.T) {
		st := hydrateUpgrade(t, UpgradeOptions{}, map[string]string{AnnotationIngressController: frontEndNginx}, bothControllers)
		if st.IngressController != frontEndNginx {
			t.Errorf("a plain upgrade over a recorded rollback settled %s", st.IngressController)
		}
		if st.IngressControllerChange {
			t.Error("a plain upgrade claims to change the record it only read")
		}
	})
	t.Run("--ingress-controller gateway replaces the record, and auto clears it", func(t *testing.T) {
		rec := map[string]string{AnnotationIngressController: frontEndNginx}
		st := hydrateUpgrade(t, UpgradeOptions{Options: Options{IngressController: frontEndGateway}}, rec, bothControllers)
		if st.IngressController != frontEndGateway || !st.IngressControllerChange || st.IngressControllerOverride != frontEndGateway {
			t.Errorf("gateway over a recorded nginx: %s change=%v override=%q", st.IngressController, st.IngressControllerChange, st.IngressControllerOverride)
		}
		st = hydrateUpgrade(t, UpgradeOptions{Options: Options{IngressController: frontEndAuto}}, rec, bothControllers)
		if st.IngressControllerOverride != "" || !st.IngressControllerChange || st.IngressController != frontEndGateway {
			t.Errorf("auto over a recorded nginx: %s change=%v override=%q", st.IngressController, st.IngressControllerChange, st.IngressControllerOverride)
		}
	})
	t.Run("a record this release does not know is refused, naming the way out", func(t *testing.T) {
		provider, _ := Get("local")
		_ = provider
		inst := atVersion(t, "ghcr.io/devicechain-io", "v0.17.0")
		inst.Annotations = map[string]string{AnnotationIngressController: "haproxy"}
		err := applyIngressControllerChoice(&State{Instance: "prod"}, inst, UpgradeOptions{})
		if err == nil || !strings.Contains(err.Error(), "--ingress-controller nginx|gateway|auto") {
			t.Errorf("err = %v", err)
		}
	})
}

// The caller of the install: the State the install runs from carries the flag, and the cluster
// variables it applies carry the consequence.
func TestInstallStateCarriesRemoveLegacyIngressToTheClusterVariables(t *testing.T) {
	cls := nginxClass()
	for _, remove := range []bool{false, true} {
		t.Run(fmt.Sprintf("remove=%v", remove), func(t *testing.T) {
			stubGatewayCertificates(t, readyGatewayCert("dci-prod"), nil)
			st := installState(ClusterBinding{KubeContext: "gke_p_z_c"}, "local", InstallOptions{RemoveLegacyIngress: remove})
			if st.RemoveLegacyIngress != remove {
				t.Fatalf("installState dropped RemoveLegacyIngress: %v", st.RemoveLegacyIngress)
			}
			vars, err := installClusterVars(t.Context(), st, fakeTypedWith(&cls))
			if err != nil {
				t.Fatal(err)
			}
			if got := slices.Contains(vars, "allow_ingress_nginx_removal=true"); got != remove {
				t.Errorf("remove=%v but the cluster variables say removal=%v: %v", remove, got, vars)
			}
		})
	}
}

func TestTheRecordedFrontEndChoiceIsWritten(t *testing.T) {
	// The compare-and-swap writer is exercised against the dynamic fake the other recorders use.
	dyn := declarationClient(declaredInstance(t, "prod", nil))
	inst := struct{ Name string }{"prod"}
	if err := recordIngressController(t.Context(), dyn, inst.Name, frontEndNginx); err != nil {
		t.Fatal(err)
	}
	back, err := readInstanceCR(t.Context(), dyn, inst.Name)
	if err != nil || back.Annotations[AnnotationIngressController] != frontEndNginx {
		t.Fatalf("recorded annotation = %v (%v)", back.Annotations, err)
	}
	if err := recordIngressController(t.Context(), dyn, inst.Name, ""); err != nil {
		t.Fatal(err)
	}
	back, _ = readInstanceCR(t.Context(), dyn, inst.Name)
	if _, still := back.Annotations[AnnotationIngressController]; still {
		t.Error("clearing the choice left the annotation")
	}
}

// ---------------------------------------------------------------------------
// The Gateway's certificate
// ---------------------------------------------------------------------------

func TestGatewaySecretFor(t *testing.T) {
	for _, c := range []struct {
		name   string
		f      frontEndFacts
		legacy bool
		want   string
	}{
		{"a new instance takes the chart's own name", frontEndFacts{}, false, ""},
		{"an instance moving off its Ingress objects takes the new one", frontEndFacts{InstanceHasIngress: true}, true, gatewayCutoverSecret},
		{"and keeps it once they are gone", frontEndFacts{InstanceHasGateway: true, InstanceGatewaySecret: gatewayCutoverSecret}, false, gatewayCutoverSecret},
		{"and through the window", frontEndFacts{InstanceHasGateway: true, InstanceHasIngress: true, InstanceGatewaySecret: gatewayCutoverSecret}, true, gatewayCutoverSecret},
		{"a Gateway already on the Ingress's name moves off it for the window", frontEndFacts{InstanceHasGateway: true, InstanceHasIngress: true, InstanceGatewaySecret: defaultTLSSecret}, true, gatewayCutoverSecret},
		{"a Gateway alone on the chart's name keeps it", frontEndFacts{InstanceHasGateway: true, InstanceGatewaySecret: defaultTLSSecret}, false, ""},
		{"a Gateway on a name of the operator's keeps that", frontEndFacts{InstanceHasGateway: true, InstanceGatewaySecret: "mine"}, false, "mine"},
	} {
		if got := gatewaySecretFor(c.f, c.legacy); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func cert(name, secret string, ownedByGateway bool, ready string) unstructured.Unstructured {
	u := unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "cert-manager.io/v1", "kind": "Certificate",
		"metadata": map[string]interface{}{"name": name, "namespace": "dci-prod"},
		"spec":     map[string]interface{}{"secretName": secret},
	}}
	if ownedByGateway {
		yes := true
		u.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "gateway.networking.k8s.io/v1", Kind: "Gateway",
			Name: instanceGatewayName, Controller: &yes}})
	}
	if ready != "" {
		_ = unstructured.SetNestedSlice(u.Object, []interface{}{map[string]interface{}{"type": "Ready", "status": ready, "message": "waiting"}}, "status", "conditions")
	}
	return u
}

// 🔴 A CERTIFICATE UNDER THE RIGHT NAME IS NOT THE GATEWAY'S. One controlled by the Ingress (or by
// nothing) and Ready is what cert-manager's Gateway support refuses to adopt, and it never comes
// back to it.
func TestGatewayCertificateState(t *testing.T) {
	const secret = "devicechain-tls-gateway"
	ok, why := gatewayCertificateState([]unstructured.Unstructured{cert("gw", secret, true, "True")}, secret)
	if !ok {
		t.Fatalf("a Ready Certificate controlled by the Gateway was refused: %s", why)
	}
	for name, certs := range map[string][]unstructured.Unstructured{
		"none at all":               nil,
		"another Secret's":          {cert("other", "something-else", true, "True")},
		"not controlled by it":      {cert("devicechain-tls", secret, false, "True")},
		"controlled but not Ready":  {cert("gw", secret, true, "False")},
		"controlled, no status yet": {cert("gw", secret, true, "")},
	} {
		if ok, why := gatewayCertificateState(certs, secret); ok || why == "" {
			t.Errorf("%s: accepted, or refused without a reason (ok=%v why=%q)", name, ok, why)
		}
	}
	if _, why := gatewayCertificateState([]unstructured.Unstructured{cert("devicechain-tls", secret, false, "True")}, secret); !strings.Contains(why, "delete certificate") {
		t.Errorf("a foreign Certificate's refusal does not say how to clear it: %s", why)
	}
}

func TestWaitForGatewayCertificate(t *testing.T) {
	prevT, prevP := gatewayCertTimeout, gatewayCertPoll
	gatewayCertTimeout, gatewayCertPoll = 50*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { gatewayCertTimeout, gatewayCertPoll = prevT, prevP })

	st := compactState(false)
	st.Instance = "prod"
	st.IngressController, st.GatewayTLSSecret = frontEndGateway, "devicechain-tls-gateway"
	issued := map[string]string{"cert-manager.io/cluster-issuer": "letsencrypt"}
	gw := gwObj("dci-prod", issued, "devicechain-tls-gateway")

	good := cert("gw", "devicechain-tls-gateway", true, "True")
	if err := waitForGatewayCertificate(t.Context(), newFakeCertDynamic(gw, &good), st); err != nil {
		t.Fatalf("a Ready Gateway Certificate was waited on in vain: %v", err)
	}
	// Negative control: the same cluster, with the Certificate the Ingress owns instead.
	foreign := cert("devicechain-tls", "devicechain-tls-gateway", false, "True")
	err := waitForGatewayCertificate(t.Context(), newFakeCertDynamic(gw, &foreign), st)
	if err == nil || !strings.Contains(err.Error(), "not controlled by the Gateway") {
		t.Fatalf("a Certificate the Gateway does not control passed the gate: %v", err)
	}
	// Nothing expected: the Gateway asks cert-manager for nothing, names no Secret, is not
	// there, or is not the front end.
	for name, tc := range map[string]struct {
		objs   []*unstructured.Unstructured
		mutate func(*State)
	}{
		"an operator-supplied Secret": {objs: []*unstructured.Unstructured{gwObj("dci-prod", nil, "devicechain-tls-gateway")}},
		"no https Secret":             {objs: []*unstructured.Unstructured{gwObj("dci-prod", issued, "")}},
		"no Gateway":                  {},
		"nginx":                       {objs: []*unstructured.Unstructured{gw}, mutate: func(s *State) { s.IngressController = frontEndNginx }},
	} {
		c := *st
		if tc.mutate != nil {
			tc.mutate(&c)
		}
		if err := waitForGatewayCertificate(t.Context(), newFakeCertDynamic(tc.objs...), &c); err != nil {
			t.Errorf("%s: waited for a certificate nothing was to issue: %v", name, err)
		}
	}
}

// ---------------------------------------------------------------------------
// A re-apply that would drop Traefik is refused before it is applied
// ---------------------------------------------------------------------------

type fakeClusterPlanner struct {
	state *tfjson.State
	plan  *tfjson.Plan
	plans int
}

func (f *fakeClusterPlanner) Show(context.Context, ...tfexec.ShowOption) (*tfjson.State, error) {
	return f.state, nil
}

func (f *fakeClusterPlanner) Plan(_ context.Context, opts ...tfexec.PlanOption) (bool, error) {
	f.plans++
	return true, nil
}

func (f *fakeClusterPlanner) ShowPlanFile(context.Context, string, ...tfexec.ShowOption) (*tfjson.Plan, error) {
	return f.plan, nil
}

// newFakeCertDynamic is a dynamic client holding the Certificates given.
func newFakeCertDynamic(objs ...*unstructured.Unstructured) dynamic.Interface {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{certificateGVR: "CertificateList", gatewayGVR: "GatewayList"})
	// Created by hand: the tracker would guess "gatewaies" for a Gateway.
	for _, o := range objs {
		gvr := certificateGVR
		if o.GetKind() == "Gateway" {
			gvr = gatewayGVR
		}
		if _, err := dyn.Resource(gvr).Namespace(o.GetNamespace()).Create(context.Background(), o, metav1.CreateOptions{}); err != nil {
			panic(err)
		}
	}
	return dyn
}

func stateWithTraefik() *tfjson.State {
	return &tfjson.State{Values: &tfjson.StateValues{RootModule: &tfjson.StateModule{
		ChildModules: []*tfjson.StateModule{{Resources: []*tfjson.StateResource{{Address: traefikAddress}}}},
	}}}
}

func planDeleting(addresses ...string) *tfjson.Plan {
	p := &tfjson.Plan{}
	for _, a := range addresses {
		p.ResourceChanges = append(p.ResourceChanges, &tfjson.ResourceChange{Address: a,
			Change: &tfjson.Change{Actions: tfjson.Actions{tfjson.ActionDelete}}})
	}
	return p
}

func TestTraefikDeletions(t *testing.T) {
	got := traefikDeletions(planDeleting("module.traefik[0].helm_release.traefik", "module.cnpg[0].helm_release.cnpg", "module.gateway_api_crds[0].x"))
	if !slices.Equal(got, []string{"module.traefik[0].helm_release.traefik", "module.gateway_api_crds[0].x"}) {
		t.Errorf("deletions = %v", got)
	}
	update := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{{Address: "module.traefik[0].helm_release.traefik",
		Change: &tfjson.Change{Actions: tfjson.Actions{tfjson.ActionUpdate}}}}}
	if got := traefikDeletions(update); len(got) != 0 {
		t.Errorf("an update of Traefik was counted as dropping it: %v", got)
	}
	replace := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{{Address: "module.traefik[0].helm_release.traefik",
		Change: &tfjson.Change{Actions: tfjson.Actions{tfjson.ActionDelete, tfjson.ActionCreate}}}}}
	if got := traefikDeletions(replace); len(got) != 1 {
		t.Errorf("a replacement of Traefik (a window with no controller) was not counted: %v", got)
	}
}

func TestADropOfTraefikIsRefusedBeforeTheApply(t *testing.T) {
	// A root whose state does not hold Traefik is never planned for the check: the fresh install pays nothing.
	f := &fakeClusterPlanner{state: &tfjson.State{}}
	if err := refuseAnApplyThatDropsTraefik(t.Context(), f, t.TempDir(), []string{"enable_ingress_nginx=false"}); err != nil {
		t.Fatal(err)
	}
	if f.plans != 0 {
		t.Errorf("a fresh cluster was planned %d time(s) for a check that cannot fire", f.plans)
	}
	// A plain install that keeps ingress-nginx is not checked either.
	f = &fakeClusterPlanner{state: stateWithTraefik()}
	if err := refuseAnApplyThatDropsTraefik(t.Context(), f, t.TempDir(), []string{"enable_traefik=true"}); err != nil || f.plans != 0 {
		t.Errorf("err=%v plans=%d", err, f.plans)
	}
	// Traefik in the state, and a plan that removes it: refused, naming what goes, before any apply.
	f = &fakeClusterPlanner{state: stateWithTraefik(), plan: planDeleting("module.traefik[0].helm_release.traefik")}
	err := refuseAnApplyThatDropsTraefik(t.Context(), f, t.TempDir(), []string{"enable_ingress_nginx=false"})
	if err == nil || !strings.Contains(err.Error(), "module.traefik[0].helm_release.traefik") || f.plans != 1 {
		t.Fatalf("a plan dropping the only controller was not refused (plans=%d): %v", f.plans, err)
	}
	// Control: the same state and a plan that leaves it alone passes.
	f = &fakeClusterPlanner{state: stateWithTraefik(), plan: planDeleting("module.nats[0].x")}
	if err := refuseAnApplyThatDropsTraefik(t.Context(), f, t.TempDir(), []string{"enable_ingress_nginx=false"}); err != nil {
		t.Errorf("a plan that keeps Traefik was refused: %v", err)
	}
}
