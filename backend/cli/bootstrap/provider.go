// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Options carries the user-supplied inputs that drive a bootstrap run.
type Options struct {
	Instance    string
	KubeContext string
	Profile     string
	DryRun      bool
	AssumeYes   bool
	// ImageRegistry/ImageVersion select the published image source (defaults
	// DefaultImageRegistry/DefaultImageVersion). BuildImages opts into building
	// from source into a local registry instead (developer path).
	ImageRegistry string
	ImageVersion  string
	BuildImages   bool
	// IngressHost is the host the instance is exposed on (default
	// DefaultIngressHost). Set it to "localhost" for a local cluster to reach the
	// console with no /etc/hosts edit. NoTLS serves plain HTTP instead of a
	// self-signed cert — combined with localhost, a zero-config http://localhost/.
	IngressHost string
	NoTLS       bool
}

// Provider abstracts the target environment (local cluster today; cloud later)
// so the pipeline can stay platform-agnostic.
type Provider interface {
	Name() string
	// EnsureCluster guarantees a usable cluster and returns the kube-context to target.
	EnsureCluster(ctx context.Context, opts Options) (kubeContext string, err error)
	// DestroyCluster deletes the cluster the instance lives in (the inverse of
	// EnsureCluster). For the local provider this deletes the kind cluster; a
	// cloud provider would tofu-destroy it.
	DestroyCluster(ctx context.Context, opts Options) error
}

// registry holds the known providers, populated by each provider's init().
var registry = map[string]Provider{}

// register adds a provider to the registry. Called from provider init() funcs.
func register(p Provider) {
	registry[p.Name()] = p
}

// Get resolves a provider by name, returning a clear error listing the known
// names when the requested provider is unknown.
func Get(name string) (Provider, error) {
	if p, ok := registry[name]; ok {
		return p, nil
	}
	return nil, fmt.Errorf("unknown provider %q; available providers: %s", name, strings.Join(Names(), ", "))
}

// Names returns the sorted list of registered provider names.
func Names() []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
