// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package kubeisolation keeps dcctl's tests from reaching a real Kubernetes cluster.
//
// Every package whose test binary links a cluster client that reads its target from the
// environment — client-go's kubeconfig loading rules, Helm's environment settings, the
// OpenTofu roots' kubeconfig variable — starts its tests from
//
//	func TestMain(m *testing.M) { os.Exit(kubeisolation.Run(m)) }
//
// which, before any test runs, leaves the process in the state CI's runners are in: no
// kubeconfig names a cluster and nothing in the environment names one either. A test that
// wants a cluster brings its own (t.Setenv("KUBECONFIG", ...)), and t.Setenv puts the
// isolated value back when it ends.
//
// 🔴 WHY THIS IS STRUCTURAL AND NOT PER-TEST. The bootstrap package once isolated its
// cluster reads one seam at a time, and each time a new door opened the tests quietly
// contacted whatever cluster the developer's current-context named — on a live GKE
// context they sent real requests, and the credential plugin client-go execs for it
// wrote into a test's fake HOME. CI never saw it: its runners have no kubeconfig, so the
// same reads failed there and the tests passed for a different reason. Pointing HOME at a
// temp dir does not help either: client-go fixes the home kubeconfig path
// (clientcmd.RecommendedHomeFile) when the package is initialised, before any test can
// change HOME. The same holds for the cluster Helm falls back to over an empty kubeconfig
// (clientcmd.ClusterDefaults, read from KUBERNETES_MASTER at init). So the environment and
// those two values are changed, once, before the first test.
//
// What this cannot fence, by construction: a tool a test EXECS reads its own defaults.
// kubectl with an empty kubeconfig tries localhost:8080; kind, ko and docker act on the
// local Docker daemon whatever KUBECONFIG says. No test in backend/cli execs any of them
// today; a test that drives a real local provider or preflight check must stub it.
package kubeisolation

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"helm.sh/helm/v3/pkg/cli"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// Run isolates this test process from every cluster its environment could name, then runs
// the tests. Call it from TestMain: os.Exit(kubeisolation.Run(m)).
//
// If isolation cannot be established the package FAILS without running a single test:
// running them un-isolated is exactly what this exists to prevent.
func Run(m *testing.M) int {
	dir, err := apply()
	if dir != "" {
		defer os.RemoveAll(dir)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "kubeisolation: refusing to run tests that could reach a real cluster: %v\n", err)
		return 1
	}
	return m.Run()
}

// apply performs the isolation and returns the temp dir holding the empty kubeconfig. It
// re-reads the result before returning, so a step that silently did nothing is an error
// rather than a partially isolated process.
func apply() (string, error) {
	dir, err := os.MkdirTemp("", "dcctl-test-kube-*")
	if err != nil {
		return "", err
	}
	cfg := filepath.Join(dir, "config")
	// The file must EXIST and be empty. A 0-byte kubeconfig loads as an empty config —
	// the "no configuration has been provided" error CI's runners get. It must exist
	// because client-go's migration rule copies a legacy ~/.kube/.kubeconfig onto a
	// MISSING RecommendedHomeFile, which would re-import the developer's config.
	if err := os.WriteFile(cfg, nil, 0o600); err != nil {
		return dir, err
	}
	// An explicit KUBECONFIG replaces the home file in client-go's precedence, and Helm's
	// cli.New() reads the same variable.
	if err := os.Setenv("KUBECONFIG", cfg); err != nil {
		return dir, err
	}
	// The OpenTofu roots pass var.kubeconfig_path to their providers as config_path, which
	// the providers prefer over KUBECONFIG and which defaults to ~/.kube/config expanded
	// from the REAL home. Unsetting it would restore that default, so it is SET instead.
	if err := os.Setenv("TF_VAR_kubeconfig_path", cfg); err != nil {
		return dir, err
	}
	// Defence for a test that sets KUBECONFIG="" (which re-enables the home fallback):
	// the home path client-go computed at init now names the same empty file.
	clientcmd.RecommendedHomeFile = cfg
	clientcmd.RecommendedConfigDir = dir
	// Helm's cli.New() hands clientcmd.ClusterDefaults to the loader as overrides, and
	// client-go computed it at init as {Server: $KUBERNETES_MASTER}, or
	// http://localhost:8080 when that is unset. Over an empty kubeconfig that default
	// IS the cluster Helm targets, and unsetting the variable now is too late to change
	// it. Emptied, the empty kubeconfig stays the "no configuration" error CI gets.
	clientcmd.ClusterDefaults = clientcmdapi.Cluster{}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if clearedVar(name) {
			if err := os.Unsetenv(name); err != nil {
				return dir, err
			}
		}
	}
	return dir, verify(cfg)
}

