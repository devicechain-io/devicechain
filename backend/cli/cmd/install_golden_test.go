// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/devicechain-io/dcctl/internal/installflow/testkit"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// 🔴 THESE GOLDENS ARE A CONTRACT, NOT A RECORDING. They characterise what `dcctl install`
// prints, on which stream, with which exit status, and how it answers a piped stdin, so
// that the install engine can be restructured underneath the command and shown to say
// exactly the same bytes afterwards. A golden that changes in a PR that set out to change
// no output is the PR's bug, not the golden's; rewrite one (-update) only when the output
// is MEANT to change, and say so in the PR.
//
// Each scenario runs dcctl in a CHILD PROCESS (this test binary, re-executed), through the
// real Execute — the same os.Exit path, the same os.Stdout/os.Stderr the colour library
// captured at init, the same bufio read of os.Stdin the kind prompt makes — because an
// in-process capture would characterise a harness rather than the program. Nothing reaches
// a host tool or a cluster: the preflight's host seams (lookPath, runCmd, portInUse,
// systemCheck) are faked, PATH is an empty directory, the kubeconfig is isolated (or a
// fixture naming an unreachable server), and the scenarios that need the install to
// SUCCEED stand a recording fake in for the engine through installEngine.
//
// CI never passes -update.
var updateInstallGoldens = flag.Bool("update", false, "rewrite cmd/testdata/install_golden from the current output")

const (
	installGoldenChildEnv = "DCCTL_INSTALL_GOLDEN_CHILD"
	installGoldenPlanEnv  = "DCCTL_INSTALL_GOLDEN_PLAN"
	installGoldenKubeEnv  = "DCCTL_INSTALL_GOLDEN_KUBECONFIG"
	installGoldenDir      = "testdata/install_golden"
	installGoldenRelease  = "1.9.0"
	installGoldenImageTag = "v1.9.0"
	installGoldenGiB      = int64(1) << 30
	installGoldenRunID    = "0123456789abcdef0123456789abcdef"
)

// installGoldenTime is the instant a --output json golden's clock is stopped at.
var installGoldenTime = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

// goldenHost is the machine the preflight sees.
type goldenHost int

const (
	// hostRoomy has every tool and a Docker engine comfortably above the full-size floor.
	hostRoomy goldenHost = iota
	// hostSmall has every tool and a 2-CPU, 4GiB engine: refused for a full-size local
	// install, warned about for a compact one.
	hostSmall
	// hostUnknownCPU has ample memory, but Docker reports no CPU count.
	hostUnknownCPU
)

// goldenEngine says what runs when the command reaches bootstrap.Install.
type goldenEngine int

const (
	// engineReal runs the real engine. Only scenarios that stop deterministically before
	// any cluster is contacted use it: a refusal at the kind prompt, a missing kind
	// binary, a missing context, the dry run's first cluster read.
	engineReal goldenEngine = iota
	// engineFakeOK records the options it was handed and succeeds, printing nothing.
	engineFakeOK
	// engineFakeErr records the options and fails.
	engineFakeErr
	// engineFakeExitStatus records the options and fails with an error that names an
	// exit status of its own, as a wrapped *exec.ExitError of a killed child does.
	engineFakeExitStatus
	// engineFakePanic records the options and panics.
	engineFakePanic
	// engineFakeReadsStdin records the options, then fails if anything can be read
	// from os.Stdin, as a kubeconfig exec plugin handed it would.
	engineFakeReadsStdin
	// engineFakeEchoesCredential records the options, prints the kubeconfig's token
	// on the process's stderr and fails with it in its error.
	engineFakeEchoesCredential
)

// fakeExitStatus is an error naming its own exit status, as *exec.ExitError does.
type fakeExitStatus struct{}

func (fakeExitStatus) Error() string { return "kind create cluster: signal: interrupt" }
func (fakeExitStatus) ExitCode() int { return 255 }

type installScenario struct {
	name string
	args []string
	// stdin is what the child reads. Empty is an immediate EOF: no terminal to answer.
	stdin string
	host  goldenHost
	// kubeContext, when set, gives the child a kubeconfig holding exactly that context
	// (pointing at an unreachable server). Otherwise the kubeconfig is empty.
	kubeContext string
	// kubeconfig, when set, is the child's kubeconfig file verbatim, in place of the one
	// kubeContext builds.
	kubeconfig string
	engine     goldenEngine
	// unpinned leaves this build without a pinned image version, as `make build` does.
	unpinned bool
	// env is added to the child's built-up environment.
	env map[string]string
	// files are written into the child's working directory, a fresh one, before it runs,
	// so an argument can name one by a relative path that does not vary between runs.
	files map[string]string
}

