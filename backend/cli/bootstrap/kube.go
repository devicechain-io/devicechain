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
// current working directory until it finds the go.work file. The infra (tofu)
// and chart (Helm) deploy assets live under deploy/ at that root.
//
// The build-from-source bootstrap path already requires the source tree (ko
// builds the images from the Go modules), so resolving the deploy assets the
// same way is consistent. Packaging them into the binary for a fully
// self-contained published-image install is a follow-up (ADR-032).
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not locate the DeviceChain source root (no go.work found above %s); "+
				"run from a source checkout for the build/infra/chart steps", mustGetwd())
		}
		dir = parent
	}
}

func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}
