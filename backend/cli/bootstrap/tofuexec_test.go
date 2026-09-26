// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"go/ast"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeTofu writes an executable that answers each subcommand terraform-exec sends with
// a well-formed document carrying a marker naming the subcommand, so what reached the
// terminal says which command put it there. Every subcommand also writes DIAG-<name> to
// stderr, which must reach the terminal for reads and progress commands alike.
//
// `destroy` exits 1 when a file named fail-destroy sits beside the binary, so a test can
// make a progress command fail without a second fake.
//
// `init` writes its argv, one argument per line, to init-argv beside the binary, so a
// test can read the flags dcctl actually handed the CLI.
func fakeTofu(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
echo "DIAG-$1" >&2
case "$1" in
  version) echo '{"terraform_version":"1.8.0","platform":"linux_amd64","provider_selections":{},"marker":"LEAK-version"}' ;;
  show)    echo '{"format_version":"1.0","marker":"LEAK-show"}' ;;
  output)  echo '{"secret":{"sensitive":true,"type":"string","value":"LEAK-output"}}' ;;
  init)    printf '%s\n' "$@" > "$(dirname "$0")/init-argv"; echo 'PROGRESS-init' ;;
  apply)   echo 'PROGRESS-apply' ;;
  destroy) echo 'PROGRESS-destroy'; if [ -f "$(dirname "$0")/fail-destroy" ]; then exit 1; fi ;;
  state)   echo "PROGRESS-state-$2" ;;
  *)       echo "unexpected subcommand $1" >&2; exit 1 ;;
