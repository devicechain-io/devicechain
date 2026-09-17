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
	// Cluster names the local cluster: the kind cluster of that name. Empty means
	// DefaultClusterName. Ignored when KubeContext is set.
	Cluster string
	// CreateCluster lets EnsureCluster create a missing local cluster. Only `dcctl
	// install` sets it: a cluster is prepared once, and instances are built on one that
	// has been.
	CreateCluster bool
	Profile       string
	DryRun        bool
	AssumeYes     bool
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
	// AllowLegacyDbRemoval passes the cutover-guard escape hatch through to
	// OpenTofu. See State for why it exists at all.
	AllowLegacyDbRemoval bool
	// EnableAreas is the raw set of extra functional areas requested via
	// --enable-area, deployed ADDITIVELY on top of the profile (e.g. lwm2m-ingest on
	// a default/compact bring-up). Resolved+validated by ResolveEnabledAreas into the
	// State's EnabledAreas before the pipeline runs.
	EnableAreas []string
}

// Provider abstracts the target environment (local cluster today; cloud later)
// so the pipeline can stay platform-agnostic.
type Provider interface {
	Name() string
	// EnsureCluster guarantees a usable cluster and returns the binding to record:
	// which cluster, the context to reach it by, and whether it is dcctl's own.
	//
	// 🔴 IT RETURNS THE CLUSTER NAME, NOT JUST THE CONTEXT, and that is the whole point of
	// the type. The caller used to receive a context, and every later step re-derived the
	// cluster from the INSTANCE name — an assumption that is false for any instance
	// bootstrapped with --kube-context, and whose failure mode was a destroy that reported
	// success while the cluster kept running. Only the provider knows this mapping;
	// returning it is what stops everyone else guessing at it.
	EnsureCluster(ctx context.Context, opts Options) (ClusterBinding, error)
	// ClusterExists reports whether the cluster the binding names is present right now.
	//
	// 🔴 IT EXISTS SO "ALREADY GONE" IS A THING DESTROY CAN SAY. kind's delete is
	// idempotent: deleting a cluster that is not there exits 0, which is exactly how
	// `destroy` once reported a successful teardown over a cluster it never touched. Destroy
	// asks this to tell an instance whose cluster was deleted by hand — nothing to uninstall,
	// only local state to clear — from one it must uninstall; the listing asks it because it
	// has no other way to tell a live instance from an orphaned state directory.
	ClusterExists(ctx context.Context, binding ClusterBinding) (bool, error)
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
