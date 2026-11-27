// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// The credential-plugin tests run a REAL plugin: this test binary, re-executed with
// execPluginHelperEnv naming what it does (TestMain hands it to execPluginHelper). It
// prints an ExecCredential, or hangs, or leaves a child behind, exactly as a plugin
// on PATH would, so the process handling under test is the production one.

const (
	execPluginHelperEnv = "DCCTL_TEST_EXEC_PLUGIN"
	// pluginPIDFileEnv names a file the plugin writes its process ID to as it starts;
	// a child it starts writes its own to the same name with ".child" added.
	pluginPIDFileEnv = "DCCTL_TEST_PLUGIN_PID_FILE"
	// The canaries: a credential the plugin issues must never appear in any output.
	pluginToken      = "plugintok-s3cr3t-5521"
	pluginAPIVersion = "client.authentication.k8s.io/v1"
)

// execPluginHelper is the plugin, by mode. Its exit status is the process's.
func execPluginHelper(mode string) int {
	if f := os.Getenv(pluginPIDFileEnv); f != "" {
		suffix := ""
		if mode == "sleeper" {
			suffix = ".child"
		}
		// Written whole, then renamed, so a reader never sees half of it.
		if os.WriteFile(f+suffix+".tmp", []byte(strconv.Itoa(os.Getpid())), 0o600) == nil {
			_ = os.Rename(f+suffix+".tmp", f+suffix)
		}
	}
	credential := func(token string, expires time.Time, cert, key string) int {
		status := map[string]any{}
		if token != "" {
			status["token"] = token
		}
		if cert != "" {
			status["clientCertificateData"], status["clientKeyData"] = cert, key
		}
		if !expires.IsZero() {
			status["expirationTimestamp"] = expires.UTC().Format(time.RFC3339Nano)
		}
		apiVersion := os.Getenv("DCCTL_TEST_ANSWER_API")
		if apiVersion == "" {
			apiVersion = pluginAPIVersion
		}
		b, _ := json.Marshal(map[string]any{"apiVersion": apiVersion, "kind": "ExecCredential", "status": status})
		_, _ = os.Stdout.Write(b)
		return 0
	}
	startSleeper := func(holdStderr bool) {
		c := exec.Command(os.Args[0])
		c.Env = append(os.Environ(), execPluginHelperEnv+"=sleeper")
		if holdStderr {
			c.Stderr = os.Stderr
		}
		if err := c.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "the sleeper did not start:", err)
			os.Exit(5)
		}
		// Not waited for: it is meant to outlive the plugin. But it is running, and has
		// said so, before the plugin goes on, or a test could miss it.
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			if _, err := os.Stat(os.Getenv(pluginPIDFileEnv) + ".child"); err == nil {
				return
			}
		}
		fmt.Fprintln(os.Stderr, "the sleeper did not report")
		os.Exit(5)
	}
	switch mode {
	case "sleeper":
		time.Sleep(time.Hour)
		return 0
	case "token":
		// The plugin checks what it was given, as a real one relies on it: the exec
		// information, the kubeconfig's environment and the caller's own.
		if why := checkExecInfo(); why != "" {
			fmt.Fprintln(os.Stderr, "bad exec info:", why)
			return 3
		}
		fmt.Fprintln(os.Stderr, "debug: issuing "+pluginToken)
		return credential(pluginToken, time.Now().Add(time.Hour), "", "")
	case "cert":
		return credential("", time.Now().Add(time.Hour), os.Getenv("DCCTL_TEST_CERT"), os.Getenv("DCCTL_TEST_KEY"))
	case "expired":
		return credential(pluginToken, time.Now().Add(-time.Hour), "", "")
	case "hang":
		startSleeper(false)
		time.Sleep(time.Hour)
		return 0
	case "stderr-holder":
		startSleeper(true)
		defer stampExit()
		return credential(pluginToken, time.Now().Add(time.Hour), "", "")
	case "huge":
		// A good credential, then 8 MiB of whitespace, which JSON allows: only the cap
		// on what is read refuses it.
		if credential(pluginToken, time.Now().Add(time.Hour), "", "") != 0 {
			return 1
		}
		chunk := []byte(strings.Repeat(" ", 64<<10))
		for i := 0; i < 128; i++ {
			if _, err := os.Stdout.Write(chunk); err != nil {
				return 1
			}
		}
		return 0
	case "expiring":
		// Good for a moment: the run outlives it.
		expires, _ := time.ParseDuration(os.Getenv("DCCTL_TEST_EXPIRES_IN"))
		return credential(pluginToken, time.Now().Add(expires), "", "")
	case "notjson":
		_, _ = io.WriteString(os.Stdout, "this is not an ExecCredential "+pluginToken)
		return 0
	case "fail":
		fmt.Fprintln(os.Stderr, "error: the login at https://user:pw-s3cr3t-77@login.example/cb?code=code-s3cr3t-88 failed")
		return 1
	case "otherhost":
		resp, err := http.Get(os.Getenv("DCCTL_TEST_OTHER_URL"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "the other host did not answer")
			return 4
		}
		_ = resp.Body.Close()
		return credential(pluginToken, time.Now().Add(time.Hour), "", "")
	}
	return execPluginHelperOS(mode, credential)
}

