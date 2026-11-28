// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installplain"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
)

func TestKindInheritsOnlyTheDefaultConsolesFiles(t *testing.T) {
	dir := t.TempDir()
	name := "kind"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(dir, name), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	original := runKindCommand
	t.Cleanup(func() { runKindCommand = original })
	for _, custom := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "custom"}[custom], func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			p := installplain.Presenter{Stdout: os.Stdout, Stderr: os.Stderr}
			if custom {
				p.Stdout, p.Stderr = &stdout, &stderr
			}
			called := false
			runKindCommand = func(cmd *exec.Cmd) error {
				called = true
				_, outFile := cmd.Stdout.(*os.File)
				_, errFile := cmd.Stderr.(*os.File)
				if outFile != !custom || errFile != !custom {
					t.Errorf("custom=%v: child stdout %T, stderr %T", custom, cmd.Stdout, cmd.Stderr)
				}
				if !custom && (cmd.Stdout != os.Stdout || cmd.Stderr != os.Stderr) {
					t.Error("kind did not inherit the process descriptors")
				}
				if custom && (cmd.Stdout == p.Stdout || cmd.Stderr == p.Stderr) {
					t.Error("custom child streams bypassed the flushing writers")
				}
				_, _ = cmd.Stdout.Write([]byte("kind stdout\n"))
				_, _ = cmd.Stderr.Write([]byte("kind stderr\n"))
				return nil
			}
			e := &InstallEngine{Provider: &fakeProvider{name: "local"}, SkipPreflight: true,
				Cluster: func(ctx context.Context, _ Provider, _ InstallOptions) error {
					return createKindCluster(ctx, "test", false)
				}}
			if err := p.Run(t.Context(), e, installflow.Request{}, nil); err != nil {
				t.Fatal(err)
			}
			if !called {
				t.Fatal("kind was not run")
			}
			if custom && (!strings.Contains(stdout.String(), "kind stdout\n") || stderr.String() != "kind stderr\n") {
				t.Errorf("custom streams: stdout %q, stderr %q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestOperatorStepPrintsOnlyToItsConsole(t *testing.T) {
	deadKubeconfig(t)
	original := applyOperatorManifests
	t.Cleanup(func() { applyOperatorManifests = original })
	for _, mode := range []string{"dry-run", "client-failure", "apply-failure", "success"} {
		t.Run(mode, func(t *testing.T) {
			dryRun := mode == "dry-run"
			applied := false
			applyOperatorManifests = func(_ context.Context, _ dynamic.Interface, _ discovery.DiscoveryInterface, manifests []byte) error {
				applied = true
				if len(manifests) == 0 {
					t.Error("no operator manifests")
				}
				if mode == "apply-failure" {
					return errors.New("apply refused")
				}
				return nil
			}
			var stdout, stderr bytes.Buffer
			ctx := withRun(t.Context(), newInstallRun(installflow.Runtime{
				Console: installflow.Console{Stdout: &stdout, Stderr: &stderr},
			}, dryRun))
			st := &State{DryRun: dryRun, KubeContext: "dead", ImageRegistry: "example.invalid/dc", ImageVersion: "v1.9.0"}
			if mode == "client-failure" {
				st.KubeContext = "missing-context"
			}
			leaked := captureOutput(t, func() {
				err := stepInstallCore(ctx, st)
				if (err == nil) != (dryRun || mode == "success") {
					t.Errorf("%s: error %v", mode, err)
				}
			})
			if applied != (mode == "success" || mode == "apply-failure") {
				t.Errorf("%s: applied=%v", mode, applied)
			}
			if leaked != "" || stderr.Len() != 0 {
				t.Errorf("output bypassed the console: stdout %q, stderr %q", leaked, stderr.String())
			}
			status := "failed."
			if dryRun {
				status = "[dry-run] would render the operator overlay and apply CRDs/RBAC/operator (example.invalid/dc/operator:v1.9.0) to dead"
			} else if mode == "success" {
				status = "done."
			}
			for _, want := range []string{"installing the operator (CRDs + RBAC + controller)... ", status} {
				if !strings.Contains(stdout.String(), want) {
					t.Errorf("console %q missing %q", stdout.String(), want)
				}
			}
		})
	}
}
