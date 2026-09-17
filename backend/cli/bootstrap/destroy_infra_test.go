// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// stubLiveInstanceInfrastructure replaces the cluster probe behind the empty-state
// refusal with a fixed answer.
func stubLiveInstanceInfrastructure(t *testing.T, found []string) {
	t.Helper()
	orig := probeLiveInstanceInfrastructure
	t.Cleanup(func() { probeLiveInstanceInfrastructure = orig })
	probeLiveInstanceInfrastructure = func(context.Context, string, string) ([]string, error) {
		return found, nil
	}
}

var deleting = metav1.NewTime(time.Unix(1_700_000_000, 0))

func natsStatefulSet(ns string, terminating bool) *appsv1.StatefulSet {
	s := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: natsStatefulSetName, Namespace: ns}}
	if terminating {
		s.DeletionTimestamp = &deleting
		s.Finalizers = []string{"test"}
	}
	return s
}

func helmStorageSecret(ns, release string, terminating bool) *corev1.Secret {
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "sh.helm.release.v1." + release + ".v1",
		Namespace: ns,
		Labels:    map[string]string{"owner": "helm", "name": release},
	}}
	if terminating {
		s.DeletionTimestamp = &deleting
		s.Finalizers = []string{"test"}
	}
	return s
}

func tsdbCluster(ns string, terminating bool) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("postgresql.cnpg.io/v1")
	u.SetKind("Cluster")
	u.SetName(tsdbClusterName)
	u.SetNamespace(ns)
	if terminating {
		u.SetDeletionTimestamp(&deleting)
		u.SetFinalizers([]string{"test"})
	}
	return u
}

func dynamicWith(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{clusterGVR: "ClusterList"}, objs...)
}

// 🔴 THE PROBE BEHIND THE EMPTY-STATE REFUSAL. Present means running and not on its way
// out; everything a legitimate resume leaves behind has to read as absent, or the destroy
// that should finish the job is refused by its own leftovers.
func TestTheEmptyStateProbeFindsOnlyLiveInfrastructure(t *testing.T) {
	const id = "acme"
	for _, tc := range []struct {
		name  string
		typed []runtime.Object
		dyn   []runtime.Object
		want  []string
	}{
		{name: "nothing there"},
		{name: "a running broker", typed: []runtime.Object{natsStatefulSet(id, false)},
			want: []string{"StatefulSet acme/dc-nats"}},
		{name: "a terminating broker", typed: []runtime.Object{natsStatefulSet(id, true)}},
		{name: "a running event store", dyn: []runtime.Object{tsdbCluster(id, false)},
			want: []string{"CloudNativePG Cluster acme/dc-tsdb"}},
		{name: "a terminating event store", dyn: []runtime.Object{tsdbCluster(id, true)}},
		// A release whose workloads are gone but whose record is not is still a release.
		{name: "an event store Helm record only", typed: []runtime.Object{helmStorageSecret(id, "dc-tsdb", false)},
			want: []string{"Helm release acme/dc-tsdb"}},
		{name: "a broker Helm record only", typed: []runtime.Object{helmStorageSecret(id, "dc-nats", false)},
			want: []string{"Helm release acme/dc-nats"}},
		{name: "a terminating Helm record", typed: []runtime.Object{helmStorageSecret(id, "dc-tsdb", true)}},
		// The chart's own release is not the instance root's.
		{name: "another release's Helm record", typed: []runtime.Object{helmStorageSecret(id, "dc-acme", false)}},
		// 🔴 NEVER PVCs: they outlive their workloads by design.
		{name: "a leftover PVC", typed: []runtime.Object{&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "dc-nats-js-dc-nats-0", Namespace: id}}}},
		// 🔴 Built before each instance had its own namespace: the broker and event store
		// run in the shared one, and only such an instance can put them there.
		{name: "a pre-namespace broker", typed: []runtime.Object{natsStatefulSet(infraNamespace, false)},
			want: []string{"StatefulSet dc-system/dc-nats"}},
		{name: "a pre-namespace event store", dyn: []runtime.Object{tsdbCluster(infraNamespace, false)},
			want: []string{"CloudNativePG Cluster dc-system/dc-tsdb"}},
		{name: "a terminating pre-namespace broker", typed: []runtime.Object{natsStatefulSet(infraNamespace, true)}},
		// The bystander: the same objects in another instance's namespace are not this one's.
		{name: "another instance's infrastructure",
			typed: []runtime.Object{natsStatefulSet("other", false), helmStorageSecret("other", "dc-tsdb", false)},
			dyn:   []runtime.Object{tsdbCluster("other", false)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := liveInstanceInfrastructure(context.Background(),
				fake.NewSimpleClientset(tc.typed...), dynamicWith(tc.dyn...), id)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("found %q, want %q", got, tc.want)
			}
		})
	}
}

