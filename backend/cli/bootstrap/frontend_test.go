// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-exec/tfexec"
	"helm.sh/helm/v3/pkg/chart"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/yaml"
)

func fakeTypedWith(objs ...runtime.Object) kubernetes.Interface {
	return fake.NewSimpleClientset(objs...)
}

func nginxClass() networkingv1.IngressClass {
	return networkingv1.IngressClass{
		ObjectMeta: metav1.ObjectMeta{Name: nginxIngressClass},
		Spec:       networkingv1.IngressClassSpec{Controller: nginxIngressController},
	}
}

func classedIngress(ns, name, class string) networkingv1.Ingress {
	return networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       networkingv1.IngressSpec{IngressClassName: &class},
	}
}

// ---------------------------------------------------------------------------
// Which front end an instance renders
// ---------------------------------------------------------------------------

// The whole decision table, one row per rule. The cluster facts are the inputs; nothing is
// recorded anywhere else.
func TestChooseFrontEnd(t *testing.T) {
	traefik := clusterFrontEnd{GatewayClass: true}
	both := clusterFrontEnd{GatewayClass: true, NginxClass: true}
	nginxOnly := clusterFrontEnd{NginxClass: true}

	cases := []struct {
		name     string
		facts    frontEndFacts
		local    bool
		override string
		drop     bool
		want     frontEndChoice // Why is not compared
		wantErr  string
	}{
		{name: "a cluster install of this release has not reached: nginx", facts: frontEndFacts{clusterFrontEnd: nginxOnly},
			want: frontEndChoice{Controller: frontEndNginx}},
		{name: "a cluster with no controller at all is refused, not defaulted", facts: frontEndFacts{},
			wantErr: "no ingress controller"},
		{name: "Traefik alone: the Gateway", facts: frontEndFacts{clusterFrontEnd: traefik},
			want: frontEndChoice{Controller: frontEndGateway}},
		{name: "Traefik alone, local: the Gateway", facts: frontEndFacts{clusterFrontEnd: traefik}, local: true,
			want: frontEndChoice{Controller: frontEndGateway}},
		{name: "both, an instance with Ingress objects: the Gateway with the Ingress beside it",
			facts: frontEndFacts{clusterFrontEnd: both, InstanceHasIngress: true},
			want:  frontEndChoice{Controller: frontEndGateway, Legacy: true}},
		{name: "both, an instance with Ingress objects, local: the same (nginx keeps serving the host)",
			facts: frontEndFacts{clusterFrontEnd: both, InstanceHasIngress: true}, local: true,
			want: frontEndChoice{Controller: frontEndGateway, Legacy: true}},
		{name: "both, --drop-legacy-ingress: the Gateway alone",
			facts: frontEndFacts{clusterFrontEnd: both, InstanceHasIngress: true}, drop: true,
			want: frontEndChoice{Controller: frontEndGateway}},
		{name: "both, an instance already on the Gateway with no Ingress left: the Gateway alone",
			facts: frontEndFacts{clusterFrontEnd: both, InstanceHasGateway: true},
			want:  frontEndChoice{Controller: frontEndGateway}},
		{name: "both, mid-cutover re-run: still both",
			facts: frontEndFacts{clusterFrontEnd: both, InstanceHasGateway: true, InstanceHasIngress: true},
			want:  frontEndChoice{Controller: frontEndGateway, Legacy: true}},
		{name: "both, a NEW instance on a cloud: the Gateway (Traefik has its own address)",
			facts: frontEndFacts{clusterFrontEnd: both},
			want:  frontEndChoice{Controller: frontEndGateway}},
		{name: "both, a NEW instance on a local cluster: nginx (the node's ports are still its)",
			facts: frontEndFacts{clusterFrontEnd: both}, local: true,
			want: frontEndChoice{Controller: frontEndNginx}},
		{name: "both, a NEW local instance, --ingress-controller gateway: the Gateway anyway",
			facts: frontEndFacts{clusterFrontEnd: both}, local: true, override: frontEndGateway,
			want: frontEndChoice{Controller: frontEndGateway}},
		{name: "--ingress-controller nginx is the way back while ingress-nginx is there",
			facts: frontEndFacts{clusterFrontEnd: both, InstanceHasGateway: true}, override: frontEndNginx,
			want: frontEndChoice{Controller: frontEndNginx}},
		{name: "--ingress-controller nginx with Traefik alone is refused: nothing would serve it",
			facts: frontEndFacts{clusterFrontEnd: traefik, InstanceHasGateway: true}, override: frontEndNginx,
			wantErr: "needs ingress-nginx"},
		{name: "--ingress-controller nginx on a cluster with no controller is refused",
			facts: frontEndFacts{}, override: frontEndNginx, wantErr: "no ingress controller"},
		{name: "an instance on a Gateway is not quietly moved to Ingress when Traefik is gone",
			facts: frontEndFacts{clusterFrontEnd: nginxOnly, InstanceHasGateway: true}, wantErr: "no GatewayClass"},
		{name: "...but --ingress-controller nginx says so, and is allowed",
			facts: frontEndFacts{clusterFrontEnd: nginxOnly, InstanceHasGateway: true}, override: frontEndNginx,
			want: frontEndChoice{Controller: frontEndNginx}},
		{name: "--ingress-controller gateway with no Gateway API controller on the cluster is refused",
			facts: frontEndFacts{clusterFrontEnd: nginxOnly, InstanceHasIngress: true}, override: frontEndGateway,
			wantErr: "dcctl install"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := chooseFrontEnd(c.facts, c.local, c.override, c.drop)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want one naming %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Controller != c.want.Controller || got.Legacy != c.want.Legacy {
				t.Errorf("got %s legacy=%v (%s), want %s legacy=%v", got.Controller, got.Legacy, got.Why,
					c.want.Controller, c.want.Legacy)
			}
			if got.Why == "" {
				t.Error("a choice that does not say why is one the operator cannot check")
			}
		})
	}
}