// stampExit records when the plugin exits, so a test can time the run from then rather
// than from a start that a slow (race-instrumented) helper binary drags out.
func stampExit() {
	if f := os.Getenv(pluginPIDFileEnv); f != "" {
		_ = os.WriteFile(f+".exit", []byte(strconv.FormatInt(time.Now().UnixNano(), 10)), 0o600)
	}
}

// sinceExit is how long after the plugin's exit (stampExit) end is.
func sinceExit(t *testing.T, pidFile string, end time.Time) time.Duration {
	t.Helper()
	b, err := os.ReadFile(pidFile + ".exit")
	if err != nil {
		t.Fatalf("the plugin did not stamp its exit: %v", err)
	}
	ns, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return end.Sub(time.Unix(0, ns))
}

// checkExecInfo is what a plugin checks of its KUBERNETES_EXEC_INFO and environment:
// "" when all is as client-go would give it.
func checkExecInfo() string {
	var info struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Spec       struct {
			Interactive bool `json:"interactive"`
			Cluster     *struct {
				Server string `json:"server"`
			} `json:"cluster"`
		} `json:"spec"`
	}
	if err := json.Unmarshal([]byte(os.Getenv("KUBERNETES_EXEC_INFO")), &info); err != nil {
		return "KUBERNETES_EXEC_INFO is not JSON"
	}
	want := os.Getenv("DCCTL_TEST_WANT_API")
	if want == "" {
		want = pluginAPIVersion
	}
	switch {
	case info.APIVersion != want || info.Kind != "ExecCredential":
		return "KUBERNETES_EXEC_INFO is " + info.APIVersion + " " + info.Kind
	case info.Spec.Interactive:
		return "the plugin was told it may be interactive"
	case os.Getenv("DCCTL_TEST_FROM_SPEC") != "spec-value":
		return "the kubeconfig's env did not reach the plugin"
	case os.Getenv("DCCTL_TEST_INHERITED") != "inherited-value":
		return "the caller's environment did not reach the plugin"
	}
	if server := os.Getenv("DCCTL_TEST_WANT_SERVER"); server != "" {
		if info.Spec.Cluster == nil || info.Spec.Cluster.Server != server {
			return "the cluster information is missing or wrong"
		}
	} else if info.Spec.Cluster != nil {
		return "cluster information was given though the kubeconfig did not ask for it"
	}
	return ""
}

// pluginCluster is an API server that knows the plugin's credential: it names the
// caller only when the credential is the plugin's token or its client certificate.
func pluginCluster(t *testing.T, tlsServer bool) (*httptest.Server, *recorder) {
	return pluginClusterWith(t, tlsServer, clusterBehaviour{})
}

