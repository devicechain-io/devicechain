// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

// teardownRig is a whole destroy, driven through Destroy with every cluster seam
// replaced: Helm's in-memory storage, fake typed and dynamic clients, and a recording
// stand-in for `tofu destroy`. What it records is the ORDER the steps ran in.
type teardownRig struct {
	home   string
	store  *storage.Storage
	typed  *fake.Clientset
	calls  []string
	tofuAt []string // the releases still stored when tofu destroy ran
}

// newTeardownRig builds a cluster holding the given releases, namespaces and
// declarations, with a record for instance "acme" whose infrastructure state lists a
// resource (so tofu destroy has work).
func newTeardownRig(t *testing.T, rels []*release.Release, namespaces []string, declared ...string) *teardownRig {
	t.Helper()
	r := &teardownRig{home: fakeHome(t)}
	unreachableKubeContext(t) // the lock and the declaration release fail fast, and say so
	writeRecord(t, InstanceRecord{Instance: "acme", Provider: "local", Cluster: "c", KubeContext: "kind-c", Managed: true})
	writeInstanceRootState(t, r.home, "acme", "instance/terraform.tfstate", stateWithAResource)
	stubLiveInstanceInfrastructure(t, nil)

	cfg, store := inMemoryHelm(t, rels...)
	r.store = store
	restoreHelm := helmActionConfigFor
	t.Cleanup(func() { helmActionConfigFor = restoreHelm })
	helmActionConfigFor = func(string) (*action.Configuration, error) { return cfg, nil }

	objs := []runtime.Object{&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: types.UID("cluster-uid")}}}
	for _, ns := range namespaces {
		objs = append(objs, namespaceNamed(ns))
	}
	r.typed = fake.NewSimpleClientset(objs...)
	deleted := map[string]bool{}
	r.typed.PrependReactor("get", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		r.calls = append(r.calls, "read install record")
		return false, nil, nil
	})
	r.typed.PrependReactor("delete", "namespaces", func(a k8stesting.Action) (bool, runtime.Object, error) {
		name := a.(k8stesting.DeleteAction).GetName()
		deleted[name] = true
		r.calls = append(r.calls, "delete namespace "+name)
		return false, nil, nil
	})
	r.typed.PrependReactor("get", "namespaces", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if name := a.(k8stesting.GetAction).GetName(); deleted[name] {
			r.calls = append(r.calls, "wait for namespace "+name)
		}
		return false, nil, nil
	})

	var decls []runtime.Object
	for _, id := range declared {
		decls = append(decls, declaredInstance(t, id, func(i *dcv1beta1.Instance) { setPhase(i, dcv1beta1.PhaseDestroying) }))
	}
	dyn := declarationClient(decls...)
	restoreClients := teardownClients
	t.Cleanup(func() { teardownClients = restoreClients })
	teardownClients = func(string) (dynamic.Interface, kubernetes.Interface, error) { return dyn, r.typed, nil }

	restoreTofu := destroyInstanceInfrastructure
	t.Cleanup(func() { destroyInstanceInfrastructure = restoreTofu })
	destroyInstanceInfrastructure = func(_ context.Context, kubeContext, instance string) error {
		r.calls = append(r.calls, fmt.Sprintf("tofu destroy %s %s", kubeContext, instance))
		live, _ := store.ListDeployed()
		for _, rel := range live {
			r.tofuAt = append(r.tofuAt, rel.Name)
		}
		return nil
	}
	return r
}

func (r *teardownRig) destroy(t *testing.T) (string, error) {
	t.Helper()
	p := &fakeProvider{name: "local", present: map[string]bool{"c": true}}
	var err error
	out := captureOutput(t, func() {
		err = Destroy(context.Background(), p, DestroyOptions{Options: Options{Instance: "acme", AssumeYes: true}})
	})
	return out, err
}

func (r *teardownRig) releases(t *testing.T) []string {
	t.Helper()
	live, err := r.store.ListDeployed()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, rel := range live {
		names = append(names, rel.Name)
	}
	slices.Sort(names)
	return names
}

func (r *teardownRig) stateRemoved() bool {
	_, err := os.Stat(filepath.Join(r.home, ".devicechain", "instances", "acme", instanceRecordFile))
	return errors.Is(err, os.ErrNotExist)
}

var wantTeardownOrder = []string{
	"tofu destroy kind-c acme",
	"read install record",
	"delete namespace acme",
	"wait for namespace acme",
}

// inOrder reports whether want appears in calls as a subsequence.
func inOrder(calls, want []string) bool {
	i := 0
	for _, c := range calls {
		if i < len(want) && c == want[i] {
			i++
		}
	}
	return i == len(want)
}

