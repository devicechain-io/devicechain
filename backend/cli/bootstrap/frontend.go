// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

// The front end an instance is served by: the Gateway API objects Traefik serves
// (ingress.controller: gateway), or the Ingress objects ingress-nginx serves (nginx).
//
// 🔴 TWO QUESTIONS, AND THEY HAVE DIFFERENT OWNERS.
//
//   - WHICH CONTROLLERS THE CLUSTER RUNS is a cluster fact, settled by `dcctl install`
//     (planClusterFrontEnd): a cluster with no ingress-nginx gets Traefik alone; one that
//     has it gets Traefik BESIDE it, and loses it only on the operator's say-so, behind a
//     refusal while any Ingress still names its class.
//   - WHICH FRONT END AN INSTANCE RENDERS is an instance fact, settled by bootstrap and
//     upgrade (chooseFrontEnd): the chart renders a Gateway and its HTTPRoutes, and while
//     the cluster still has ingress-nginx and the instance still has Ingress objects, the
//     Ingress objects too (ingress.legacy), so the host keeps answering where it always
//     did until DNS has moved.
//
// Both read the cluster rather than a recorded choice. A record is a second copy of the
// answer that disagrees with the cluster the day someone removes a controller by hand;
// what is on the cluster is what is serving.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/fatih/color"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

const (
	frontEndNginx   = "nginx"
	frontEndGateway = "gateway"

	// nginxIngressClass and nginxIngressController are what the cluster root installs:
	// OpenTofu's ingress_class default, and the controller string the IngressClass carries.
	nginxIngressClass      = "nginx"
	nginxIngressController = "k8s.io/ingress-nginx"
	// defaultIngressClassAnnotation marks the IngressClass an Ingress with no class falls to.
	defaultIngressClassAnnotation = "ingressclass.kubernetes.io/is-default-class"
	legacyIngressClassAnnotation  = "kubernetes.io/ingress.class"

	// traefikGatewayClass is the GatewayClass the cluster root registers (OpenTofu
	// gateway_class) and the chart's ingress.gateway.className default names.
	traefikGatewayClass = "traefik"
	// instanceGatewayName is the Gateway the chart renders in every instance namespace.
	instanceGatewayName = "devicechain"
)

var gatewayGVR = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"}
var httpRouteGVR = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}
var gatewayClassGVR = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gatewayclasses"}

// ValidFrontEnds are the values --ingress-controller accepts.
var ValidFrontEnds = []string{frontEndNginx, frontEndGateway}

// ValidateFrontEnd refuses a value --ingress-controller does not know. Empty is "decide
// from the cluster".
func ValidateFrontEnd(v string) error {
	if v == "" || v == frontEndNginx || v == frontEndGateway {
		return nil
	}
	return fmt.Errorf("--ingress-controller %q: must be %s (or omitted, to decide from the cluster)",
		v, strings.Join(ValidFrontEnds, " or "))
}

// clusterFrontEnd is what the cluster runs, read once.
type clusterFrontEnd struct {
	// GatewayClass is whether the Traefik GatewayClass exists.
	GatewayClass bool
	// NginxClass is whether an IngressClass served by ingress-nginx exists.
	NginxClass bool
	// NginxIngresses are the Ingress objects ingress-nginx serves ("namespace/name"),
	// without the one object this root owns, Grafana's, which moves with the controller.
	NginxIngresses []string
}

// frontEndFacts adds what this instance already has.
type frontEndFacts struct {
	clusterFrontEnd
	// InstanceHasIngress: the instance's namespace holds an Ingress ingress-nginx serves.
	InstanceHasIngress bool
	// InstanceHasGateway: the instance's namespace holds the chart's Gateway.
	InstanceHasGateway bool
}

// frontEndChoice is what an instance renders.
type frontEndChoice struct {
	Controller string
	// Legacy renders the Ingress objects beside the Gateway (the cutover window).
	Legacy bool
	// Why is one sentence, printed.
	Why string
}

