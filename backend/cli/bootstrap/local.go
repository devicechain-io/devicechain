// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	assets "github.com/devicechain-io/dc-deploy"
	"github.com/fatih/color"
)

// localPrefixes are name heuristics for contexts that point at a local cluster.
// We match these as prefix or substring against context names so we can
// auto-detect a target when the user doesn't pass --kube-context.
var localPrefixes = []string{"kind-", "minikube", "k3d-", "docker-desktop", "rancher-desktop"}

// localProvider targets a developer's local Kubernetes cluster.
type localProvider struct{}

func (localProvider) Name() string { return "local" }

// EnsureCluster resolves (and, if needed, creates) the kube-context to target
// for a local install. The local provider deploys to kind, so by default it
// targets a kind cluster named after the instance (context kind-<instance>):
// it is used if it already exists, and created otherwise. An explicit
// --kube-context overrides this and is never auto-created.
func (localProvider) EnsureCluster(ctx context.Context, opts Options) (string, error) {
	names, _, err := KubeContexts()
	if err != nil {
		return "", fmt.Errorf("loading kube contexts: %w", err)
	}

	// Explicit context: verify it exists, then use it (never auto-create — a
	// missing explicit context is almost always a typo).
	if opts.KubeContext != "" {
		if !containsString(names, opts.KubeContext) {
			return "", fmt.Errorf("kube-context %q not found; available contexts: %s",
				opts.KubeContext, strings.Join(names, ", "))
		}
		fmt.Println(color.WhiteString("Using kube-context %s.", color.GreenString(opts.KubeContext)))
		return opts.KubeContext, nil
	}

	// Default: a kind cluster named after the instance.
	clusterName := opts.Instance
	kubeContext := "kind-" + clusterName
	if containsString(names, kubeContext) {
		fmt.Println(color.WhiteString("Using existing kind cluster %s.", color.GreenString(kubeContext)))
		return kubeContext, nil
	}

	// Not present — create it.
	if opts.DryRun {
		fmt.Println(color.YellowString("[dry-run] would create kind cluster %q (context %s)", clusterName, kubeContext))
		return kubeContext, nil
	}
	if !opts.AssumeYes &&
		!confirm(fmt.Sprintf("No local cluster found. Create a kind cluster %q now?", clusterName)) {
		return "", fmt.Errorf(
			"no local cluster and creation declined; create one (e.g. `kind create cluster`) " +
				"or pass --kube-context, then re-run")
	}
	if err := createKindCluster(ctx, clusterName); err != nil {
		return "", err
	}
	return kubeContext, nil
}

// DestroyCluster deletes the kind cluster the instance lives in. It only deletes
// clusters it manages (kind-<instance>): an explicit --kube-context is refused,
// since it may point at a real cluster the user did not mean to delete. kind's
// delete is idempotent — a missing cluster is not an error.
func (localProvider) DestroyCluster(ctx context.Context, opts Options) error {
	if opts.KubeContext != "" {
		return fmt.Errorf(
			"refusing to delete the explicitly targeted context %q; the local provider only deletes kind clusters it manages — "+
				"omit --kube-context to delete kind-%s, or use --keep-cluster to uninstall just the instance",
			opts.KubeContext, opts.Instance)
	}
	if _, err := exec.LookPath("kind"); err != nil {
		return fmt.Errorf("kind not found on PATH; install it (https://kind.sigs.k8s.io) and re-run")
	}
	return run(ctx, "kind", "delete", "cluster", "--name", opts.Instance)
}

// createKindCluster creates a kind cluster from the embedded topology (the same
// config deploy/local/up.sh uses). kind streams its own progress.
func createKindCluster(ctx context.Context, name string) error {
	if _, err := exec.LookPath("kind"); err != nil {
		return fmt.Errorf("kind not found on PATH; install it (https://kind.sigs.k8s.io) and re-run")
	}

	cfg, err := os.CreateTemp("", "dcctl-kind-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(cfg.Name())
	// Strip the config's hard-coded cluster name so --name governs.
	if _, err := cfg.Write(stripClusterName(assets.KindClusterConfig())); err != nil {
		return err
	}
	if err := cfg.Close(); err != nil {
		return err
	}

	fmt.Println(color.WhiteString("Creating kind cluster %q:", name))
	cmd := exec.CommandContext(ctx, "kind", "create", "cluster",
		"--name", name, "--config", cfg.Name(), "--wait", "90s")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("creating kind cluster %q: %w", name, err)
	}
	return nil
}

// stripClusterName drops the top-level `name:` field from the kind config so the
// --name flag is the single source of the cluster name.
func stripClusterName(cfg []byte) []byte {
	lines := strings.Split(string(cfg), "\n")
	kept := lines[:0]
	for _, ln := range lines {
		if strings.HasPrefix(ln, "name:") {
			continue
		}
		kept = append(kept, ln)
	}
	return []byte(strings.Join(kept, "\n"))
}

// confirm asks the user a yes/no question on stdin, defaulting to no.
func confirm(prompt string) bool {
	fmt.Print(color.WhiteString("%s [y/N]: ", prompt))
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false
	}
	switch strings.TrimSpace(strings.ToLower(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

// looksLocal reports whether a context name matches a local-cluster heuristic.
func looksLocal(name string) bool {
	for _, p := range localPrefixes {
		if strings.HasPrefix(name, p) || strings.Contains(name, p) {
			return true
		}
	}
	return false
}

// containsString reports whether s is in the slice.
func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

// Register the local provider at package load time.
func init() {
	register(localProvider{})
}
