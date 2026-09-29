// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dcctl/internal/kubeisolation"
)

// With KUBECONFIG naming a live API server — what a developer's current-context looks like
// to this process — this package's tests send it nothing.
//
// TestZeroPlanWritesNothingThroughTheStep is the entry point KNOWN to read a cluster from
// the environment: before TestMain isolated this package, it reached a developer's live GKE
// cluster, and the credential plugin client-go ran for it wrote into the test's fake HOME.
// It is the positive instance, not an inventory; what covers every other test here is that
// TestMain isolates the whole process before any of them runs.
func TestThisPackageRunsAgainstAnEmptyKubeconfig(t *testing.T) {
	kubeisolation.RequireIsolated(t, "TestZeroPlanWritesNothingThroughTheStep")
}

// The positive control for that entry point: on a dry run the render step DOES read a
// cluster its environment names, so the zero above means "isolated", not "the step stopped
// reading". The step must also have taken the read's SUCCESS branch — a request counted
// while the step fell back to "could not read" would make this control exercise the
// fallback instead of the read.
func TestTheRenderStepReachesAConfiguredCluster(t *testing.T) {
	path, requests, _ := kubeisolation.CountingKubeconfig(t)
	t.Setenv("KUBECONFIG", path)
	fakeHome(t)
	st := &State{Instance: "prod", BuildImages: true, DryRun: true, Values: map[string]string{}}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var err error
	out := captureStdout(t, func() { err = stepRenderConfig(ctx, st) })
	if err != nil {
		t.Fatalf("stepRenderConfig: %v", err)
	}
	if got := requests(); got < 1 {
		t.Fatal("stepRenderConfig no longer reads the cluster on a dry run, so " +
			"TestThisPackageRunsAgainstAnEmptyKubeconfig's zero says nothing about it -- pick an entry point that does")
	}
	if strings.Contains(out, "could not read the database archive state") {
		t.Fatalf("the step reached the server but took its read-failure branch:\n%s", out)
	}
}