// chooseFrontEnd decides an instance's front end. Pure, so every row of the table is a test.
//
// override is --ingress-controller; drop is --drop-legacy-ingress.
//
//	override nginx                          -> nginx. The way back, and the way to stay.
//	no GatewayClass on the cluster          -> nginx. `dcctl install` of this release has not
//	                                           run; the instance renders what the controller
//	                                           that exists can serve. override gateway refuses.
//	GatewayClass, no ingress-nginx          -> gateway, alone.
//	both, the instance has routing already  -> gateway; with the Ingress objects beside it
//	                                           while it still has them and drop is not given.
//	both, a NEW instance                    -> gateway on a cloud (Traefik has its own
//	                                           address); nginx on a local cluster, where the
//	                                           node's 80/443 are still ingress-nginx's and a
//	                                           Gateway would answer nowhere a browser looks.
//	                                           override gateway takes the Gateway there anyway.
func chooseFrontEnd(f frontEndFacts, local bool, override string, drop bool) (frontEndChoice, error) {
	if override == frontEndNginx {
		return frontEndChoice{Controller: frontEndNginx, Why: "--ingress-controller nginx"}, nil
	}
	if !f.GatewayClass {
		if override == frontEndGateway {
			return frontEndChoice{}, fmt.Errorf("--ingress-controller gateway needs the Gateway API controller, and this cluster " +
				"has no GatewayClass " + traefikGatewayClass + ". Run `dcctl install` with this release first: it installs Traefik " +
				"(beside ingress-nginx, which it leaves serving)")
		}
		return frontEndChoice{Controller: frontEndNginx,
			Why: "this cluster has no Gateway API controller yet (run `dcctl install` with this release to add one)"}, nil
	}
	if !f.NginxClass {
		return frontEndChoice{Controller: frontEndGateway, Why: "the cluster's controller is Traefik"}, nil
	}
	// Both controllers are on the cluster.
	if f.InstanceHasIngress || f.InstanceHasGateway {
		legacy := f.InstanceHasIngress && !drop
		why := "the instance moves to the Gateway; ingress-nginx still serves its host until --drop-legacy-ingress"
		if !legacy {
			why = "the instance is served by the Gateway alone"
		}
		return frontEndChoice{Controller: frontEndGateway, Legacy: legacy, Why: why}, nil
	}
	if local && override != frontEndGateway {
		return frontEndChoice{Controller: frontEndNginx,
			Why: "a new instance on a local cluster whose host ports are still ingress-nginx's (move the cluster with `dcctl install --remove-legacy-ingress`)"}, nil
	}
	return frontEndChoice{Controller: frontEndGateway, Why: "a new instance on a cluster with Traefik"}, nil
}

// ingressServedByNginx reports whether ingress-nginx serves ing: it names the class, the
// legacy class annotation, or names none while the nginx class is the cluster default.
func ingressServedByNginx(ing networkingv1.Ingress, nginxClasses map[string]bool, nginxIsDefault bool) bool {
	if c := ing.Spec.IngressClassName; c != nil {
		return nginxClasses[*c]
	}
	if c, ok := ing.Annotations[legacyIngressClassAnnotation]; ok {
		return nginxClasses[c]
	}
	return nginxIsDefault
}

// isClusterOwnedIngress is the Ingress the cluster root renders for Grafana, which is
// switched to Traefik in the same apply that removes ingress-nginx and so must not count
// against the removal.
func isClusterOwnedIngress(ing networkingv1.Ingress) bool {
	return ing.Namespace == monitoringNamespace && ing.Labels["app.kubernetes.io/name"] == "grafana"
}

// frontEndFactsFrom is readFrontEndFacts' decision, with no cluster in it.
func frontEndFactsFrom(classes []networkingv1.IngressClass, ings []networkingv1.Ingress,
	gatewayClass bool, instanceGateway bool, instance string) frontEndFacts {
	nginxClasses := map[string]bool{}
	nginxIsDefault := false
	for _, c := range classes {
		if c.Spec.Controller == nginxIngressController {
			nginxClasses[c.Name] = true
			if c.Annotations[defaultIngressClassAnnotation] == "true" {
				nginxIsDefault = true
			}
		}
	}
	f := frontEndFacts{InstanceHasGateway: instanceGateway}
	f.GatewayClass = gatewayClass
	f.NginxClass = len(nginxClasses) > 0
	own := InstanceNamespace(instance)
	for _, ing := range ings {
		if !ingressServedByNginx(ing, nginxClasses, nginxIsDefault) {
			continue
		}
		if ing.Namespace == own {
			f.InstanceHasIngress = true
		}
		if !isClusterOwnedIngress(ing) {
			f.NginxIngresses = append(f.NginxIngresses, ing.Namespace+"/"+ing.Name)
		}
	}
	sort.Strings(f.NginxIngresses)
	return f
}

