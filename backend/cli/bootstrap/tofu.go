// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"

	assets "github.com/devicechain-io/dc-deploy"
	"github.com/hashicorp/terraform-exec/tfexec"
)

// applyInfra extracts the embedded OpenTofu config into a stable per-instance
// working directory and runs init+apply through terraform-exec. The config (.tf
// + modules) is refreshed from the binary on every run, but terraform.tfstate
// lives in that directory and persists across runs so the apply is idempotent.
func applyInfra(ctx context.Context, st *State) error {
	tofuBin, err := findTofu()
	if err != nil {
		return err
	}

	workdir, err := instanceStateDir(st.Instance, "infra")
	if err != nil {
		return err
	}
	if err := extractFS(assets.OpenTofu(), workdir); err != nil {
		return fmt.Errorf("extracting infrastructure config: %w", err)
	}

	tf, err := tfexec.NewTerraform(workdir, tofuBin)
	if err != nil {
		return err
	}
	// Stream tofu's own progress so a long apply is not a silent wait.
	tf.SetStdout(os.Stdout)
	tf.SetStderr(os.Stderr)

	if err := tf.Init(ctx); err != nil {
		return fmt.Errorf("tofu init: %w", err)
	}

	opts := []tfexec.ApplyOption{tfexec.Var("kubeconfig_context=" + st.KubeContext)}
	// On a kind/minikube node, ingress-nginx must bind the node's 80/443 via
	// hostPort; a LoadBalancer stays <pending> and times out the apply.
	if looksLocal(st.KubeContext) {
		opts = append(opts, tfexec.Var("ingress_use_host_port=true"))
	}
	if err := tf.Apply(ctx, opts...); err != nil {
		return fmt.Errorf("tofu apply: %w", err)
	}
	return nil
}

// findTofu locates the OpenTofu (preferred) or Terraform CLI on PATH. Acquiring
// the binary automatically when absent is a follow-up; the preflight checks
// guide the user to install it for now.
func findTofu() (string, error) {
	for _, bin := range []string{"tofu", "terraform"} {
		if p, err := exec.LookPath(bin); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("neither 'tofu' nor 'terraform' found on PATH; install OpenTofu (https://opentofu.org) and re-run")
}

// instanceStateDir returns a stable, per-instance directory under the user's
// home for persistent bootstrap state (e.g. ~/.devicechain/<instance>/<sub>),
// creating it if necessary.
func instanceStateDir(instance, sub string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".devicechain", instance, sub)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// extractFS writes every file in an embedded fs.FS into dir, recreating the
// directory structure. Existing files are overwritten; files already in dir but
// not in the FS (e.g. terraform.tfstate) are left untouched.
func extractFS(src fs.FS, dir string) error {
	return fs.WalkDir(src, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dir, path)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := fs.ReadFile(src, path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
}
