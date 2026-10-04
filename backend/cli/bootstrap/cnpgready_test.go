// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

const (
	testWaitBound = 60 * time.Millisecond
	testWaitPoll  = time.Millisecond
)

// aCluster is a CloudNativePG Cluster named name in ns, healthy on its counts: `ready` of
// `instances` instances ready, in the given phase.
func aCluster(ns, name string, instances, ready int64, phase string) *unstructured.Unstructured {
	u := anEventStore(instances, "8Gi", "pg:1")
	u.SetNamespace(ns)
	u.SetName(name)
	_ = unstructured.SetNestedField(u.Object, ready, "status", "readyInstances")
	_ = unstructured.SetNestedField(u.Object, phase, "status", "phase")
	_ = unstructured.SetNestedField(u.Object, name+"-1", "status", "currentPrimary")
	_ = unstructured.SetNestedField(u.Object, name+"-1", "status", "targetPrimary")
	return u
}

// cnpgPods are n live instance pods of the named Cluster, on image pg:1.
func cnpgPods(ns, cluster string, n int) []runtime.Object {
	var out []runtime.Object
	for i := 0; i < n; i++ {
		out = append(out, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: cluster + "-" + string(rune('1'+i)), Namespace: ns,
				Labels: map[string]string{"cnpg.io/cluster": cluster}},
			Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "postgres", Image: "pg:1"}}},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		})
	}
	return out
}

// within runs fn and fails the test if it returned before the bound (a wait that gave up
// early is not a wait) or took 2s or more (a hang is not a pass).
func within(t *testing.T, bound time.Duration, fn func() error) error {
	t.Helper()
	start := time.Now()
	err := fn()
	if el := time.Since(start); el < bound || el >= 2*time.Second {
		t.Errorf("the wait took %s; it should run its %s bound out and no more", el, bound)
	}
	return err
}

// 🔴 A CLUSTER SHORT OF INSTANCES MUST NOT READ AS READY, and the error must say how
// short. The three cases are the three ways a wait can end badly: replicas still
// joining (phase unhealthy), replicas running but not yet ready (the real join window,
// where the count is the ONLY failing condition), and a Cluster that cannot be read.
func TestWaitForCNPGClusterReadyRefusesAClusterShortOfInstances(t *testing.T) {
	for _, tc := range []struct {
		name  string
		phase string
		pods  int
	}{
		{"replicas still joining", "Creating a new replica", 2},
		{"replicas running but not ready", healthyClusterPhase, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dyn := fakeDyn(aCluster("dc-x", "dc-tsdb", 3, 1, tc.phase),
				// A different Cluster, fully healthy, in the same namespace.
				aCluster("dc-x", "dc-rdb", 3, 3, healthyClusterPhase))
			typed := fake.NewSimpleClientset(append(cnpgPods("dc-x", "dc-tsdb", tc.pods), cnpgPods("dc-x", "dc-rdb", 3)...)...)
			err := within(t, testWaitBound, func() error {
				return waitForCNPGClusterReady(context.Background(), dyn, typed, "dc-x", "dc-tsdb", testWaitBound, testWaitPoll)
			})
			if err == nil {
				t.Fatal("a Cluster with 1 of 3 instances ready was reported ready")
			}
			for _, want := range []string{"dc-x/dc-tsdb", "1 of 3 instances ready"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q lacks %q", err, want)
				}
			}
		})
	}
}

// The counterweight: a gate that only ever refuses is an outage.
func TestWaitForCNPGClusterReadyAcceptsAHealthyCluster(t *testing.T) {
	dyn := fakeDyn(aCluster("dc-x", "dc-tsdb", 3, 3, healthyClusterPhase))
	typed := fake.NewSimpleClientset(cnpgPods("dc-x", "dc-tsdb", 3)...)
	if err := waitForCNPGClusterReady(context.Background(), dyn, typed, "dc-x", "dc-tsdb", time.Second, testWaitPoll); err != nil {
		t.Fatalf("refused a healthy 3-of-3 Cluster: %v", err)
	}
}

