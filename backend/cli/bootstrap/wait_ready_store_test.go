// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

// instanceWithStore is a rolled-out instance whose event store has `ready` of 3 instances
// ready, beside a fully healthy Cluster of another name.
func instanceWithStore(ready int64, pods int) (*fake.Clientset, *dynamicfake.FakeDynamicClient) {
	phase := healthyClusterPhase
	if ready < 3 {
		phase = "Creating a new replica"
	}
	typed := fake.NewSimpleClientset(append(cnpgPods("dc-inst", TsdbClusterName, pods),
		areaDeployment("device-management", 4, 4, 2, 2, 2, 2))...)
	return typed, fakeDyn(
		aCluster("dc-inst", TsdbClusterName, 3, ready, phase),
		// A healthy Cluster of another name in the same namespace must not satisfy the wait.
		aCluster("dc-inst", RdbClusterName, 3, 3, healthyClusterPhase))
}

var stepNineWaits = instanceReadyWaits{areas: time.Second, store: testWaitBound, poll: testWaitPoll}

// 🔴 ON THE OLD STEP 9 THIS RETURNED NIL: every Deployment had rolled out, and nothing
// asked about the replicas.
func TestStepNineWaitsForEveryEventStoreInstance(t *testing.T) {
	typed, dyn := instanceWithStore(1, 1)
	err := within(t, testWaitBound, func() error {
		return waitForInstanceReady(context.Background(), typed, dyn, "dc-inst", stepNineWaits)
	})
	if err == nil {
		t.Fatal("step 9 passed over an event store with 1 of 3 instances ready")
	}
	for _, want := range []string{"1 of 3 instances ready", "dc-inst/dc-tsdb", "run the same `dcctl bootstrap` command again"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

func TestStepNineAcceptsAFullyJoinedEventStore(t *testing.T) {
	typed, dyn := instanceWithStore(3, 3)
	if err := waitForInstanceReady(context.Background(), typed, dyn, "dc-inst", stepNineWaits); err != nil {
		t.Fatalf("step 9 refused a healthy instance: %v", err)
	}
}

// The areas are waited for first: with none rendered the areas' error is the one
// returned, and the store (which is short) was never waited on.
func TestStepNineDoesNotWaitOnTheStoreBeforeTheAreas(t *testing.T) {
	_, dyn := instanceWithStore(1, 1)
	typed := fake.NewSimpleClientset(cnpgPods("dc-inst", TsdbClusterName, 1)...)
	w := instanceReadyWaits{areas: testWaitBound, store: time.Hour, poll: testWaitPoll}
	err := within(t, testWaitBound, func() error {
		return waitForInstanceReady(context.Background(), typed, dyn, "dc-inst", w)
	})
	if err == nil || !strings.Contains(err.Error(), "rendered nothing to wait for") {
		t.Fatalf("error = %v, want the areas' refusal", err)
	}
}

// 🔴 THE CALL SITE: the step itself, through its client seam. Restoring the bare areas
// wait in stepWaitReady leaves every other test here green.
func TestStepWaitReadyWaitsForTheEventStore(t *testing.T) {
	typed, dyn := instanceWithStore(1, 1)
	oldClients, oldBounds := stepWaitReadyClients, stepWaitReadyBounds
	t.Cleanup(func() { stepWaitReadyClients, stepWaitReadyBounds = oldClients, oldBounds })
	stepWaitReadyClients = func(string) (dynamic.Interface, kubernetes.Interface, error) { return dyn, typed, nil }
	stepWaitReadyBounds = stepNineWaits
	st := &State{Values: map[string]string{"namespace": "dc-inst"}}
	err := within(t, testWaitBound, func() error { return stepWaitReady(context.Background(), st) })
	if err == nil || !strings.Contains(err.Error(), "1 of 3 instances ready") {
		t.Fatalf("stepWaitReady over a short event store = %v", err)
	}
}