// installScenarios are the characterised runs. The names are the golden file names.
var installScenarios = []installScenario{
	// Cobra's own surfaces.
	{name: "help-install", args: []string{"install", "--help"}},
	{name: "help-bootstrap", args: []string{"bootstrap", "--help"}},
	{name: "unknown-flag", args: []string{"install", "local", "--bogus"}},
	{name: "missing-provider", args: []string{"install"}},
	{name: "unknown-provider", args: []string{"install", "gcp", "--skip-preflight"}},

	// Refusals settled from argv, before any host or cluster is asked anything.
	{name: "refuse-no-tls-without-compact", args: []string{"install", "local", "--no-tls"}},
	{name: "refuse-dev-with-version", args: []string{"install", "local", "--dev", "--version", "v1.2.3"}},
	{name: "refuse-unpinned-build", args: []string{"install", "local"}, unpinned: true},
	{name: "refuse-max-connections", args: []string{"install", "local", "--max-connections", "50"}},
	{name: "refuse-ui-unknown", args: []string{"install", "local", "--ui", "fancy"}},

	// Preflight.
	{name: "preflight-unknown-cpu", args: []string{"install", "local"},
		host: hostUnknownCPU, engine: engineFakeOK},
	{name: "preflight-unknown-cpu-compact", args: []string{"install", "local", "--compact"},
		host: hostUnknownCPU, engine: engineFakeOK},
	// The standalone doctor shares the child harness and pins its own output.
	{name: "dcctl-preflight-unknown-cpu", args: []string{"preflight", "local"}, host: hostUnknownCPU},
	{name: "preflight-fail-undersized-docker", args: []string{"install", "local"}, host: hostSmall},
	{name: "preflight-warn-compact-small-docker", args: []string{"install", "local", "--compact"},
		host: hostSmall, engine: engineFakeOK},
	{name: "preflight-pass-remote-context", args: []string{"install", "local", "--kube-context", "prod"},
		kubeContext: "prod", engine: engineFakeOK},

	// The kind prompt, through the real engine. PATH holds no kind, so an ACCEPTED prompt
	// ends at "kind not found on PATH" and a declined one at the refusal: the two
	// outcomes are what tell the answers apart.
	{name: "kind-prompt-piped-y", args: []string{"install", "local", "--skip-preflight"}, stdin: "y\n"},
	{name: "kind-prompt-piped-yes-uppercase", args: []string{"install", "local", "--skip-preflight"}, stdin: "YES\n"},
	{name: "kind-prompt-piped-n", args: []string{"install", "local", "--skip-preflight"}, stdin: "n\n"},
	{name: "kind-prompt-no-stdin", args: []string{"install", "local", "--skip-preflight"}},
	{name: "kind-prompt-assume-yes", args: []string{"install", "local", "--skip-preflight", "--yes"}},
	{name: "kube-context-not-found", args: []string{"install", "local", "--kube-context", "prod", "--skip-preflight"}},
	{name: "dry-run", args: []string{"install", "local", "--skip-preflight", "--dry-run"}},

	// What the command hands the engine, and how a success and an engine failure exit.
	{name: "plan-compact-ha", args: []string{"install", "local", "--compact", "--ha", "--skip-preflight"},
		engine: engineFakeOK},
	{name: "plan-compact-explicit-false", args: []string{"install", "local", "--compact", "--no-tls=false",
		"--no-monitoring=false", "--skip-preflight"}, engine: engineFakeOK},
	{name: "plan-dev", args: []string{"install", "local", "--dev", "--skip-preflight"}, engine: engineFakeOK},
	{name: "plan-restore-remote", args: []string{"install", "local", "--kube-context", "prod", "--skip-preflight",
		"--restore-rdb-from", "dc-rdb", "--restore-rdb-at", "2026-01-02T03:04:05Z",
		"--database-node-selector", "pool=db", "--database-toleration", "dedicated=db:NoSchedule",
		"--max-connections", "900", "--relational-store-max-wal-mib", "2048"},
		kubeContext: "prod", engine: engineFakeOK},
	{name: "engine-error", args: []string{"install", "local", "--skip-preflight", "--yes"}, engine: engineFakeErr},
	// A URL credential whose password is an ordinary word ("install", also the start of
	// every step id). The terminal UI's review hides it; nothing it does may reach the
	// plain or JSON output, which must be what they were before the review existed.
	{name: "plan-url-credentials", args: []string{"install", "local", "--skip-preflight",
		"--registry", "https://dcuser:install@registry.example.test/dc",
		"--restore-rdb-from", "s3://dcuser:install@bucket/rdb?token=install#install"}, engine: engineFakeOK},
	{name: "json-plan-url-credentials", args: []string{"install", "local", "--skip-preflight", "-o", "json",
		"--registry", "https://dcuser:install@registry.example.test/dc",
		"--restore-rdb-from", "s3://dcuser:install@bucket/rdb?token=install#install"}, engine: engineFakeOK},

	// --output json: the same runs as the event stream, with the run id and the clock
	// fixed. Stdout is NDJSON and nothing else; the reason a run failed is on stderr,
	// as it always was. install_output_test.go decodes every line of these strictly.
	{name: "json-dry-run", args: []string{"install", "local", "--skip-preflight", "--dry-run", "-o", "json"}},
	{name: "json-preflight-fail-undersized-docker", args: []string{"install", "local", "--output", "json"},
		host: hostSmall},
	{name: "json-preflight-pass-remote-context", args: []string{"install", "local", "--kube-context", "prod",
		"-o", "json"}, kubeContext: "prod", engine: engineFakeOK},
	// A "y" on stdin that the stream must NOT read: the run is refused at the prompt.
	{name: "json-kind-refused", args: []string{"install", "local", "--skip-preflight", "-o", "json"},
		stdin: "y\n"},
	{name: "json-kind-assume-yes", args: []string{"install", "local", "--skip-preflight", "--yes", "-o", "json"}},
	{name: "json-engine-error", args: []string{"install", "local", "--skip-preflight", "--yes", "-o", "json"},
		engine: engineFakeErr},
	{name: "json-refuse-no-tls-without-compact", args: []string{"install", "local", "--no-tls", "-o", "json"}},
	// The notices the command prints before the run (compact and ha footprints) go to
	// stderr: stdout is the stream's alone.
	{name: "json-plan-compact-ha", args: []string{"install", "local", "--compact", "--ha", "--skip-preflight",
		"--output=json"}, engine: engineFakeOK},

	// A failure that names an exit status of its own (a killed kind child) still exits 1,
	// in both formats: the stream's verdict, and what text output has always exited.
	{name: "engine-exit-status", args: []string{"install", "local", "--skip-preflight", "--yes"},
		engine: engineFakeExitStatus},
	{name: "json-engine-exit-status", args: []string{"install", "local", "--skip-preflight", "--yes", "-o", "json"},
		engine: engineFakeExitStatus},
	// --help with --output json is help, on stderr: stdout carries events or nothing.
	{name: "json-help", args: []string{"install", "local", "--output", "json", "--help"}},
	// A "y" on stdin that nothing in the run can read: the engine looks, and finds EOF.
	{name: "json-stdin-unreadable", args: []string{"install", "local", "--skip-preflight", "--yes", "-o", "json"},
		stdin: "y\n", engine: engineFakeReadsStdin},
}