// clusterBehaviour is how a pluginCluster departs from an ordinary API server.
type clusterBehaviour struct {
	// versionNeedsAuth refuses an anonymous version request, as some providers do.
	versionNeedsAuth bool
	// ssrDelay holds the self review's answer back this long.
	ssrDelay time.Duration
}

func pluginClusterWith(t *testing.T, tlsServer bool, b clusterBehaviour) (*httptest.Server, *recorder) {
	t.Helper()
	api := &recorder{}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.note(r)
		w.Header().Set("Content-Type", "application/json")
		known := r.Header.Get("Authorization") == "Bearer "+pluginToken ||
			(r.TLS != nil && len(r.TLS.PeerCertificates) > 0 && r.TLS.PeerCertificates[0].Subject.CommonName == "alice")
		if r.URL.Path == ssrPath && b.ssrDelay > 0 {
			time.Sleep(b.ssrDelay)
		}
		switch {
		case r.URL.Path == "/version" && !b.versionNeedsAuth:
			_, _ = io.WriteString(w, versionJSON)
		case !known:
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Unauthorized","code":401}`)
		case r.URL.Path == ssrPath:
			_, _ = io.WriteString(w, `{"kind":"SelfSubjectReview","apiVersion":"authentication.k8s.io/v1","status":{"userInfo":{"username":"plugin-user"}}}`)
		default:
			_, _ = io.WriteString(w, `{"kind":"SelfSubjectAccessReview","apiVersion":"authorization.k8s.io/v1","status":{"allowed":true}}`)
		}
	})
	var srv *httptest.Server
	if tlsServer {
		srv = httptest.NewUnstartedServer(h)
		srv.TLS = &tls.Config{ClientAuth: tls.RequestClientCert}
		srv.StartTLS()
	} else {
		srv = httptest.NewServer(h)
	}
	t.Cleanup(srv.Close)
	return srv, api
}

// pluginConfig is a context whose credentials come from the helper plugin in mode.
func pluginConfig(t *testing.T, host, mode string, env ...clientcmdapi.ExecEnvVar) (*rest.Config, string) {
	t.Helper()
	t.Setenv("DCCTL_TEST_INHERITED", "inherited-value")
	pidFile := filepath.Join(t.TempDir(), "plugin.pid")
	env = append([]clientcmdapi.ExecEnvVar{{Name: execPluginHelperEnv, Value: mode},
		{Name: "DCCTL_TEST_FROM_SPEC", Value: "spec-value"}, {Name: pluginPIDFileEnv, Value: pidFile},
		// A race-instrumented helper otherwise sleeps a second at exit.
		{Name: "GORACE", Value: "atexit_sleep_ms=0"}}, env...)
	return &rest.Config{Host: host, ExecProvider: &clientcmdapi.ExecConfig{
		Command: os.Args[0], APIVersion: pluginAPIVersion, Env: env,
		InteractiveMode: clientcmdapi.IfAvailableExecInteractiveMode,
	}}, pidFile
}

// pluginRun is one advisory run against a context with a plugin.
type pluginRun struct {
	rows []installflow.PreflightObservation
	end  time.Time
	// took is from the start, or from the cancel when the run was cancelled.
	took time.Duration
	// output is everything the run wrote anywhere it can be seen: every row's text and
	// fix, and the process's own stdout and stderr.
	output string
}

