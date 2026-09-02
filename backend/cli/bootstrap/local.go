// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bufio"
	"context"
	"errors"
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
func (localProvider) EnsureCluster(ctx context.Context, opts Options) (ClusterBinding, error) {
	names, _, err := KubeContexts()
	if err != nil {
		return ClusterBinding{}, fmt.Errorf("loading kube contexts: %w", err)
	}

	// Explicit context: verify it exists, then use it (never auto-create — a
	// missing explicit context is almost always a typo).
	//
	// 🔴 THIS IS THE ADOPTED CASE, and it is the one the old code could not represent.
	// The operator pointed dcctl at a cluster BY NAME; dcctl neither created it nor
	// named it, so it is not dcctl's to delete. Both validation rigs arrive here.
	if opts.KubeContext != "" {
		if !containsString(names, opts.KubeContext) {
			return ClusterBinding{}, fmt.Errorf("kube-context %q not found; available contexts: %s",
				opts.KubeContext, strings.Join(names, ", "))
		}
		fmt.Println(color.WhiteString("Using kube-context %s.", color.GreenString(opts.KubeContext)))
		return ClusterBinding{
			// Recovered only when the context follows kind's own convention. A context
			// named `prod-eu-west` says nothing about its cluster's name, and an empty
			// string is the honest answer — nothing downstream may delete what it
			// cannot name, and the listing prints the context instead.
			Cluster:     clusterNameFromKindContext(opts.KubeContext),
			KubeContext: opts.KubeContext,
			Managed:     false,
		}, nil
	}

	// Default: a kind cluster named after the instance. Managed either way — see the
	// note on ClusterBinding.Managed for why REUSING one still counts as ours.
	clusterName := opts.Instance
	kubeContext := kindContext(clusterName)
	binding := ClusterBinding{Cluster: clusterName, KubeContext: kubeContext, Managed: true}
	if containsString(names, kubeContext) {
		fmt.Println(color.WhiteString("Using existing kind cluster %s.", color.GreenString(kubeContext)))
		return binding, nil
	}

	// Not present — create it.
	if opts.DryRun {
		fmt.Println(color.YellowString("[dry-run] would create kind cluster %q (context %s)", clusterName, kubeContext))
		return binding, nil
	}
	if !opts.AssumeYes &&
		!confirm(fmt.Sprintf("No local cluster found. Create a kind cluster %q now?", clusterName)) {
		return ClusterBinding{}, fmt.Errorf(
			"no local cluster and creation declined; create one (e.g. `kind create cluster`) " +
				"or pass --kube-context, then re-run")
	}
	if err := createKindCluster(ctx, clusterName); err != nil {
		return ClusterBinding{}, err
	}
	return binding, nil
}

// kindContext is kind's context naming convention, in one place so the prefix is not
// spelled into half a dozen callers.
func kindContext(cluster string) string { return kindContextPrefix + cluster }

// clusterNameFromKindContext recovers a kind cluster name from its context, or "" when
// the context is not one of kind's. The empty return is load-bearing: it is what stops a
// destroy naming a cluster it only assumed the name of.
func clusterNameFromKindContext(kubeContext string) string {
	if !strings.HasPrefix(kubeContext, kindContextPrefix) {
		return ""
	}
	return strings.TrimPrefix(kubeContext, kindContextPrefix)
}

const kindContextPrefix = "kind-"

