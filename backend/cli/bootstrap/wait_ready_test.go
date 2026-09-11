// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// areaDeployment builds an area Deployment with the status counts a rollout moves.
func areaDeployment(name string, generation, observed, desired, updated, replicas, available int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "dc-inst", Generation: int64(generation)},
		Spec:       appsv1.DeploymentSpec{Replicas: &desired},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: int64(observed),
			UpdatedReplicas:    updated,
			Replicas:           replicas,
			AvailableReplicas:  available,
		},
	}
}

// 🔴 EVERY UNREADY CASE BELOW SATISFIES `AvailableReplicas >= desired` — the check
// this gate used until now. That is what makes them a control rather than a test:
// restore the old predicate in waitForAreas and every one of them reports ready, so
// each case can only fail for the reason it names. The loop asserts that property of
// the fixtures rather than trusting the table, because a case edited to be "more
// realistic" can quietly stop satisfying it and take the control with it.
func TestWaitForAreasRefusesAnUnfinishedRollout(t *testing.T) {
	cases := []struct {
		name string
		dep  *appsv1.Deployment
	}{
		{
			// The apply landed but the controller has not seen it, so the three
			// counts below still describe the template being replaced.
			name: "the new template has not been observed",
			dep:  areaDeployment("device-management", 4, 3, 2, 2, 2, 2),
		},
		{
			// Half rolled: one new pod, one old one still serving. Availability is
			// satisfied throughout by the pod that is on its way out.
			name: "only some replicas are on the new template",
			dep:  areaDeployment("event-processing", 4, 4, 2, 1, 2, 2),
		},
		{
			// Every replica is updated and available, but the old ones have not
			// terminated — so old credentials are still being served.
			name: "an old replica is still running",
			dep:  areaDeployment("user-management", 4, 4, 2, 2, 3, 3),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			desired := *tc.dep.Spec.Replicas
			if tc.dep.Status.AvailableReplicas < desired {
				t.Fatalf("this case no longer satisfies the check it exists to defeat "+
					"(available %d < desired %d), so it does not measure what it claims",
					tc.dep.Status.AvailableReplicas, desired)
			}
			if deploymentRolledOut(tc.dep) {
				t.Fatal("reported rolled out while the rollout was still in progress")
			}

			client := fake.NewSimpleClientset(tc.dep)
			err := waitForAreas(context.Background(), client, "dc-inst", 30*time.Millisecond, time.Millisecond)
			if err == nil {
				t.Fatal("waitForAreas reported the areas ready over an unfinished rollout")
			}
		})
	}
}

// The counterweight: a gate that only ever refuses is an outage, so the finished
// shape must pass. Without this, deleting the whole predicate would still look green.
func TestWaitForAreasAcceptsAFinishedRollout(t *testing.T) {
	client := fake.NewSimpleClientset(
		areaDeployment("device-management", 4, 4, 2, 2, 2, 2),
		areaDeployment("event-processing", 1, 1, 1, 1, 1, 1),
	)
	if err := waitForAreas(context.Background(), client, "dc-inst", time.Second, time.Millisecond); err != nil {
		t.Fatalf("refused a fully rolled-over instance: %v", err)
	}
}

// 🔴 An empty namespace must not answer a question about health. Nothing is
// unready, so every per-Deployment check passes vacuously and the loop would
// otherwise report success for an instance that was never installed.
func TestWaitForAreasRefusesAnEmptyNamespace(t *testing.T) {
	err := waitForAreas(context.Background(), fake.NewSimpleClientset(), "dc-inst", 30*time.Millisecond, time.Millisecond)
	if err == nil {
		t.Fatal("reported ready for a namespace holding no Deployments")
	}
}

// The wait must abandon a cancelled run rather than holding the pipeline to its own
// deadline — the claim fence in Run() cannot be reached while this is sleeping.
func TestWaitForAreasHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Unready under every candidate predicate, including the naive one, so this
	// test fails for cancellation and not because the fixture happened to settle.
	client := fake.NewSimpleClientset(areaDeployment("device-management", 4, 3, 2, 0, 0, 0))
	err := waitForAreas(ctx, client, "dc-inst", time.Hour, time.Second)
	if err != context.Canceled {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

// A gate that can now fail has to say what failed. The counts alone ("3/5 ready")
// send the operator to kubectl to work out which area stalled and on which of the
// four conditions — so the message names the Deployment and its numbers, the way
// the operator-upgrade path already does.
func TestTheTimeoutNamesTheAreaThatDidNotConverge(t *testing.T) {
	client := fake.NewSimpleClientset(
		areaDeployment("device-management", 4, 4, 2, 2, 2, 2), // converged
		areaDeployment("event-processing", 7, 7, 3, 1, 3, 3),  // stuck
	)
	err := waitForAreas(context.Background(), client, "dc-inst", 30*time.Millisecond, time.Millisecond)
	if err == nil {
		t.Fatal("reported ready over a stalled area")
	}
	msg := err.Error()
	for _, want := range []string{"event-processing", "1/3 updated", "dc-inst"} {
		if !strings.Contains(msg, want) {
			t.Errorf("timeout message does not mention %q; an operator cannot act on it:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "device-management") {
		t.Errorf("timeout message names an area that had converged, which buries the real one:\n%s", msg)
	}
}

// The empty namespace fails for a different reason and must say so — "0/0 ready" is
// a rollout that stalled only if you already know nothing was installed.
func TestTheEmptyNamespaceSaysNothingWasRendered(t *testing.T) {
	err := waitForAreas(context.Background(), fake.NewSimpleClientset(), "dc-inst", 30*time.Millisecond, time.Millisecond)
	if err == nil {
		t.Fatal("reported ready for a namespace holding no Deployments")
	}
	if !strings.Contains(err.Error(), "rendered nothing") {
		t.Errorf("an empty namespace reports as a stalled rollout, not as an empty install:\n%s", err)
	}
}
