// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// namespacedState is a state holding the broker and event store releases in given
// namespaces.
type namespacedState struct {
	nats, tsdb string
	err        error
}

func (f namespacedState) Show(context.Context, ...tfexec.ShowOption) (*tfjson.State, error) {
	if f.err != nil {
		return nil, f.err
	}
	release := func(address, ns string) *tfjson.StateResource {
		return &tfjson.StateResource{Address: address, AttributeValues: map[string]any{"namespace": ns}}
	}
	root := &tfjson.StateModule{}
	if f.nats != "" {
		root.ChildModules = append(root.ChildModules, &tfjson.StateModule{
			Resources: []*tfjson.StateResource{release("module.nats.helm_release.nats", f.nats)}})
	}
	if f.tsdb != "" {
		root.ChildModules = append(root.ChildModules, &tfjson.StateModule{
			Resources: []*tfjson.StateResource{release("module.cnpg_tsdb.helm_release.cluster", f.tsdb)}})
	}
	return &tfjson.State{Values: &tfjson.StateValues{RootModule: root}}, nil
}

// 🔴 AN INSTANCE WHOSE BROKER OR EVENT STORE IS IN THE SHARED NAMESPACE IS REFUSED, each
// release on its own — a fence satisfied by one of the two would let the apply replace the
// other.
func TestAnInstanceBuiltInTheSharedNamespaceIsRefused(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []namespacedState{
		{nats: "dc-system", tsdb: "dc-system"},
		{nats: "dc-system", tsdb: "acme"},
		{nats: "acme", tsdb: "dc-system"},
	} {
		err := checkInstanceInItsOwnNamespace(ctx, tc, "acme")
		if err == nil || !strings.Contains(err.Error(), "dcctl destroy acme") {
			t.Errorf("nats=%s tsdb=%s: want a refusal naming the rebuild, got %v", tc.nats, tc.tsdb, err)
		}
	}
	// The counterweights: an instance already in its own namespace, and one with no state.
	if err := checkInstanceInItsOwnNamespace(ctx, namespacedState{nats: "acme", tsdb: "acme"}, "acme"); err != nil {
		t.Errorf("an instance in its own namespace was refused: %v", err)
	}
	if err := checkInstanceInItsOwnNamespace(ctx, namespacedState{}, "acme"); err != nil {
		t.Errorf("a fresh instance was refused: %v", err)
	}
	// Fails closed on a state it cannot read.
	if err := checkInstanceInItsOwnNamespace(ctx, namespacedState{err: errors.New("boom")}, "acme"); err == nil {
		t.Error("an unreadable state was read as nothing to refuse")
	}
}

// The instance's copy of the archive credential is the cluster's, exactly, in the
// instance's namespace — and a copy, so changing it cannot change the cluster's.
func TestTheInstanceArchiveCredentialIsACopyInItsOwnNamespace(t *testing.T) {
	st := &State{Instance: "acme"}
	cluster := ownedSecret{Name: "dc-object-store-credentials", Namespace: infraNamespace, Scope: ownerCluster,
		Data: map[string]string{keyMinioUser: "u", keyMinioPassword: "p"}}
	got := instanceArchiveCredential(st, cluster)
	if got.Namespace != "acme" || got.Scope != ownerInstance || got.Name != cluster.Name {
		t.Errorf("copy = %s/%s scope %q", got.Namespace, got.Name, got.Scope)
	}
	if got.Data[keyMinioUser] != "u" || got.Data[keyMinioPassword] != "p" {
		t.Error("the copy does not carry the cluster credential's keys and values")
	}
	got.Data[keyMinioUser] = "changed"
	if cluster.Data[keyMinioUser] != "u" || cluster.Namespace != infraNamespace {
		t.Error("the copy shares state with the cluster's credential")
	}
}

func svc(ns string, nodePort int32) corev1.Service {
	return corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "dc-nats-mqtt"},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{NodePort: nodePort}}}}
}

func ing(ns, host string) networkingv1.Ingress {
	return networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "dc"},
		Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{Host: host}}}}
}

