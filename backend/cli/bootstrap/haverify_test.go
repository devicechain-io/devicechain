// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func pod(name, node string, phase corev1.PodPhase) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       corev1.PodSpec{NodeName: node},
		Status:     corev1.PodStatus{Phase: phase},
	}
}

// TestPlacedPodsDropsTheUnscheduledServer is the case the placement check
// actually turns on.
//
// A hard topologySpreadConstraint leaves a server PENDING when there is nowhere
// left to put it — that is the whole reason the constraint is hard rather than
// soft. A pending pod has an empty node name, so counting it yields three pods
// across three distinct "nodes", one of them the empty string, and the
// distinct-node assertion passes on a cluster that is running two servers.
func TestPlacedPodsDropsTheUnscheduledServer(t *testing.T) {
	got := placedPods([]corev1.Pod{
		pod("dc-nats-0", "node-a", corev1.PodRunning),
		pod("dc-nats-1", "node-b", corev1.PodRunning),
		pod("dc-nats-2", "", corev1.PodPending),
	})
	if len(got) != 2 {
		t.Fatalf("an unscheduled broker pod must not count as placed; got %+v", got)
	}
	for _, p := range got {
		if p.Node == "" {
			t.Fatalf("pod %q was kept with no node, which would read as a distinct node "+
				"and let a two-server cluster pass a three-node placement check", p.Name)
		}
	}
}

// TestPlacedPodsDropsTerminatedAndPreservesTheRest keeps the filter honest in the
// other direction: a filter that dropped everything would satisfy the case above
// and make the placement check fail on every healthy cluster.
func TestPlacedPodsDropsTerminatedAndPreservesTheRest(t *testing.T) {
	got := placedPods([]corev1.Pod{
		pod("dc-nats-0", "node-a", corev1.PodRunning),
		pod("dc-nats-1", "node-b", corev1.PodRunning),
		pod("dc-nats-2", "node-c", corev1.PodRunning),
		pod("dc-nats-old", "node-a", corev1.PodSucceeded),
		pod("dc-nats-dead", "node-b", corev1.PodFailed),
	})
	if len(got) != 3 {
		t.Fatalf("the three running servers must be kept and the finished pods dropped; got %+v", got)
	}
	nodes := map[string]bool{}
	for _, p := range got {
		nodes[p.Node] = true
	}
	if len(nodes) != 3 {
		t.Fatalf("expected three distinct nodes; got %v", nodes)
	}
}

// TestPlacedPodsOnNoPods pins that an empty listing yields an empty result rather
// than anything that could read as a satisfied check. Verify turns that into a
// finding, and this is the half that makes sure it gets the chance.
func TestPlacedPodsOnNoPods(t *testing.T) {
	if got := placedPods(nil); len(got) != 0 {
		t.Fatalf("no pods must produce no placements; got %+v", got)
	}
}
