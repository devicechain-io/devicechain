// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
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

// Ctrl+C must abandon the wait rather than hold the operator for the rest of a
// five-minute deadline. The root command turns a signal into a cancelled context,
// and this step is the longest-sleeping one in the pipeline — note that the pipeline
// deliberately does NOT cancel between steps, so a signal is the only thing that
// cancels this.
//
// The assertion is errors.Is rather than ==, and the difference is not pedantry: the
// fake client ignores ctx, so the loop reaches the select and returns the bare
// error. A real client fails inside List(ctx) first and the error comes back wrapped,
// which an == comparison would call a failure while the behaviour was correct.
func TestWaitForAreasHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Unready under every candidate predicate, including the naive one, so this
	// test fails for cancellation and not because the fixture happened to settle.
	client := fake.NewSimpleClientset(areaDeployment("device-management", 4, 3, 2, 0, 0, 0))
	err := waitForAreas(ctx, client, "dc-inst", time.Hour, time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want a cancelled context, got %v", err)
	}
}

// 🔴 EVERY OTHER TEST HERE IS SATISFIED BY A FUNCTION THAT LISTS ONCE AND RETURNS.
// The ready fixture passes on the first pass, the unready ones only need some error,
// and the cancelled one never reaches the sleep — so a loop that gave up after one
// poll would pass all of them, and waiting is the entire job. This is the test that
// requires a second pass: the first List reports a rollout in progress, the second
// reports it finished, and only a function that actually polls again can return nil.
//
// A reactor rather than a goroutine updating the object, so the two passes are
// ordered by construction instead of by a sleep racing the poll interval.
func TestWaitForAreasPollsUntilTheRolloutFinishes(t *testing.T) {
	client := fake.NewSimpleClientset()
	var lists int
	client.PrependReactor("list", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		lists++
		d := areaDeployment("device-management", 4, 4, 2, 2, 2, 2) // finished
		if lists == 1 {
			d = areaDeployment("device-management", 4, 4, 2, 1, 2, 2) // still rolling
		}
		return true, &appsv1.DeploymentList{Items: []appsv1.Deployment{*d}}, nil
	})

	if err := waitForAreas(context.Background(), client, "dc-inst", time.Second, time.Millisecond); err != nil {
		t.Fatalf("gave up on a rollout that finished while it was waiting: %v", err)
	}
	if lists < 2 {
		t.Fatalf("listed %d time(s); the wait returned without ever polling again, so nothing here measures waiting", lists)
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

// 🔴 TWO OF THE FOUR ROLLOUT CONDITIONS ARE NOT COVERED BY THIS FILE, AND CANNOT BE.
// The table above guards its own fixtures with `available >= desired`, and with
// `updated <= desired` in every row the fourth condition is then always satisfied —
// so "every replica recreated" and "the new ones actually came up" are held up by
// TestWaitForRolloutRejectsStatesTheNaiveCheckAccepts and
// TestWaitForRolloutWaitsForAnUnavailableNewPod in upgrade_test.go instead.
//
// That is sound only while the predicate is shared. If anyone ever gives waitForAreas
// its own copy, this file silently stops covering half the check — so the copy is the
// thing to refuse, not the coverage gap.
func TestTheRolloutPredicateIsSharedWithTheUpgradePath(t *testing.T) {
	// Reached from both callers, so a divergence has to be deliberate rather than
	// accidental. If this ever needs deleting, read the comment above first.
	d := areaDeployment("device-management", 4, 4, 2, 2, 2, 2)
	if !deploymentRolledOut(d) {
		t.Fatal("the shared predicate rejects a fully rolled-over Deployment")
	}
}

// The timeout a caller passes has to be the timeout enforced, or the number this
// function reports in its own error message is a fiction. The assertion is a LOWER
// bound on elapsed time, which is the direction that cannot flake: a loaded machine
// only ever makes the wait longer, while any arithmetic that shortens the deadline —
// halving it, dropping a unit, starting the clock in the wrong place — comes back
// early and fails.
func TestTheWaitLastsAsLongAsItWasGiven(t *testing.T) {
	const timeout = 60 * time.Millisecond
	client := fake.NewSimpleClientset(areaDeployment("device-management", 4, 4, 2, 1, 2, 2))

	start := time.Now()
	err := waitForAreas(context.Background(), client, "dc-inst", timeout, time.Millisecond)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("reported ready over a stalled rollout")
	}
	if elapsed < timeout {
		t.Fatalf("gave up after %s on a %s deadline; the timeout it enforces is not the "+
			"one it was given, nor the one it names in its error", elapsed, timeout)
	}
	if !strings.Contains(err.Error(), timeout.String()) {
		t.Errorf("the error does not name the deadline it enforced (%s):\n%s", timeout, err)
	}
}