// A cluster with no CloudNativePG CRD cannot hold an event store. The API server reports
// a missing TYPE as a 404 with no object details, and a RESTMapper-backed client as a
// NoMatch — both are absent, never an error that blocks the destroy.
func TestTheEmptyStateProbeReadsAMissingCRDAsAbsent(t *testing.T) {
	for name, crdErr := range map[string]error{
		"404 with no details": apierrors.NewNotFound(schema.GroupResource{}, ""),
		"no kind match":       &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "postgresql.cnpg.io", Kind: "Cluster"}},
	} {
		t.Run(name, func(t *testing.T) {
			dyn := dynamicWith()
			dyn.PrependReactor("get", "clusters", func(k8stesting.Action) (bool, runtime.Object, error) {
				return true, nil, crdErr
			})
			got, err := liveInstanceInfrastructure(context.Background(), fake.NewSimpleClientset(), dyn, "acme")
			if err != nil || len(got) != 0 {
				t.Errorf("a missing CRD: found %q, err %v; want nothing and no error", got, err)
			}
		})
	}
	// The counterweight: any OTHER failure to ask is not "absent".
	dyn := dynamicWith()
	dyn.PrependReactor("get", "clusters", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(clusterGVR.GroupResource(), tsdbClusterName, errors.New("no"))
	})
	if _, err := liveInstanceInfrastructure(context.Background(), fake.NewSimpleClientset(), dyn, "acme"); err == nil {
		t.Error("a forbidden read was treated as an absent event store")
	}
}

