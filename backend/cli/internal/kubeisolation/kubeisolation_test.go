// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package kubeisolation

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/cli"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// kubeconfigAtStart is the KUBECONFIG this process was STARTED with, before Run replaced
// it. Only TestLeakOnPurpose reads it: it is how that test reaches the cluster the harness
// handed the child, through the same channel RequireIsolated uses.
var kubeconfigAtStart string

func TestMain(m *testing.M) {
	kubeconfigAtStart = os.Getenv("KUBECONFIG")
	os.Exit(Run(m))
}

// This package's own tests, like every isolated package's, send nothing to a live server
// named by KUBECONFIG or by KUBERNETES_MASTER.
func TestThisPackageRunsAgainstAnEmptyKubeconfig(t *testing.T) {
	RequireIsolated(t)
}

// saveGlobals restores, when t ends, the client-go values apply overwrites.
func saveGlobals(t *testing.T) {
	home, configDir, defaults := clientcmd.RecommendedHomeFile, clientcmd.RecommendedConfigDir, clientcmd.ClusterDefaults
	t.Cleanup(func() {
		clientcmd.RecommendedHomeFile, clientcmd.RecommendedConfigDir, clientcmd.ClusterDefaults = home, configDir, defaults
	})
}

// apply closes every door, checked by value: each variable that can name a cluster is
// ABSENT (not merely empty), both kubeconfig paths name one existing 0-byte file, the
// cluster Helm falls back to is empty, and an unrelated variable is left alone.
func TestApplyClearsEveryAmbientDoor(t *testing.T) {
	saveGlobals(t)
	// What client-go computes at init when KUBERNETES_MASTER is set.
	clientcmd.ClusterDefaults = clientcmdapi.Cluster{Server: "https://master.invalid"}
	cleared := map[string]string{
		"KUBERNETES_SERVICE_HOST":   "10.0.0.1",
		"KUBERNETES_SERVICE_PORT":   "443",
		"KUBERNETES_MASTER":         "https://master.invalid",
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
	if !reflect.DeepEqual(clientcmd.ClusterDefaults, clientcmdapi.Cluster{}) {
		t.Errorf("ClusterDefaults = %+v, want the zero Cluster", clientcmd.ClusterDefaults)
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
	// Nor do Helm's settings, which layer ClusterDefaults over the same rules.
	if cfg, err := cli.New().RESTClientGetter().ToRESTConfig(); err == nil {
		t.Errorf("Helm's settings still build a cluster config naming %q, want an error", cfg.Host)
	}
}

// verify refuses every partial isolation it re-reads: the full state passes, and each
// door reopened on its own is reported.
func TestVerifyRefusesAPartialIsolation(t *testing.T) {
	saveGlobals(t)
	dir, err := apply()
	if dir != "" {
		t.Cleanup(func() { os.RemoveAll(dir) })
	}
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	cfg := filepath.Join(dir, "config")
	if err := verify(cfg); err != nil {
		t.Fatalf("verify refused the state apply left: %v", err)
	}
	for _, c := range []struct {
		name   string
		reopen func(t *testing.T)
	}{
		{"KUBECONFIG replaced", func(t *testing.T) { t.Setenv("KUBECONFIG", "/nonexistent/real") }},
		{"kubeconfig_path replaced", func(t *testing.T) { t.Setenv("TF_VAR_kubeconfig_path", "/nonexistent/real") }},
		{"home kubeconfig restored", func(t *testing.T) { clientcmd.RecommendedHomeFile = "/nonexistent/real" }},
		{"cluster defaults restored", func(t *testing.T) {
			clientcmd.ClusterDefaults = clientcmdapi.Cluster{Server: "http://localhost:8080"}
		}},
		{"kubeconfig not empty", func(t *testing.T) {
			if err := os.WriteFile(cfg, []byte("apiVersion: v1\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.WriteFile(cfg, nil, 0o600) })
		}},
		{"kubeconfig missing", func(t *testing.T) {
			if err := os.Remove(cfg); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.WriteFile(cfg, nil, 0o600) })
		}},
		{"KUBERNETES_MASTER set", func(t *testing.T) { t.Setenv("KUBERNETES_MASTER", "https://master.invalid") }},
		{"HELM_KUBETOKEN set", func(t *testing.T) { t.Setenv("HELM_KUBETOKEN", "x") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			saveGlobals(t)
			c.reopen(t)
			if err := verify(cfg); err == nil {
				t.Error("verify accepted it")
			}
		})
	}
}

// Run fails closed: when isolation cannot be established — here, no temp dir for the
// empty kubeconfig — the package exits nonzero WITHOUT running a single test.
func TestRunRefusesWhenIsolationFails(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), os.Args[0],
		"-test.run=^TestApplyClearsEveryAmbientDoor$", "-test.v", "-test.count=1")
	cmd.Env = append(os.Environ(), "TMPDIR="+filepath.Join(t.TempDir(), "absent"))
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("the child exited %v, want status 1:\n%s", err, out)
	}
	if !strings.Contains(string(out), "refusing to run tests that could reach a real cluster") {
		t.Errorf("the child did not report the refusal:\n%s", out)
	}
	if strings.Contains(string(out), "=== RUN") {
		t.Errorf("the child ran tests without isolation:\n%s", out)
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

	// And RequireIsolated's verdict turns each of these into a failure. The clean reading
	// of the same output is the counterweight: the verdict is not simply "always refuse".
	named := []string{"TestLeakOnPurpose"}
	if err := verdict(out, nil, named, 0); err != nil {
		t.Fatalf("the verdict refused a clean reading: %v", err)
	}
	if err := verdict(out, exitErr, named, requests()); err == nil || !strings.Contains(err.Error(), "request(s)") {
		t.Errorf("the verdict on a counted leak was %v, want the request count reported", err)
	}
	if err := verdict(out, nil, []string{"TestLeakOnPurpose", "TestNeverRan"}, 0); err == nil ||
		!strings.Contains(err.Error(), "never ran TestNeverRan") {
		t.Errorf("the verdict on a test the child never ran was %v, want it reported", err)
	}
	if err := verdict(out, errors.New("exit status 1"), named, 0); err == nil ||
		!strings.Contains(err.Error(), "the child failed") {
		t.Errorf("the verdict on a failed child was %v, want it reported", err)
	}
}

// A probe that reaches nothing makes every child read zero, so RequireIsolated must refuse
// to run the child at all rather than read that zero as isolation.
func TestABlindInstrumentIsRefused(t *testing.T) {
	if os.Getenv(childEnv) == "1" {
		t.Skip("only the parent checks its instrument")
	}
	blind := append(append([]probe(nil), probes...), probe{"a probe that reaches nothing", func(*testing.T) {}})
	err := checkIsolated(t, blind, []string{t.Name()})
	if err == nil || !strings.Contains(err.Error(), "a probe that reaches nothing") ||
		!strings.Contains(err.Error(), "the instrument is blind") {
		t.Fatalf("checkIsolated with a blind probe returned %v, want the blind probe named", err)
	}
}
