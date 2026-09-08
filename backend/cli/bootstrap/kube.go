// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"errors"
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

// BindingSource says where a resolved ClusterBinding came from, so callers can be
// honest about how much they actually know.
type BindingSource string

const (
	// BindingFromFlag: the operator named the context on the command line.
	BindingFromFlag BindingSource = "flag"
	// BindingFromRecord: read from the instance record written at bootstrap. The only
	// source that is actually KNOWN rather than assumed.
	BindingFromRecord BindingSource = "record"
	// BindingGuessed: no record, so the old kind-<instance> convention was assumed.
	// 🔴 Every caller that acts on one of these must SAY it is guessing.
	BindingGuessed BindingSource = "guess"
	// BindingUnreadable: a record EXISTS and could not be trusted — corrupt, or naming a
	// different instance. It carries no usable binding, and a caller that acts on it
	// anyway is acting on nothing. Distinct from BindingGuessed on purpose: "we never
	// knew" and "we knew and the answer is unusable" call for different behaviour, and
	// collapsing them is what let a corrupt file become a confident deletion.
	BindingUnreadable BindingSource = "unreadable"
)

// ResolveBinding resolves which cluster to act on for an instance that already
// exists, and says where that answer came from. It never creates anything.
//
// Shared by `destroy` (both paths) and `upgrade` — the commands that act on a live
// instance without bringing one up. Kept in one place because all three used to derive
// `kind-<instance>` independently, and that derivation is WRONG for any instance
// bootstrapped with --kube-context: `hack/ha-rig.sh` puts instance `harig` in cluster
// `devicechain-ha`, so all three were quietly targeting a cluster that does not exist.
// `destroy` reported success for it; `upgrade` would have failed to find the instance.
//
// Precedence, and each step is deliberate:
//  1. An explicit --kube-context. The operator is standing in front of us; nothing on
//     disk outranks that. It is Managed:false because a context named by hand is, by
//     definition, not the cluster dcctl named by convention.
//  2. The instance record. The one source that was written down when both halves were
//     in hand.
//  3. The guess. Kept so an instance bootstrapped before records existed is still
//     actionable — degrading LOUDLY beats refusing to touch a live instance.
func ResolveBinding(opts Options) (ClusterBinding, BindingSource) {
	if opts.KubeContext != "" {
		return ClusterBinding{
			Cluster:     clusterNameFromKindContext(opts.KubeContext),
			KubeContext: opts.KubeContext,
			Managed:     false,
		}, BindingFromFlag
	}
	rec, err := ReadInstanceRecord(opts.Instance)
	switch {
	case err == nil:
		return rec.Binding(), BindingFromRecord
	case errors.Is(err, ErrNoInstanceRecord):
		// Genuinely absent: the pre-record state. Fall through to the guess.
	default:
		// 🔴 A RECORD THAT WAS REJECTED IS NOT A RECORD THAT IS ABSENT, AND TREATING THEM
		// THE SAME IS HOW A CORRUPT FILE GETS SOMEBODY ELSE'S CLUSTER DELETED. This used
		// to be `if err == nil`, so a truncated JSON document or a record naming another
		// instance fell silently into the guess — which returns Managed:true and
		// `kind-<instance>`. For instance `harig` that means running `kind delete cluster
		// --name harig` against whatever unrelated cluster happens to carry that name,
		// while `devicechain-ha` survives. And the operator was told "no record of which
		// cluster this instance lives in", which is false: there is one, and it was
		// refused. ReadInstanceRecord's own refusal was dead code at its only call site.
		return ClusterBinding{}, BindingUnreadable
	}
	return GuessBinding(opts.Instance), BindingGuessed
}