func TestValidateFrontEnd(t *testing.T) {
	for _, ok := range []string{"", "nginx", "gateway"} {
		if err := ValidateFrontEnd(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	if err := ValidateFrontEnd("traefik"); err == nil {
		t.Error("an unknown front end was accepted")
	}
}

// The facts are read from objects, and the three ways an Ingress can name its class all count.
func TestFrontEndFactsFromTheObjects(t *testing.T) {
	annotated := networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Namespace: "legacy", Name: "a",
		Annotations: map[string]string{legacyIngressClassAnnotation: nginxIngressClass}}}
	byDefault := networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Namespace: "defaulted", Name: "b"}}
	other := classedIngress("elsewhere", "c", "traefik")
	own := classedIngress(InstanceNamespace("alpha"), "devicechain-api", nginxIngressClass)
	grafana := classedIngress(monitoringNamespace, "grafana", nginxIngressClass)
	grafana.Labels = map[string]string{"app.kubernetes.io/name": "grafana", "app.kubernetes.io/instance": monitoringReleaseName}

	t.Run("an Ingress with no class is nginx's only while nginx is the default class", func(t *testing.T) {
		notDefault := frontEndFactsFrom([]networkingv1.IngressClass{nginxClass()},
			[]networkingv1.Ingress{byDefault}, true, nil, "alpha", true)
		if len(notDefault.NginxIngresses) != 0 {
			t.Errorf("an Ingress naming no class was counted while nginx is not the default: %v", notDefault.NginxIngresses)
		}
		def := nginxClass()
		def.Annotations = map[string]string{defaultIngressClassAnnotation: "true"}
		isDefault := frontEndFactsFrom([]networkingv1.IngressClass{def}, []networkingv1.Ingress{byDefault}, true, nil, "alpha", true)
		if !slices.Equal(isDefault.NginxIngresses, []string{"defaulted/b"}) {
			t.Errorf("with nginx the default class, an Ingress naming none is nginx's: %v", isDefault.NginxIngresses)
		}
	})

	f := frontEndFactsFrom([]networkingv1.IngressClass{nginxClass()},
		[]networkingv1.Ingress{annotated, other, own, grafana}, true, &instanceGatewayFact{}, "alpha", true)
	if !f.NginxClass || !f.GatewayClass || !f.InstanceHasGateway {
		t.Errorf("facts = %+v", f)
	}
	if !f.InstanceHasIngress {
		t.Error("the instance's own nginx Ingress was not seen")
	}
	// The legacy annotation counts; another class does not; Grafana's own Ingress, which the
	// cluster root moves in the same apply as the removal, does not block it.
	if want := []string{"dci-alpha/devicechain-api", "legacy/a"}; !slices.Equal(f.NginxIngresses, want) {
		t.Errorf("NginxIngresses = %v, want %v", f.NginxIngresses, want)
	}
	// An IngressClass served by another controller is not nginx.
	foreign := nginxClass()
	foreign.Spec.Controller = "example.com/other"
	if frontEndFactsFrom([]networkingv1.IngressClass{foreign}, nil, true, nil, "", true).NginxClass {
		t.Error("an IngressClass of another controller was taken for ingress-nginx")
	}
}

// settleFrontEnd records the choice on the State and what the chart is then handed.
func TestSettleFrontEndFeedsTheChartValues(t *testing.T) {
	stub := func(f frontEndFacts) {
		prev := readFrontEndFacts
		t.Cleanup(func() { readFrontEndFacts = prev })
		readFrontEndFacts = func(context.Context, string, string) (frontEndFacts, error) { return f, nil }
	}
	both := clusterFrontEnd{GatewayClass: true, NginxClass: true}

	t.Run("a cutover renders the Gateway with the Ingress beside it", func(t *testing.T) {
		stub(frontEndFacts{clusterFrontEnd: both, InstanceHasIngress: true})
		st := compactState(false)
		st.Values["ingressHost"] = "iot.example.com"
		if err := settleFrontEnd(t.Context(), st); err != nil {
			t.Fatal(err)
		}
		ing := ingressValues(st)
		legacy, _ := ing["legacy"].(map[string]interface{})
		if ing["controller"] != frontEndGateway || legacy["enabled"] != true {
			t.Errorf("ingress values = %v", ing)
		}
	})
	t.Run("an unsettled State renders the Gateway, the default", func(t *testing.T) {
		ing := ingressValues(&State{Values: map[string]string{"ingressHost": "h"}})
		if ing["controller"] != frontEndGateway {
			t.Errorf("ingress values = %v", ing)
		}
		if _, ok := ing["legacy"]; ok {
			t.Error("an unsettled State asked for the cutover window")
		}
	})
	t.Run("nginx names itself, and the chart is never left to its own default", func(t *testing.T) {
		stub(frontEndFacts{clusterFrontEnd: clusterFrontEnd{NginxClass: true}})
		st := compactState(false)
		if err := settleFrontEnd(t.Context(), st); err != nil {
			t.Fatal(err)
		}
		if ing := ingressValues(st); ing["controller"] != frontEndNginx {
			t.Errorf("ingress values = %v", ing)
		}
	})
	t.Run("a refused choice stops the run", func(t *testing.T) {
		stub(frontEndFacts{clusterFrontEnd: clusterFrontEnd{NginxClass: true}})
		st := compactState(false)
		st.IngressControllerOverride = frontEndGateway
		if err := settleFrontEnd(t.Context(), st); err == nil {
			t.Fatal("--ingress-controller gateway was accepted on a cluster with no Gateway API controller")
		}
	})
	t.Run("an unreadable cluster stops a run and not a rehearsal", func(t *testing.T) {
		prev := readFrontEndFacts
		t.Cleanup(func() { readFrontEndFacts = prev })
		readFrontEndFacts = func(context.Context, string, string) (frontEndFacts, error) {
			return frontEndFacts{}, errors.New("no route to host")
		}
		if err := settleFrontEnd(t.Context(), compactState(false)); err == nil {
			t.Error("a run went on without knowing what the cluster serves")
		}
		dry := compactState(false)
		dry.DryRun = true
		if err := settleFrontEnd(t.Context(), dry); err != nil {
			t.Errorf("a rehearsal against a cluster that is not there was refused: %v", err)
		}
		if dry.IngressController != frontEndGateway {
			t.Errorf("a rehearsal assumed %q, want the new-install default", dry.IngressController)
		}
	})
}

// ---------------------------------------------------------------------------
// The overlay: nginx-only keys are retired when there is no Ingress to apply them to
// ---------------------------------------------------------------------------