// runWithPlugin runs the advisory checks against cfg. cancelWhen, when set, cancels the
// run's context as soon as it reports true (polled), as an Exit does.
func runWithPlugin(t *testing.T, cfg *rest.Config, cancelWhen func() bool) pluginRun {
	t.Helper()
	r := cloudDiagnosticsRunner(prodTarget, func(life context.Context, _ cloudTarget) (cloudProbes, error) {
		p, err := cloudProbesFrom(life, cfg)
		p.http = cloudHTTPClient(newCloudFakes(t))
		return p, err
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var cancelledAt time.Time
	if cancelWhen != nil {
		go func() {
			for ctx.Err() == nil {
				if cancelWhen() {
					mu.Lock()
					cancelledAt = time.Now()
					mu.Unlock()
					cancel()
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
		}()
	}
	var run pluginRun
	var out strings.Builder
	stdio := captureStdio(t)
	start := time.Now()
	_, err := r.RunPreflight(ctx, func(o installflow.PreflightObservation) {
		if o.Observation == installflow.ObserveCheck {
			run.rows = append(run.rows, o)
		}
		out.WriteString(o.Text.String() + "\n" + o.Fix.String() + "\n")
	})
	end := time.Now()
	run.end = end
	out.WriteString(stdio())
	if err != nil {
		t.Fatalf("the advisory run errored: %v", err)
	}
	run.output = out.String()
	mu.Lock()
	if cancelledAt.IsZero() {
		run.took = end.Sub(start)
	} else {
		run.took = end.Sub(cancelledAt)
	}
	mu.Unlock()
	for _, o := range run.rows {
		if o.Verdict == installflow.CheckFail {
			t.Errorf("an advisory row reads FAIL: %q", o.Text)
		}
	}
	// 🔴 THE CREDENTIAL NEVER APPEARS, in any form: rows, fixes, the process's output.
	for _, canary := range []string{pluginToken, "s3cr3t", "BEGIN", "PRIVATE KEY"} {
		if strings.Contains(run.output, canary) {
			t.Errorf("the output carries %q:\n%s", canary, run.output)
		}
	}
	return run
}

// captureStdio swaps the process's stdout and stderr for pipes until the returned
// function is called, which restores them and returns what was written.
func captureStdio(t *testing.T) func() string {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = w, w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	restored := false
	restore := func() string {
		if restored {
			return ""
		}
		restored = true
		os.Stdout, os.Stderr = oldOut, oldErr
		_ = w.Close()
		s := <-done
		_ = r.Close()
		return s
	}
	t.Cleanup(func() { restore() })
	return restore
}

// pids are the process IDs the plugin and its child wrote, those that exist.
func pids(pidFile string) []int {
	var out []int
	for _, f := range []string{pidFile, pidFile + ".child"} {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
			out = append(out, pid)
		}
	}
	return out
}

// assertNoProcessLeft fails when the plugin or its child is still running a moment
// after the run.
func assertNoProcessLeft(t *testing.T, pidFile string, want int) {
	t.Helper()
	ps := pids(pidFile)
	if len(ps) != want {
		t.Fatalf("%d process ID(s) written, want %d", len(ps), want)
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for _, pid := range ps {
		for processAlive(pid) {
			if time.Now().After(deadline) {
				t.Errorf("process %d is still running after the run", pid)
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func authRow(t *testing.T, run pluginRun) installflow.PreflightObservation {
	t.Helper()
	return row(t, run.rows, "authentication")
}

// 🔴 A PLUGIN'S TOKEN AUTHENTICATES, through the same guarded client: the reviews carry
// it, nothing else does, and authentication passes on the API server's own word.
func TestAnExecPluginsTokenAuthenticates(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(cfg *rest.Config, srv string)
	}{
		{"v1", func(*rest.Config, string) {}},
		{"v1beta1 with no interactiveMode", func(cfg *rest.Config, _ string) {
			cfg.ExecProvider.APIVersion, cfg.ExecProvider.InteractiveMode = "client.authentication.k8s.io/v1beta1", ""
			cfg.ExecProvider.Env = append(cfg.ExecProvider.Env,
				clientcmdapi.ExecEnvVar{Name: "DCCTL_TEST_WANT_API", Value: "client.authentication.k8s.io/v1beta1"},
				clientcmdapi.ExecEnvVar{Name: "DCCTL_TEST_ANSWER_API", Value: "client.authentication.k8s.io/v1beta1"})
		}},
		{"never interactive", func(cfg *rest.Config, _ string) {
			cfg.ExecProvider.InteractiveMode = clientcmdapi.NeverExecInteractiveMode
		}},
		{"with cluster information", func(cfg *rest.Config, srv string) {
			cfg.ExecProvider.ProvideClusterInfo = true
			cfg.ExecProvider.Env = append(cfg.ExecProvider.Env, clientcmdapi.ExecEnvVar{Name: "DCCTL_TEST_WANT_SERVER", Value: srv})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, api := pluginCluster(t, false)
			cfg, pidFile := pluginConfig(t, srv.URL, "token")
			tc.edit(cfg, srv.URL)
			run := runWithPlugin(t, cfg, nil)
			if r := authRow(t, run); r.Verdict != installflow.CheckPass {
				t.Fatalf("authentication reads %s %q (%s)", r.Verdict, r.Text, r.Fix)
			}
			if r := row(t, run.rows, "may create namespaces"); r.Verdict != installflow.CheckPass {
				t.Errorf("may create namespaces reads %s %q", r.Verdict, r.Text)
			}
			seen, auth := api.snapshot()
			if len(auth) != 1+len(cloudAccessNeeds) {
				t.Errorf("%d credentialed requests, want the self review and %d access reviews: %v", len(auth), len(cloudAccessNeeds), seen)
			}
			for _, a := range auth {
				if a != "Bearer "+pluginToken {
					t.Errorf("a request carried %q", a)
				}
			}
			assertNoProcessLeft(t, pidFile, 1)
		})
	}
}

// A plugin's client certificate authenticates too, presented on the same transport.
func TestAnExecPluginsClientCertAuthenticates(t *testing.T) {
	srv, api := pluginCluster(t, true)
	certPEM, keyPEM := clientCert(t)
	cfg, _ := pluginConfig(t, srv.URL, "cert", clientcmdapi.ExecEnvVar{Name: "DCCTL_TEST_CERT", Value: string(certPEM)},
		clientcmdapi.ExecEnvVar{Name: "DCCTL_TEST_KEY", Value: string(keyPEM)})
	cfg.CAData = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	run := runWithPlugin(t, cfg, nil)
	if r := authRow(t, run); r.Verdict != installflow.CheckPass {
		t.Fatalf("authentication reads %s %q (%s)", r.Verdict, r.Text, r.Fix)
	}
	if _, auth := api.snapshot(); len(auth) != 0 {
		t.Errorf("a certificate credential sent a header: %v", auth)
	}
}

// 🔴 AN EXPIRED CREDENTIAL IS NEVER SENT: the run says it could not authenticate, and no
// request carries it.
func TestAnExpiredPluginCredentialIsNotUsed(t *testing.T) {
	srv, api := pluginCluster(t, false)
	cfg, _ := pluginConfig(t, srv.URL, "expired")
	run := runWithPlugin(t, cfg, nil)
	if r := authRow(t, run); r.Verdict != installflow.CheckUnknown || !strings.Contains(r.Text.String(), "expired") {
		t.Errorf("authentication reads %s %q", r.Verdict, r.Text)
	}
	for _, o := range run.rows {
		if strings.HasPrefix(o.Text.String(), "may ") && o.Verdict != installflow.CheckNotChecked {
			t.Errorf("%q reads %s", o.Text, o.Verdict)
		}
	}
	if _, auth := api.snapshot(); len(auth) != 0 {
		t.Errorf("the expired credential was sent: %v", auth)
	}
}

// 🔴 A CREDENTIAL IS NOT SENT PAST ITS EXPIRY, even one that was good when it arrived:
// the guard refuses a request once the credential it carries has expired.
func TestTheGuardRefusesARequestPastTheCredentialsExpiry(t *testing.T) {
	srv, api := pluginCluster(t, false)
	for _, tc := range []struct {
		name     string
		notAfter time.Time
		sent     bool
	}{
		{"expired", time.Now().Add(-time.Second), false},
		{"live", time.Now().Add(time.Hour), true},
		{"no expiry", time.Time{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, _ := api.snapshot()
			kube, err := cloudKubeClientUntil(context.Background(), &rest.Config{Host: srv.URL, BearerToken: pluginToken}, tc.notAfter)
			if err != nil {
				t.Fatal(err)
			}
			_, err = kube.Discovery().ServerVersionWithContext(context.Background())
			after, _ := api.snapshot()
			if sent := len(after) > len(before); sent != tc.sent || (err == nil) != tc.sent {
				t.Errorf("sent %v (err %v), want %v", sent, err, tc.sent)
			}
		})
	}
}

// 🔴 A HUNG PLUGIN IS BOUNDED AND KILLED, with what it started: the run reads UNKNOWN
// when the plugin's budget runs out, and leaves no process behind.
func TestAHungPluginIsBoundedAndKilled(t *testing.T) {
	old := execPluginTimeout
	t.Cleanup(func() { execPluginTimeout = old })
	execPluginTimeout = 1500 * time.Millisecond
	srv, _ := pluginCluster(t, false)
	cfg, pidFile := pluginConfig(t, srv.URL, "hang")
	run := runWithPlugin(t, cfg, nil)
	if r := authRow(t, run); r.Verdict != installflow.CheckUnknown || !strings.Contains(r.Text.String(), "did not finish") {
		t.Errorf("authentication reads %s %q", r.Verdict, r.Text)
	}
	if run.took > execPluginTimeout+commandWaitDelay {
		t.Errorf("the run took %v", run.took)
	}
	assertNoProcessLeft(t, pidFile, 2)
}

// 🔴 AN EXIT KILLS A RUNNING PLUGIN AT ONCE: the run returns within commandWaitDelay of
// the cancel, and the plugin and its child are gone.
func TestAnExitKillsARunningPlugin(t *testing.T) {
	srv, _ := pluginCluster(t, false)
	cfg, pidFile := pluginConfig(t, srv.URL, "hang")
	run := runWithPlugin(t, cfg, func() bool { return len(pids(pidFile)) == 2 })
	if run.took >= commandWaitDelay {
		t.Errorf("the run returned %v after the Exit", run.took)
	}
	for _, o := range run.rows {
		if o.Verdict == installflow.CheckPass && strings.HasPrefix(o.Text.String(), "authentication") {
			t.Errorf("an exited run reads %q", o.Text)
		}
	}
	assertNoProcessLeft(t, pidFile, 2)
}

// A child that keeps the plugin's stderr open does not hold the run: the plugin's own
// exit ends it, and the child is killed with it.
func TestAChildHoldingStderrDoesNotHoldTheRun(t *testing.T) {
	srv, _ := pluginCluster(t, false)
	cfg, pidFile := pluginConfig(t, srv.URL, "stderr-holder")
	run := runWithPlugin(t, cfg, nil)
	// Held, it would end commandWaitDelay after the plugin's exit, or never.
	if after := sinceExit(t, pidFile, run.end); after >= commandWaitDelay/2 {
		t.Errorf("the run ended %v after the plugin exited", after)
	}
	if r := authRow(t, run); r.Verdict != installflow.CheckPass {
		t.Errorf("authentication reads %s %q (%s)", r.Verdict, r.Text, r.Fix)
	}
	assertNoProcessLeft(t, pidFile, 2)
}

// 🔴 A PLUGIN THAT MUST BE INTERACTIVE IS NEVER RUN: interactiveMode Always, or a v1
// entry that does not say (client-go refuses one), reads NOT CHECKED, and nothing starts.
func TestAPluginThatMustBeInteractiveIsNotRun(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode clientcmdapi.ExecInteractiveMode
	}{
		{"Always", clientcmdapi.AlwaysExecInteractiveMode},
		{"unstated on v1", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, api := pluginCluster(t, false)
			cfg, pidFile := pluginConfig(t, srv.URL, "token")
			cfg.ExecProvider.InteractiveMode = tc.mode
			run := runWithPlugin(t, cfg, nil)
			if r := authRow(t, run); r.Verdict != installflow.CheckNotChecked {
				t.Errorf("authentication reads %s %q", r.Verdict, r.Text)
			}
			if len(pids(pidFile)) != 0 {
				t.Error("the plugin was run")
			}
			if _, auth := api.snapshot(); len(auth) != 0 {
				t.Errorf("credentials were sent: %v", auth)
			}
		})
	}
}

// 🔴 WHAT A PLUGIN PRINTS IS NEVER TAKEN ON TRUST: output that is huge, not JSON, of
// another API version, or a failure, reads UNKNOWN and is never quoted.
func TestAPluginsBadOutputIsRefused(t *testing.T) {
	for _, tc := range []struct {
		mode string
		env  []clientcmdapi.ExecEnvVar
	}{
		{mode: "huge"},
		{mode: "notjson"},
		{mode: "token", env: []clientcmdapi.ExecEnvVar{{Name: "DCCTL_TEST_ANSWER_API", Value: "client.authentication.k8s.io/v1beta1"}}},
		{mode: "fail"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			srv, api := pluginCluster(t, false)
			cfg, pidFile := pluginConfig(t, srv.URL, tc.mode, tc.env...)
			run := runWithPlugin(t, cfg, nil)
			if r := authRow(t, run); r.Verdict != installflow.CheckUnknown {
				t.Errorf("authentication reads %s %q", r.Verdict, r.Text)
			}
			if _, auth := api.snapshot(); len(auth) != 0 {
				t.Errorf("credentials were sent: %v", auth)
			}
			assertNoProcessLeft(t, pidFile, 1)
		})
	}
}

// A failing plugin's stderr is shown, capped and with every URL's credentials removed:
// it is usually the one line that says what to do.
func TestAFailingPluginsStderrIsShownRedacted(t *testing.T) {
	srv, _ := pluginCluster(t, false)
	cfg, _ := pluginConfig(t, srv.URL, "fail")
	run := runWithPlugin(t, cfg, nil) // asserts no "s3cr3t" anywhere
	r := authRow(t, run)
	if !strings.Contains(r.Fix.String(), "login.example") {
		t.Errorf("the plugin's own words are not shown: %q", r.Fix)
	}
}

// A plugin that is not installed is a problem the install will meet: WARN, not PASS.
func TestAMissingPluginWarns(t *testing.T) {
	srv, _ := pluginCluster(t, false)
	cfg, _ := pluginConfig(t, srv.URL, "token")
	cfg.ExecProvider.Command = filepath.Join(t.TempDir(), "no-such-plugin")
	run := runWithPlugin(t, cfg, nil)
	if r := authRow(t, run); r.Verdict != installflow.CheckWarn || !strings.Contains(r.Text.String(), "no-such-plugin") {
		t.Errorf("authentication reads %s %q", r.Verdict, r.Text)
	}
}

// 🔴 WHATEVER THE PLUGIN ITSELF CONTACTS, THE CREDENTIAL GOES ONLY TO THE API SERVER: a
// plugin that calls another host gets that host nothing of dcctl's.
func TestAPluginsRequestToAnotherHostGetsNoCredential(t *testing.T) {
	other := &recorder{}
	otherSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { other.note(r) }))
	defer otherSrv.Close()
	srv, api := pluginCluster(t, false)
	cfg, _ := pluginConfig(t, srv.URL, "otherhost", clientcmdapi.ExecEnvVar{Name: "DCCTL_TEST_OTHER_URL", Value: otherSrv.URL + "/token"})
	run := runWithPlugin(t, cfg, nil)
	if r := authRow(t, run); r.Verdict != installflow.CheckPass {
		t.Fatalf("authentication reads %s %q (%s)", r.Verdict, r.Text, r.Fix)
	}
	seen, auth := other.snapshot()
	if len(seen) != 1 || len(auth) != 0 {
		t.Errorf("the other host saw %v with credentials %v; want only the plugin's own request", seen, auth)
	}
	if _, auth := api.snapshot(); len(auth) == 0 {
		t.Error("the API server was never sent the credential")
	}
}

// 🔴 AN AUTH-PROVIDER (the legacy gcp and azure entries) IS NEVER RUN: authentication and
// permissions read NOT CHECKED, and the API server is sent no credentials.
func TestAnAuthProviderIsNeverRun(t *testing.T) {
	srv, api := pluginCluster(t, false)
	cfg := &rest.Config{Host: srv.URL, AuthProvider: &clientcmdapi.AuthProviderConfig{Name: "gcp",
		Config: map[string]string{"cmd-path": os.Args[0], "access-token": pluginToken}}}
	run := runWithPlugin(t, cfg, nil)
	if r := authRow(t, run); r.Verdict != installflow.CheckNotChecked || !strings.Contains(r.Text.String(), "plugin") {
		t.Errorf("authentication reads %s %q", r.Verdict, r.Text)
	}
	for _, o := range run.rows {
		if strings.HasPrefix(o.Text.String(), "may ") && o.Verdict != installflow.CheckNotChecked {
			t.Errorf("%q reads %s", o.Text, o.Verdict)
		}
	}
	if _, auth := api.snapshot(); len(auth) != 0 {
		t.Errorf("the API server was sent credentials %v", auth)
	}
}

// A provider that refuses an anonymous version request (the plugin runs after it) is
// still authenticated by the plugin: that refusal says nothing of the credential.
func TestAnAnonymousVersionRefusalDoesNotCountAgainstThePlugin(t *testing.T) {
	srv, _ := pluginClusterWith(t, false, clusterBehaviour{versionNeedsAuth: true})
	cfg, _ := pluginConfig(t, srv.URL, "token")
	run := runWithPlugin(t, cfg, nil)
	if r := authRow(t, run); r.Verdict != installflow.CheckPass {
		t.Fatalf("authentication reads %s %q (%s)", r.Verdict, r.Text, r.Fix)
	}
}

// 🔴 A PLUGIN'S CREDENTIAL IS NOT SENT PAST ITS EXPIRY: one good for a moment carries the
// self review, and once it has expired the access reviews are not sent with it.
func TestAPluginCredentialIsNotSentPastItsExpiry(t *testing.T) {
	// Long enough to outlast the helper's start under -race, short enough to expire
	// while the self review is held.
	srv, api := pluginClusterWith(t, false, clusterBehaviour{ssrDelay: 7 * time.Second})
	cfg, _ := pluginConfig(t, srv.URL, "expiring", clientcmdapi.ExecEnvVar{Name: "DCCTL_TEST_EXPIRES_IN", Value: "5s"})
	run := runWithPlugin(t, cfg, nil)
	if r := authRow(t, run); r.Verdict != installflow.CheckPass {
		t.Fatalf("authentication reads %s %q (%s)", r.Verdict, r.Text, r.Fix)
	}
	for _, o := range run.rows {
		if strings.HasPrefix(o.Text.String(), "may ") && o.Verdict == installflow.CheckPass {
			t.Errorf("%q reads PASS on an expired credential", o.Text)
		}
	}
	if seen, auth := api.snapshot(); len(auth) != 1 {
		t.Errorf("%d requests carried the credential, want only the self review: %v", len(auth), seen)
	}
}

// 🔴 A PLUGIN'S CREDENTIAL GOES THROUGH THE SAME GUARD as static credentials: an API
// server that redirects the self review elsewhere does not get it followed, so the other
// host is never sent the credential, and authentication does not pass.
func TestAPluginsCredentialIsNeverRedirected(t *testing.T) {
	other := &recorder{}
	otherSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { other.note(r) }))
	defer otherSrv.Close()
	api := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.note(r)
		if r.URL.Path == "/version" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, versionJSON)
			return
		}
		http.Redirect(w, r, otherSrv.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	cfg, _ := pluginConfig(t, srv.URL, "token")
	run := runWithPlugin(t, cfg, nil)
	if r := authRow(t, run); r.Verdict == installflow.CheckPass {
		t.Errorf("authentication reads PASS through a redirect")
	}
	if seen, auth := other.snapshot(); len(seen) != 0 || len(auth) != 0 {
		t.Errorf("the redirect was followed: the other host saw %v with %v", seen, auth)
	}
	if _, auth := api.snapshot(); len(auth) == 0 {
		t.Error("the API server was never sent the credential")
	}
}
