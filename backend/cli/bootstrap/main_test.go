// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"os"
	"testing"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/devicechain-io/dcctl/internal/kubeisolation"
)

// TestMain runs this package's tests against an empty kubeconfig, with nothing else in the
// environment naming a cluster (kubeisolation.Run), so NO cluster read in the package —
// through any seam, present or future — can reach the developer's current-context. That is
// the state CI's runners are in, so a local run means what a CI run means.
//
// 🔴 ISOLATION USED TO BE DONE HERE ONE SEAM AT A TIME, AND IT LEAKED AT EACH NEW ONE. The
// namespace precheck, then the credential lookup, then the archive-state read all built a
// kube config from the ambient environment; on a developer machine with a live kubeconfig
// the tests driving them QUIETLY CONTACTED THAT CLUSTER and passed on its answers, while CI,
// with no kubeconfig, passed or failed on a config error instead. Pointing HOME at a temp
// dir (fakeHome) never isolated any of it: client-go fixes the home kubeconfig path when
// the package is initialised, before a test can move HOME.
//
// The two seam defaults below no longer exist for isolation. They exist for DETERMINISM:
// "what a cluster with nothing in it answers", instead of an empty-config error from a read
// the test is not about.
//   - namespacePrecheckClient: an empty fake. "No namespace by this name" is what a fresh
//     cluster answers, and it keeps a test about something else — the host refusal, the
//     connection budget — from having to describe a namespace world it does not care about.
//     A test that DOES care calls stubNamespacePrecheck.
//   - readInstanceCredentialSecret: "no Secret by that name". That refuses a bootstrap onto a
//     store that is already ours, which is the SAFE direction for a test that has no opinion
//     — a default of "the credentials are there" would let a test pass because the guard was
//     silently disabled. A test that cares calls stubInstanceCredentials.
func TestMain(m *testing.M) {
	namespacePrecheckClient = func(string) (kubernetes.Interface, error) {
		return fake.NewSimpleClientset(), nil
	}
	readInstanceCredentialSecret = func(context.Context, string, string) (bool, error) {
		return false, nil
	}
	os.Exit(kubeisolation.Run(m))
}
