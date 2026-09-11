// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// unreachableKubeContext points KUBECONFIG at a file that names no contexts at
// all, so kubeClients fails while BUILDING the config rather than while talking
// to a server.
//
// That distinction is the difference between a 0.00s test and a 30s one:
// endDestroy gives itself a detached 30-second budget, and a config that points
// at a dead endpoint spends every second of it inside client-go's retries. What
// these tests need to observe is whether the removal was ATTEMPTED, and a
// config-building failure answers that just as well and immediately.
func unreachableKubeContext(t *testing.T) string {
	t.Helper()
	kubeconfig := filepath.Join(t.TempDir(), "config")
	const cfg = "apiVersion: v1\nkind: Config\nclusters: []\ncontexts: []\nusers: []\n"
	if err := os.WriteFile(kubeconfig, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", kubeconfig)
	return "no-such-context"
}

// attemptedRemoval reports whether endDestroy tried to take the declaration away.
// The attempt cannot succeed against an unreachable cluster, and it does not need
// to: what is under test is the DECISION, and the warning it prints when the
// removal fails is proof the decision was "remove it".
func attemptedRemoval(out string) bool {
	return strings.Contains(out, "could not be removed")
}

// 🔴 A FAILED DESTROY MUST NOT DELETE THE DECLARATION. The first version of
// endDestroy ran as an unconditional defer, so a destroy that died at the Helm
// uninstall stripped the finalizer and removed the CR while every workload was
// still running — manufacturing the orphaned instance that the finalizer exists to
// prevent, from inside the change that added it.
//
// Both directions are pinned here because either one alone is satisfiable by a
// function that always does the same thing.
func TestAFailedDestroyLeavesTheDeclarationAndASuccessfulOneRemovesIt(t *testing.T) {
	t.Run("a destroy that failed leaves it in place", func(t *testing.T) {
		kubeContext := unreachableKubeContext(t)
		failed := errors.New("uninstalling the instance release: helm timed out")

		out := captureOutput(t, func() {
			endDestroy(t.Context(), nil, kubeContext, "prod", true, &failed)
		})

		if attemptedRemoval(out) {
			t.Fatalf("a destroy that did not finish still tried to remove the declaration, which "+
				"is how an instance ends up running with nothing describing it:\n%s", out)
		}
		if !strings.Contains(out, "left in place") {
			t.Errorf("the operator is not told the declaration survived, so a re-run looks like a "+
				"fresh install to them:\n%s", out)
		}
	})

	t.Run("a destroy that succeeded removes it", func(t *testing.T) {
		kubeContext := unreachableKubeContext(t)
		var noFailure error

		out := captureOutput(t, func() {
			endDestroy(t.Context(), nil, kubeContext, "prod", true, &noFailure)
		})

		if !attemptedRemoval(out) {
			t.Fatalf("a completed destroy left its declaration behind; the next bootstrap would "+
				"be refused by a record of an instance that no longer exists:\n%s", out)
		}
		// The warning is also the escape hatch, and it has to name it: the finalizer
		// means nobody can delete this object with kubectl alone.
		if !strings.Contains(out, "dcctl instances release") {
			t.Errorf("the failure to remove it does not say how to remove it by hand:\n%s", out)
		}
	})

	// 🔴 NOT BEING TOLD THE OUTCOME IS NOT THE SAME AS BEING TOLD IT SUCCEEDED, and
	// the guard used to read it that way (`failed == nil || *failed == nil`), which
	// made the deleting branch reachable by OMISSION rather than by decision — a
	// third call site that forgot the argument would have got the old bug back with
	// nothing to see in review. There is no caller passing nil today; this is what
	// keeps the default safe if one appears.
	t.Run("an unknown outcome leaves it in place", func(t *testing.T) {
		kubeContext := unreachableKubeContext(t)

		out := captureOutput(t, func() {
			endDestroy(t.Context(), nil, kubeContext, "prod", true, nil)
		})

		if attemptedRemoval(out) {
			t.Fatalf("a destroy that never reported its outcome removed the declaration anyway:\n%s", out)
		}
	})

	// The third state, and it is not the same as either: when the CLUSTER is going
	// away the declaration goes with it, so reaching into a cluster that is
	// mid-teardown to tidy one object is a way to fail at the last step for no gain.
	t.Run("a cluster being deleted takes the declaration with it", func(t *testing.T) {
		kubeContext := unreachableKubeContext(t)
		var noFailure error

		out := captureOutput(t, func() {
			endDestroy(t.Context(), nil, kubeContext, "prod", false, &noFailure)
		})

		if attemptedRemoval(out) {
			t.Errorf("dcctl reached into a cluster it had just deleted to tidy one object:\n%s", out)
		}
		if strings.Contains(out, "left in place") {
			t.Errorf("a declaration that went with its cluster was reported as surviving:\n%s", out)
		}
	})
}

// 🔴 THE LOCK IS HANDED BACK WHATEVER HAPPENED. A destroy that failed has every
// reason to leave the declaration behind and none at all to keep the cluster lock:
// the process is exiting either way, and a Lease it leaves behind sends the next
// run — most often the same operator retrying the destroy — down the reclaim path,
// a full lease duration of waiting to answer a question this process knew.
func TestTheClusterLockIsReleasedWhetherOrNotTheDestroySucceeded(t *testing.T) {
	for _, tc := range []struct {
		name   string
		failed error
	}{
		{"after a destroy that finished", nil},
		{"after a destroy that failed", errors.New("uninstalling the instance release: helm timed out")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kubeContext := unreachableKubeContext(t)
			cs := fake.NewClientset()
			claim, err := AcquireClaim(t.Context(), cs, testClaimNS, "prod", testKubeContext)
			if err != nil {
				t.Fatal(err)
			}

			failed := tc.failed
			captureOutput(t, func() {
				endDestroy(t.Context(), claim, kubeContext, "prod", true, &failed)
			})

			if _, err := cs.CoordinationV1().Leases(testClaimNS).Get(
				t.Context(), claimLeaseName, metav1.GetOptions{}); err == nil {
				t.Error("the cluster lock outlived the destroy that held it")
			}
		})
	}
}