// installCheckScenarios run in a child like installScenarios but have no golden: what
// they print holds a path in a temporary directory, or a goroutine stack. The tests
// that run them hold them to properties instead.
var installCheckScenarios = []installScenario{
	{name: "backup-endpoint-deprecated", args: []string{"install", "local", "--skip-preflight", "--backup-credentials-file", "backup.json"},
		engine: engineFakeOK, files: map[string]string{"backup.json": deprecatedBackupEndpointFile}},
	{name: "json-backup-endpoint-deprecated", args: []string{"install", "local", "--skip-preflight", "--backup-credentials-file", "backup.json", "-o", "json"},
		engine: engineFakeOK, files: map[string]string{"backup.json": deprecatedBackupEndpointFile}},
	{name: "backup-endpoint-clean", args: []string{"install", "local", "--skip-preflight", "--backup-credentials-file", "backup.json"},
		engine: engineFakeOK, files: map[string]string{"backup.json": cleanBackupEndpointFile}},
	{name: "json-backup-endpoint-clean", args: []string{"install", "local", "--skip-preflight", "--backup-credentials-file", "backup.json", "-o", "json"},
		engine: engineFakeOK, files: map[string]string{"backup.json": cleanBackupEndpointFile}},
	// A kubeconfig client-go refuses, with credentials client-go's reason would quote.
	{name: "kubeconfig-duplicate-users", args: []string{"install", "local", "--kube-context", "prod"},
		kubeconfig: canaryKubeconfig},
	{name: "kubeconfig-duplicate-users-engine", args: []string{"install", "local", "--kube-context", "prod",
		"--skip-preflight"}, kubeconfig: canaryKubeconfig},
	{name: "json-kubeconfig-duplicate-users", args: []string{"install", "local", "--kube-context", "prod",
		"-o", "json"}, kubeconfig: canaryKubeconfig},
	{name: "json-kubeconfig-duplicate-users-engine", args: []string{"install", "local", "--kube-context", "prod",
		"--skip-preflight", "-o", "json"}, kubeconfig: canaryKubeconfig},
	// An engine panic: the stream still ends with its summary, and the exit agrees.
	{name: "json-engine-panic", args: []string{"install", "local", "--skip-preflight", "--yes", "-o", "json"},
		engine: engineFakePanic},
	// A usable kubeconfig whose token reaches the process's stderr and the error.
	{name: "kubeconfig-credential-echoed", args: []string{"install", "local", "--kube-context", "prod",
		"--skip-preflight"}, kubeconfig: echoKubeconfig, engine: engineFakeEchoesCredential},
	// A found value (AWS_PROFILE=prod) beside a context named "production".
	{name: "plain-profile-prod", args: []string{"install", "local", "--kube-context", "production"},
		kubeconfig: profileKubeconfig, engine: engineFakeOK},
	{name: "plain-profile-none", args: []string{"install", "local", "--kube-context", "production"},
		kubeconfig: twoContextKubeconfig, engine: engineFakeOK},
	{name: "plain-profile-dev", args: []string{"install", "local", "--kube-context", "production"},
		kubeconfig: devProfileKubeconfig, engine: engineFakeOK},
	{name: "json-profile-dev", args: []string{"install", "local", "--kube-context", "production", "-o", "json"},
		kubeconfig: devProfileKubeconfig, engine: engineFakeOK},
	{name: "json-profile-prod", args: []string{"install", "local", "--kube-context", "production", "-o", "json"},
		kubeconfig: profileKubeconfig, engine: engineFakeOK},
	{name: "install-proxy-url", args: []string{"install", "local", "--kube-context", "prod", "--skip-preflight"},
		kubeconfig: proxyKubeconfig},
	{name: "json-install-proxy-url", args: []string{"install", "local", "--kube-context", "prod", "--skip-preflight",
		"-o", "json"}, kubeconfig: proxyKubeconfig},
	{name: "bootstrap-proxy-url-dry-run", args: []string{"bootstrap", "local", "acme", "--kube-context", "prod",
		"--dry-run"}, kubeconfig: proxyKubeconfig},
	{name: "bootstrap-proxy-url-no-escrow", args: []string{"bootstrap", "local", "acme", "--kube-context", "prod",
		"--skip-preflight", "--no-escrow"}, kubeconfig: proxyKubeconfig},
	{name: "json-kubeconfig-credential-echoed", args: []string{"install", "local", "--kube-context", "prod",
		"--skip-preflight", "-o", "json"}, kubeconfig: echoKubeconfig, engine: engineFakeEchoesCredential},
}

