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
}

// Provider abstracts the target environment (local cluster today; cloud later)
// so the pipeline can stay platform-agnostic.
type Provider interface {
	Name() string
	// EnsureCluster guarantees a usable cluster and returns the kube-context to target.
	EnsureCluster(ctx context.Context, opts Options) (kubeContext string, err error)
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
