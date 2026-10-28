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
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/fatih/color"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

// frontEndAuto is the --ingress-controller value that drops a recorded choice and returns the
// instance to being decided from the cluster.
const frontEndAuto = "auto"

// AnnotationIngressController is the declaration annotation holding the operator's front-end
// choice (nginx or gateway). Absent means "decide from the cluster". It is an INSTANCE fact, like
// the values overlay and the trusted proxies, because dcctl re-derives everything it renders on
// every run: a choice that lived only in a flag would be undone by the next plain upgrade, which
// is exactly when a rollback to the Ingress objects most needs to stay rolled back.
const AnnotationIngressController = "core.devicechain.io/ingress-controller"

// ValidateFrontEnd refuses a value --ingress-controller does not know. Empty is "not given".
func ValidateFrontEnd(v string) error {
	if v == "" || v == frontEndNginx || v == frontEndGateway || v == frontEndAuto {
		return nil
	}
	return fmt.Errorf("--ingress-controller %q: must be %s or %s (or omitted, to keep what the instance records, "+
		"else decide from the cluster)", v, strings.Join(ValidFrontEnds, " or "), frontEndAuto)
}

// applyIngressControllerChoice settles the front-end override of an upgrade: the flag when it is
// given (auto clears), else the one the declaration records.
func applyIngressControllerChoice(st *State, inst *dcv1beta1.Instance, opts UpgradeOptions) error {
	recorded := inst.Annotations[AnnotationIngressController]
	if recorded != "" && recorded != frontEndNginx && recorded != frontEndGateway {
		return fmt.Errorf("the front-end choice recorded on instance %q (annotation %s) is %q, which this release does "+
			"not know. Replace it with --ingress-controller nginx|gateway|auto. Nothing has been changed",
			st.Instance, AnnotationIngressController, recorded)
	}
	if err := ValidateFrontEnd(opts.IngressController); err != nil {
		return err
	}
	want := recorded
	switch opts.IngressController {
	case "":
	case frontEndAuto:
		want = ""
	default:
		want = opts.IngressController
	}
	st.IngressControllerRecorded = recorded
	st.IngressControllerOverride = want
	st.IngressControllerChange = opts.IngressController != "" && want != recorded
	st.DropLegacyIngress = opts.DropLegacyIngress
	return nil
}