func TestNginxOnlyOverlayKeysAreRetiredUnderTheGateway(t *testing.T) {
	overlay := func() map[string]interface{} {
		return map[string]interface{}{
			"replicas": 2,
			"ingress": map[string]interface{}{
				"uploadMaxBodySize": "2g",
				"className":         "nginx",
				"annotations":       map[string]interface{}{"nginx.ingress.kubernetes.io/proxy-read-timeout": "120"},
			},
		}
	}
	t.Run("an overlay the instance already recorded is dropped from the render, kept in the record", func(t *testing.T) {
		st := compactState(false)
		st.IngressController = frontEndGateway
		st.ValuesOverlay, st.ValuesOverlayRecorded = overlay(), overlay()
		if err := checkNginxOnlyOverlay(st); err != nil {
			t.Fatalf("a recorded overlay was refused: %v", err)
		}
		vals := authoringValues(st)
		ing := vals["ingress"].(map[string]interface{})
		for _, k := range []string{"uploadMaxBodySize", "className", "annotations"} {
			if _, ok := ing[k]; ok {
				t.Errorf("ingress.%s still reaches the chart under the Gateway", k)
			}
		}
		if vals["replicas"] != 2 {
			t.Errorf("an ordinary overlay key was dropped with them: replicas = %v", vals["replicas"])
		}
		if _, ok := st.ValuesOverlay["ingress"].(map[string]interface{})["uploadMaxBodySize"]; !ok {
			t.Error("the overlay in the State, which is what is recorded, lost the key; --ingress-controller nginx could not bring it back")
		}
	})
	t.Run("a value typed on this run is refused, not silently dropped", func(t *testing.T) {
		st := compactState(false)
		st.IngressController = frontEndGateway
		st.ValuesOverlay = overlay() // nothing recorded: every key is new
		err := checkNginxOnlyOverlay(st)
		if err == nil || !strings.Contains(err.Error(), "ingress.uploadMaxBodySize") {
			t.Fatalf("a new uploadMaxBodySize under the Gateway was not refused: %v", err)
		}
	})
	t.Run("a changed value is a new value", func(t *testing.T) {
		st := compactState(false)
		st.IngressController = frontEndGateway
		st.ValuesOverlay, st.ValuesOverlayRecorded = overlay(), overlay()
		st.ValuesOverlay["ingress"].(map[string]interface{})["uploadMaxBodySize"] = "4g"
		if err := checkNginxOnlyOverlay(st); err == nil {
			t.Error("an uploadMaxBodySize that CHANGED under the Gateway was accepted")
		}
	})
	t.Run("control: the Ingress front end, and the cutover window, still apply them", func(t *testing.T) {
		for _, mutate := range []func(*State){
			func(st *State) { st.IngressController = frontEndNginx },
			func(st *State) { st.IngressController, st.LegacyIngress = frontEndGateway, true },
		} {
			st := compactState(false)
			mutate(st)
			st.ValuesOverlay = overlay()
			if err := checkNginxOnlyOverlay(st); err != nil {
				t.Errorf("%s legacy=%v: %v", st.IngressController, st.LegacyIngress, err)
			}
			ing := authoringValues(st)["ingress"].(map[string]interface{})
			if ing["uploadMaxBodySize"] != "2g" {
				t.Errorf("%s legacy=%v: uploadMaxBodySize = %v, want the operator's 2g", st.IngressController, st.LegacyIngress, ing["uploadMaxBodySize"])
			}
		}
	})
}

// The overlay allowlist for the new keys: the Gateway's own are tunable, the front-end selector and the
// cutover window are dcctl's.
func TestOverlayAllowlistForTheGatewayKeys(t *testing.T) {
	if why := refuseOverlayKey([]string{"ingress", "gateway", "className"}); why != "" {
		t.Errorf("ingress.gateway.className refused: %s", why)
	}
	for _, path := range [][]string{
		{"ingress", "controller"},
		{"ingress", "legacy", "enabled"},
		{"ingress", "gateway", "tlsSecretName"},
		{"ingress", "gateway", "annotations", "cert-manager.io/issuer"},
		{"ingress", "gateway", "annotations", "Cert-Manager.io/cluster-issuer"},
	} {
		if refuseOverlayKey(path) == "" {
			t.Errorf("%s was accepted in an overlay; dcctl owns it", strings.Join(path, "."))
		}
	}
	if why := refuseOverlayKey([]string{"ingress", "gateway", "annotations", "example.com/x"}); why != "" {
		t.Errorf("an ordinary Gateway annotation was refused: %s", why)
	}
}

// ---------------------------------------------------------------------------
// The cluster root: which controllers it runs, and the removal preflight
// ---------------------------------------------------------------------------

