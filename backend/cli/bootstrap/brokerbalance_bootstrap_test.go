// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"

	"k8s.io/client-go/kubernetes"
)

func withRebalanceClient(t *testing.T, f func(string) (*kubernetes.Clientset, error)) {
	t.Helper()
	old := stepBrokerRebalanceClient
	stepBrokerRebalanceClient = f
	t.Cleanup(func() { stepBrokerRebalanceClient = old })
}

// A fresh install leaves broker leadership wherever the streams happened to be created (r34: one server
// led nothing, and resolved-events shared a server with capture and the MQTT store). Bootstrap evens it
// out once the instance is ready: after the readiness wait, so every stream exists and is led.
func TestBootstrapEvensOutBrokerLeadershipOnceTheInstanceIsReady(t *testing.T) {
	ready := stepIndex(t, stepWaitReady)
	rebalance := stepIndex(t, stepBrokerRebalance)
	if rebalance <= ready {
		t.Fatalf("stepBrokerRebalance runs at %d and stepWaitReady at %d: leadership would be planned over "+
			"streams that do not exist yet", rebalance, ready)
	}
}

// The step is best effort, like the upgrade's: a failure is a warning and never fails the bootstrap,
// and what it prints says the instance is installed.
func TestABootstrapRebalanceFailureWarnsAndNeverFailsTheBootstrap(t *testing.T) {
	withRebalanceClient(t, func(string) (*kubernetes.Clientset, error) { return nil, nil })
	called := false
	withRebalance(t, func(context.Context, *State, *kubernetes.Clientset) (string, error) {
		called = true
		return "", errors.New("connecting to the broker: refused")
	})
	var err error
	out := captureStdout(t, func() { err = stepBrokerRebalance(context.Background(), &State{Instance: "x"}) })
	if err != nil {
		t.Fatalf("a failed rebalance failed the bootstrap: %v", err)
	}
	if !called {
		t.Fatal("the step did not run the rebalance")
	}
	for _, want := range []string{"Warning", "connecting to the broker: refused", "instance is installed", "dcctl broker rebalance"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "upgrade is complete") {
		t.Errorf("a bootstrap's warning talks about an upgrade:\n%s", out)
	}
}

// No kube client is a warning too, and the rebalance is not attempted without one.
func TestABootstrapRebalanceWithNoClusterWarns(t *testing.T) {
	withRebalanceClient(t, func(string) (*kubernetes.Clientset, error) { return nil, errors.New("no kubeconfig") })
	withRebalance(t, func(context.Context, *State, *kubernetes.Clientset) (string, error) {
		t.Fatal("rebalance attempted without a client")
		return "", nil
	})
	var err error
	out := captureStdout(t, func() { err = stepBrokerRebalance(context.Background(), &State{Instance: "x"}) })
	if err != nil || !strings.Contains(out, "no kubeconfig") {
		t.Fatalf("err=%v out=%s", err, out)
	}
}

// A dry run touches nothing.
func TestADryRunBootstrapDoesNotRebalance(t *testing.T) {
	withRebalanceClient(t, func(string) (*kubernetes.Clientset, error) {
		t.Fatal("a dry run built a client")
		return nil, nil
	})
	withRebalance(t, func(context.Context, *State, *kubernetes.Clientset) (string, error) {
		t.Fatal("a dry run rebalanced")
		return "", nil
	})
	if err := stepBrokerRebalance(context.Background(), &State{Instance: "x", DryRun: true}); err != nil {
		t.Fatal(err)
	}
}