// readFrontEndFacts reads what the cluster runs and what the instance has. Indirected, like
// the other cluster reads, so the settle step can be driven without a cluster.
//
// A Gateway API CRD that is not installed is "no GatewayClass", "no Gateway": that is the
// state of every cluster `dcctl install` of this release has not reached.
var readFrontEndFacts = func(ctx context.Context, kubeContext, instance string) (frontEndFacts, error) {
	dyn, _, typed, err := kubeClients(kubeContext)
	if err != nil {
		return frontEndFacts{}, fmt.Errorf("connecting to the cluster to see which ingress controllers it runs: %w", err)
	}
	classes, ings, err := listIngressObjects(ctx, typed)
	if err != nil {
		return frontEndFacts{}, err
	}
	gc, err := gatewayAPIObjectExists(ctx, dyn, gatewayClassGVR, "", traefikGatewayClass)
	if err != nil {
		return frontEndFacts{}, fmt.Errorf("looking for GatewayClass %s: %w", traefikGatewayClass, err)
	}
	gw := false
	if instance != "" {
		gw, err = gatewayAPIObjectExists(ctx, dyn, gatewayGVR, InstanceNamespace(instance), instanceGatewayName)
		if err != nil {
			return frontEndFacts{}, fmt.Errorf("looking for this instance's Gateway: %w", err)
		}
	}
	return frontEndFactsFrom(classes, ings, gc, gw, instance), nil
}

// listIngressObjects reads every IngressClass and Ingress on the cluster.
func listIngressObjects(ctx context.Context, typed kubernetes.Interface) ([]networkingv1.IngressClass, []networkingv1.Ingress, error) {
	classes, err := typed.NetworkingV1().IngressClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("listing IngressClasses: %w", err)
	}
	ings, err := typed.NetworkingV1().Ingresses(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("listing Ingresses: %w", err)
	}
	return classes.Items, ings.Items, nil
}

// gatewayAPIObjectExists is a Get that treats an absent CRD as an absent object.
func gatewayAPIObjectExists(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource,
	namespace, name string) (bool, error) {
	var err error
	if namespace == "" {
		_, err = dyn.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
	} else {
		_, err = dyn.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	}
	switch {
	case err == nil:
		return true, nil
	case apierrors.IsNotFound(err), meta.IsNoMatchError(err):
		return false, nil
	default:
		return false, err
	}
}

// settleFrontEnd decides the front end this run renders and records it on the State. A run
// that cannot read the cluster stops, except a rehearsal, which assumes a cluster with
// Traefik alone and says so.
//
// Called after the values overlay is settled, because retiring the nginx-only overlay
// keys depends on the answer.
func settleFrontEnd(ctx context.Context, st *State) error {
	facts, err := readFrontEndFacts(ctx, st.KubeContext, st.Instance)
	if err != nil {
		if !st.DryRun {
			return err
		}
		fmt.Println(color.YellowString("  warning: could not read the cluster's ingress controllers (%v); "+
			"this rehearsal assumes Traefik alone.", err))
		facts = frontEndFacts{clusterFrontEnd: clusterFrontEnd{GatewayClass: true}}
	}
	choice, err := chooseFrontEnd(facts, looksLocal(st.KubeContext), st.IngressControllerOverride, st.DropLegacyIngress)
	if err != nil {
		return err
	}
	st.IngressController, st.LegacyIngress = choice.Controller, choice.Legacy
	fmt.Printf("  front end        %s%s (%s)\n", choice.Controller, map[bool]string{true: " + legacy Ingress"}[choice.Legacy], choice.Why)
	return checkNginxOnlyOverlay(st)
}