// 🔴 THE SEQUENCE RUNS, NOT JUST THE CALLS EXIST. A source check that uninstallInstance
// names destroyInstanceRoot and waitForNamespaceGone let both a seam that ran nothing
// and a never-taken branch around the wait survive. Driven end to end: the chart release
// is gone before tofu destroy, which runs before the database drop, which runs before
// the namespace delete, which is waited on — and the neighbour is untouched.
func TestUninstallInstanceRunsEveryStepInOrder(t *testing.T) {
	r := newTeardownRig(t,
		[]*release.Release{deviceChainRelease(helmReleaseNameFor("acme"), "acme"), deviceChainRelease(helmReleaseNameFor("b"), "b")},
		[]string{"acme", "b"}, "acme", "b")
	out, err := r.destroy(t)
	if err != nil {
		t.Fatalf("destroy failed: %v\n%s", err, out)
	}
	if !inOrder(r.calls, wantTeardownOrder) {
		t.Errorf("steps ran as:\n  %s\nwant, in order:\n  %s", strings.Join(r.calls, "\n  "), strings.Join(wantTeardownOrder, "\n  "))
	}
	if slices.Contains(r.tofuAt, helmReleaseNameFor("acme")) {
		t.Errorf("tofu destroy ran while the chart release was still installed (%q)", r.tofuAt)
	}
	if got := r.releases(t); !slices.Equal(got, []string{helmReleaseNameFor("b")}) {
		t.Errorf("releases left: %q, want only the neighbour's", got)
	}
	if slices.Contains(r.calls, "delete namespace b") {
		t.Error("the neighbour's namespace was deleted")
	}
	if !r.stateRemoved() {
		t.Error("a destroy that finished kept the local state")
	}
}

// 🔴 A DESTROY RESUMED AFTER ITS CHART UNINSTALL, ON A CLUSTER HOLDING ANOTHER INSTANCE.
// The release is gone, so the only DeviceChain release left is the neighbour's — which
// read as the foreign-release refusal, kept the record, and told the operator to
// destroy the neighbour. An instance that still has its own footprint is a resume.
func TestADestroyResumedAfterItsChartUninstallFinishesOnAMultiInstanceCluster(t *testing.T) {
	for name, footprint := range map[string]struct {
		namespaces []string
		declared   []string
	}{
		"declaration reading Destroying and namespace": {[]string{"acme", "b"}, []string{"acme", "b"}},
		"declaration only (namespace already gone)":    {[]string{"b"}, []string{"acme", "b"}},
		"namespace only (built before declarations)":   {[]string{"acme", "b"}, []string{"b"}},
	} {
		t.Run(name, func(t *testing.T) {
			r := newTeardownRig(t, []*release.Release{deviceChainRelease(helmReleaseNameFor("b"), "b")},
				footprint.namespaces, footprint.declared...)
			out, err := r.destroy(t)
			if err != nil {
				t.Fatalf("the resumed destroy failed: %v\n%s", err, out)
			}
			if strings.Contains(out+err2s(err), "dcctl destroy <provider> b") {
				t.Errorf("the resume told the operator to destroy the neighbour:\n%s", out)
			}
			if !slices.Contains(r.calls, "tofu destroy kind-c acme") {
				t.Errorf("the resume did not run tofu destroy: %q", r.calls)
			}
			if slices.Contains(footprint.namespaces, "acme") && !inOrder(r.calls, wantTeardownOrder) {
				t.Errorf("the resume ran %q", r.calls)
			}
			if got := r.releases(t); !slices.Equal(got, []string{helmReleaseNameFor("b")}) {
				t.Errorf("releases left: %q, want the neighbour's untouched", got)
			}
			if !r.stateRemoved() {
				t.Errorf("the resumed destroy kept the local state:\n%s", out)
			}
			if strings.Contains(out, "was not installed") {
				t.Errorf("a resume was reported as a stale record:\n%s", out)
			}
		})
	}
}

