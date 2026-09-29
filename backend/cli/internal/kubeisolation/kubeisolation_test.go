// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package kubeisolation

import (
	"os"
	"path/filepath"
	"testing"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/tools/clientcmd"
)

// kubeconfigAtStart is the KUBECONFIG this process was STARTED with, before Run replaced
// it. Only TestLeakOnPurpose reads it: it is how that test reaches the cluster the harness
// handed the child, through the same channel RequireIsolated uses.
var kubeconfigAtStart string

func TestMain(m *testing.M) {
	kubeconfigAtStart = os.Getenv("KUBECONFIG")
	os.Exit(Run(m))
}

// apply closes every door, checked by value: each variable that can name a cluster is
// ABSENT (not merely empty), both kubeconfig paths name one existing 0-byte file, and an
// unrelated variable is left alone.
func TestApplyClearsEveryAmbientDoor(t *testing.T) {
	home, configDir := clientcmd.RecommendedHomeFile, clientcmd.RecommendedConfigDir
	t.Cleanup(func() { clientcmd.RecommendedHomeFile, clientcmd.RecommendedConfigDir = home, configDir })
	cleared := map[string]string{
		"KUBERNETES_SERVICE_HOST":   "10.0.0.1",
		"KUBERNETES_SERVICE_PORT":   "443",
		"KUBE_HOST":                 "https://x",
		"KUBE_CONFIG_PATH":          "/x",
		"KUBE_CTX":                  "x",
		"HELM_KUBEAPISERVER":        "https://x",
		"HELM_KUBECONTEXT":          "x",
		"HELM_KUBETOKEN":            "x",
		"TF_VAR_kubeconfig_context": "x",
	}
	for k, v := range cleared {
		t.Setenv(k, v)
	}
	t.Setenv("KUBECONFIG", "/nonexistent/real")
	t.Setenv("TF_VAR_kubeconfig_path", "/nonexistent/real")
	t.Setenv("DCCTL_KUBEISOLATION_UNRELATED", "kept")

	dir, err := apply()
	if dir != "" {
		t.Cleanup(func() { os.RemoveAll(dir) })
	}
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	cfg := filepath.Join(dir, "config")
	for _, name := range []string{"KUBECONFIG", "TF_VAR_kubeconfig_path"} {
		if got := os.Getenv(name); got != cfg {
			t.Errorf("%s = %q, want %q", name, got, cfg)
		}
	}
	if clientcmd.RecommendedHomeFile != cfg {
		t.Errorf("RecommendedHomeFile = %q, want %q", clientcmd.RecommendedHomeFile, cfg)
	}
	if clientcmd.RecommendedConfigDir != dir {
		t.Errorf("RecommendedConfigDir = %q, want %q", clientcmd.RecommendedConfigDir, dir)
	}
	fi, err := os.Stat(cfg)
	if err != nil {
		t.Fatalf("the isolated kubeconfig does not exist: %v", err)
	}
	if fi.Size() != 0 {
		t.Errorf("the isolated kubeconfig is %d bytes, want 0", fi.Size())
	}
	for name := range cleared {
		if v, ok := os.LookupEnv(name); ok {
			t.Errorf("%s is still set (%q)", name, v)
		}
	}
	if got := os.Getenv("DCCTL_KUBEISOLATION_UNRELATED"); got != "kept" {
		t.Errorf("an unrelated variable was changed: %q", got)
	}
	// And the result is the state CI's runners are in: the default loading rules yield
	// no usable config at all.
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if _, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).ClientConfig(); !clientcmd.IsEmptyConfig(err) {
		t.Errorf("the isolated process still builds a cluster config (err = %v), want the empty-config error", err)
	}
}

// leakEnv makes TestLeakOnPurpose act; without it that test skips.
const leakEnv = "DCCTL_KUBEISOLATION_LEAK"

// TestLeakOnPurpose is the deliberate leak TestTheChildHarnessReportsALeak runs in a child:
// it bypasses isolation by building a client from the kubeconfig the child was STARTED
// with, which is the counting server's when the harness is working.
func TestLeakOnPurpose(t *testing.T) {
	if os.Getenv(childEnv) != "1" || os.Getenv(leakEnv) != "1" {
		t.Skip("runs only as TestTheChildHarnessReportsALeak's child")
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfigAtStart)
	if err != nil {
		t.Fatalf("the child was not handed a usable kubeconfig (%q): %v", kubeconfigAtStart, err)
	}
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = dc.ServerVersion()
	t.Logf("leaked a request to %s: %v", cfg.Host, err)
}

// The child harness must be SHOWN to report a leak before its zero means anything: a child
// that bypasses isolation on purpose is counted, and its RUN line is found.
func TestTheChildHarnessReportsALeak(t *testing.T) {
	path, requests, _ := CountingKubeconfig(t)
	out, exitErr := runChild(t, "^TestLeakOnPurpose$", path, leakEnv+"=1")
	if exitErr != nil {
		t.Fatalf("the leaking child failed (%v):\n%s", exitErr, out)
	}
	if !ranTest(out, "TestLeakOnPurpose") {
		t.Fatalf("the child never ran TestLeakOnPurpose:\n%s", out)
	}
	if got := requests(); got < 1 {
		t.Fatalf("a child that leaked on purpose was counted as %d request(s); the harness cannot see a leak:\n%s", got, out)
	}
}