// installScenario returns the scenario named name, from either list.
func findInstallScenario(t *testing.T, name string) installScenario {
	t.Helper()
	for _, list := range [][]installScenario{installScenarios, installCheckScenarios, bootstrapScenarios} {
		for _, s := range list {
			if s.name == name {
				return s
			}
		}
	}
	t.Fatalf("no scenario %q", name)
	return installScenario{}
}

// TestInstallGoldens is the parent: it runs each scenario in a child and compares what
// the child said with the committed golden.
func TestInstallGoldens(t *testing.T) {
	seen := map[string]bool{}
	for _, sc := range installScenarios {
		if seen[sc.name] {
			t.Fatalf("duplicate scenario %q", sc.name)
		}
		seen[sc.name] = true
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			compareInstallGolden(t, filepath.Join(installGoldenDir, sc.name+".txt"), runInstallChild(t, sc))
		})
	}
}

// TestInstallGoldensHaveScenarios refuses a golden with no scenario behind it: a contract
// nothing checks.
func TestInstallGoldensHaveScenarios(t *testing.T) {
	seen := map[string]bool{}
	for _, sc := range installScenarios {
		seen[sc.name] = true
	}
	entries, err := os.ReadDir(installGoldenDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if n, ok := strings.CutSuffix(e.Name(), ".txt"); ok && !seen[n] && !strings.HasPrefix(n, "flags-") {
			t.Errorf("golden %s has no scenario", e.Name())
		}
	}
}