// writeInstanceRootState plants a state document where the instance root keeps it.
func writeInstanceRootState(t *testing.T, home, instance, rel, doc string) {
	t.Helper()
	p := filepath.Join(home, ".devicechain", "instances", instance, "infra", rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
}

const (
	stateWithAResource = `{"version":4,"resources":[{"mode":"managed","type":"helm_release","name":"nats",` +
		`"module":"module.nats","instances":[{"attributes":{}}]}]}`
	stateWithDataOnly = `{"version":4,"resources":[{"mode":"data","type":"kubernetes_resources",` +
		`"name":"legacy_db_statefulsets","instances":[{"attributes":{}}]}]}`
	stateAfterDestroy = `{"version":4,"resources":[]}`
)

// 🔴 EMPTY STATE OVER A RUNNING INSTANCE IS REFUSED, and only then. Each row says what
// the state holds and what the cluster probe would find.
func TestTheEmptyStateRefusal(t *testing.T) {
	const id = "acme"
	running := []string{"StatefulSet acme/dc-nats"}
	for _, tc := range []struct {
		name       string
		rel, doc   string // "" = no state file at all
		live       []string
		wantErr    bool
		wantHas    bool
		wantProbed bool
	}{
		{name: "no state, nothing running", wantProbed: true},
		{name: "no state, broker running", live: running, wantErr: true, wantProbed: true},
		// A destroy that finished tofu destroy and died after it: resumable.
		{name: "emptied state, nothing running", rel: "instance/terraform.tfstate", doc: stateAfterDestroy, wantProbed: true},
		{name: "emptied state, broker running", rel: "instance/terraform.tfstate", doc: stateAfterDestroy,
			live: running, wantErr: true, wantProbed: true},
		{name: "data sources only, broker running", rel: "instance/terraform.tfstate", doc: stateWithDataOnly,
			live: running, wantErr: true, wantProbed: true},
		{name: "guards only, broker running", rel: "instance/terraform.tfstate",
			doc:  `{"version":4,"resources":[{"mode":"managed","type":"terraform_data","name":"cutover_guard","instances":[{"index_key":"tsdb"}]}]}`,
			live: running, wantErr: true, wantProbed: true},
		// State lists something: tofu destroy has work, and the cluster is not asked.
		{name: "state with resources", rel: "instance/terraform.tfstate", doc: stateWithAResource,
			live: running, wantHas: true},
		{name: "pre-relocation state with resources", rel: "terraform.tfstate", doc: stateWithAResource,
			live: running, wantHas: true},
		// Fails closed: "cannot read" is never "empty".
		{name: "unparseable state", rel: "instance/terraform.tfstate", doc: "{not json", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := fakeHome(t)
			if tc.rel != "" {
				writeInstanceRootState(t, home, id, tc.rel, tc.doc)
			}
			probed := false
			orig := probeLiveInstanceInfrastructure
			t.Cleanup(func() { probeLiveInstanceInfrastructure = orig })
			probeLiveInstanceInfrastructure = func(_ context.Context, _, instance string) ([]string, error) {
				probed = true
				if instance != id {
					t.Errorf("probed instance %q", instance)
				}
				return tc.live, nil
			}

			has, err := refuseUndestroyableInstance(context.Background(), "kind-x", id)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "--without-state") {
				t.Errorf("the refusal does not name --without-state: %v", err)
			}
			if has != tc.wantHas {
				t.Errorf("stateHasResources = %v, want %v", has, tc.wantHas)
			}
			if probed != tc.wantProbed {
				t.Errorf("probed = %v, want %v", probed, tc.wantProbed)
			}
		})
	}
}

// 🔴 THE REFUSAL COMES BEFORE ANY CHANGE, AND --without-state SKIPS IT. Driven through
// Destroy: a refused destroy never reaches the uninstall and keeps every byte of local
// state; the override never asks the cluster about state at all.
func TestDestroyRefusesAnEmptyStateBeforeChangingAnything(t *testing.T) {
	home := fakeHome(t)
	writeRecord(t, InstanceRecord{Instance: "acme", Provider: "local", Cluster: "c", KubeContext: "kind-c", Managed: true})
	p := &fakeProvider{name: "local", present: map[string]bool{"c": true}}
	stubLiveInstanceInfrastructure(t, []string{"CloudNativePG Cluster acme/dc-tsdb"})

	var err error
	out := captureOutput(t, func() {
		err = Destroy(context.Background(), p, DestroyOptions{Options: Options{Instance: "acme", AssumeYes: true}})
	})
	if err == nil || !strings.Contains(err.Error(), "--without-state") {
		t.Fatalf("want the empty-state refusal, got %v", err)
	}
	if strings.Contains(out, "uninstalling instance release") {
		t.Errorf("the refusal came after the uninstall began:\n%s", out)
	}
	if _, statErr := os.Stat(filepath.Join(home, ".devicechain", "instances", "acme", instanceRecordFile)); statErr != nil {
		t.Errorf("a refused destroy removed local state: %v", statErr)
	}
}

func TestWithoutStateNeverAsksWhetherTheStateIsEmpty(t *testing.T) {
	fakeHome(t)
	writeRecord(t, InstanceRecord{Instance: "acme", Provider: "local", Cluster: "c", KubeContext: "kind-c", Managed: true})
	p := &fakeProvider{name: "local", present: map[string]bool{"c": true}}
	orig := probeLiveInstanceInfrastructure
	t.Cleanup(func() { probeLiveInstanceInfrastructure = orig })
	probeLiveInstanceInfrastructure = func(context.Context, string, string) ([]string, error) {
		t.Error("--without-state still ran the empty-state probe")
		return nil, nil
	}

	var err error
	out := captureOutput(t, func() {
		err = Destroy(context.Background(), p, DestroyOptions{Options: Options{Instance: "acme", AssumeYes: true}, WithoutState: true})
	})
	// The uninstall reaches a cluster that is not there and fails; reaching it is the point.
	if err == nil || !strings.Contains(out, "uninstalling instance release") {
		t.Errorf("--without-state did not go on to the uninstall (err %v):\n%s", err, out)
	}
}