// All instances ready but the Cluster not settled must not read "3 of 3 instances ready",
// whose first clause reads as success.
func TestWaitForCNPGClusterReadyDoesNotSayAllReadyForAnUnsettledPrimary(t *testing.T) {
	cl := aCluster("dc-x", "dc-rdb", 3, 3, healthyClusterPhase)
	_ = unstructured.SetNestedField(cl.Object, "dc-rdb-2", "status", "targetPrimary")
	err := within(t, testWaitBound, func() error {
		return waitForCNPGClusterReady(context.Background(), fakeDyn(cl),
			fake.NewSimpleClientset(cnpgPods("dc-x", "dc-rdb", 3)...), "dc-x", "dc-rdb", testWaitBound, testWaitPoll)
	})
	if err == nil {
		t.Fatal("a Cluster whose primary is moving was reported ready")
	}
	if !strings.Contains(err.Error(), "all 3 instances are ready, but it is not healthy") ||
		!strings.Contains(err.Error(), "primary is moving") || strings.Contains(err.Error(), "3 of 3") {
		t.Errorf("error = %q", err)
	}
}

func TestWaitForCNPGClusterReadyNamesAClusterItCouldNotRead(t *testing.T) {
	err := within(t, testWaitBound, func() error {
		return waitForCNPGClusterReady(context.Background(), fakeDyn(), fake.NewSimpleClientset(),
			"dc-x", "dc-tsdb", testWaitBound, testWaitPoll)
	})
	if err == nil {
		t.Fatal("a Cluster that does not exist was reported ready")
	}
	if !strings.Contains(err.Error(), "dc-x/dc-tsdb could not be read") || strings.Contains(err.Error(), "0 of 0") {
		t.Errorf("error = %q", err)
	}
}

func TestWaitForCNPGClusterReadyReturnsACancelAsACancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	err := waitForCNPGClusterReady(ctx, fakeDyn(aCluster("dc-x", "dc-tsdb", 3, 1, "Creating a new replica")),
		fake.NewSimpleClientset(), "dc-x", "dc-tsdb", time.Hour, testWaitPoll)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled wait returned %v, want context.Canceled", err)
	}
	if strings.Contains(err.Error(), "instances ready after") {
		t.Errorf("a cancel was reported as a timeout: %q", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("a cancelled wait did not return promptly")
	}
}

// --- install ------------------------------------------------------------------------

// 🔴 INSTALL MUST NOT REPORT SUCCESS OVER A RELATIONAL STORE SHORT OF INSTANCES, and the
// record is written anyway: it states what was applied, which is true.
func TestInstallRecordsThenWaitsForEveryRelationalStoreInstance(t *testing.T) {
	rec := aCompleteInstall()
	typed := fake.NewSimpleClientset(cnpgPods("dc-system", "dc-rdb", 1)...)
	dyn := fakeDyn(aCluster("dc-system", "dc-rdb", 3, 1, "Creating a new replica"))
	err := within(t, testWaitBound, func() error {
		return recordInstallAndWaitForStore(context.Background(), &State{}, typed, dyn, rec, testWaitBound, testWaitPoll)
	})
	if err == nil {
		t.Fatal("install succeeded over a relational store with 1 of 3 instances ready")
	}
	for _, want := range []string{"dc-system/dc-rdb", "1 of 3 instances ready",
		"run the same `dcctl install` command again", "kubectl -n dc-system get clusters.postgresql.cnpg.io dc-rdb"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	got, rerr := readInstallRecord(context.Background(), typed, rec.ClusterUID)
	if rerr != nil || got.Phase != installPhaseInstalled {
		t.Errorf("the record was not written before the wait failed: %+v, %v", got, rerr)
	}
}

func TestInstallSucceedsOverAFullyJoinedRelationalStore(t *testing.T) {
	rec := aCompleteInstall()
	typed := fake.NewSimpleClientset(cnpgPods("dc-system", "dc-rdb", 3)...)
	dyn := fakeDyn(aCluster("dc-system", "dc-rdb", 3, 3, healthyClusterPhase))
	if err := recordInstallAndWaitForStore(context.Background(), &State{}, typed, dyn, rec, time.Second, testWaitPoll); err != nil {
		t.Fatalf("install refused a healthy 3-of-3 store: %v", err)
	}
	if got, err := readInstallRecord(context.Background(), typed, rec.ClusterUID); err != nil || got.Phase != installPhaseInstalled {
		t.Errorf("record = %+v, %v", got, err)
	}
}

// 🔴 THE CLUSTER LOCK IS GONE WHILE THE WAIT RUNS: the wait is read-only, and a Lease
// held for fifteen minutes of watching refuses every bootstrap the record has let through.
func TestInstallHandsTheClusterLockBackBeforeItWaits(t *testing.T) {
	rec := aCompleteInstall()
	cs := fake.NewSimpleClientset(cnpgPods("dc-system", "dc-rdb", 1)...)
	claim, err := AcquireClaim(t.Context(), cs, testClaimNS, "prod", testKubeContext)
	if err != nil {
		t.Fatal(err)
	}
	st := &State{Claim: claim}
	var leaseDuringWait []bool
	cs.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		// The tracker, not the clientset: a clientset call from inside a reactor deadlocks.
		_, gerr := cs.Tracker().Get(schema.GroupVersionResource{Group: "coordination.k8s.io", Version: "v1", Resource: "leases"},
			testClaimNS, claimLeaseName)
		leaseDuringWait = append(leaseDuringWait, !apierrors.IsNotFound(gerr))
		return false, nil, nil
	})
	dyn := fakeDyn(aCluster("dc-system", "dc-rdb", 3, 1, "Creating a new replica"))
	if err := recordInstallAndWaitForStore(context.Background(), st, cs, dyn, rec, testWaitBound, testWaitPoll); err == nil {
		t.Fatal("expected the wait to fail")
	}
	if len(leaseDuringWait) == 0 {
		t.Fatal("the wait never listed pods, so the test observed nothing")
	}
	for _, held := range leaseDuringWait {
		if held {
			t.Fatal("the cluster Lease was still held while the install waited for the store")
		}
	}
	if st.Claim != nil {
		t.Error("st.Claim was left set, so Install's deferred release would repeat the release")
	}
}