// recordIngressController writes the choice onto the declaration, or removes it when empty.
// A compare-and-swap, like every declaration write.
func recordIngressController(ctx context.Context, dyn dynamic.Interface, instance, value string) error {
	inst, err := readInstanceCR(ctx, dyn, instance)
	if err != nil {
		return err
	}
	if inst == nil {
		return fmt.Errorf("the declaration for instance %q disappeared before the front-end choice could be recorded; "+
			"re-run to start from a clean read", instance)
	}
	if value == "" {
		if _, had := inst.Annotations[AnnotationIngressController]; !had {
			return nil
		}
		delete(inst.Annotations, AnnotationIngressController)
	} else {
		if inst.Annotations == nil {
			inst.Annotations = map[string]string{}
		}
		inst.Annotations[AnnotationIngressController] = value
	}
	obj, err := instanceToUnstructured(inst)
	if err != nil {
		return err
	}
	if _, err := dyn.Resource(instanceGVR).Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
		if apierrors.IsConflict(err) {
			return fmt.Errorf("the declaration for instance %q changed while the front-end choice was being recorded; "+
				"something else is editing it -- re-run once you know what", instance)
		}
		return fmt.Errorf("recording the front-end choice on instance %q: %w", instance, err)
	}
	return nil
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
	// InstanceGatewaySecret is the certificate Secret that Gateway's https listener names, empty
	// when there is no Gateway or it names none. The Gateway keeps its own name for good.
	InstanceGatewaySecret string
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
	// FAIL CLOSED. Every branch below that names a front end first has the controller that
	// serves it on the cluster; a choice nothing can serve renders green and answers nowhere.
	if !f.GatewayClass && !f.NginxClass {
		return frontEndChoice{}, errors.New("this cluster has no ingress controller: no GatewayClass " + traefikGatewayClass +
			" and no IngressClass served by ingress-nginx. Run `dcctl install` to install one; nothing has been changed")
	}
	if override == frontEndNginx {
		if !f.NginxClass {
			return frontEndChoice{}, errors.New("--ingress-controller nginx needs ingress-nginx, and this cluster has no " +
				"IngressClass it serves (it was removed, or never installed). Nothing has been changed")
		}
		return frontEndChoice{Controller: frontEndNginx, Why: "--ingress-controller nginx"}, nil
	}
	if !f.GatewayClass {
		if override == frontEndGateway {
			return frontEndChoice{}, errors.New("--ingress-controller gateway needs the Gateway API controller, and this cluster " +
				"has no GatewayClass " + traefikGatewayClass + ". Run `dcctl install` with this release first: it installs Traefik " +
				"(beside ingress-nginx, which it leaves serving)")
		}
		if f.InstanceHasGateway {
			// Falling back to Ingress objects here would put the instance on a different front
			// end, and a different certificate Secret, because a controller went missing.
			return frontEndChoice{}, errors.New("this instance is served by a Gateway, and the cluster has no GatewayClass " +
				traefikGatewayClass + " any more (Traefik was removed, or is not yet installed here). Run `dcctl install` to restore " +
				"it; to put the instance back on ingress-nginx say so with --ingress-controller nginx. Nothing has been changed")
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
	// EITHER field counts. An Ingress names its class in spec or in the legacy annotation, and a
	// controller serves an Ingress if either says it is its own; reading one and ignoring the
	// other would let the removal pass over an Ingress that is still being served.
	named := false
	if c := ing.Spec.IngressClassName; c != nil {
		named = true
		if nginxClasses[*c] {
			return true
		}
	}
	if c, ok := ing.Annotations[legacyIngressClassAnnotation]; ok {
		named = true
		if nginxClasses[c] {
			return true
		}
	}
	return !named && nginxIsDefault
}

// isClusterOwnedIngress is the Ingress the cluster root renders for Grafana, which is
// switched to Traefik in the same apply that removes ingress-nginx and so must not count
// against the removal. Only while this root manages monitoring, and only the one its own
// release created: anyone else's Ingress in that namespace still blocks the removal.
func isClusterOwnedIngress(ing networkingv1.Ingress, monitoringManaged bool) bool {
	return monitoringManaged && ing.Namespace == monitoringNamespace &&
		ing.Labels["app.kubernetes.io/name"] == "grafana" &&
		ing.Labels["app.kubernetes.io/instance"] == monitoringReleaseName
}

// monitoringReleaseName is the Helm release the cluster root names the monitoring stack (the
// module's default; the root does not override it).
const monitoringReleaseName = "kube-prometheus-stack"

// instanceGatewayFact is what is read off an instance's Gateway.
type instanceGatewayFact struct{ Secret string }

// acmeSolverLabel is the label cert-manager puts on the Ingress it creates for an HTTP-01 challenge.
const acmeSolverLabel = "acme.cert-manager.io/http01-solver"

// isACMESolverIngress is whether ing is a cert-manager HTTP-01 solver Ingress.
func isACMESolverIngress(ing networkingv1.Ingress) bool {
	return ing.Labels[acmeSolverLabel] == "true"
}

// frontEndFactsFrom is readFrontEndFacts' decision, with no cluster in it.
func frontEndFactsFrom(classes []networkingv1.IngressClass, ings []networkingv1.Ingress,
	gatewayClass bool, gw *instanceGatewayFact, instance string, monitoringManaged bool) frontEndFacts {
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
	f := frontEndFacts{InstanceHasGateway: gw != nil}
	if gw != nil {
		f.InstanceGatewaySecret = gw.Secret
	}
	f.GatewayClass = gatewayClass
	f.NginxClass = len(nginxClasses) > 0
	own := InstanceNamespace(instance)
	for _, ing := range ings {
		if !ingressServedByNginx(ing, nginxClasses, nginxIsDefault) {
			continue
		}
		if isACMESolverIngress(ing) {
			// cert-manager's HTTP-01 solver Ingress lives for the length of one challenge and is not
			// the instance's: counting it would re-render the legacy Ingress objects after a drop and
			// block the controller's removal until the challenge is cleaned up.
			continue
		}
		if ing.Namespace == own {
			f.InstanceHasIngress = true
		}
		if !isClusterOwnedIngress(ing, monitoringManaged) {
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
	gcObj, err := getGatewayAPIObject(ctx, dyn, gatewayClassGVR, "", traefikGatewayClass)
	if err != nil {
		return frontEndFacts{}, fmt.Errorf("looking for GatewayClass %s: %w", traefikGatewayClass, err)
	}
	var gw *instanceGatewayFact
	if instance != "" {
		obj, err := getGatewayAPIObject(ctx, dyn, gatewayGVR, InstanceNamespace(instance), instanceGatewayName)
		if err != nil {
			return frontEndFacts{}, fmt.Errorf("looking for this instance's Gateway: %w", err)
		}
		if obj != nil {
			gw = &instanceGatewayFact{Secret: gatewayListenerSecret(obj.Object)}
		}
	}
	return frontEndFactsFrom(classes, ings, gcObj != nil, gw, instance, true), nil
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

// getGatewayAPIObject is a Get that treats an absent CRD as an absent object (nil).
func getGatewayAPIObject(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource,
	namespace, name string) (*unstructured.Unstructured, error) {
	var obj *unstructured.Unstructured
	var err error
	if namespace == "" {
		obj, err = dyn.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
	} else {
		obj, err = dyn.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	}
	switch {
	case err == nil:
		return obj, nil
	case apierrors.IsNotFound(err), meta.IsNoMatchError(err):
		return nil, nil
	default:
		return nil, err
	}
}

// gatewayListenerSecret is the certificate Secret a Gateway's https listener names.
func gatewayListenerSecret(obj map[string]interface{}) string {
	listeners, _, _ := unstructured.NestedSlice(obj, "spec", "listeners")
	for _, l := range listeners {
		lm, ok := l.(map[string]interface{})
		if !ok || lm["name"] != "https" {
			continue
		}
		refs, _, _ := unstructured.NestedSlice(lm, "tls", "certificateRefs")
		for _, r := range refs {
			if rm, ok := r.(map[string]interface{}); ok {
				if n, ok := rm["name"].(string); ok {
					return n
				}
			}
		}
	}
	return ""
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
	if st.DropLegacyIngress && choice.Controller == frontEndGateway && !choice.Legacy && facts.InstanceHasIngress {
		if err := refuseDropWithoutGatewayCertificate(ctx, st); err != nil {
			return err
		}
	}
	st.IngressController, st.LegacyIngress = choice.Controller, choice.Legacy
	st.GatewayTLSSecret = ""
	if choice.Controller == frontEndGateway {
		st.GatewayTLSSecret = gatewaySecretFor(facts, choice.Legacy)
	}
	why := choice.Why
	if st.IngressControllerOverride != "" {
		why += "; recorded on the instance, so a later upgrade keeps it until --ingress-controller auto"
	}
	fmt.Printf("  front end        %s%s (%s)\n", choice.Controller, map[bool]string{true: " + legacy Ingress"}[choice.Legacy], why)
	return checkNginxOnlyOverlay(st)
}

// refuseDropWithoutGatewayCertificate is the precondition of --drop-legacy-ingress: dropping the
// Ingress objects makes the Gateway the only thing serving the host, so the Gateway's
// certificate must already be Ready and controlled by the Gateway. Settled before anything is
// rendered or written, so a refusal changes nothing.
//
// A rehearsal that cannot read the cluster says so and carries on, like the rest of the settle.
func refuseDropWithoutGatewayCertificate(ctx context.Context, st *State) error {
	vs, err := readGatewayCertificates(ctx, st.KubeContext, st.Instance)
	if err != nil {
		if st.DryRun {
			fmt.Println(color.YellowString("  warning: could not read the Gateway's certificate (%v); "+
				"this rehearsal cannot check --drop-legacy-ingress against it.", err))
			return nil
		}
		return err
	}
	return refuseUnlessGatewayCertificatesReady(vs, true,
		"--drop-legacy-ingress stops rendering the Ingress objects that serve this instance's host, but its Gateway "+
			"has no usable certificate (Ready, and issued as configured), so the host would stop answering over https (or present a certificate clients cannot trust)",
		"Move DNS to the new controller so the certificate can be issued, wait for `kubectl -n "+
			InstanceNamespace(st.Instance)+" get certificate` to show it Ready, and if the reason above names the Secret's issuer, delete that Secret so cert-manager issues it again, then run it again.")
}

const (
	// defaultTLSSecret is the chart's ingress.tls.secretName, the Secret an Ingress-served host has.
	defaultTLSSecret = "devicechain-tls"
	// gatewayCutoverSecret is the Secret the Gateway takes while an instance moves off its Ingress
	// objects.
	gatewayCutoverSecret = "devicechain-tls-gateway"
)

// gatewaySecretFor is the certificate Secret the Gateway names.
//
// 🔴 THE GATEWAY WAITS FOR A NEW SECRET, THE SERVING SIDE DOES NOT. While an instance has Ingress
// objects DNS still reaches the controller serving them, on devicechain-tls. Moving the Gateway,
// which serves nothing yet, onto a Secret that does not exist is invisible; moving the Ingress
// objects would have the live host present a default certificate until the new one was issued
// (minutes under ACME, indefinitely if the order fails). The Gateway then KEEPS its name: the
// Ingress objects going away does not hand it the old Secret back, because the old Certificate
// belongs to the Ingress it was issued for. Read off the Gateway itself, so it follows what is
// deployed rather than a record of it.
func gatewaySecretFor(f frontEndFacts, legacy bool) string {
	secret := f.InstanceGatewaySecret
	if secret == "" && !f.InstanceHasIngress {
		return "" // a new instance: the chart's own name
	}
	if secret == "" || (legacy && secret == defaultTLSSecret) {
		return gatewayCutoverSecret
	}
	if secret == defaultTLSSecret {
		return "" // the chart's own name, already the Gateway's
	}
	return secret
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

// The --aws-nlb values.
const (
	awsNLBTraefik = "traefik"
	awsNLBNginx   = "nginx"
	awsNLBAll     = "all"
	awsNLBNone    = "none"
)

// ValidateAWSNLB refuses a value --aws-nlb does not take. Empty is "keep what is there".
func ValidateAWSNLB(v string) error {
	switch v {
	case "", awsNLBTraefik, awsNLBNginx, awsNLBAll, awsNLBNone:
		return nil
	}
	return fmt.Errorf("--aws-nlb %q: want traefik, nginx, all or none (or leave it off to keep what each controller's "+
		"Service already has)", v)
}

// The namespaces of the controllers this root installs (the root's defaults, which dcctl does
// not override). The read is scoped to them and to the release's own labels, never cluster-wide,
// so a controller someone else runs elsewhere with the annotation decides nothing here.
const (
	traefikNamespace      = "traefik"
	ingressNginxNamespace = "ingress-nginx"
)

// awsNLBOnService is whether the root's controller Service carries the NLB annotation, and
// whether such a Service (a LoadBalancer) exists at all.
func awsNLBOnService(ctx context.Context, typed kubernetes.Interface, ns, selector string) (annotated, exists bool, err error) {
	svcs, err := typed.CoreV1().Services(ns).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return false, false, fmt.Errorf("listing the Services in %s: %w", ns, err)
	}
	for _, s := range svcs.Items {
		if s.Spec.Type != corev1.ServiceTypeLoadBalancer {
			continue
		}
		exists = true
		if s.Annotations[awsLoadBalancerTypeAnnotation] == "nlb" {
			annotated = true
		}
	}
	return annotated, exists, nil
}

// awsNLBPlan is the cluster root's NLB settings for both controllers, and a warning for each
// that would change an existing Service.
//
//	""        keep what each of THIS root's controller Services already has
//	traefik   Traefik's on, nginx's as it is;  nginx: the reverse;  all: both on
//	none      both off
//
// 🔴 ABSENT MEANS KEEP, BECAUSE THE ROOT'S VARIABLE DEFAULTS TO OFF. A re-run that forgot the flag
// would otherwise take the annotation off, which replaces the load balancer and the hostname
// clients resolve. Like the controllers themselves, it is read from the cluster, not recorded.
func awsNLBPlan(ctx context.Context, typed kubernetes.Interface, mode string) (vars, warnings []string, err error) {
	controllers := []struct{ name, variable, ns, selector string }{
		{awsNLBTraefik, "traefik_aws_nlb", traefikNamespace,
			"app.kubernetes.io/name=traefik,app.kubernetes.io/instance=traefik-" + traefikNamespace},
		{awsNLBNginx, "ingress_nginx_aws_nlb", ingressNginxNamespace,
			"app.kubernetes.io/name=ingress-nginx,app.kubernetes.io/instance=ingress-nginx,app.kubernetes.io/component=controller"},
	}
	for _, c := range controllers {
		have, exists, err := awsNLBOnService(ctx, typed, c.ns, c.selector)
		if err != nil {
			return nil, nil, err
		}
		want := have
		switch mode {
		case c.name, awsNLBAll:
			want = true
		case awsNLBNone:
			want = false
		}
		if want {
			vars = append(vars, c.variable+"=true")
		}
		if exists && want != have {
			what := "gives the " + c.name + " Service a Network Load Balancer"
			if !want {
				what = "takes the " + c.name + " Service's Network Load Balancer off"
			}
			warnings = append(warnings, "--aws-nlb "+what+": AWS REPLACES the load balancer, so the hostname "+
				"clients resolve changes. Repoint DNS to the new one afterwards")
		}
	}
	return vars, warnings, nil
}

// awsLoadBalancerTypeAnnotation is what makes EKS provision a Network Load Balancer.
const awsLoadBalancerTypeAnnotation = "service.beta.kubernetes.io/aws-load-balancer-type"

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
	f := frontEndFactsFrom(classes, ings, false, nil, "", !st.NoMonitoring)
	plan, err := planClusterFrontEnd(f.clusterFrontEnd, looksLocal(st.KubeContext), st.RemoveLegacyIngress)
	if err != nil {
		return err
	}
	if st.RemoveLegacyIngress && slices.Contains(plan.Vars, "allow_ingress_nginx_removal=true") {
		// 🔴 EVERY INSTANCE ON THE CLUSTER, not only the one named: the controller is shared, and an
		// instance whose Gateway has no certificate yet is one removing it leaves with nothing.
		vs, err := readGatewayCertificates(ctx, st.KubeContext, "")
		if err != nil {
			return err
		}
		if err := refuseUnlessGatewayCertificatesReady(vs, false,
			"--remove-legacy-ingress is refused while these instances' Gateways have no usable certificate (Ready, and issued as configured). Removing "+
				"ingress-nginx does not change how they are served, but the check keeps a broken Gateway "+
				"certificate from going unnoticed until nothing else serves the host",
			"Wait for each Certificate to show Ready (`kubectl get certificate -A`), then run it again."); err != nil {
			return err
		}
	}
	nlb, nlbWarnings, err := awsNLBPlan(ctx, typed, st.AWSNLB)
	if err != nil {
		return err
	}
	plan.Vars = append(plan.Vars, nlb...)
	for _, w := range nlbWarnings {
		fmt.Println(color.YellowString("  warning: %s", w))
	}
	st.ClusterFrontEndVars = plan.Vars
	for _, n := range plan.Notes {
		fmt.Printf("  ingress          %s\n", n)
	}
	return nil
}

// sayFrontEnd is the front-end line of the upgrade report: what serves the host, and the choice
// the declaration records.
func sayFrontEnd(st *State) string {
	out := st.IngressController
	if st.LegacyIngress {
		out += " + legacy Ingress objects"
	}
	switch rec := st.IngressControllerOverride; {
	case rec != "" && st.IngressControllerChange:
		out += "; recorded on the instance: " + rec
	case rec != "":
		out += "; the instance records " + rec + " (--ingress-controller auto clears it)"
	case st.IngressControllerChange:
		out += "; the recorded choice is cleared"
	}
	return out
}

var certificateGVR = schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}

// gatewayCertTimeout bounds the wait for the Gateway's Certificate. Variables, so a test need not wait.
var (
	gatewayCertTimeout = 5 * time.Minute
	gatewayCertPoll    = 3 * time.Second
)

// gatewayCertificateSecret is the ONE predicate for "a cert-manager Certificate is expected for
// this Gateway": its https listener names a Secret AND the Gateway asks cert-manager to fill it
// (an Issuer or ClusterIssuer annotation, or the ACME shorthand). It returns that Secret, or ""
// when nothing is expected (no TLS, or a Secret the operator supplies). Read off the Gateway
// itself, so it is what is deployed, not a second reading of the flags. The wait after an
// upgrade and the drop/remove checks both ask it.
func gatewayCertificateSecret(g *unstructured.Unstructured) string {
	secret := gatewayListenerSecret(g.Object)
	ann := g.GetAnnotations()
	if secret != "" && (ann["cert-manager.io/issuer"] != "" || ann["cert-manager.io/cluster-issuer"] != "" ||
		ann["kubernetes.io/tls-acme"] == "true") {
		return secret
	}
	return ""
}

// servingTLSSecret is the Secret whose ca.crt a client that should verify the instance trusts: the
// one the front end serving the host presents. While the Ingress objects still render they serve
// it, on the chart's name; the Gateway alone serves its own.
func servingTLSSecret(st *State) string {
	if st.IngressController == frontEndGateway && !st.LegacyIngress {
		return gatewaySecretOf(st)
	}
	return defaultTLSSecret
}

// gatewaySecretOf is the Secret the Gateway's listener names for this run.
func gatewaySecretOf(st *State) string {
	if st.GatewayTLSSecret != "" {
		return st.GatewayTLSSecret
	}
	return defaultTLSSecret
}

// gatewayCertificateState looks through an instance namespace's Certificates for the one issuing
// into secret, and says whether it is the Gateway's and Ready.
//
// 🔴 "A CERTIFICATE EXISTS FOR THAT SECRET" IS NOT ENOUGH. cert-manager's Gateway support refuses a
// Certificate it does not control and does not come back to it, so a Certificate left over from an
// Ingress (or made by hand) under the Gateway's Secret name leaves the Gateway with no certificate
// of its own, silently, while something named right sits in the namespace. It has to be controlled
// BY THE GATEWAY, and Ready.
func gatewayCertificateState(certs []unstructured.Unstructured, secret string) (ready bool, why string) {
	for i := range certs {
		c := &certs[i]
		if got, _, _ := unstructured.NestedString(c.Object, "spec", "secretName"); got != secret {
			continue
		}
		owned := false
		for _, o := range c.GetOwnerReferences() {
			if o.Kind == "Gateway" && o.Name == instanceGatewayName && o.Controller != nil && *o.Controller {
				owned = true
			}
		}
		if !owned {
			return false, fmt.Sprintf("Certificate %s issues into Secret %s but is not controlled by the Gateway %s, "+
				"and cert-manager will not adopt it. Delete it (kubectl -n %s delete certificate %s) so the Gateway's own is created",
				c.GetName(), secret, instanceGatewayName, c.GetNamespace(), c.GetName())
		}
		conds, _, _ := unstructured.NestedSlice(c.Object, "status", "conditions")
		for _, cond := range conds {
			if cm, ok := cond.(map[string]interface{}); ok && cm["type"] == "Ready" {
				if cm["status"] == "True" {
					return true, ""
				}
				return false, fmt.Sprintf("Certificate %s is not Ready: %v", c.GetName(), cm["message"])
			}
		}
		return false, fmt.Sprintf("Certificate %s has not reported Ready yet", c.GetName())
	}
	return false, fmt.Sprintf("no Certificate issues into Secret %s: cert-manager has not created the Gateway's (is its Gateway "+
		"support on? `kubectl -n cert-manager logs deploy/cert-manager` says)", secret)
}

// gatewayCertLegacyTimeout bounds the same wait while the instance's Ingress objects are still
// rendered. Short, because it can only ever end in a warning: see waitForGatewayCertificate.
var gatewayCertLegacyTimeout = 30 * time.Second

// waitForGatewayCertificate holds an upgrade that put an instance on the Gateway until the
// Gateway has a certificate of its own, and fails with the reason when it does not. The services
// being rolled out proves nothing about this: a Gateway with no certificate is a green release.
//
// 🔴 WHILE THE INGRESS OBJECTS ARE STILL RENDERED THE GATE ONLY WARNS. The Gateway serves nothing
// until DNS moves, and the host is being served, certificate and all, by the Ingress objects, so
// an upgrade into the window is healthy whether or not the Gateway's certificate exists yet. A
// solver that can only succeed once DNS points at the new controller (HTTP-01, most of them)
// cannot issue it before the operator has moved DNS, which is a step after this upgrade; failing
// here would make the cutover impossible to start. The certificate is instead a precondition of
// the step that makes the Gateway the only front end: --drop-legacy-ingress refuses until it is
// Ready (settleFrontEnd), and --remove-legacy-ingress re-checks every instance.
func waitForGatewayCertificate(ctx context.Context, dyn dynamic.Interface, st *State) error {
	if st.IngressController != frontEndGateway {
		return nil
	}
	ns := InstanceNamespace(st.Instance)
	g, err := getGatewayAPIObject(ctx, dyn, gatewayGVR, ns, instanceGatewayName)
	if err != nil {
		return fail("reading the instance's Gateway", err)
	}
	if g == nil {
		return nil
	}
	secret := gatewayCertificateSecret(g)
	if secret == "" {
		return nil
	}
	timeout := gatewayCertTimeout
	if st.LegacyIngress {
		timeout = gatewayCertLegacyTimeout
	}
	doing("waiting for the Gateway's certificate")
	deadline := time.Now().Add(timeout)
	for {
		list, err := dyn.Resource(certificateGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
		var why string
		switch {
		case err != nil && (apierrors.IsNotFound(err) || meta.IsNoMatchError(err)):
			why = "cert-manager's Certificate API is not installed on this cluster"
		case err != nil:
			return fail("waiting for the Gateway's certificate", err)
		default:
			var ready bool
			if ready, why = gatewayCertificateState(list.Items, secret); ready {
				var ierr error
				if ready, why, ierr = gatewayCertificateIssued(ctx, dyn, g, secret); ierr != nil {
					return fail("waiting for the Gateway's certificate", ierr)
				}
			}
			if ready {
				done()
				return nil
			}
		}
		if time.Now().After(deadline) {
			if st.LegacyIngress {
				fmt.Println(color.YellowString("not ready yet."))
				fmt.Println(color.YellowString("  warning: the Gateway has no certificate yet: %s.\n"+
					"  The host is unaffected: its Ingress objects still serve it, with their own certificate. The Gateway's "+
					"certificate may only be issuable once DNS points at the new controller (HTTP-01 needs the host to reach it), "+
					"so move DNS, then check `kubectl -n %s get certificate`. `dcctl upgrade --drop-legacy-ingress` refuses "+
					"until it is Ready.", why, ns))
				return nil
			}
			return fail("waiting for the Gateway's certificate", fmt.Errorf("%s. The instance's services are up, but its "+
				"Gateway has no certificate, so https answers with the controller's default one until it does; "+
				"re-run the upgrade once it is fixed", why))
		}
		select {
		case <-ctx.Done():
			return fail("waiting for the Gateway's certificate", ctx.Err())
		case <-time.After(gatewayCertPoll):
		}
	}
}

// gatewayCertVerdict is what one instance's Gateway says about its own certificate.
type gatewayCertVerdict struct {
	Namespace string
	// Gateway is whether the instance has a Gateway at all.
	Gateway bool
	// Expected is whether cert-manager is to issue its certificate (the Gateway carries an issuer
	// annotation). A Gateway with a Secret the operator supplies has nothing to wait for.
	Expected bool
	Ready    bool
	Why      string
}

// readGatewayCertificates reads the Gateway certificate verdict of one instance, or of every
// instance on the cluster when instance is empty. Indirected like the other cluster reads.
var readGatewayCertificates = func(ctx context.Context, kubeContext, instance string) ([]gatewayCertVerdict, error) {
	dyn, _, _, err := kubeClients(kubeContext)
	if err != nil {
		return nil, fmt.Errorf("connecting to the cluster to read the Gateway's certificate: %w", err)
	}
	return gatewayCertificatesFrom(ctx, dyn, instance)
}

// gatewayCertificatesFrom is readGatewayCertificates' read, over a client.
func gatewayCertificatesFrom(ctx context.Context, dyn dynamic.Interface, instance string) ([]gatewayCertVerdict, error) {
	var gws []unstructured.Unstructured
	if instance != "" {
		obj, err := getGatewayAPIObject(ctx, dyn, gatewayGVR, InstanceNamespace(instance), instanceGatewayName)
		if err != nil {
			return nil, fmt.Errorf("looking for this instance's Gateway: %w", err)
		}
		if obj == nil {
			return []gatewayCertVerdict{{Namespace: InstanceNamespace(instance)}}, nil
		}
		gws = append(gws, *obj)
	} else {
		list, err := dyn.Resource(gatewayGVR).Namespace(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
		switch {
		case err == nil:
			for _, g := range list.Items {
				if g.GetName() == instanceGatewayName {
					gws = append(gws, g)
				}
			}
		case apierrors.IsNotFound(err), meta.IsNoMatchError(err):
		default:
			return nil, fmt.Errorf("listing Gateways: %w", err)
		}
	}
	var out []gatewayCertVerdict
	for i := range gws {
		g := &gws[i]
		v := gatewayCertVerdict{Namespace: g.GetNamespace(), Gateway: true}
		secret := gatewayCertificateSecret(g)
		v.Expected = secret != ""
		if v.Expected {
			certs, err := dyn.Resource(certificateGVR).Namespace(v.Namespace).List(ctx, metav1.ListOptions{})
			switch {
			case err == nil:
				v.Ready, v.Why = gatewayCertificateState(certs.Items, secret)
				if v.Ready {
					var ierr error
					if v.Ready, v.Why, ierr = gatewayCertificateIssued(ctx, dyn, g, secret); ierr != nil {
						return nil, ierr
					}
				}
			case apierrors.IsNotFound(err), meta.IsNoMatchError(err):
				v.Why = "cert-manager's Certificate API is not installed on this cluster"
			default:
				return nil, fmt.Errorf("listing Certificates in %s: %w", v.Namespace, err)
			}
		}
		out = append(out, v)
	}
	return out, nil
}

// refuseUnlessGatewayCertificatesReady is the precondition of making the Gateway the only front
// end: every verdict that expects a certificate has a Ready one the Gateway controls. A
// verdict for an instance with no Gateway is a refusal only when missingIsRefusal (the instance
// is being moved off its Ingress objects, which needs a Gateway to move to).
func refuseUnlessGatewayCertificatesReady(vs []gatewayCertVerdict, missingIsRefusal bool, what, remedy string) error {
	var bad []string
	for _, v := range vs {
		switch {
		case !v.Gateway:
			if missingIsRefusal {
				bad = append(bad, fmt.Sprintf("%s: the instance has no Gateway yet", v.Namespace))
			}
		case v.Expected && !v.Ready:
			bad = append(bad, fmt.Sprintf("%s: %s", v.Namespace, v.Why))
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("%s:\n  %s\n%s Nothing has been changed", what, strings.Join(bad, "\n  "), remedy)
}
