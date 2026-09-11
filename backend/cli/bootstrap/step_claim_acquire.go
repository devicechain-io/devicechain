// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dck8s "github.com/devicechain-io/dc-k8s/config"
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
func stepClaimCluster(ctx context.Context, st *State) error {
	manifests, err := dck8s.RenderOperator("")
	if err != nil {
		return fail("rendering the operator overlay to find where the cluster lock lives", err)
	}
	ns, err := operatorNamespace(manifests)
	if err != nil {
		return fail("reading the operator namespace", err)
	}
	st.OperatorNamespace = ns

	doing("claiming the cluster")
	if st.DryRun {
		fmt.Println()
		wouldDo("take the cluster lock in namespace " + ns)
		// A dry run takes no lock — it is a plan, and a plan that mutates the
		// cluster is not one. It still reports the claim it would have met, since
		// "another operator is already running" is part of the answer to "what
		// would this do".
		return reportExistingClaim(ctx, st)
	}

	_, _, typed, err := kubeClients(st.KubeContext)
	if err != nil {
		return fail("building kube clients", err)
	}
	if _, err := typed.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: ns},
	}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fail("creating namespace "+ns, err)
	}

	claim, err := AcquireClaim(ctx, typed, ns, st.Instance, st.KubeContext)
	if err != nil {
		return err
	}
	st.Claim = claim
	done()
	return nil
}
