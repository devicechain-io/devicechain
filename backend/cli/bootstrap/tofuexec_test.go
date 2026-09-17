// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeTofu writes an executable that answers each subcommand terraform-exec sends with
// a well-formed document carrying a marker naming the subcommand, so what reached the
// terminal says which command put it there.
func fakeTofu(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
case "$1" in
  version) echo '{"terraform_version":"1.8.0","platform":"linux_amd64","provider_selections":{},"marker":"LEAK-version"}' ;;
  show)    echo '{"format_version":"1.0","marker":"LEAK-show"}' ;;
  output)  echo '{"secret":{"sensitive":true,"type":"string","value":"LEAK-output"}}' ;;
  init)    echo 'PROGRESS-init' ;;
  apply)   echo 'PROGRESS-apply' ;;
  destroy) echo 'PROGRESS-destroy' ;;
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
	out := captureOutput(t, func() {
		// Built INSIDE the capture: os.Stdout is read when a writer is set, so a runner
		// built before it would stream to the real terminal and this test would pass
		// over exactly the leak it exists to catch (measured).
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
}