// 🔴 WHAT ANOTHER INSTANCE HOLDS IS REPORTED; WHAT THIS INSTANCE HOLDS IS NOT. A re-run of
// the instance that owns the port or host must keep it, not be told it is taken.
func TestSingletonsAreAttributedToOtherInstancesOnly(t *testing.T) {
	got := singletonsFrom(
		[]corev1.Service{svc("alpha", localMQTTNodePort), svc("gamma", 30001)},
		[]networkingv1.Ingress{ing("alpha", "localhost"), ing("gamma", "gamma.localhost")},
		"beta", "localhost")
	if got.MQTTNodePortHolder != "alpha" || got.HostHolder != "alpha" {
		t.Errorf("beta sees %+v, want both held by alpha", got)
	}
	own := singletonsFrom(
		[]corev1.Service{svc("alpha", localMQTTNodePort)},
		[]networkingv1.Ingress{ing("alpha", "localhost")},
		"alpha", "localhost")
	if own != (clusterSingletons{}) {
		t.Errorf("alpha's own port and host were reported as taken: %+v", own)
	}
	if err := refuseAHostAnotherInstanceServes(got, "beta", "localhost"); err == nil ||
		!strings.Contains(err.Error(), "--host") {
		t.Errorf("a host another instance serves was not refused with the way out: %v", err)
	}
	if err := refuseAHostAnotherInstanceServes(own, "alpha", "localhost"); err != nil {
		t.Errorf("an instance's own host was refused: %v", err)
	}
}

// The node port goes to the first local instance and to no other.
func TestOnlyTheFirstLocalInstanceAsksForTheMQTTNodePort(t *testing.T) {
	want := "nats_mqtt_node_port=31883"
	first := &State{Instance: "alpha", KubeContext: "kind-dev", Values: map[string]string{}}
	if !slices.Contains(infraVars(first), want) {
		t.Errorf("the first local instance does not ask for the MQTT node port: %v", infraVars(first))
	}
	second := &State{Instance: "beta", KubeContext: "kind-dev", Values: map[string]string{mqttNodePortHolderKey: "alpha"}}
	for _, v := range infraVars(second) {
		if strings.HasPrefix(v, "nats_mqtt_node_port=") {
			t.Errorf("a second instance asks for a node port another instance holds: %s", v)
		}
	}
}

func stubSingletons(t *testing.T, held clusterSingletons, err error) {
	t.Helper()
	orig := readClusterSingletons
	t.Cleanup(func() { readClusterSingletons = orig })
	readClusterSingletons = func(context.Context, string, string, string) (clusterSingletons, error) {
		return held, err
	}
}

// The step, not just the decision: a host another instance serves stops the run, a node
// port another instance holds is recorded for the apply, and "could not tell" stops the run.
func TestTheSingletonStepRefusesAHostAndRecordsTheNodePort(t *testing.T) {
	stubSingletons(t, clusterSingletons{HostHolder: "alpha"}, nil)
	st := &State{Instance: "beta", IngressHost: "localhost", Values: map[string]string{}}
	if err := stepCheckClusterSingletons(context.Background(), st); err == nil ||
		!strings.Contains(err.Error(), "--host") {
		t.Errorf("a host alpha serves was not refused: %v", err)
	}

	stubSingletons(t, clusterSingletons{MQTTNodePortHolder: "alpha"}, nil)
	st = &State{Instance: "beta", IngressHost: "beta.localhost", KubeContext: "kind-alpha", Values: map[string]string{}}
	if err := stepCheckClusterSingletons(context.Background(), st); err != nil {
		t.Fatalf("a held node port refused the run instead of building without it: %v", err)
	}
	for _, v := range infraVars(st) {
		if strings.HasPrefix(v, "nats_mqtt_node_port=") {
			t.Errorf("the step did not reach the apply: beta still asks for %s", v)
		}
	}

	stubSingletons(t, clusterSingletons{}, errors.New("forbidden"))
	st = &State{Instance: "beta", Values: map[string]string{}}
	if err := stepCheckClusterSingletons(context.Background(), st); err == nil {
		t.Error("a cluster that would not say what it holds was read as holding nothing")
	}
}

// 🔴 THE STEP COMES BEFORE THE FIRST WRITE. A refusal after the operator install or the
// declaration leaves a declared instance that was never built.
func TestTheSingletonStepRunsBeforeAnythingIsWritten(t *testing.T) {
	var names []string
	for _, s := range NewDefaultPipeline().Steps {
		names = append(names, s.Name)
	}
	check := slices.Index(names, "Check what other instances hold")
	core := slices.Index(names, "Install core components")
	declare := slices.Index(names, "Declare the instance")
	if check < 0 || check > core || check > declare {
		t.Errorf("the singleton check is at %d, after the first write (core %d, declare %d): %v", check, core, declare, names)
	}
}

// The namespace fence is only a fence if openInstanceRoot runs it — which needs a tofu
// binary and state to reach, so the call is held by the source.
func TestOpenInstanceRootRunsTheNamespaceFence(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "tofu.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range file.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "openInstanceRoot" {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "checkInstanceInItsOwnNamespace" {
					found = true
				}
			}
			return true
		})
	}
	if !found {
		t.Error("openInstanceRoot no longer runs checkInstanceInItsOwnNamespace, so an instance built in the " +
			"shared namespace has its broker and event store replaced by the apply")
	}
}