func err2s(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// 🔴 THE NEGATIVE CONTROLS: the resume is for an instance that is HERE. A name with no
// footprint beside another instance is still a stale record, removed without touching
// anything; and a release whose name and values contradict is never resumed over.
func TestOnlyAnInstanceWithAFootprintIsResumed(t *testing.T) {
	t.Run("no footprint is a stale record", func(t *testing.T) {
		r := newTeardownRig(t, []*release.Release{deviceChainRelease(helmReleaseNameFor("b"), "b")}, []string{"b"}, "b")
		out, err := r.destroy(t)
		if err != nil {
			t.Fatalf("err = %v\n%s", err, out)
		}
		if !strings.Contains(out, "was not installed") {
			t.Errorf("a name with nothing here was not reported as a stale record:\n%s", out)
		}
		if slices.Contains(r.calls, "tofu destroy kind-c acme") || slices.Contains(r.calls, "delete namespace b") {
			t.Errorf("a stale record ran the teardown: %q", r.calls)
		}
	})
	t.Run("a contradictory release is refused even with a footprint", func(t *testing.T) {
		r := newTeardownRig(t, []*release.Release{deviceChainRelease(helmReleaseNameFor("acme"), "b")}, []string{"acme"}, "acme")
		out, err := r.destroy(t)
		var foreign *foreignReleaseError
		if !errors.As(err, &foreign) || foreign.Absent {
			t.Fatalf("want the contradiction refusal, got %v\n%s", err, out)
		}
		if len(r.calls) != 0 || r.stateRemoved() {
			t.Errorf("a refused destroy went on: calls %q, state removed %v", r.calls, r.stateRemoved())
		}
	})
}

const preSplitState = `{"version":4,"resources":[` +
	`{"module":"module.cnpg_rdb","mode":"managed","type":"helm_release","name":"cluster","instances":[{"attributes":{}}]},` +
	`{"module":"module.cnpg[0]","mode":"managed","type":"helm_release","name":"cnpg","instances":[{"attributes":{}}]},` +
	`{"mode":"managed","type":"terraform_data","name":"cutover_guard","instances":[{"index_key":"rdb"},{"index_key":"tsdb"}]}]}`

// The addresses read from the file are spelled the way the fence's list spells them.
func TestStateFileAddressesAreSpelledLikeStateList(t *testing.T) {
	doc, err := parseStateDocument([]byte(preSplitState))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"module.cnpg_rdb.helm_release.cluster", "module.cnpg[0].helm_release.cnpg",
		`terraform_data.cutover_guard["rdb"]`, `terraform_data.cutover_guard["tsdb"]`}
	if got := doc.addresses(); !slices.Equal(got, want) {
		t.Errorf("addresses %q, want %q", got, want)
	}
}

// 🔴 THE PRE-SPLIT REFUSAL COMES BEFORE ANYTHING CHANGES. It used to fire only inside
// tofu destroy — after the chart was uninstalled — while saying nothing had been removed.
func TestAPreSplitStateIsRefusedBeforeTheChartIsUninstalled(t *testing.T) {
	r := newTeardownRig(t, []*release.Release{deviceChainRelease(helmReleaseNameFor("acme"), "acme")}, []string{"acme"}, "acme")
	writeInstanceRootState(t, r.home, "acme", "terraform.tfstate", preSplitState)
	probed := false
	probeLiveInstanceInfrastructure = func(context.Context, string, string) ([]string, error) {
		probed = true
		return nil, nil
	}
	out, err := r.destroy(t)
	if err == nil || !strings.Contains(err.Error(), "--without-state") ||
		!strings.Contains(err.Error(), "module.cnpg_rdb.helm_release.cluster") ||
		!strings.Contains(err.Error(), "Nothing has been removed.") {
		t.Fatalf("want the pre-split refusal, got %v", err)
	}
	if strings.Contains(out, "uninstalling instance release") || len(r.calls) != 0 || probed {
		t.Errorf("the refusal came after work began (calls %q, probed %v):\n%s", r.calls, probed, out)
	}
	if got := r.releases(t); len(got) != 1 {
		t.Errorf("the chart release was uninstalled by a refused destroy: %q", got)
	}
	if r.stateRemoved() {
		t.Error("a refused destroy removed local state")
	}

	// 🔑 ITS REMEDY WORKS: --without-state removes the instance, skips tofu destroy, and
	// says what it left.
	var werr error
	wout := captureOutput(t, func() {
		werr = Destroy(context.Background(), &fakeProvider{name: "local", present: map[string]bool{"c": true}},
			DestroyOptions{Options: Options{Instance: "acme", AssumeYes: true}, WithoutState: true})
	})
	if werr != nil {
		t.Fatalf("--without-state over a pre-split state failed: %v\n%s", werr, wout)
	}
	if slices.Contains(r.calls, "tofu destroy kind-c acme") {
		t.Error("--without-state ran tofu destroy")
	}
	if !strings.Contains(wout, "still holds the cluster prerequisites") || !strings.Contains(wout, "WITHOUT tofu destroy") {
		t.Errorf("--without-state did not say what it left:\n%s", wout)
	}
	if len(r.releases(t)) != 0 || !r.stateRemoved() || !slices.Contains(r.calls, "delete namespace acme") {
		t.Errorf("--without-state did not remove the instance: releases %q, calls %q", r.releases(t), r.calls)
	}
}

// The tofu-side fence stays as a second layer, and says the chart is already gone.
func TestTheTofuSidePreSplitFenceSaysTheChartIsGone(t *testing.T) {
	err := checkDestroyFences(context.Background(), &fakeState{addresses: []string{"module.cnpg_rdb.helm_release.cluster"}}, "acme")
	if err == nil || strings.Contains(err.Error(), "Nothing has been removed.") ||
		!strings.Contains(err.Error(), "already been uninstalled") {
		t.Errorf("the second-layer refusal does not say what has happened: %v", err)
	}
	if err := checkDestroyFences(context.Background(), &fakeState{addresses: []string{"module.nats.helm_release.nats"}}, "acme"); err != nil {
		t.Errorf("a post-split state was refused: %v", err)
	}
}