// nginxOnlyOverlayKeys are the tunable values that only mean something to the Ingress objects.
var nginxOnlyOverlayKeys = [][]string{
	{"ingress", "uploadMaxBodySize"},
	{"ingress", "className"},
	{"ingress", "annotations"},
}

// retiresNginxOnlyOverlay is whether the Ingress-only overlay keys have nothing to apply to:
// the instance is served by the Gateway alone.
func retiresNginxOnlyOverlay(st *State) bool {
	return st.IngressController == frontEndGateway && !st.LegacyIngress
}

// effectiveOverlay is the overlay the render merges: the operator's, less the Ingress-only keys
// when the instance has no Ingress objects to apply them to. The record is untouched, so the
// keys apply again under --ingress-controller nginx.
//
// 🔴 THEY ARE NOT MERELY INERT. ingress.annotations under the Gateway front end FAILS the
// render (the chart refuses a value it would otherwise ignore), so an instance that carried
// an annotation from its nginx days would be unable to render at all.
func effectiveOverlay(st *State) map[string]interface{} {
	if len(st.ValuesOverlay) == 0 || !retiresNginxOnlyOverlay(st) {
		return st.ValuesOverlay
	}
	overlay := deepCopyValues(st.ValuesOverlay)
	for _, path := range nginxOnlyOverlayKeys {
		deletePath(overlay, path)
	}
	return overlay
}

// checkNginxOnlyOverlay says what the retirement does to this run's overlay, before anything
// is written. A value the operator just typed is REFUSED, not dropped: they would otherwise
// believe the edge enforces it. One the instance already recorded is dropped with a warning,
// because the operator did not just choose it.
func checkNginxOnlyOverlay(st *State) error {
	if !retiresNginxOnlyOverlay(st) || len(st.ValuesOverlay) == 0 {
		return nil
	}
	var kept, fresh []string
	for _, path := range nginxOnlyOverlayKeys {
		v, ok := lookupPath(st.ValuesOverlay, path)
		if !ok {
			continue
		}
		key := strings.Join(path, ".")
		if rv, was := lookupPath(st.ValuesOverlayRecorded, path); was && fmt.Sprint(rv) == fmt.Sprint(v) {
			kept = append(kept, key)
		} else {
			fresh = append(fresh, key)
		}
	}
	if len(fresh) > 0 {
		return fmt.Errorf("the values overlay sets %s, which only the Ingress front end reads, and this instance "+
			"is served by the Gateway: the Gateway API controller caps no request body and takes no annotations "+
			"from the instance, so the value would have no effect. Remove it from the overlay (the largest "+
			"artifact is functionalAreas.update-management.config.updates.maxArtifactBytes). Nothing has been changed",
			strings.Join(fresh, ", "))
	}
	if len(kept) > 0 {
		fmt.Println(color.YellowString("  warning: the overlay recorded on this instance sets %s; the Gateway front end has "+
			"no use for it, so it is not applied (it stays recorded, and applies again under --ingress-controller nginx).",
			strings.Join(kept, ", ")))
	}
	return nil
}

func lookupPath(m map[string]interface{}, path []string) (interface{}, bool) {
	var cur interface{} = m
	for _, seg := range path {
		mm, ok := cur.(map[string]interface{})
		if !ok {
			return nil, false
		}
		if cur, ok = mm[seg]; !ok {
			return nil, false
		}
	}
	return cur, true
}

func deletePath(m map[string]interface{}, path []string) {
	for _, seg := range path[:len(path)-1] {
		next, ok := m[seg].(map[string]interface{})
		if !ok {
			return
		}
		m = next
	}
	delete(m, path[len(path)-1])
}

// ErrLegacyIngressInUse is the refusal to remove ingress-nginx while Ingress objects still
// name its class. Typed so the command layer and the tests can tell it from a failed read.
type ErrLegacyIngressInUse struct {
	Ingresses []string
}

