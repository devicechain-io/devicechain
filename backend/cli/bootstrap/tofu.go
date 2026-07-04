// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
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
	// Broker authentication (ADR-025): enable auth callout on NATS and pass the
	// minted public issuer + the bcrypt hash of the service password. The plaintext
	// password + seed go into the instance config in helmInstall; nats-server
	// bcrypt-compares the plaintext, so the broker and clients agree. (tfexec passes
	// vars as argv, no shell — the hash's `$` is literal.)
	if pub := st.Values["natsCalloutIssuerPublic"]; pub != "" {
		opts = append(opts,
			tfexec.Var("nats_enable_auth=true"),
			tfexec.Var("nats_callout_issuer_public="+pub),
			tfexec.Var("nats_service_password_bcrypt="+st.Values["natsServicePasswordBcrypt"]),
		)
	}
	if err := tf.Apply(ctx, opts...); err != nil {
		return fmt.Errorf("tofu apply: %w", err)
	}

	// Read the NATS TLS material back out (ADR-025): the broker terminates TLS and
	// emits its CA, which the Helm step threads into the instance config so
	// services dial over TLS. The broker flag and the client flag come from the
	// same outputs so they cannot drift apart.
	outputs, err := tf.Output(ctx)
	if err != nil {
		return fmt.Errorf("reading tofu outputs: %w", err)
	}
	// Decode errors are propagated, not swallowed: a broker that terminates TLS
	// paired with a client that (silently) fell back to plaintext is the one
	// failure the two-flags-one-source design exists to prevent, and NATS'
	// retry-forever masks it as a healthy-but-mute service. Fail the bootstrap
	// loudly instead.
	if meta, ok := outputs["nats_tls_enabled"]; ok {
		var enabled bool
		if err := json.Unmarshal(meta.Value, &enabled); err != nil {
			return fmt.Errorf("decoding nats_tls_enabled output: %w", err)
		}
		if enabled {
			st.Values["natsTlsEnabled"] = "true"
		}
	}
	if meta, ok := outputs["nats_ca"]; ok {
		var ca string
		if err := json.Unmarshal(meta.Value, &ca); err != nil {
			return fmt.Errorf("decoding nats_ca output: %w", err)
		}
		st.Values["natsCA"] = ca
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

// instanceRoot returns the per-instance state root (~/.devicechain/<instance>)
// without creating it — used by destroy to remove all persisted state for an
// instance (tofu tfstate and friends).
func instanceRoot(instance string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".devicechain", instance), nil
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