// runInstallChild re-executes this test binary as dcctl for one scenario and renders the
// result in the golden format.
func runInstallChild(t *testing.T, sc installScenario) []byte {
	t.Helper()
	r := runInstallChildRaw(t, sc)
	var out bytes.Buffer
	fmt.Fprintf(&out, "args: %s\n", strings.Join(sc.args, " "))
	fmt.Fprintf(&out, "stdin: %q\n", sc.stdin)
	fmt.Fprintf(&out, "exit: %d\n", r.exit)
	writeGoldenSection(&out, "stdout", r.stdout)
	writeGoldenSection(&out, "stderr", r.stderr)
	writeGoldenSection(&out, "engine", []byte(r.engine))
	return out.Bytes()
}

// childResult is what one child run said.
type childResult struct {
	exit           int
	stdout, stderr []byte
	engine         string
}

// runInstallChildRaw re-executes this test binary as dcctl for one scenario.
func runInstallChildRaw(t *testing.T, sc installScenario) childResult {
	t.Helper()
	tmp := t.TempDir()
	emptyPath := filepath.Join(tmp, "bin")
	home := filepath.Join(tmp, "home")
	temp := filepath.Join(tmp, "tmp")
	for _, d := range []string{emptyPath, home, temp} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	plan := filepath.Join(tmp, "plan.json")

	cmd := exec.Command(os.Args[0], "-test.run=^TestInstallGoldenChild$", "-test.count=1")
	// A built-up environment, not the developer's: nothing the host exports may reach the
	// output. PATH is an empty directory, so no real tool can run.
	env := []string{
		installGoldenChildEnv + "=" + sc.name,
		installGoldenPlanEnv + "=" + plan,
		"PATH=" + emptyPath,
		"HOME=" + home,
		"USERPROFILE=" + home,
		"APPDATA=" + filepath.Join(home, "AppData", "Roaming"),
		"LOCALAPPDATA=" + filepath.Join(home, "AppData", "Local"),
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"TMPDIR=" + temp,
		"TMP=" + temp,
		"TEMP=" + temp,
	}
	if runtime.GOOS == "windows" {
		env = append(env, "SystemRoot="+os.Getenv("SystemRoot"))
	}
	if d := os.Getenv("GOCOVERDIR"); d != "" {
		env = append(env, "GOCOVERDIR="+d)
	}
	if sc.kubeContext != "" || sc.kubeconfig != "" {
		content := sc.kubeconfig
		if content == "" {
			content = goldenKubeconfig(sc.kubeContext)
		}
		kc := filepath.Join(tmp, "kubeconfig")
		if err := os.WriteFile(kc, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		env = append(env, installGoldenKubeEnv+"="+kc)
	}
	envKeys := make([]string, 0, len(sc.env))
	for k := range sc.env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	for _, k := range envKeys {
		env = append(env, k+"="+sc.env[k])
	}
	if len(sc.files) > 0 {
		work := filepath.Join(tmp, "work")
		if err := os.MkdirAll(work, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, content := range sc.files {
			if err := os.WriteFile(filepath.Join(work, name), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		cmd.Dir = work
	}
	cmd.Env = env
	cmd.Stdin = strings.NewReader(sc.stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	exit := 0
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running the child: %v", err)
		}
		exit = ee.ExitCode()
	}

	engine := "not called\n"
	if b, err := os.ReadFile(plan); err == nil {
		engine = string(b)
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}

	return childResult{exit: exit, stdout: stdout.Bytes(), stderr: stderr.Bytes(), engine: engine}
}

// writeGoldenSection writes one stream with its exact length, so a missing or extra
// trailing newline is part of the contract rather than lost in the framing.
func writeGoldenSection(out *bytes.Buffer, name string, b []byte) {
	fmt.Fprintf(out, "-- %s (%d bytes) --\n", name, len(b))
	out.Write(b)
	out.WriteString("\n")
}

func goldenKubeconfig(context string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: golden
  cluster:
    server: https://127.0.0.1:1
contexts:
- name: %s
  context:
    cluster: golden
    user: golden
current-context: %s
users:
- name: golden
  user:
    token: not-a-real-token
`, context, context)
}

func compareInstallGolden(t *testing.T, path string, got []byte) {
	t.Helper()
	if *updateInstallGoldens {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it, and review what it captured)", err)
	}
	if bytes.Equal(got, want) {
		return
	}
	gl, wl := strings.SplitAfter(string(got), "\n"), strings.SplitAfter(string(want), "\n")
	for i := 0; i < len(gl) || i < len(wl); i++ {
		var g, w string
		if i < len(gl) {
			g = gl[i]
		}
		if i < len(wl) {
			w = wl[i]
		}
		if g != w {
			t.Fatalf("%s differs from the golden at line %d:\n got: %q\nwant: %q\n\nfull output:\n%s",
				path, i+1, g, w, got)
		}
	}
	t.Fatalf("%s differs from the golden", path)
}

// TestInstallGoldenChild is the child half. It does nothing unless the parent started it.
func TestInstallGoldenChild(t *testing.T) {
	name := os.Getenv(installGoldenChildEnv)
	if name == "" {
		// Not a skip: CI refuses unallowed skips, and outside a child there is nothing to
		// do — the parent runs this function in a process of its own.
		return
	}
	var sc *installScenario
	for _, list := range [][]installScenario{installScenarios, installCheckScenarios, bootstrapScenarios, installUIScenarios(),
		bootstrapUIScenarios()} {
		for i := range list {
			if list[i].name == name {
				sc = &list[i]
			}
		}
	}
	if sc == nil {
		fmt.Fprintf(os.Stderr, "no scenario %q\n", name)
		os.Exit(99)
	}

	// A release build, as an operator runs one: both stamps goreleaser sets.
	Version = installGoldenRelease
	if !sc.unpinned {
		bootstrap.DefaultImageVersion = installGoldenImageTag
	}
	if kc := os.Getenv(installGoldenKubeEnv); kc != "" {
		if err := os.Setenv("KUBECONFIG", kc); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(99)
		}
	}
	fakeGoldenHost(*sc)
	// --output json: a fixed run id and a clock that does not move, so a stream is
	// reproducible byte for byte. Text output never reads either.
	installStreamRunID = installGoldenRunID
	installStreamClock = testkit.NewManualClock(installGoldenTime)

	switch sc.engine {
	case engineFakeOK, engineFakeErr, engineFakeExitStatus, engineFakePanic, engineFakeReadsStdin,
		engineFakeEchoesCredential:
		installEngine = func(_ context.Context, p bootstrap.Provider, opts bootstrap.InstallOptions) error {
			recordGoldenPlan(p, opts)
			switch sc.engine {
			case engineFakeErr:
				return fmt.Errorf("the fake engine failed")
			case engineFakeExitStatus:
				return fmt.Errorf("creating kind cluster %q: %w", "devicechain", fakeExitStatus{})
			case engineFakePanic:
				panic("the fake engine panicked")
			case engineFakeEchoesCredential:
				// What a kubeconfig credential plugin debugging itself does: its
				// credential, on the process's stderr, and the same in the error.
				fmt.Fprintln(os.Stderr, "plugin: token "+kubeCanaryToken)
				return fmt.Errorf("apply failed\nError: provider says %s", kubeCanaryToken)
			case engineFakeReadsStdin:
				b := make([]byte, 16)
				if n, err := os.Stdin.Read(b); n > 0 || err == nil {
					return fmt.Errorf("the engine read %q from stdin", b[:n])
				}
			}
			return nil
		}
	}

	rootCmd.SetArgs(sc.args)
	Execute()
	os.Exit(0)
}

// fakeGoldenHost presents the preflight with the scenario's machine.
func fakeGoldenHost(sc installScenario) {
	lookPath = func(name string) (string, error) { return "/fake/" + name, nil }
	cpus, mem := "8", fmt.Sprint(16*installGoldenGiB)
	if sc.host == hostSmall {
		cpus, mem = "2", fmt.Sprint(4*installGoldenGiB)
	}
	if sc.host == hostUnknownCPU {
		cpus = ""
	}
	runCmd = func(name string, args ...string) string {
		switch strings.Join(append([]string{name}, args...), " ") {
		case "docker info --format {{.ServerVersion}}":
			return "29.0.0"
		case "docker context show":
			return "default"
		case "docker info --format {{.NCPU}}":
			return cpus
		case "docker info --format {{.MemTotal}}":
			return mem
		case "docker info --format {{.Driver}}":
			return "overlay2"
		case "docker info --format {{.DockerRootDir}}":
			return "/var/lib/docker"
		case "/fake/tofu version -json":
			return `{"terraform_version":"1.10.6"}`
		}
		if name == "kubectl" && len(args) > 0 && args[0] == "version" && sc.kubeContext != "" {
			return `{"serverVersion":{"major":"1","minor":"31","gitVersion":"v1.31.4"}}`
		}
		return ""
	}
	portInUse = func(int) bool { return false }
	// The platform's own system checks read /proc, statfs and the real disk; they are the
	// one part of the preflight whose output is the HOST's rather than dcctl's.
	systemCheck = func(*doctor, string, bool) {}
}

// recordGoldenPlan writes what the engine was handed where the parent will read it.
func recordGoldenPlan(p bootstrap.Provider, opts bootstrap.InstallOptions) {
	b, err := json.MarshalIndent(struct {
		Provider string
		Options  bootstrap.InstallOptions
	}{p.Name(), opts}, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "golden: recording the plan: %v\n", err)
		os.Exit(99)
	}
	if err := os.WriteFile(os.Getenv(installGoldenPlanEnv), append(b, '\n'), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "golden: recording the plan: %v\n", err)
		os.Exit(99)
	}
}

// TestInstallFlagSnapshots pins the flag sets of install and bootstrap: every name,
// shorthand, type, default, no-value default, repeatability, deprecation and usage line.
// The flags are a served interface; a change here must be additive.
func TestInstallFlagSnapshots(t *testing.T) {
	for _, c := range []*cobra.Command{installCmd, bootstrapCmd} {
		t.Run(c.Name(), func(t *testing.T) {
			compareInstallGolden(t, filepath.Join(installGoldenDir, "flags-"+c.Name()+".txt"), describeFlagSet(c))
		})
	}
}

func describeFlagSet(c *cobra.Command) []byte {
	// Cobra adds --help when a command first EXECUTES, so without this the snapshot would
	// depend on whether another test in the package ran the command first.
	c.InitDefaultHelpFlag()
	var flags []*pflag.Flag
	c.Flags().VisitAll(func(f *pflag.Flag) { flags = append(flags, f) })
	c.InheritedFlags().VisitAll(func(f *pflag.Flag) { flags = append(flags, f) })
	sort.SliceStable(flags, func(i, j int) bool { return flags[i].Name < flags[j].Name })
	var out bytes.Buffer
	fmt.Fprintf(&out, "command: %s\nflags: %d\n", c.CommandPath(), len(flags))
	for _, f := range flags {
		fmt.Fprintf(&out, "\n--%s\n", f.Name)
		if f.Shorthand != "" {
			fmt.Fprintf(&out, "  shorthand: -%s\n", f.Shorthand)
		}
		typ := f.Value.Type()
		fmt.Fprintf(&out, "  type: %s\n", typ)
		fmt.Fprintf(&out, "  default: %q\n", f.DefValue)
		if f.NoOptDefVal != "" {
			fmt.Fprintf(&out, "  no-value default: %q\n", f.NoOptDefVal)
		}
		if strings.HasSuffix(typ, "Slice") || strings.HasSuffix(typ, "Array") {
			out.WriteString("  repeatable: true\n")
		}
		if f.Hidden {
			out.WriteString("  hidden: true\n")
		}
		if f.Deprecated != "" {
			fmt.Fprintf(&out, "  deprecated: %q\n", f.Deprecated)
		}
		if len(f.Annotations) > 0 {
			keys := make([]string, 0, len(f.Annotations))
			for k := range f.Annotations {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Fprintf(&out, "  annotation %s: %q\n", k, f.Annotations[k])
			}
		}
		fmt.Fprintf(&out, "  usage: %s\n", f.Usage)
	}
	return out.Bytes()
}
