// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"strings"

	"github.com/fatih/color"
)

// localPrefixes are name heuristics for contexts that point at a local cluster.
// We match these as prefix or substring against context names so we can
// auto-detect a target when the user doesn't pass --kube-context.
var localPrefixes = []string{"kind-", "minikube", "k3d-", "docker-desktop", "rancher-desktop"}

// localProvider targets a developer's local Kubernetes cluster.
type localProvider struct{}

func (localProvider) Name() string { return "local" }

// EnsureCluster resolves the kube-context to target for a local install. It
// never creates a cluster in this skeleton; it only selects an existing one.
func (localProvider) EnsureCluster(ctx context.Context, opts Options) (string, error) {
	fmt.Print(color.WhiteString("Detecting local Kubernetes context... "))

	names, current, err := KubeContexts()
	if err != nil {
		fmt.Println(color.RedString("failed."))
		return "", fmt.Errorf("loading kube contexts: %w", err)
	}

	// Explicit context: verify it exists, then use it.
	if opts.KubeContext != "" {
		if !containsString(names, opts.KubeContext) {
			fmt.Println(color.RedString("not found."))
			return "", fmt.Errorf("kube-context %q not found; available contexts: %s",
				opts.KubeContext, strings.Join(names, ", "))
		}
		fmt.Println(color.GreenString("using %s.", opts.KubeContext))
		return opts.KubeContext, nil
	}

	// Auto-detect: collect contexts whose names look local.
	local := make([]string, 0, len(names))
	for _, name := range names {
		if looksLocal(name) {
			local = append(local, name)
		}
	}

	switch len(local) {
	case 1:
		fmt.Println(color.GreenString("using %s.", local[0]))
		return local[0], nil
	case 0:
		fmt.Println(color.RedString("none found."))
		// TODO(ADR-032): offer to create a cluster (needs --yes)
		return "", fmt.Errorf(
			"no local Kubernetes context detected (current-context: %q); "+
				"create one with 'minikube start' or 'kind create cluster', then re-run", current)
	default:
		fmt.Println(color.RedString("ambiguous."))
		return "", fmt.Errorf(
			"multiple local contexts found (%s); select one with --kube-context",
			strings.Join(local, ", "))
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