// The dry run describes the teardown that would run, and the override when given.
func TestTheDryRunDescribesTheInstanceRootDestroy(t *testing.T) {
	fakeHome(t)
	writeRecord(t, InstanceRecord{Instance: "acme", Provider: "local", Cluster: "c", KubeContext: "kind-c", Managed: true})
	p := &fakeProvider{name: "local", present: map[string]bool{"c": true}}
	for without, want := range map[bool]string{false: "run tofu destroy", true: "SKIP tofu destroy (--without-state)"} {
		out := captureOutput(t, func() {
			_ = Destroy(context.Background(), p, DestroyOptions{Options: Options{Instance: "acme", DryRun: true}, WithoutState: without})
		})
		if !strings.Contains(out, want) || !strings.Contains(out, "wait until it is gone") {
			t.Errorf("without-state=%v: dry run does not say %q:\n%s", without, want, out)
		}
	}
}

// 🔴 A DESTROY THAT SKIPPED tofu destroy DOES NOT CLOSE GREEN, and says what it skipped.
func TestTheWithoutStateClosingLineSaysTofuDestroyWasSkipped(t *testing.T) {
	line := destroyedLine("acme", "c", "", true)
	if !strings.Contains(line, "WITHOUT tofu destroy") || strings.Contains(line, "destroyed;") {
		t.Errorf("closing line = %q", line)
	}
	if want := destroyedLine("acme", "c", "", false); line == want {
		t.Error("the override's closing line is the complete destroy's")
	}
	if l := destroyedLine("acme", "c", "not installed", true); !strings.Contains(l, "LEFT on the shared relational store: not installed") {
		t.Errorf("the override's line drops the database note: %q", l)
	}
}

// recordingTofu is a destroyTofu that records what it was asked, in order.
type recordingTofu struct {
	stateLister
	calls      *[]string
	destroyErr error
}

func (r recordingTofu) StateRm(_ context.Context, address string, _ ...tfexec.StateRmCmdOption) error {
	*r.calls = append(*r.calls, "state rm "+address)
	return nil
}

func (r recordingTofu) Destroy(_ context.Context, opts ...tfexec.DestroyOption) error {
	var vars []string
	for _, o := range opts {
		// VarOption's assignment is unexported; its printed form carries it.
		vars = append(vars, strings.Trim(fmt.Sprintf("%v", o), "&{}"))
	}
	*r.calls = append(*r.calls, "destroy "+strings.Join(vars, " "))
	return r.destroyErr
}

func recordingUninstall(calls *[]string, err error) providerReleaseUninstaller {
	return func(_ context.Context, kubeContext, namespace, name string) (bool, error) {
		*calls = append(*calls, fmt.Sprintf("uninstall %s %s/%s", kubeContext, namespace, name))
		return err == nil, err
	}
}

// statePlus is a state holding the given plain addresses plus, optionally, the event
// store release in a given namespace.
type statePlus struct {
	addresses []string
	tsdbNs    string
}

func (s statePlus) Show(ctx context.Context, _ ...tfexec.ShowOption) (*tfjson.State, error) {
	st, _ := (&fakeState{addresses: s.addresses}).Show(ctx)
	if s.tsdbNs != "" {
		st.Values.RootModule.ChildModules = append(st.Values.RootModule.ChildModules, &tfjson.StateModule{
			Resources: []*tfjson.StateResource{{Address: tsdbReleaseAddress,
				AttributeValues: map[string]any{"namespace": s.tsdbNs, "name": "dc-tsdb"}}}})
	}
	return st, nil
}