// A post-split state is not refused by the file check either.
func TestAPostSplitStateFileIsNotPreSplit(t *testing.T) {
	home := fakeHome(t)
	writeInstanceRootState(t, home, "acme", "instance/terraform.tfstate", `{"version":4,"resources":[`+
		`{"module":"module.nats","mode":"managed","type":"helm_release","name":"nats","instances":[{}]},`+
		`{"mode":"managed","type":"terraform_data","name":"cutover_guard","instances":[{"index_key":"tsdb"}]}]}`)
	st, err := readInstanceRootState("acme")
	if err != nil || len(st.PreSplit) != 0 || st.Resources != 1 {
		t.Errorf("state = %+v, err %v; want one resource and nothing pre-split", st, err)
	}
}

// 🔴 --without-state OVER A STATE THAT CANNOT BE READ SAYS SO.
func TestWithoutStateWarnsAboutAnUnreadableState(t *testing.T) {
	r := newTeardownRig(t, nil, []string{"acme"}, "acme")
	writeInstanceRootState(t, r.home, "acme", "instance/terraform.tfstate", "{not json")
	var err error
	out := captureOutput(t, func() {
		err = Destroy(context.Background(), &fakeProvider{name: "local", present: map[string]bool{"c": true}},
			DestroyOptions{Options: Options{Instance: "acme", AssumeYes: true}, WithoutState: true})
	})
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if !strings.Contains(out, "cannot be read") || !strings.Contains(out, "parsing the state") {
		t.Errorf("no warning about the unreadable state:\n%s", out)
	}
}

// 🔴 A TRANSIENT READ ERROR DURING THE WAIT IS NOT THE ANSWER. It used to abort the wait
// and be reported as a namespace still terminating after the whole timeout, with the
// error dropped.
func TestTheNamespaceWaitRidesOutATransientReadError(t *testing.T) {
	blip := apierrors.NewInternalError(errors.New("etcdserver: leader changed"))
	nsGone := apierrors.NewNotFound(schema.GroupResource{Resource: "namespaces"}, "acme")
	terminating := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "acme", DeletionTimestamp: &deleting, Finalizers: []string{"kubernetes"}},
		Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceTerminating},
	}

	t.Run("a blip then gone is success", func(t *testing.T) {
		c := fake.NewSimpleClientset(terminating.DeepCopy())
		gets := 0
		c.PrependReactor("get", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
			gets++
			switch {
			case gets < 2:
				return false, nil, nil
			case gets < 5:
				return true, nil, blip
			}
			return true, nil, nsGone
		})
		if err := waitForNamespaceGone(context.Background(), c, "acme", 5*time.Second, time.Millisecond); err != nil {
			t.Errorf("a wait that saw a blip and then the namespace gone failed: %v", err)
		}
	})

	t.Run("still failing at the deadline reports the error", func(t *testing.T) {
		c := fake.NewSimpleClientset(terminating.DeepCopy())
		gets := 0
		c.PrependReactor("get", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
			gets++
			if gets < 2 {
				return false, nil, nil
			}
			return true, nil, blip
		})
		err := waitForNamespaceGone(context.Background(), c, "acme", 50*time.Millisecond, time.Millisecond)
		if err == nil {
			t.Fatal("a namespace never confirmed gone was reported gone")
		}
		if !apierrors.IsInternalError(err) || !strings.Contains(err.Error(), "leader changed") {
			t.Errorf("the last read error is not reported and wrapped: %v", err)
		}
		if strings.Contains(err.Error(), "50ms:") && !strings.Contains(err.Error(), "latest read failed") {
			t.Errorf("reported as a plain timeout: %v", err)
		}
		if gets < 5 {
			t.Errorf("the wait stopped at the first failed read (%d reads)", gets)
		}
	})

	t.Run("every read failing", func(t *testing.T) {
		c := fake.NewSimpleClientset()
		c.PrependReactor("get", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, blip
		})
		err := waitForNamespaceGone(context.Background(), c, "acme", 20*time.Millisecond, time.Millisecond)
		if err == nil || !apierrors.IsInternalError(err) || !strings.Contains(err.Error(), "Local state has been kept") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("a cancelled context is an interrupt", func(t *testing.T) {
		c := fake.NewSimpleClientset(terminating.DeepCopy())
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := waitForNamespaceGone(ctx, c, "acme", time.Minute, time.Millisecond)
		if err == nil || !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "interrupted") {
			t.Errorf("err = %v", err)
		}
	})
}