func TestRecordInstallAndWaitForStoreRecordsNothingWithoutTheClaim(t *testing.T) {
	rec := aCompleteInstall()
	typed := fake.NewSimpleClientset()
	err := recordInstallAndWaitForStore(context.Background(), &State{Claim: lostClaim(t)}, typed,
		fakeDyn(aCluster("dc-system", "dc-rdb", 3, 3, healthyClusterPhase)), rec, testWaitBound, testWaitPoll)
	if err == nil {
		t.Fatal("recorded an install over a lock that was lost")
	}
	if cm, gerr := getInstallRecordMap(context.Background(), typed); gerr != nil || cm != nil {
		t.Error("an install record exists though the lock was lost")
	}
}

// --- the hold, the read flag, the zero guard, and the callers ------------------------

// 🔴 READINESS MUST HOLD, NOT BE SEEN ONCE. A Cluster read in the instant after its spec
// changed still reads healthy; the wait is the gate for install, bootstrap and upgrade,
// so a healthy first read followed by a short one must not pass.
func TestWaitForCNPGClusterReadyDoesNotTrustASingleHealthyRead(t *testing.T) {
	healthy := aCluster("dc-x", "dc-tsdb", 3, 3, healthyClusterPhase)
	short := aCluster("dc-x", "dc-tsdb", 3, 1, "Creating a new replica")
	dyn := fakeDyn(healthy)
	gets := 0
	dyn.PrependReactor("get", "clusters", func(k8stesting.Action) (bool, runtime.Object, error) {
		gets++
		if gets == 1 {
			return true, healthy, nil
		}
		return true, short, nil
	})
	typed := fake.NewSimpleClientset(cnpgPods("dc-x", "dc-tsdb", 3)...)
	err := within(t, testWaitBound, func() error {
		return waitForCNPGClusterReady(context.Background(), dyn, typed, "dc-x", "dc-tsdb", testWaitBound, testWaitPoll)
	})
	if err == nil {
		t.Fatal("one healthy read, then a short Cluster, was reported ready")
	}
	if !strings.Contains(err.Error(), "1 of 3 instances ready") {
		t.Errorf("error = %q", err)
	}
}

