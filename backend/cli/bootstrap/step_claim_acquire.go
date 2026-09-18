// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dcctl/operator"
)

// stepClaimCluster takes the cluster lock, and it runs BEFORE anything is applied.
//
// 🔴 THE POSITION IS THE POINT, AND THE FIRST VERSION HAD IT WRONG. The claim used
// to sit after "Install core components", which server-side-applies the CRDs, the
// RBAC and the operator Deployment — cluster-scoped objects, at this run's chosen
// version. Those are exactly the objects the lock's own rationale names as
// contended, and exactly the ones `dcctl upgrade` takes the lock to apply. So a
// second bootstrap, on its way to being refused, would first have applied its
// operator version over a cluster the first one was mid-apply on. The lock
// excluded the runs and not the writes.
//
// Taking it here needs one thing to exist: a namespace to put the Lease in. That
// is why this step creates the operator's namespace and only the namespace. The
// object is idempotent, it is the same one the overlay declares (read from the
// rendered manifests rather than named here, so a kustomize rename cannot leave
// the lock somewhere nothing else looks), and creating it early costs nothing —
// the overlay applies it again a step later without complaint.
//
// The DECLARATION still cannot be written until the CRDs are installed, which is
// why declaring is a separate step after the install rather than part of this one.
// It is a thin wrapper over ClaimCluster, which `dcctl install` calls too. What
// stays here is the progress framing and the dry-run rehearsal — the parts that
// differ between a pipeline step and a command — while the part that must NOT
// differ, where the lock lives and how it is taken, has one implementation.
func stepClaimCluster(ctx context.Context, st *State) error {
	if st.DryRun {
		// A dry run takes no lock — it is a plan, and a plan that mutates the
		// cluster is not one. It still resolves the namespace, so that a rehearsal
		// which could not even render the overlay says so rather than reporting a
		// plan it could not have built.
		ns, err := operator.Namespace()
		if err != nil {
			return fail("reading the operator namespace", err)
		}
		st.OperatorNamespace = ns

		doing("claiming the cluster")
		fmt.Println()
		wouldDo("take the cluster lock in namespace " + ns)
		// "Another operator is already running" is part of the answer to "what
		// would this do", so it is reported even though nothing is taken.
		return reportExistingClaim(ctx, st)
	}

	doing("claiming the cluster")
	claim, ns, err := ClaimCluster(ctx, st.KubeContext, st.Instance)
	// The namespace is recorded even when the claim was refused: it is resolved
	// before the lock is contended, and a refusal is one of the paths that still
	// wants to say where it was looking.
	if ns != "" {
		st.OperatorNamespace = ns
	}
	if err != nil {
		return err
	}
	st.Claim = claim
	done()
	return nil
}
