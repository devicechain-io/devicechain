// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/devicechain-io/dcctl/internal/secretenv"
	"github.com/hashicorp/terraform-exec/tfexec"
)

// applyOption keeps the two kinds of apply input dcctl uses readable by both
// runners. terraform-exec's options hide their values; the plain runner still
// receives those same options, in the same order.
type applyOption struct {
	variable string
	plan     string
}

func applyVar(v string) applyOption  { return applyOption{variable: v} }
func applyPlan(p string) applyOption { return applyOption{plan: p} }

func plainApplyOptions(opts []applyOption) []tfexec.ApplyOption {
	out := make([]tfexec.ApplyOption, 0, len(opts))
	for _, o := range opts {
		if o.plan != "" {
			out = append(out, tfexec.DirOrPlan(o.plan))
		} else {
			out = append(out, tfexec.Var(o.variable))
		}
	}
	return out
}

// observedApplyArgs mirrors the defaults of terraform-exec v0.26.0 for the
// variables and saved plans dcctl applies. It passes arguments directly, never
// through a shell; a saved plan remains the last argument.
func observedApplyArgs(opts []applyOption) []string {
	args := []string{"apply", "-no-color", "-auto-approve", "-input=false",
		"-lock=true", "-parallelism=10", "-refresh=true"}
	var plan string
	for _, o := range opts {
		if o.plan != "" {
			plan = o.plan
		} else {
			args = append(args, "-var", o.variable)
		}
	}
	args = append(args, "-json")
	if plan != "" {
		args = append(args, plan)
	}
	return args
}

// observedApply bypasses terraform-exec's ReadBytes drain: by the time its writer
// is called, a whole unterminated line has already been allocated. io.Copy feeds
// bounded chunks directly to the switch and hence the observer's streaming cap.
// This is used ONLY for ObserveTools; plain applies still use terraform-exec.
func (t *tofuExec) observedApply(ctx context.Context, opts []applyOption) error {
	cmd := exec.CommandContext(ctx, t.ExecPath(), observedApplyArgs(opts)...)
	cmd.Dir = t.WorkingDir()
	cmd.Env = observedApplyEnv()
	if runtime.GOOS != "windows" {
		cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
		cmd.WaitDelay = t.waitDelay
	}
	observedProcessAttrs(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	defer stdout.Close()
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	defer stderr.Close()
	if err := cmd.Start(); err != nil {
		return err
	}
	var stdoutErr, stderrErr error
	// Error capture is bounded too; stderr is still streamed in full.
	capture := &boundedApplyError{}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, stdoutErr = io.Copy(t.stdout, stdout)
	}()
	go func() {
		defer wg.Done()
		_, stderrErr = io.Copy(io.MultiWriter(t.stderr, capture), stderr)
	}()
	// As in terraform-exec, drain before Wait: a provider holding a pipe open is
	// still governed by runUntilAbandoned, with the switch detached on return.
	wg.Wait()
	err = cmd.Wait()
	if ctx.Err() != nil {
		return fmt.Errorf("%w: %v", ctx.Err(), err)
	}
	if err != nil {
		return fmt.Errorf("%w\n%s", err, capture.text)
	}
	if stdoutErr != nil {
		return stdoutErr
	}
	return stderrErr
}

// Inherit provider inputs and keep terraform-exec's automation and logging
// defaults. dcctl never configures a custom environment or log path on its runners.
func observedApplyEnv() []string {
	env := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		env[key] = value
	}
	env["CHECKPOINT_DISABLE"] = os.Getenv("CHECKPOINT_DISABLE")
	ua := strings.TrimSpace(os.Getenv("TF_APPEND_USER_AGENT"))
	if ua == "" || ua == terraformExecUserAgent {
		ua = terraformExecUserAgent
	} else {
		ua += " " + terraformExecUserAgent
	}
	env["TF_APPEND_USER_AGENT"] = ua
	for _, key := range []string{"TF_LOG", "TF_LOG_CORE", "TF_LOG_PATH", "TF_LOG_PROVIDER"} {
		env[key] = ""
	}
	env["TF_IN_AUTOMATION"] = "1"
	delete(env, "TF_WORKSPACE")
	result := make([]string, 0, len(env))
	for key, value := range env {
		result = append(result, key+"="+value)
	}
	// tofu writes all of this to its log under TF_LOG=trace, and hands it to every provider.
	return secretenv.Scrub(result)
}

var terraformExecUserAgent = func() string {
	// terraform-exec's version accessor is internal. Normal binaries expose the
	// dependency version through build metadata; test binaries may omit it. Keep
	// that fallback in sync with go.mod, guarded by the execution parity test.
	version := "0.26.0"
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range info.Deps {
			if dep.Path == "github.com/hashicorp/terraform-exec" {
				if dep.Replace != nil && dep.Replace.Version != "" {
					dep = dep.Replace
				}
				if dep.Version != "" {
					version = strings.TrimPrefix(dep.Version, "v")
				}
				break
			}
		}
	}
	return "HashiCorp-terraform-exec/" + version
}()

type boundedApplyError struct{ text []byte }

func (w *boundedApplyError) Write(p []byte) (int, error) {
	n := len(p)
	if left := (64 << 10) - len(w.text); left > 0 {
		w.text = append(w.text, p[:min(left, n)]...)
	}
	return n, nil
}
