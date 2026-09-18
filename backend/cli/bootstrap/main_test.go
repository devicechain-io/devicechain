// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"os"
	"testing"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

// TestMain points the namespace precheck's cluster call at an empty fake for the whole
// package, so that no test can reach a real cluster through it.
//
// 🔴 THIS EXISTS BECAUSE THE SUITE WAS GREEN FOR AN ENVIRONMENTAL REASON. The precheck
// became part of stepCheckClusterSingletons, which several tests drive for reasons of
// their own — the host refusal, the connection budget. Its default client builds a kube
// config from the ambient environment, so on a developer machine with a live kubeconfig
// those tests QUIETLY CONTACTED THAT CLUSTER, got "no such namespace" from it, and
// passed. On CI, which has no kubeconfig, the same tests failed on a config error from a
// read they were never about. The test that matters most here — the budget refusal — was
// then reporting on the developer's cluster rather than on the code.
//
// An empty fake is the right default rather than a failing one: "no namespace by this
// name" is what a fresh cluster answers, it is deterministic, and it keeps a test that is
// about something else from having to describe a namespace world it does not care about.
// A test that DOES care calls stubNamespacePrecheck, which replaces this for its duration
// and restores it afterwards.
// 🔴 THE SAME TRAP, SECOND DOOR. readInstanceCredentialSecret reaches a cluster the same
// way and for the same kind of question, so it gets the same package-wide default: what a
// cluster with nothing in it answers. "No Secret by that name" refuses a bootstrap onto a
// store that is already ours, which is the SAFE direction for a test that has no opinion —
// a default of "the credentials are there" would let a test pass because the guard was
// silently disabled. A test that cares calls stubInstanceCredentials.
func TestMain(m *testing.M) {
	namespacePrecheckClient = func(string) (kubernetes.Interface, error) {
		return fake.NewSimpleClientset(), nil
	}
	readInstanceCredentialSecret = func(context.Context, string, string) (bool, error) {
		return false, nil
	}
	os.Exit(m.Run())
}
