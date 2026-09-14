// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/devicechain-io/dcctl/dcdir"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// clusterRecordFile is the cluster record's name inside its own directory, mirroring
// instanceRecordFile inside an instance's.
const clusterRecordFile = "cluster.json"

// identityNamespace is the namespace whose UID identifies a cluster.
//
// 🔴 kube-system AND NOT default, THOUGH BOTH WOULD WORK TODAY. Either namespace's UID
// is minted once when the cluster is created and never changes, so either distinguishes
// one cluster from the next. They differ in what an OPERATOR can do to them: `default`
// is an ordinary namespace somebody can delete and let the API server recreate, which
// would silently re-identify a cluster that had not changed at all. kube-system holds
// the control plane and deleting it is not a thing a cluster survives. The conventional
// Kubernetes cluster identifier is this one for the same reason.
const identityNamespace = "kube-system"

// ClusterUID reads the identity of the cluster behind a client.
//
// 🔴 A CLUSTER IS NOT ITS CONTEXT NAME, AND THE DIFFERENCE IS ROUTINE HERE RATHER THAN
// EXOTIC. ClusterBinding.KubeContext is DERIVED for a managed cluster — GuessBinding
// builds "kind-" + instance — so `kind delete cluster` followed by `kind create cluster`
// yields a byte-identical context name in front of a cluster that holds none of the
// resources the old local state describes. Measured on 2026-09-14: two rounds under one
// name gave kube-system UIDs 163e7f17-d87c-42fe-8bc0-e672e35f5ee7 and
// 446b60a1-b6f8-4cf0-9e14-ced15bc26170, each stable for as long as its cluster lived.
// Given how much this project uses kind, that is the ordinary case and not a corner.
//
// It reads a namespace, so it answers on a cluster with nothing installed on it — which
// is when the first caller needs it.
func ClusterUID(ctx context.Context, typed kubernetes.Interface) (string, error) {
	ns, err := typed.CoreV1().Namespaces().Get(ctx, identityNamespace, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("reading the %s namespace to identify the cluster: %w", identityNamespace, err)
	}
	uid := string(ns.UID)
	if uid == "" {
		// 🔴 A NAMESPACE WITH NO UID IS NOT A CLUSTER WITH NO IDENTITY. The API server
		// assigns one on creation and it is never empty, so this means the object came
		// from something that is not an API server — a fake with an under-specified
		// fixture is the realistic source, and letting it through would key state on the
		// empty string, which is the clusters directory itself rather than one cluster.
		return "", fmt.Errorf(
			"the %s namespace carries no UID, so this cluster cannot be identified; "+
				"an API server always assigns one", identityNamespace)
	}
	return uid, nil
}

// IdentifyCluster reads the identity of the cluster a context points at.
//
// The client is built here rather than taken as an argument so the command layer never
// has to; ClusterUID keeps the client parameter because that is the half worth testing
// against a fake, and a cluster nobody can connect to is not a case this package has an
// opinion about.
func IdentifyCluster(ctx context.Context, kubeContext string) (string, error) {
	_, _, typed, err := kubeClients(kubeContext)
	if err != nil {
		return "", fmt.Errorf("building a client to identify the cluster: %w", err)
	}
	return ClusterUID(ctx, typed)
}

// ClusterRecord is what dcctl knows locally about a cluster it has installed on.
//
// 🔑 IT EXISTS TO MAKE A UID DIRECTORY LEGIBLE, and that is a real job rather than a
// courtesy. The directory under clusters/ is named by an opaque UUID because a name
// cannot be trusted to identify a cluster; the cost is that `ls ~/.devicechain/clusters`
// tells an operator nothing. The name they know goes in here, where it can be wrong
// without anything being keyed on it.
type ClusterRecord struct {
	UID string `json:"uid"`
	// Cluster and KubeContext are the names a human uses for this cluster. 🔴 NOTHING
	// MAY KEY ON EITHER — they are recorded for reading, and a rebuilt kind cluster
	// carries the previous one's context name unchanged.
	Cluster      string    `json:"cluster,omitempty"`
	KubeContext  string    `json:"kubeContext"`
	FirstSeenAt  time.Time `json:"firstSeenAt"`
	DcctlVersion string    `json:"dcctlVersion,omitempty"`
}

// clusterStateDir returns this cluster's directory, creating it owner-only.
//
// It walks the modes back down every level for the same reason instanceStateDir does:
// MkdirAll leaves an EXISTING directory exactly as it found it, so a tree an older dcctl
// created at 0755 would keep those modes while every assertion about a fresh install
// passed. clusters/ sits above whatever prerequisite state lands here, so it is one of
// the levels that matters rather than a parent nobody looks at.
//
// 🔑 IT TAKES NO SUBDIRECTORY, THOUGH instanceStateDir DOES. The prerequisite root's
// state will want one and does not exist yet; a parameter added now would be a branch no
// caller takes and no test can reach, which is the same thing the inventory refuses an
// entry for. Add it with the root that needs it.
func clusterStateDir(uid string) (string, error) {
	root, err := dcdir.Root()
	if err != nil {
		return "", err
	}
	clusters, err := dcdir.Sibling(dcdir.Clusters)
	if err != nil {
		return "", err
	}
	dir, err := dcdir.Cluster(uid)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, stateDirMode); err != nil {
		return "", err
	}
	for _, p := range []string{root, clusters, dir} {
		if err := os.Chmod(p, stateDirMode); err != nil {
			return "", fmt.Errorf("restricting permissions on %s: %w", p, err)
		}
	}
	return dir, nil
}

// WriteClusterRecord persists what is known about a cluster, replacing any previous
// record.
//
// Replacing rather than merging, for the same reason WriteInstanceRecord does: the names
// in it are the mutable half. A cluster reachable under a new context name must have the
// record CORRECTED, and one that only ever accumulated would preserve exactly the
// staleness the UID key exists to avoid.
func WriteClusterRecord(rec ClusterRecord) error {
	dir, err := clusterStateDir(rec.UID)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return writeRecordFile(dir, clusterRecordFile, append(b, '\n'))
}