func TestPlanClusterFrontEnd(t *testing.T) {
	has := func(p clusterFrontEndPlan, v string) bool { return slices.Contains(p.Vars, v) }
	none := clusterFrontEnd{}
	nginx := clusterFrontEnd{NginxClass: true}

	t.Run("a fresh cloud cluster gets Traefik and never the retired controller", func(t *testing.T) {
		p, err := planClusterFrontEnd(none, false, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range []string{"enable_traefik=true", "enable_gateway_api_crds=true", "enable_ingress_nginx=false"} {
			if !has(p, v) {
				t.Errorf("missing %s: %v", v, p.Vars)
			}
		}
		if has(p, "traefik_use_host_port=true") || has(p, "ingress_use_host_port=true") {
			t.Errorf("a cloud cluster was given host ports: %v", p.Vars)
		}
	})
	t.Run("a fresh local cluster gives Traefik the host ports", func(t *testing.T) {
		p, _ := planClusterFrontEnd(none, true, false)
		if !has(p, "traefik_use_host_port=true") || has(p, "ingress_use_host_port=true") || !has(p, "enable_ingress_nginx=false") {
			t.Errorf("vars = %v", p.Vars)
		}
	})
	t.Run("a cluster with ingress-nginx keeps it and gains Traefik beside it", func(t *testing.T) {
		p, err := planClusterFrontEnd(nginx, false, false)
		if err != nil {
			t.Fatal(err)
		}
		if has(p, "enable_ingress_nginx=false") || has(p, "allow_ingress_nginx_removal=true") {
			t.Errorf("a plain install planned to remove the controller that is serving: %v", p.Vars)
		}
		// The cluster root's default is OFF (a new cluster never gets the retired controller), so a
		// cluster that has it keeps it only by being told to. Absent, the plan would be a removal.
		if !has(p, "enable_ingress_nginx=true") {
			t.Errorf("a plain install over ingress-nginx did not say to keep it, and the root's default is off: %v", p.Vars)
		}
		if !has(p, "enable_traefik=true") {
			t.Errorf("Traefik was not installed beside it: %v", p.Vars)
		}
	})
	t.Run("on a local cluster ingress-nginx keeps the host ports until it is removed", func(t *testing.T) {
		p, _ := planClusterFrontEnd(nginx, true, false)
		if !has(p, "ingress_use_host_port=true") || has(p, "traefik_use_host_port=true") {
			t.Errorf("vars = %v: exactly one controller may hold the node's 80/443", p.Vars)
		}
	})
	t.Run("removal with no Ingress left removes it, and a local Traefik takes the ports in the same apply", func(t *testing.T) {
		p, err := planClusterFrontEnd(nginx, true, true)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range []string{"enable_ingress_nginx=false", "allow_ingress_nginx_removal=true", "traefik_use_host_port=true"} {
			if !has(p, v) {
				t.Errorf("missing %s: %v", v, p.Vars)
			}
		}
		if has(p, "ingress_use_host_port=true") {
			t.Errorf("both controllers asked for the host ports: %v", p.Vars)
		}
		cloud, _ := planClusterFrontEnd(nginx, false, true)
		if has(cloud, "traefik_use_host_port=true") || !has(cloud, "allow_ingress_nginx_removal=true") {
			t.Errorf("cloud removal vars = %v", cloud.Vars)
		}
	})
	t.Run("asking to remove what is not there is a note, not a failure", func(t *testing.T) {
		p, err := planClusterFrontEnd(none, false, true)
		if err != nil || len(p.Notes) == 0 {
			t.Errorf("err = %v, notes = %v", err, p.Notes)
		}
	})
}

// 🔴 THE PREFLIGHT, WITH ITS NEGATIVE CONTROL. The same cluster is planned twice, the second
// time with one Ingress of nginx's class on it: the first is allowed and the second is
// refused, naming the Ingress. Without the pair, "refused" could be any refusal.
func TestRemovalIsRefusedWhileAnIngressOfTheNginxClassRemains(t *testing.T) {
	classes := []networkingv1.IngressClass{nginxClass()}
	clean := frontEndFactsFrom(classes, []networkingv1.Ingress{
		classedIngress("dci-a", "devicechain-api", "traefik"), // another controller's: not a blocker
	}, true, nil, "", true)
	if _, err := planClusterFrontEnd(clean.clusterFrontEnd, false, true); err != nil {
		t.Fatalf("the control (no Ingress of nginx's class) was refused: %v", err)
	}

	dirty := frontEndFactsFrom(classes, []networkingv1.Ingress{
		classedIngress("dci-a", "devicechain-api", "traefik"),
		classedIngress("dci-b", "devicechain-api", nginxIngressClass),
	}, true, nil, "", true)
	_, err := planClusterFrontEnd(dirty.clusterFrontEnd, false, true)
	var inUse *ErrLegacyIngressInUse
	if !errors.As(err, &inUse) {
		t.Fatalf("removal with an nginx Ingress on the cluster returned %v, want ErrLegacyIngressInUse", err)
	}
	if !slices.Equal(inUse.Ingresses, []string{"dci-b/devicechain-api"}) {
		t.Errorf("the refusal names %v, want exactly the Ingress that blocks it", inUse.Ingresses)
	}
	for _, want := range []string{"dci-b/devicechain-api", "--drop-legacy-ingress", "Nothing has been changed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
	// And the refusal is about REMOVAL: the same dirty cluster takes a plain install.
	if _, err := planClusterFrontEnd(dirty.clusterFrontEnd, false, false); err != nil {
		t.Errorf("a plain install over a cluster that still serves Ingresses was refused: %v", err)
	}
	// A long list is cut, with the count kept.
	var many []networkingv1.Ingress
	for i := 0; i < 20; i++ {
		many = append(many, classedIngress(fmt.Sprintf("dci-%02d", i), "devicechain-api", nginxIngressClass))
	}
	big := frontEndFactsFrom(classes, many, true, nil, "", true)
	_, err = planClusterFrontEnd(big.clusterFrontEnd, false, true)
	if err == nil || !strings.Contains(err.Error(), "20 Ingress") || !strings.Contains(err.Error(), "and 8 more") {
		t.Errorf("a long refusal does not keep its count: %v", err)
	}
}

// The install builds the plan from the cluster it is handed, through the one producer of the
// cluster variables.
func TestInstallClusterVarsCarryTheFrontEndPlan(t *testing.T) {
	st := &State{KubeContext: "gke_p_z_c", RemoveLegacyIngress: true}
	// A cluster with an nginx Ingress on it, as a fake API server.
	cls, blocker := nginxClass(), classedIngress("dci-b", "devicechain-api", nginxIngressClass)
	typed := fakeTypedWith(&cls, &blocker)
	if _, err := installClusterVars(t.Context(), st, typed); err == nil {
		t.Fatal("--remove-legacy-ingress over a cluster serving an nginx Ingress was not refused")
	}
	st = &State{KubeContext: "gke_p_z_c"}
	vars, err := installClusterVars(t.Context(), st, typed)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(vars, "enable_traefik=true") || slices.Contains(vars, "enable_ingress_nginx=false") {
		t.Errorf("a plain install over nginx planned %v", vars)
	}
	fresh, err := installClusterVars(t.Context(), &State{KubeContext: "gke_p_z_c"}, fakeTypedWith())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(fresh, "enable_ingress_nginx=false") {
		t.Errorf("a fresh cluster planned %v", fresh)
	}
	// With no client there is nothing to read, and nothing is decided.
	none, err := installClusterVars(t.Context(), &State{KubeContext: "gke_p_z_c"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range none {
		if strings.HasPrefix(v, "enable_ingress_nginx=") || strings.HasPrefix(v, "enable_traefik=") {
			t.Errorf("%q: the controllers were planned from nothing", v)
		}
	}
}

// ---------------------------------------------------------------------------
// The chart, rendered, for every profile and every front end
// ---------------------------------------------------------------------------

type frontEndVariant struct {
	name       string
	controller string
	legacy     bool
}

var frontEndVariants = []frontEndVariant{
	{"nginx", frontEndNginx, false},
	{"gateway", frontEndGateway, false},
	{"cutover (gateway + legacy Ingress)", frontEndGateway, true},
}

// kinds counts the routing objects of a manifest and returns each one's certificate Secret.
type routingKinds struct {
	ingresses, gateways, routes int
	ingressSecrets              []string
	gatewaySecrets              []string
	hosts                       map[string]bool
}

func routingKindsOf(t *testing.T, manifest string) routingKinds {
	t.Helper()
	out := routingKinds{hosts: map[string]bool{}}
	for _, doc := range strings.Split(manifest, "\n---") {
		var obj struct {
			Kind string `json:"kind"`
			Spec struct {
				TLS []struct {
					Hosts      []string `json:"hosts"`
					SecretName string   `json:"secretName"`
				} `json:"tls"`
				Rules     []struct{ Host string }
				Hostnames []string `json:"hostnames"`
				Listeners []struct {
					Hostname string `json:"hostname"`
					TLS      struct {
						CertificateRefs []struct{ Name string } `json:"certificateRefs"`
					} `json:"tls"`
				} `json:"listeners"`
			} `json:"spec"`
		}
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			continue
		}
		switch obj.Kind {
		case "Ingress":
			out.ingresses++
			for _, tl := range obj.Spec.TLS {
				out.ingressSecrets = append(out.ingressSecrets, tl.SecretName)
			}
			for _, r := range obj.Spec.Rules {
				out.hosts[r.Host] = true
			}
		case "Gateway":
			out.gateways++
			for _, l := range obj.Spec.Listeners {
				out.hosts[l.Hostname] = true
				for _, ref := range l.TLS.CertificateRefs {
					out.gatewaySecrets = append(out.gatewaySecrets, ref.Name)
				}
			}
		case "HTTPRoute":
			out.routes++
		}
	}
	return out
}

// Every install profile × every front end, rendered through the production path (helmValues,
// composed into the instance configuration, with the chart's schema), and held to what each
// front end promises.
func TestEveryProfileRendersEveryFrontEnd(t *testing.T) {
	t.Parallel()
	ch := chartForTest(t)
	for i, p := range renderProfiles {
		for _, v := range frontEndVariants {
			t.Run(p.name+"/"+v.name, func(t *testing.T) {
				t.Parallel()
				vals, err := frontEndVariantValues(t.Context(), ch, p, i, v)
				if err != nil {
					t.Fatal(err)
				}
				manifest, err := renderChartClientSide(t.Context(), ch, vals)
				if err != nil {
					t.Fatalf("rendering: %v", err)
				}
				k := routingKindsOf(t, manifest)
				switch {
				case v.controller == frontEndNginx:
					if k.ingresses == 0 || k.gateways != 0 || k.routes != 0 {
						t.Errorf("nginx rendered %+v", k)
					}
				case !v.legacy:
					if k.ingresses != 0 || k.gateways != 1 || k.routes == 0 {
						t.Errorf("the Gateway rendered %+v", k)
					}
				default:
					if k.ingresses == 0 || k.gateways != 1 || k.routes == 0 {
						t.Errorf("the cutover rendered %+v, want both front ends", k)
					}
					// Never two shims on one Secret, and one host for both.
					if len(k.gatewaySecrets) > 0 && len(k.ingressSecrets) > 0 {
						for _, g := range k.gatewaySecrets {
							for _, ing := range k.ingressSecrets {
								if g == ing {
									t.Errorf("the Gateway and an Ingress both name Secret %q: two cert-manager shims would fight over it", g)
								}
							}
						}
					}
					if len(k.hosts) != 1 {
						t.Errorf("the two front ends serve %v, want one host", k.hosts)
					}
				}
			})
		}
	}
}

// frontEndVariantValues is the install values of renderProfiles row i under front end variant v.
func frontEndVariantValues(ctx context.Context, ch *chart.Chart, p renderProfile, i int, v frontEndVariant) (map[string]interface{}, error) {
	st := p.st(profileInstance(i))
	st.IngressController, st.LegacyIngress = v.controller, v.legacy
	if v.legacy {
		st.GatewayTLSSecret = gatewayCutoverSecret
	}
	vals, _, err := releaseValues(ctx, ch, st, nil)
	if err != nil {
		return nil, fmt.Errorf("composing the release values: %w", err)
	}
	if p.overrides != nil {
		ov := p.overrides()
		// A row that names its own front end is a row about that front end.
		if ing, ok := ov["ingress"].(map[string]interface{}); ok {
			delete(ing, "controller")
		}
		vals = mergeFrontEndOverride(ov, vals)
	}
	return vals, nil
}

// mergeFrontEndOverride merges overrides over base the way profileValues does.
func mergeFrontEndOverride(override, base map[string]interface{}) map[string]interface{} {
	out := copyValues(base)
	var walk func(dst, src map[string]interface{})
	walk = func(dst, src map[string]interface{}) {
		for k, v := range src {
			if sm, ok := v.(map[string]interface{}); ok {
				if dm, ok := dst[k].(map[string]interface{}); ok {
					walk(dm, sm)
					continue
				}
			}
			dst[k] = copyValue(v)
		}
	}
	walk(out, override)
	return out
}

// The chart refuses the combinations that would make the cutover window misbehave, by message.
func TestTheChartRefusesAMisbuiltCutover(t *testing.T) {
	t.Parallel()
	ch := chartForTest(t)
	render := func(mutate func(ing map[string]interface{})) error {
		st := compactState(false)
		st.IngressController, st.LegacyIngress, st.GatewayTLSSecret = frontEndGateway, true, gatewayCutoverSecret
		vals, _, err := releaseValues(t.Context(), ch, st, nil)
		if err != nil {
			t.Fatal(err)
		}
		mutate(vals["ingress"].(map[string]interface{}))
		_, err = renderChartClientSide(t.Context(), ch, vals)
		return err
	}
	if err := render(func(map[string]interface{}) {}); err != nil {
		t.Fatalf("the control (a well-formed cutover) did not render: %v", err)
	}
	// The cutover renders with the Gateway on its own Secret; naming the Ingress objects' one is refused.
	err := render(func(ing map[string]interface{}) {
		ing["gateway"] = map[string]interface{}{"tlsSecretName": "devicechain-tls"}
	})
	if err == nil || !strings.Contains(err.Error(), "must differ from ingress.tls.secretName") {
		t.Errorf("a Gateway Secret equal to the Ingress objects' was not refused by name: %v", err)
	}
	err = render(func(ing map[string]interface{}) { delete(ing, "gateway") })
	if err == nil || !strings.Contains(err.Error(), "must differ from ingress.tls.secretName") {
		t.Errorf("a cutover with no Gateway Secret of its own was not refused by name: %v", err)
	}
	err = render(func(ing map[string]interface{}) { ing["controller"] = frontEndNginx })
	if err == nil || !strings.Contains(err.Error(), "needs ingress.controller: gateway") {
		t.Errorf("legacy.enabled on the nginx front end was not refused by name: %v", err)
	}
	// Annotations are the Ingress objects': refused for a Gateway alone, applied in the window.
	err = render(func(ing map[string]interface{}) {
		ing["annotations"] = map[string]interface{}{"nginx.ingress.kubernetes.io/proxy-read-timeout": "120"}
	})
	if err != nil {
		t.Errorf("an annotation for the Ingress objects of a cutover window was refused: %v", err)
	}
	err = render(func(ing map[string]interface{}) {
		delete(ing, "legacy")
		ing["annotations"] = map[string]interface{}{"nginx.ingress.kubernetes.io/proxy-read-timeout": "120"}
	})
	if err == nil || !strings.Contains(err.Error(), "have no effect when ingress.controller is gateway") {
		t.Errorf("an annotation with no Ingress to apply to was not refused by name: %v", err)
	}
}

// ---------------------------------------------------------------------------
// The upgrade path: an nginx-front instance becomes a Gateway-front one
// ---------------------------------------------------------------------------

// An instance that was served by ingress-nginx is upgraded on a cluster that now runs Traefik
// as well. Its values are carried, it renders the Gateway with its Ingress beside it under a
// distinct certificate Secret, no other instance is told its host is taken, and after the
// operator drops the legacy set it is served by the Gateway alone.
func TestAnNginxInstanceUpgradesToTheGatewayWithoutCollidingWithItself(t *testing.T) {
	const host = "iot.example.com"
	instance := "alpha"

	previously := func() *State {
		// The full profile: it carries update-management, whose upload route is the one the
		// body limit belongs to.
		st := profileState(false, false, "full")(instance)
		st.IngressHost = host
		st.Values["ingressHost"] = host
		st.ValuesOverlay = map[string]interface{}{
			"replicas": 2,
			"ingress":  map[string]interface{}{"uploadMaxBodySize": "2g"},
		}
		st.ValuesOverlayRecorded = copyValues(st.ValuesOverlay)
		return st
	}

	// Before: nginx.
	before := previously()
	before.IngressController = frontEndNginx
	oldManifest := renderFor(t, before)
	if k := routingKindsOf(t, oldManifest); k.ingresses == 0 || k.gateways != 0 {
		t.Fatalf("the starting point is not an nginx-front instance: %+v", k)
	}

	// The cluster as the upgrade finds it: both controllers, the instance's own Ingress.
	prev := readFrontEndFacts
	t.Cleanup(func() { readFrontEndFacts = prev })
	readFrontEndFacts = func(context.Context, string, string) (frontEndFacts, error) {
		return frontEndFacts{clusterFrontEnd: clusterFrontEnd{GatewayClass: true, NginxClass: true}, InstanceHasIngress: true}, nil
	}

	// During: the Gateway, and the Ingress beside it.
	during := previously()
	if err := settleFrontEnd(t.Context(), during); err != nil {
		t.Fatal(err)
	}
	if during.IngressController != frontEndGateway || !during.LegacyIngress {
		t.Fatalf("settled %s legacy=%v, want the cutover window", during.IngressController, during.LegacyIngress)
	}
	duringManifest := renderFor(t, during)
	k := routingKindsOf(t, duringManifest)
	if k.ingresses == 0 || k.gateways != 1 || k.routes == 0 {
		t.Fatalf("the window rendered %+v", k)
	}
	if !k.hosts[host] || len(k.hosts) != 1 {
		t.Errorf("the window serves %v, want exactly %s: values were not carried", k.hosts, host)
	}
	// 🔴 THE GATEWAY WAITS FOR A NEW SECRET; THE SERVING SIDE KEEPS ITS OWN. DNS still reaches the
	// Ingress objects, so moving them onto a Secret that does not exist yet would have the live
	// host present a default certificate until it was issued.
	if !slices.Equal(k.gatewaySecrets, []string{gatewayCutoverSecret}) {
		t.Errorf("the Gateway names %v, want the new %s", k.gatewaySecrets, gatewayCutoverSecret)
	}
	for _, s := range k.ingressSecrets {
		if s != defaultTLSSecret {
			t.Errorf("a legacy Ingress names Secret %q, want the instance's own %s it already serves on", s, defaultTLSSecret)
		}
	}
	// The overlay carried: replicas, and the legacy upload Ingress still has its limit.
	if !strings.Contains(duringManifest, `nginx.ingress.kubernetes.io/proxy-body-size: "2g"`) {
		t.Error("the operator's uploadMaxBodySize no longer reaches the legacy upload Ingress during the window")
	}
	if during.ValuesOverlay["replicas"] != 2 {
		t.Error("the overlay did not carry")
	}

	// No host collision: the instance is not told its own host is taken, another instance is.
	claims := []gatewayHostClaim{{Namespace: InstanceNamespace(instance), Host: host}}
	ings := []networkingv1.Ingress{{ObjectMeta: metav1.ObjectMeta{Namespace: InstanceNamespace(instance), Name: "devicechain-web"},
		Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{Host: host}}}}}
	if own := singletonsFrom(nil, ings, claims, instance, host); own.HostHolder != "" {
		t.Errorf("the instance's own Gateway and Ingress were reported as a collision with held by %q", own.HostHolder)
	}
	if other := singletonsFrom(nil, ings, claims, "beta", host); other.HostHolder != InstanceNamespace(instance) {
		t.Errorf("a second instance on the same host was not refused: holder %q", other.HostHolder)
	}

	// The cluster now holds the Gateway the window rendered, on its own Secret.
	readFrontEndFacts = func(context.Context, string, string) (frontEndFacts, error) {
		return frontEndFacts{clusterFrontEnd: clusterFrontEnd{GatewayClass: true, NginxClass: true},
			InstanceHasIngress: true, InstanceHasGateway: true, InstanceGatewaySecret: gatewayCutoverSecret}, nil
	}

	// After --drop-legacy-ingress: the Gateway alone, and the nginx-only keys retire.
	stubGatewayCertificates(t, readyGatewayCert(InstanceNamespace(instance)), nil)
	after := previously()
	after.DropLegacyIngress = true
	err := settleFrontEnd(t.Context(), after)
	if err != nil {
		t.Fatalf("dropping the legacy set refused the recorded overlay: %v", err)
	}
	if after.IngressController != frontEndGateway || after.LegacyIngress {
		t.Fatalf("settled %s legacy=%v, want the Gateway alone", after.IngressController, after.LegacyIngress)
	}
	afterManifest := renderFor(t, after)
	if k := routingKindsOf(t, afterManifest); k.ingresses != 0 || k.gateways != 1 {
		t.Errorf("after the drop the instance renders %+v", k)
	}
	if strings.Contains(afterManifest, "proxy-body-size") {
		t.Error("an nginx body limit survived into a Gateway-only render")
	}
	// 🔴 THE GATEWAY KEEPS ITS OWN SECRET once the Ingress objects are gone: the old name belongs to
	// the Certificate issued for the Ingress, and handing it back would put two owners on one.
	if k := routingKindsOf(t, afterManifest); !slices.Equal(k.gatewaySecrets, []string{gatewayCutoverSecret}) {
		t.Errorf("after the drop the Gateway names %v, want it to keep %s", k.gatewaySecrets, gatewayCutoverSecret)
	}
	// And a later plain upgrade, with the Ingress objects gone, keeps it too.
	readFrontEndFacts = func(context.Context, string, string) (frontEndFacts, error) {
		return frontEndFacts{clusterFrontEnd: clusterFrontEnd{GatewayClass: true, NginxClass: true},
			InstanceHasGateway: true, InstanceGatewaySecret: gatewayCutoverSecret}, nil
	}
	plain := previously()
	if err := settleFrontEnd(t.Context(), plain); err != nil {
		t.Fatal(err)
	}
	if plain.LegacyIngress || plain.GatewayTLSSecret != gatewayCutoverSecret {
		t.Errorf("a plain upgrade after the drop settled legacy=%v secret=%q", plain.LegacyIngress, plain.GatewayTLSSecret)
	}
	// The other direction: nothing about a rollback is lost.
	back := previously()
	back.IngressControllerOverride = frontEndNginx
	if err := settleFrontEnd(t.Context(), back); err != nil {
		t.Fatal(err)
	}
	if k := routingKindsOf(t, renderFor(t, back)); k.gateways != 0 || k.ingresses == 0 {
		t.Errorf("--ingress-controller nginx did not return the instance to its Ingress objects: %+v", k)
	}
}

func renderFor(t *testing.T, st *State) string {
	t.Helper()
	vals, _, err := releaseValues(t.Context(), chartForTest(t), st, nil)
	if err != nil {
		t.Fatalf("composing the release values: %v", err)
	}
	manifest, err := renderChartClientSide(t.Context(), chartForTest(t), vals)
	if err != nil {
		t.Fatalf("rendering: %v", err)
	}
	return manifest
}

// Gateways and HTTPRoutes claim a host as an Ingress does.
func TestAHostAGatewayOrRouteClaimsIsTaken(t *testing.T) {
	const host = "iot.example.com"
	for name, claims := range map[string][]gatewayHostClaim{
		"a Gateway listener": {{Namespace: "dci-alpha", Host: host}},
		"an HTTPRoute":       {{Namespace: "dci-alpha", Host: "IOT.example.com"}}, // host names are not case sensitive
	} {
		got := singletonsFrom(nil, nil, claims, "beta", host)
		if got.HostHolder != "dci-alpha" {
			t.Errorf("%s: holder = %q, want dci-alpha", name, got.HostHolder)
		}
	}
	// Negative control: another host, and the instance's own namespace, are not collisions.
	if got := singletonsFrom(nil, nil, []gatewayHostClaim{{Namespace: "dci-alpha", Host: "other.example.com"}}, "beta", host); got.HostHolder != "" {
		t.Errorf("a different host collided: %q", got.HostHolder)
	}
	if got := singletonsFrom(nil, nil, []gatewayHostClaim{{Namespace: InstanceNamespace("beta"), Host: host}}, "beta", host); got.HostHolder != "" {
		t.Errorf("an instance collided with itself: %q", got.HostHolder)
	}
}

// An install that turned ingress-nginx off must not be recorded over a skipped Traefik.
func TestAnInstallWithNoControllerLeftIsRefused(t *testing.T) {
	meta := func(v string) tfexec.OutputMeta { return tfexec.OutputMeta{Value: json.RawMessage(v)} }
	off := []string{"enable_traefik=true", "enable_ingress_nginx=false"}

	err := refuseAClusterLeftWithNoController(off, map[string]tfexec.OutputMeta{
		"gateway_class":      meta(`null`),
		"gateway_api_notice": meta(`"the cluster's Gateway API CRDs are v1.2.0, below the v1.5.1 floor"`),
	})
	if err == nil || !strings.Contains(err.Error(), "below the v1.5.1 floor") || !strings.Contains(err.Error(), "no ingress controller") {
		t.Fatalf("a cluster with no controller was accepted, or refused without the reason: %v", err)
	}
	// Controls, one per way it could be wrongly refused.
	if err := refuseAClusterLeftWithNoController(off, map[string]tfexec.OutputMeta{"gateway_class": meta(`"traefik"`)}); err != nil {
		t.Errorf("a cluster with Traefik was refused: %v", err)
	}
	keep := []string{"enable_traefik=true"} // ingress-nginx kept: a skipped Traefik is a warning there
	if err := refuseAClusterLeftWithNoController(keep, map[string]tfexec.OutputMeta{"gateway_class": meta(`null`)}); err != nil {
		t.Errorf("a cluster that kept ingress-nginx was refused: %v", err)
	}
}

// An Ingress is nginx's if EITHER field names an nginx class, whatever the other says; the root's
// guard reads it the same way.
func TestAnIngressIsNginxsIfEitherFieldSaysSo(t *testing.T) {
	classes := map[string]bool{nginxIngressClass: true}
	other := "traefik"
	for name, c := range map[string]struct {
		ing  networkingv1.Ingress
		want bool
	}{
		"spec only": {networkingv1.Ingress{Spec: networkingv1.IngressSpec{IngressClassName: ptrTo(nginxIngressClass)}}, true},
		"annotation only": {networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{legacyIngressClassAnnotation: nginxIngressClass}}}, true},
		"spec says another class, annotation says nginx": {networkingv1.Ingress{
			ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{legacyIngressClassAnnotation: nginxIngressClass}},
			Spec:       networkingv1.IngressSpec{IngressClassName: &other}}, true},
		"spec says nginx, annotation says another class": {networkingv1.Ingress{
			ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{legacyIngressClassAnnotation: other}},
			Spec:       networkingv1.IngressSpec{IngressClassName: ptrTo(nginxIngressClass)}}, true},
		"both say another class": {networkingv1.Ingress{
			ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{legacyIngressClassAnnotation: other}},
			Spec:       networkingv1.IngressSpec{IngressClassName: &other}}, false},
		"names another class while nginx is the default": {networkingv1.Ingress{
			Spec: networkingv1.IngressSpec{IngressClassName: &other}}, false},
	} {
		if got := ingressServedByNginx(c.ing, classes, true); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}