// 🔴 THE ORDER PAST prevent_destroy, AND ITS RESUME. Uninstall where the state says the
// release is, remove the entry only when listed, then a plain destroy carrying BOTH vars.
func TestTheInstanceRootTeardownSequence(t *testing.T) {
	destroy := "destroy kubeconfig_context=kind-x instance_namespace=acme"
	for _, tc := range []struct {
		name  string
		state stateLister
		want  []string
	}{
		{name: "a live instance", state: statePlus{addresses: []string{"module.nats.helm_release.nats"}, tsdbNs: "acme"},
			want: []string{"uninstall kind-x acme/dc-tsdb", "state rm " + tsdbReleaseAddress, destroy}},
		// Resumed after the entry was removed: no state rm (it would exit 1), and the
		// uninstall still runs, tolerating a release already gone.
		{name: "resumed after state rm", state: statePlus{addresses: []string{"module.nats.helm_release.nats"}},
			want: []string{"uninstall kind-x acme/dc-tsdb", destroy}},
		// 🔴 Built before namespaces: the namespace fence is NOT run here, and the release
		// is uninstalled where it actually is.
		{name: "built in the shared namespace", state: statePlus{tsdbNs: infraNamespace},
			want: []string{"uninstall kind-x dc-system/dc-tsdb", "state rm " + tsdbReleaseAddress, destroy}},
		// 🔴 Retired-infrastructure addresses are what destroy exists to remove.
		{name: "holding retired infrastructure", state: statePlus{addresses: []string{retiredStateAddresses[0]}},
			want: []string{"uninstall kind-x acme/dc-tsdb", destroy}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			err := destroyOpenedInstanceRoot(context.Background(), recordingTofu{stateLister: tc.state, calls: &calls},
				"kind-x", "acme", recordingUninstall(&calls, nil))
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(calls, tc.want) {
				t.Errorf("calls:\n  %s\nwant:\n  %s", strings.Join(calls, "\n  "), strings.Join(tc.want, "\n  "))
			}
		})
	}
}

// 🔴 A PRE-SPLIT STATE IS REFUSED BEFORE ANYTHING, and the refusal names the override —
// destroying that state would take the shared relational database.
func TestTheInstanceRootTeardownRefusesPreSplitState(t *testing.T) {
	var calls []string
	err := destroyOpenedInstanceRoot(context.Background(),
		recordingTofu{stateLister: statePlus{addresses: []string{"module.cnpg_rdb.helm_release.cluster"}, tsdbNs: "acme"}, calls: &calls},
		"kind-x", "acme", recordingUninstall(&calls, nil))
	if err == nil || !strings.Contains(err.Error(), "--without-state") || !strings.Contains(err.Error(), "module.cnpg_rdb.helm_release.cluster") {
		t.Errorf("want the pre-split refusal naming --without-state, got %v", err)
	}
	if len(calls) != 0 {
		t.Errorf("the refusal came after %q", calls)
	}
}

// A failed uninstall stops before the state entry goes, so the re-run still knows where
// the release is.
func TestAFailedEventStoreUninstallKeepsItsStateEntry(t *testing.T) {
	var calls []string
	err := destroyOpenedInstanceRoot(context.Background(),
		recordingTofu{stateLister: statePlus{tsdbNs: "acme"}, calls: &calls},
		"kind-x", "acme", recordingUninstall(&calls, errors.New("boom")))
	if err == nil {
		t.Fatal("a failed uninstall was swallowed")
	}
	if !slices.Equal(calls, []string{"uninstall kind-x acme/dc-tsdb"}) {
		t.Errorf("went on after a failed uninstall: %q", calls)
	}
}

// 🔴 WHICH FENCES A DESTROY RUNS is held by the source: the teardown must not open the
// root through openInstanceRoot, nor call the apply-path fences whose remedy is destroy.
func TestDestroyRunsOnlyThePreSplitFence(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "destroy_infra.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	called := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok {
				called[id.Name] = true
			}
		}
		return true
	})
	for _, forbidden := range []string{"openInstanceRoot", "checkNoRetiredInfrastructure",
		"checkInstanceInItsOwnNamespace", "checkHaNotTornDown", "checkNoPreSplitInfrastructure"} {
		if called[forbidden] {
			t.Errorf("the destroy path calls %s", forbidden)
		}
	}
	for _, required := range []string{"checkDestroyFences", "stateAddressesPresent", "relocateRootState",
		"removeSupersededRootConfig", "hardenStateFiles", "destroyOpenedInstanceRoot"} {
		if !called[required] {
			t.Errorf("the destroy path no longer calls %s", required)
		}
	}
}