// verify re-reads what apply set.
func verify(cfg string) error {
	if got := os.Getenv("KUBECONFIG"); got != cfg {
		return fmt.Errorf("KUBECONFIG is %q after isolation, want %q", got, cfg)
	}
	if got := os.Getenv("TF_VAR_kubeconfig_path"); got != cfg {
		return fmt.Errorf("TF_VAR_kubeconfig_path is %q after isolation, want %q", got, cfg)
	}
	if clientcmd.RecommendedHomeFile != cfg {
		return fmt.Errorf("client-go's home kubeconfig is %q after isolation, want %q", clientcmd.RecommendedHomeFile, cfg)
	}
	if !reflect.DeepEqual(clientcmd.ClusterDefaults, clientcmdapi.Cluster{}) {
		return fmt.Errorf("client-go's cluster defaults still name %q after isolation", clientcmd.ClusterDefaults.Server)
	}
	fi, err := os.Stat(cfg)
	if err != nil {
		return err
	}
	if fi.Size() != 0 {
		return fmt.Errorf("the isolated kubeconfig %s is %d bytes, want 0", cfg, fi.Size())
	}
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); clearedVar(name) {
			return fmt.Errorf("%s is still set after isolation", name)
		}
	}
	return nil
}

// clearedVar reports whether an environment variable can point a cluster client somewhere
// on its own, without a kubeconfig:
//   - KUBERNETES_SERVICE_HOST/PORT: client-go's in-cluster fallback, taken when the loaded
//     kubeconfig is empty — a test run inside a pod would otherwise reach THAT cluster;
//   - KUBERNETES_MASTER: already read into clientcmd.ClusterDefaults at init, which apply
//     empties; it is cleared as well so nothing started from this process inherits it;
//   - KUBE_*: the OpenTofu kubernetes/helm providers' environment (KUBE_CONFIG_PATH,
//     KUBE_HOST, KUBE_TOKEN, KUBE_CTX, ...);
//   - HELM_KUBE*: Helm's API server, token and context overrides, which bypass the
//     kubeconfig entirely;
//   - TF_VAR_kubeconfig_context: the roots' context variable (the path is SET, see apply).
func clearedVar(name string) bool {
	switch name {
	case "KUBERNETES_SERVICE_HOST", "KUBERNETES_SERVICE_PORT", "KUBERNETES_MASTER", "TF_VAR_kubeconfig_context":
		return true
	}
	return strings.HasPrefix(name, "KUBE_") || strings.HasPrefix(name, "HELM_KUBE")
}

// CountingKubeconfig starts a stand-in API server that counts every request it receives
// and answers each with a 404 NotFound Status, writes a kubeconfig whose current-context
// targets it, and returns that kubeconfig's path, the request count so far, and a reset.
func CountingKubeconfig(t *testing.T) (path string, requests func() int64, reset func()) {
	t.Helper()
	path, _, requests, reset = countingServer(t)
	return path, requests, reset
}

// countingServer is CountingKubeconfig that also returns the server's URL, for the doors
// that name a server directly rather than through a kubeconfig.
func countingServer(t *testing.T) (path, url string, requests func() int64, reset func()) {
	t.Helper()
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintf(w, `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure",`+
			`"message":"counted by kubeisolation","reason":"NotFound","code":404}`)
	}))
	t.Cleanup(srv.Close)
	path = filepath.Join(t.TempDir(), "counting-kubeconfig")
	body := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: counting
  cluster:
    server: %s
users:
- name: counting
  user:
    token: counting
contexts:
- name: counting
  context:
    cluster: counting
    user: counting