func ptrTo(s string) *string { return &s }

// Grafana's own Ingress is exempt only while this root manages monitoring, and only the release's.
func TestGrafanasIngressIsExemptOnlyWhenThisRootOwnsIt(t *testing.T) {
	g := classedIngress(monitoringNamespace, "grafana", nginxIngressClass)
	g.Labels = map[string]string{"app.kubernetes.io/name": "grafana", "app.kubernetes.io/instance": monitoringReleaseName}
	classes := []networkingv1.IngressClass{nginxClass()}
	count := func(ing networkingv1.Ingress, managed bool) int {
		return len(frontEndFactsFrom(classes, []networkingv1.Ingress{ing}, true, nil, "", managed).NginxIngresses)
	}
	if count(g, true) != 0 {
		t.Error("the release's own Grafana Ingress blocked the removal")
	}
	if count(g, false) != 1 {
		t.Error("with --no-monitoring the root owns no Grafana, and its Ingress must block")
	}
	foreign := g.DeepCopy()
	foreign.Labels["app.kubernetes.io/instance"] = "somebody-elses"
	if count(*foreign, true) != 1 {
		t.Error("another release's Grafana Ingress was exempted")
	}
}

// ---------------------------------------------------------------------------
// ingress-nginx is deprecated: the chart default, the notice, and what an upgrade keeps
// ---------------------------------------------------------------------------