// DestroyCluster deletes the kind cluster the binding names.
//
// 🔴 THE NAME COMES FROM THE BINDING, NOT FROM THE INSTANCE. That single change is what
// this whole record exists for: the previous version ran `kind delete cluster --name
// <instance>`, and for any instance bootstrapped into a differently-named cluster that
// deleted nothing — silently, because kind's delete is IDEMPOTENT and a missing cluster
// exits 0. The caller reported success over a no-op. Now the caller reads the recorded
// cluster and passes it here, and a cluster that is genuinely already gone is reported as
// gone by the caller, which checked.
//
// The Managed re-check is defence in depth. destroy already refuses to reach an adopted
// cluster; this makes deleting somebody else's cluster take two independent mistakes.
func (localProvider) DestroyCluster(ctx context.Context, binding ClusterBinding, opts Options) error {
	if !binding.Managed {
		return fmt.Errorf(
			"refusing to delete cluster %q: it was not created or named by dcctl (instance %q was bootstrapped with an explicit --kube-context). "+
				"Use --keep-cluster to uninstall just the instance, or delete the cluster with the tool that made it",
			binding.describe(), opts.Instance)
	}
	if binding.Cluster == "" {
		return fmt.Errorf(
			"refusing to delete a cluster for instance %q: the recorded binding names no cluster (context %q). "+
				"Use --keep-cluster to uninstall just the instance",
			opts.Instance, binding.KubeContext)
	}
	if _, err := exec.LookPath("kind"); err != nil {
		return fmt.Errorf("kind not found on PATH; install it (https://kind.sigs.k8s.io) and re-run")
	}
	return run(ctx, "kind", "delete", "cluster", "--name", binding.Cluster)
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

// Confirm asks the user a yes/no question on stdin, defaulting to no. Exported for the
// cmd layer's bulk teardown, which asks once for a whole run rather than per instance.
func Confirm(prompt string) bool { return confirm(prompt) }

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

// ClusterExists reports whether the kind cluster the binding names is present.
//
// 🔴 A FAILURE TO ASK IS NOT AN ANSWER OF "NO", AND CONFLATING THEM REBUILDS THE ORIGINAL
// DEFECT EXACTLY. An earlier version of this function returned (false, nil) on any
// non-zero exit from kind, on the stated belief that kind exits non-zero when there are
// no clusters. That belief is FALSE — kind v0.32 logs "No kind clusters found." and exits
// 0 — so the only thing the swallow actually caught was kind failing to reach Docker: an
// unset socket, a DOCKER_HOST pointing at a remote builder, a `docker context use`. Every
// live cluster then reads as absent, and `dcctl destroy` deletes the instance's tfstate
// and prints "destroyed" over a cluster that is still running. That is the sentence this
// whole record exists to stop printing, reintroduced one level down.
//
// So a broken query is an ERROR. An empty list is exit 0 with no output, and is "no".
func (localProvider) ClusterExists(ctx context.Context, binding ClusterBinding) (bool, error) {
	if binding.Cluster == "" {
		// 🔴 THIS ANSWER IS WEAKER THAN THE ONE ABOVE, AND ONLY THE LISTING MAY USE IT.
		// An adopted context that follows no convention dcctl can read leaves us with no
		// cluster name to ask about, so the best available signal is whether kubeconfig
		// still carries the context — and a kubeconfig entry's absence is NOT a cluster's
		// absence. Someone pointing at a different KUBECONFIG would look like a deleted
		// cluster. destroyEverything therefore refuses to conclude "gone" from an unnamed
		// binding at all; this branch exists so `instances list` has something to print.
		names, _, err := KubeContexts()
		if err != nil {
			return false, fmt.Errorf("loading kube contexts: %w", err)
		}
		return containsString(names, binding.KubeContext), nil
	}
	if _, err := exec.LookPath("kind"); err != nil {
		return false, fmt.Errorf("kind not found on PATH; install it (https://kind.sigs.k8s.io) and re-run")
	}
	out, err := exec.CommandContext(ctx, "kind", "get", "clusters").Output()
	if err != nil {
		detail := ""
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			detail = ": " + strings.TrimSpace(string(ee.Stderr))
		}
		return false, fmt.Errorf(
			"could not ask kind which clusters exist%s (is Docker running, and is DOCKER_HOST/docker context pointing at it?): %w",
			detail, err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == binding.Cluster {
			return true, nil
		}
	}
	return false, nil
}