esac
`
	bin := filepath.Join(dir, "tofu")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// TestTofuExecStreamsProgressNotReads pins that the state and the outputs never reach
// the terminal while the progress of every command that changes something does.
//
// Init runs FIRST on a fresh runner on purpose: that is where terraform-exec runs its
// own `version -json` for the first time, inside a command whose stdout is streamed.
func TestTofuExecStreamsProgressNotReads(t *testing.T) {
	root, bin := t.TempDir(), fakeTofu(t)
	ctx := context.Background()
	var failures []string
	out, errOut := captureStdoutAndStderr(t, func() {
		// Built INSIDE the capture: os.Stdout and os.Stderr are read when a writer is set,
		// so a runner built before it would stream to the real terminal and this test
		// would pass over exactly the leak it exists to catch (measured).
		tf, err := newTofuExec(root, bin)
		if err != nil {
			failures = append(failures, "construct: "+err.Error())
			return
		}
		if err := tf.Init(ctx); err != nil {
			failures = append(failures, "init: "+err.Error())
		}
		if _, err := tf.Show(ctx); err != nil {
			failures = append(failures, "show: "+err.Error())
		}
		outputs, err := tf.Output(ctx)
		if err != nil {
			failures = append(failures, "output: "+err.Error())
		} else if _, ok := outputs["secret"]; !ok {
			failures = append(failures, "output: the fake's output was not parsed, so this proves nothing about it")
		}
		if _, _, err := tf.Version(ctx, true); err != nil {
			failures = append(failures, "version: "+err.Error())
		}
		if err := applyWithCNPGAdmissionRetry(ctx, tf, nil, "tofu apply", nil); err != nil {
			failures = append(failures, "apply: "+err.Error())
		}
		if err := tf.StateRm(ctx, tsdbReleaseAddress); err != nil {
			failures = append(failures, "state rm: "+err.Error())
		}
		if err := tf.Destroy(ctx); err != nil {
			failures = append(failures, "destroy: "+err.Error())
		}
		// And a read AFTER the progress commands, so a stream left on by one of them fails.
		if _, err := tf.Show(ctx); err != nil {
			failures = append(failures, "show after destroy: "+err.Error())
		}
	})
	for _, f := range failures {
		t.Error(f)
	}
	for _, want := range []string{"PROGRESS-init", "PROGRESS-apply", "PROGRESS-state-rm", "PROGRESS-destroy"} {
		if !strings.Contains(out, want) {
			t.Errorf("progress %q did not reach stdout; got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "LEAK-") {
		t.Errorf("a read's result reached stdout:\n%s", out)
	}
	// 🔴 STDERR STREAMS FOR EVERY COMMAND, reads included: it carries tofu's diagnostics,
	// and a runner that discards it turns every failure into an exit status with no reason.
	for _, want := range []string{"DIAG-init", "DIAG-show", "DIAG-output", "DIAG-version", "DIAG-apply", "DIAG-state", "DIAG-destroy"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("diagnostics %q did not reach stderr; got:\n%s", want, errOut)
		}
	}
}

// TestTofuInitMovesTheLockToThePins pins that every init dcctl runs is -upgrade, so a
// machine whose lock file predates a provider pin is moved onto the pin instead of
// failing the version constraint on every command against that cluster.
func TestTofuInitMovesTheLockToThePins(t *testing.T) {
	root, bin := t.TempDir(), fakeTofu(t)
	var initErr error
	captureStdoutAndStderr(t, func() {
		tf, err := newTofuExec(root, bin)
		if err != nil {
			initErr = err
			return
		}
		initErr = tf.Init(context.Background())
	})
	if initErr != nil {
		t.Fatalf("init: %v", initErr)
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(bin), "init-argv"))
	if err != nil {
		// Without a recorded init, "no -upgrade=false" would read as a clean pass.
		t.Fatalf("the fake never recorded an init, so this test would prove nothing: %v", err)
	}
	args := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if args[0] != "init" {
		t.Fatalf("recorded argv is not an init: %q", args)
	}
	// The VALUE of the flag the CLI acts on -- the last one, when a flag repeats --
	// not merely that some "-upgrade" appears. terraform-exec always emits one, and
	// its default is false.
	var upgrade []string
	for _, a := range args {
		if a == "-upgrade" || strings.HasPrefix(a, "-upgrade=") {
			upgrade = append(upgrade, a)
		}
	}
	if len(upgrade) == 0 || upgrade[len(upgrade)-1] != "-upgrade=true" {
		t.Errorf("init ran with upgrade flags %q, want the last to be -upgrade=true; an existing "+
			"cluster's lock would stay on the provider its first install resolved, and every "+
			"command against it would fail the pinned version constraint", upgrade)
	}
}

// TestAFailedProgressCommandStillQuietsStdout pins that the stream is switched off
// whatever the progress command returned. A failed destroy is exactly when an operator
// reaches for a read next, and a stream left on by the failure would print the state.
func TestAFailedProgressCommandStillQuietsStdout(t *testing.T) {
	root, bin := t.TempDir(), fakeTofu(t)
	if err := os.WriteFile(filepath.Join(filepath.Dir(bin), "fail-destroy"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var failures []string
	out, _ := captureStdoutAndStderr(t, func() {
		tf, err := newTofuExec(root, bin)
		if err != nil {
			failures = append(failures, "construct: "+err.Error())
			return
		}
		if err := tf.Destroy(ctx); err == nil {
			failures = append(failures, "destroy: the fake was told to fail and did not, so this proves nothing")
		}
		if _, err := tf.Show(ctx); err != nil {
			failures = append(failures, "show after failed destroy: "+err.Error())
		}
	})
	for _, f := range failures {
		t.Error(f)
	}
	if !strings.Contains(out, "PROGRESS-destroy") {
		t.Errorf("the failing destroy's progress did not reach stdout, so it never streamed; got:\n%s", out)
	}
	if strings.Contains(out, "LEAK-") {
		t.Errorf("a read after a failed progress command reached stdout:\n%s", out)
	}
}

// TestOnlyTofuExecBuildsARawRunner pins that newTofuExec is the one constructor. A runner
// from tfexec.NewTerraform has no per-command stdout switch: the moment anyone points its
// stdout at the terminal, `show -json` streams the whole state and `output -json` every
// output, sensitive values included, onto the operator's screen and into CI logs.
func TestOnlyTofuExecBuildsARawRunner(t *testing.T) {
	fset, files := packageFiles(t)
	found := 0
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "NewTerraform" {
				return true
			}
			pos := fset.Position(sel.Pos())
			if pos.Filename != "tofuexec.go" {
				t.Errorf("%s calls NewTerraform directly. Build runners with newTofuExec: a raw "+
					"terraform-exec runner has no quiet-stdout default, so a read on it streams "+
					"the state and the outputs, secrets included, to the terminal", pos)
				return true
			}
			found++
			return true
		})
	}
	// The positive half: a gate over a renamed or removed constructor finds nothing and passes.
	if found == 0 {
		t.Fatal("tofuexec.go no longer calls NewTerraform, so this gate matched nothing and checked nothing")
	}
}

// captureStdoutAndStderr runs fn with os.Stdout and os.Stderr redirected, returning both.
func captureStdoutAndStderr(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	origErr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		var sb strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			sb.Write(buf[:n])
			if err != nil {
				break
			}
		}
		done <- sb.String()
	}()
	stdout = captureOutput(t, fn)
	_ = w.Close()
	os.Stderr = origErr
	return stdout, <-done
}