// The chart's own default is the Gateway. dcctl always names the front end, so this is what an
// install that does not come through dcctl (helm directly) gets, and what a State that settled
// nothing would get if ingressValues ever stopped naming it.
func TestTheChartDefaultsToTheGatewayFrontEnd(t *testing.T) {
	t.Parallel()
	ch := chartForTest(t)
	ing, _ := ch.Values["ingress"].(map[string]interface{})
	if ing["controller"] != frontEndGateway {
		t.Fatalf("the chart's ingress.controller default is %v, want %q", ing["controller"], frontEndGateway)
	}
	st := compactState(false)
	st.IngressController = frontEndGateway
	vals, _, err := releaseValues(t.Context(), ch, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Leave the front end out, as a plain `helm install --set ingress.enabled=true` does.
	delete(vals["ingress"].(map[string]interface{}), "controller")
	manifest, err := renderChartClientSide(t.Context(), ch, vals)
	if err != nil {
		t.Fatal(err)
	}
	k := routingKindsOf(t, manifest)
	if k.ingresses != 0 || k.gateways != 1 || k.routes == 0 {
		t.Errorf("a chart given no front end rendered %+v, want the Gateway alone", k)
	}
}

// The notice names ingress-nginx for every instance still served by it, alone or in the
// cutover window, and is silent for one on the Gateway alone. It is a notice, never an error.
func TestIngressNginxDeprecationNotice(t *testing.T) {
	if w := ingressNginxDeprecation(frontEndChoice{Controller: frontEndGateway}); w != "" {
		t.Errorf("an instance on the Gateway alone was warned: %q", w)
	}
	for name, c := range map[string]frontEndChoice{
		"nginx alone":    {Controller: frontEndNginx},
		"cutover window": {Controller: frontEndGateway, Legacy: true},
	} {
		w := ingressNginxDeprecation(c)
		if !strings.Contains(w, "DEPRECATED") || !strings.Contains(w, "ingress-nginx") {
			t.Errorf("%s: notice = %q", name, w)
		}
	}
}

// An instance that runs ingress-nginx and is upgraded stays on it, still serving, when the
// cluster has nothing else to serve it, and is only ever moved by an install that added Traefik
// first. The default flip changes what a NEW instance gets and nothing about these.
func TestAnUpgradeKeepsAnNginxInstanceOnNginxUntilTheClusterHasTraefik(t *testing.T) {
	stub := func(f frontEndFacts) {
		prev := readFrontEndFacts
		t.Cleanup(func() { readFrontEndFacts = prev })
		readFrontEndFacts = func(context.Context, string, string) (frontEndFacts, error) { return f, nil }
	}
	t.Run("a cluster that has only ingress-nginx", func(t *testing.T) {
		stub(frontEndFacts{clusterFrontEnd: clusterFrontEnd{NginxClass: true}, InstanceHasIngress: true})
		st := compactState(false)
		st.Values["ingressHost"] = "iot.example.com"
		if err := settleFrontEnd(t.Context(), st); err != nil {
			t.Fatal(err)
		}
		if st.IngressController != frontEndNginx || st.LegacyIngress {
			t.Errorf("an nginx instance was moved: controller=%q legacy=%v", st.IngressController, st.LegacyIngress)
		}
		if ing := ingressValues(st); ing["controller"] != frontEndNginx {
			t.Errorf("the chart was handed %v, and would render its default instead of the Ingress objects", ing)
		}
	})
	t.Run("a recorded nginx choice survives Traefik arriving", func(t *testing.T) {
		stub(frontEndFacts{clusterFrontEnd: clusterFrontEnd{NginxClass: true, GatewayClass: true}, InstanceHasIngress: true})
		st := compactState(false)
		st.IngressControllerOverride = frontEndNginx // what the declaration's annotation yields
		if err := settleFrontEnd(t.Context(), st); err != nil {
			t.Fatal(err)
		}
		if st.IngressController != frontEndNginx || st.LegacyIngress {
			t.Errorf("a recorded nginx choice was undone: controller=%q legacy=%v", st.IngressController, st.LegacyIngress)
		}
	})
}