// The read flag describes the LAST poll: a Cluster read once and unreadable since is
// reported as unreadable, not as the stale count from the first poll.
func TestWaitForCNPGClusterReadyReportsTheLastPollsFailureNotAnEarlierCount(t *testing.T) {
	short := aCluster("dc-x", "dc-tsdb", 3, 1, "Creating a new replica")
	dyn := fakeDyn(short)
	gets := 0
	dyn.PrependReactor("get", "clusters", func(k8stesting.Action) (bool, runtime.Object, error) {
		gets++
		if gets == 1 {
			return true, short, nil
		}
		return true, nil, errors.New("the API server is unreachable")
	})
	err := within(t, testWaitBound, func() error {
		return waitForCNPGClusterReady(context.Background(), dyn, fake.NewSimpleClientset(cnpgPods("dc-x", "dc-tsdb", 1)...),
			"dc-x", "dc-tsdb", testWaitBound, testWaitPoll)
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "could not be read") || strings.Contains(err.Error(), "1 of 3 instances ready") {
		t.Errorf("error = %q", err)
	}
}

// A Cluster declaring no instances is not ready: with none ready either, the counts agree.
func TestCNPGClusterReadyRefusesAClusterWithNoInstances(t *testing.T) {
	ok, why := cnpgClusterReady(aCluster("dc-x", "dc-tsdb", 0, 0, healthyClusterPhase), nil)
	if ok || !strings.Contains(why, "0 of 0 instances are ready") {
		t.Errorf("ready = %v, reason = %q", ok, why)
	}
}

// 🔴 UPGRADE'S EVENT STORE WAIT IS DRIVEN: the closure applyUpgradeInfra calls must fail
// over a short store, or dropping the call would leave upgrade reporting success over it.
func TestUpgradeStoreWaitRefusesAShortEventStore(t *testing.T) {
	st := &State{Instance: "x"}
	ns := InstanceNamespace("x")
	dyn := fakeDyn(aCluster(ns, TsdbClusterName, 3, 1, "Creating a new replica"))
	typed := fake.NewSimpleClientset(cnpgPods(ns, TsdbClusterName, 1)...)
	ctx, cancel := context.WithTimeout(context.Background(), testWaitBound)
	defer cancel()
	err := clusterInfraWaits(st, typed, dyn).store(ctx)
	if err == nil {
		t.Fatal("the upgrade's event store wait passed over a store with 1 of 3 instances ready")
	}
	if !strings.Contains(err.Error(), "waiting for the event store to be healthy") {
		t.Errorf("error = %q", err)
	}
}

// 🔴 INSTALL'S TAIL RETURNS THE WAIT'S ERROR AND SAYS NOTHING OF SUCCESS: a short store
// must not be followed by the "prerequisites installed ... Next: build an instance" report.
func TestFinishInstallOverAShortStoreReturnsTheErrorAndReportsNoSuccess(t *testing.T) {
	rec := aCompleteInstall()
	typed := fake.NewSimpleClientset(cnpgPods("dc-system", "dc-rdb", 1)...)
	dyn := fakeDyn(aCluster("dc-system", "dc-rdb", 3, 1, "Creating a new replica"))
	var err error
	out := captureOutput(t, func() {
		err = finishInstall(context.Background(), &State{}, "kind", typed, dyn, rec, testWaitBound, testWaitPoll)
	})
	if err == nil {
		t.Fatal("install finished over a relational store with 1 of 3 instances ready")
	}
	if strings.Contains(out, "Next: build an instance") || strings.Contains(out, "prerequisites installed") {
		t.Errorf("a success report was printed over a short store:\n%s", out)
	}
}

func TestFinishInstallOverAJoinedStoreReportsSuccess(t *testing.T) {
	rec := aCompleteInstall()
	typed := fake.NewSimpleClientset(cnpgPods("dc-system", "dc-rdb", 3)...)
	dyn := fakeDyn(aCluster("dc-system", "dc-rdb", 3, 3, healthyClusterPhase))
	var err error
	out := captureOutput(t, func() {
		err = finishInstall(context.Background(), &State{}, "kind", typed, dyn, rec, time.Second, testWaitPoll)
	})
	if err != nil || !strings.Contains(out, "Next: build an instance") {
		t.Errorf("err = %v, output:\n%s", err, out)
	}
}

// A restore's replicas clone after the primary is up, so its timeout says so, and the
// same text is absent for an ordinary install.
func TestInstallWaitErrorExplainsARestoresSlowReplicas(t *testing.T) {
	for _, tc := range []struct {
		name    string
		restore RestorePlan
		want    bool
	}{
		{"restore", RestorePlan{RdbFrom: "s3://archive"}, true},
		{"ordinary", RestorePlan{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := aCompleteInstall()
			typed := fake.NewSimpleClientset(cnpgPods("dc-system", "dc-rdb", 1)...)
			dyn := fakeDyn(aCluster("dc-system", "dc-rdb", 3, 1, "Creating a new replica"))
			err := recordInstallAndWaitForStore(context.Background(), &State{Restore: tc.restore}, typed, dyn, rec,
				testWaitBound, testWaitPoll)
			if err == nil {
				t.Fatal("expected the wait to fail")
			}
			if got := strings.Contains(err.Error(), "replicas clone the recovered store"); got != tc.want {
				t.Errorf("restore hint present = %v, want %v: %q", got, tc.want, err)
			}
		})
	}
}