func (e *ErrLegacyIngressInUse) Error() string {
	shown := e.Ingresses
	more := ""
	if len(shown) > 12 {
		shown, more = shown[:12], fmt.Sprintf("\n  ... and %d more", len(e.Ingresses)-12)
	}
	return fmt.Sprintf("ingress-nginx is still serving %d Ingress object(s), and removing the controller leaves them in "+
		"place with nothing to serve them, so every host they carry stops answering:\n  %s%s\n"+
		"Move each instance to the Gateway first: point DNS at the new controller, then run "+
		"`dcctl upgrade <provider> <instance> --drop-legacy-ingress` for it. Nothing has been changed",
		len(e.Ingresses), strings.Join(shown, "\n  "), more)
}

// clusterFrontEndPlan is what `dcctl install` asks of the cluster root.
type clusterFrontEndPlan struct {
	// Vars are the -var settings, "name=value".
	Vars []string
	// Notes are printed before the apply.
	Notes []string
}

// planClusterFrontEnd decides which controllers the cluster root runs.
//
//	no ingress-nginx on the cluster     -> Traefik alone. A fresh cluster never gets the retired
//	                                       controller; one it was removed from does not get it back.
//	ingress-nginx present               -> kept, Traefik added beside it. On a local cluster
//	                                       ingress-nginx keeps the node's host ports and Traefik
//	                                       sits on a NodePort until the removal hands them over.
//	ingress-nginx present, removal asked -> refused while any Ingress names its class (the
//	                                       preflight); otherwise removed, and on a local cluster
//	                                       Traefik takes the host ports in the same apply.
//
// local is whether the cluster is the local kind-style one that publishes node host ports.
func planClusterFrontEnd(f clusterFrontEnd, local, removeLegacy bool) (clusterFrontEndPlan, error) {
	var p clusterFrontEndPlan
	p.Vars = append(p.Vars, "enable_gateway_api_crds=true", "enable_traefik=true")
	switch {
	case !f.NginxClass:
		p.Vars = append(p.Vars, "enable_ingress_nginx=false")
		if local {
			p.Vars = append(p.Vars, "traefik_use_host_port=true")
		}
		if removeLegacy {
			p.Notes = append(p.Notes, "--remove-legacy-ingress: this cluster has no ingress-nginx to remove")
		}
	case removeLegacy:
		if len(f.NginxIngresses) > 0 {
			return clusterFrontEndPlan{}, &ErrLegacyIngressInUse{Ingresses: f.NginxIngresses}
		}
		p.Vars = append(p.Vars, "enable_ingress_nginx=false", "allow_ingress_nginx_removal=true")
		if local {
			p.Vars = append(p.Vars, "traefik_use_host_port=true")
		}
		p.Notes = append(p.Notes, "removing ingress-nginx; no Ingress object names its class")
	default:
		if local {
			p.Vars = append(p.Vars, "ingress_use_host_port=true")
		}
		p.Notes = append(p.Notes, "ingress-nginx stays and keeps serving; Traefik is installed beside it. When every "+
			"instance has moved (`dcctl upgrade --drop-legacy-ingress`), `dcctl install --remove-legacy-ingress` removes it")
	}
	return p, nil
}

// settleClusterFrontEnd reads the cluster and plans the cluster root's controllers into the
// State, for infraVars to emit. A read that fails stops the install: planning from a guess
// would either remove a controller that is serving or install one that was removed on purpose.
func settleClusterFrontEnd(ctx context.Context, st *State, typed kubernetes.Interface) error {
	if typed == nil {
		// A rehearsal of a first install has no cluster to ask. It decides nothing, and the cluster
		// root's own defaults stand; a run that applies is always handed a client.
		st.ClusterFrontEndVars = nil
		return nil
	}
	classes, ings, err := listIngressObjects(ctx, typed)
	if err != nil {
		return err
	}
	f := frontEndFactsFrom(classes, ings, false, false, "")
	plan, err := planClusterFrontEnd(f.clusterFrontEnd, looksLocal(st.KubeContext), st.RemoveLegacyIngress)
	if err != nil {
		return err
	}
	st.ClusterFrontEndVars = plan.Vars
	for _, n := range plan.Notes {
		fmt.Printf("  ingress          %s\n", n)
	}
	return nil
}