// 🔴 THE NAMESPACE WAIT. Gone is success; still there at the deadline is an error that
// says why, so Destroy returns before removing the local state a resume needs.
func TestTheNamespaceWait(t *testing.T) {
	ctx := context.Background()
	if err := waitForNamespaceGone(ctx, fake.NewSimpleClientset(), "acme", time.Second, time.Millisecond); err != nil {
		t.Errorf("an absent namespace: %v", err)
	}

	c := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "acme"}})
	gets := 0
	c.PrependReactor("get", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		gets++
		if gets < 3 {
			return false, nil, nil
		}
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Resource: "namespaces"}, "acme")
	})
	if err := waitForNamespaceGone(ctx, c, "acme", 5*time.Second, time.Millisecond); err != nil {
		t.Errorf("a namespace that finished deleting: %v", err)
	}

	stuck := fake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "acme", DeletionTimestamp: &deleting, Finalizers: []string{"kubernetes"}},
		Status: corev1.NamespaceStatus{Phase: corev1.NamespaceTerminating, Conditions: []corev1.NamespaceCondition{{
			Type: corev1.NamespaceFinalizersRemaining, Status: corev1.ConditionTrue, Reason: "SomeFinalizersRemain",
			Message: "Some content in the namespace has finalizers remaining: example.io/hold in 1 resource instances",
		}}},
	})
	err := waitForNamespaceGone(ctx, stuck, "acme", 20*time.Millisecond, time.Millisecond)
	if err == nil {
		t.Fatal("a namespace that never went was reported gone")
	}
	for _, want := range []string{"terminating", "example.io/hold", "Local state has been kept"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the timeout does not say %q: %v", want, err)
		}
	}
}

// A namespace already terminating is waited on, not deleted again: the API server
// answers that delete with a Conflict.
func TestATerminatingInstanceNamespaceIsWaitedOnNotDeletedAgain(t *testing.T) {
	c := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: "acme", Labels: map[string]string{"devicechain.io/instance": "acme"},
		DeletionTimestamp: &deleting, Finalizers: []string{"kubernetes"},
	}})
	c.PrependReactor("delete", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "namespaces"}, "acme",
			errors.New("the system is ensuring all content is removed from this namespace"))
	})
	deleted, err := removeInstanceNamespace(context.Background(), c, "acme")
	if err != nil || !deleted {
		t.Errorf("deleted=%v err=%v; want a namespace on its way out reported as going", deleted, err)
	}
	// And one this run did not touch is not waited on.
	if deleted, _ := removeInstanceNamespace(context.Background(), fake.NewSimpleClientset(), "acme"); deleted {
		t.Error("an absent namespace was reported as deleted")
	}
}

func TestManagedResourceCount(t *testing.T) {
	for doc, want := range map[string]int{
		"":                 0,
		stateAfterDestroy:  0,
		stateWithDataOnly:  0,
		stateWithAResource: 1,
		// 🔴 terraform_data is state-only: guards alone describe nothing running, and
		// counting them skipped the live probe.
		`{"resources":[{"mode":"managed","type":"terraform_data","name":"cutover_guard","instances":[{"index_key":"tsdb"}]}]}`: 0,
		`{"resources":[{"mode":"managed","type":"terraform_data","name":"g","instances":[{}]},` +
			`{"mode":"managed","type":"helm_release","name":"nats","instances":[{}]}]}`: 1,
		`{"resources":[{"mode":"managed","instances":[]}]}`: 0,
	} {
		parsed, err := parseStateDocument([]byte(doc))
		if got := parsed.managed(); err != nil || got != want {
			t.Errorf("%s: got %d (%v), want %d", doc, got, err, want)
		}
	}
}
