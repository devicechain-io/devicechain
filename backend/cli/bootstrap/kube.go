// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// kubeClients builds the dynamic, discovery and typed clients for the chosen
// context from a lazily-loaded REST config (never the dc-k8s global client,
// which dies when no cluster is reachable — see RestConfig).
func kubeClients(kubeContext string) (dynamic.Interface, *discovery.DiscoveryClient, *kubernetes.Clientset, error) {
	cfg, err := RestConfig(kubeContext)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("building kube config: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, nil, nil, err
	}
	disco, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, nil, nil, err
	}
	typed, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, nil, nil, err
	}
	return dyn, disco, typed, nil
}

// repoRoot locates the DeviceChain source tree root by walking up from the
// current working directory until it finds the go.work file. Only the developer
// --build path needs it: ko builds the service and operator images from the Go
// modules under that root. The infra (tofu) and chart (Helm) assets are embedded
// in the binary, so the published-image path needs no source tree.
func repoRoot() (string, error) {
	start, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for dir := start; ; {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not locate the DeviceChain source root (no go.work found above %s); "+
				"run from a source checkout for the --build image step", start)
		}
		dir = parent
	}
}