current-context: counting
`, srv.URL)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, srv.URL, n.Load, func() { n.Store(0) }
}

// childEnv marks the re-executed test binary: inside it, RequireIsolated only probes.
const childEnv = "DCCTL_KUBEISOLATION_CHILD"

// childTimeout bounds a child run; a child that hits it has HUNG, which is not a verdict.
const childTimeout = 90 * time.Second

// probes are the ways dcctl builds a cluster client, each of which RequireIsolated shows
// reaches a live server before a zero from the child means anything.
var probes = []probe{
	{"client-go's default loading rules", probeClientGo},
	{"Helm's environment settings", probeHelm},
}

type probe struct {
	name string
	run  func(t *testing.T)
}

// RequireIsolated proves, by value, that this package's TestMain isolates its tests: it
// re-runs this test binary with KUBECONFIG and KUBERNETES_MASTER both naming a
// request-counting API server — what a developer's live current-context looks like to the
// process — runs the probes and the named entry points in it, and requires that the server
// saw NO request.
//
// The entry points are the tests KNOWN to read a cluster from the environment. They are
// the positive instances, not an inventory: what covers every other test in the package is
// that isolation is applied to the whole process before any of them runs, which the probes
// show for the process as a whole.
//
// It must be called from a top-level test.
func RequireIsolated(t *testing.T, entryPoints ...string) {
	t.Helper()
	if os.Getenv(childEnv) == "1" {
		for _, p := range probes {
			p.run(t)
		}
		return
	}
	if strings.Contains(t.Name(), "/") {
		t.Fatalf("RequireIsolated must be called from a top-level test, not the subtest %s", t.Name())
	}
	if err := checkIsolated(t, probes, append([]string{t.Name()}, entryPoints...)); err != nil {
		t.Fatal(err)
	}
}

// checkIsolated is RequireIsolated's parent half. It returns, rather than reports, the
// verdict, so that the harness's own tests can show each way it must refuse.
func checkIsolated(t *testing.T, probes []probe, names []string) error {
	t.Helper()
	path, url, requests, reset := countingServer(t)

	// Positive control: each probe DOES reach a cluster the environment names, so a zero
	// from the child means the child was isolated, not that the instrument was blind.
	t.Setenv("KUBECONFIG", path)
	for _, p := range probes {
		reset()
		p.run(t)
		if requests() < 1 {
			return fmt.Errorf("the probe through %s sent no request to a kubeconfig that names a live server: "+
				"the instrument is blind", p.name)
		}
	}
	reset()

	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = regexp.QuoteMeta(n)
	}
	out, exitErr := runChild(t, "^("+strings.Join(quoted, "|")+")$", path, "KUBERNETES_MASTER="+url)
	return verdict(out, exitErr, names, requests())
}

// verdict reads a child run: it is a pass only if every named test ran, the counting
// server saw no request, and the child itself passed.
func verdict(out string, exitErr error, names []string, requests int64) error {
	for _, n := range names {
		if !ranTest(out, n) {
			return fmt.Errorf("the child never ran %s, so its request count is not a verdict:\n%s", n, out)
		}
	}
	if requests != 0 {
		return fmt.Errorf("with KUBECONFIG naming a live API server, this package's tests sent it %d request(s): "+
			"its TestMain does not isolate it (want os.Exit(kubeisolation.Run(m))):\n%s", requests, out)
	}
	if exitErr != nil {
		return fmt.Errorf("the child failed (%v):\n%s", exitErr, out)
	}
	return nil
}

// runChild re-executes this test binary with -test.run=run, KUBECONFIG=kubeconfig and the
// child marker, plus any extra environment, and returns its combined output. A child that
// outlives childTimeout fails the test as a hang: a killed child has no verdict to read.
func runChild(t *testing.T, run, kubeconfig string, extraEnv ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), childTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0],
		"-test.run="+run, "-test.v", "-test.count=1", "-test.timeout=80s")
	env := make([]string, 0, len(os.Environ())+2+len(extraEnv))
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); name != "KUBECONFIG" && name != childEnv {
			env = append(env, kv)
		}
	}
	cmd.Env = append(append(env, "KUBECONFIG="+kubeconfig, childEnv+"=1"), extraEnv...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("the child hung past %v, which is not a verdict:\n%s", childTimeout, out)
	}
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatalf("could not run the child: %v", err)
	}
	return string(out), err
}

// ranTest reports whether a -test.v run's output shows the named test starting.
func ranTest(out, name string) bool {
	return regexp.MustCompile(`(?m)^=== RUN\s+` + regexp.QuoteMeta(name) + `$`).MatchString(out)
}

// probeClientGo builds a client exactly as dcctl's bootstrap.RestConfig does — the default
// loading rules, no overrides — and, if that succeeds, asks the server for its version. An
// error is the isolated outcome, so it is only logged.
func probeClientGo(t *testing.T) {
	t.Helper()
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).ClientConfig()
	askVersion(t, "client-go", cfg, err)
}

// probeHelm builds a client exactly as dcctl's Helm calls do — cli.New() and its REST
// client getter, which passes clientcmd.ClusterDefaults as overrides — and asks the same.
func probeHelm(t *testing.T) {
	t.Helper()
	cfg, err := cli.New().RESTClientGetter().ToRESTConfig()
	askVersion(t, "helm", cfg, err)
}

func askVersion(t *testing.T, via string, cfg *rest.Config, err error) {
	t.Helper()
	if err != nil {
		t.Logf("probe (%s): no cluster config: %v", via, err)
		return
	}
	cfg.Timeout = 5 * time.Second
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		t.Logf("probe (%s): %v", via, err)
		return
	}
	_, err = dc.ServerVersion()
	t.Logf("probe (%s): asked %s for its version: %v", via, cfg.Host, err)
}
