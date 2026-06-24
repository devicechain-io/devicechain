// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// fieldOwner identifies the operator as the server-side-apply field manager.
const fieldOwner = client.FieldOwner("dc-operator")

// applyOwned server-side-applies obj as a controller-owned child of owner.
//
// Server-side apply writes only the fields the operator actually sets, so
// server-defaulted fields (container termination paths, Service clusterIP,
// Deployment strategy, ...) are not treated as drift. This avoids the
// write-amplification loop a whole-object CreateOrUpdate would cause: each
// spurious Update would bump resourceVersion, trip the Owns(...) watch, and
// re-enqueue the owner indefinitely. Re-applying an unchanged object is a no-op.
func applyOwned(ctx context.Context, c client.Client, scheme *runtime.Scheme, owner, obj client.Object) error {
	if err := controllerutil.SetControllerReference(owner, obj, scheme); err != nil {
		return err
	}
	// Server-side apply requires the object's GroupVersionKind to be populated.
	gvks, _, err := scheme.ObjectKinds(obj)
	if err != nil {
		return err
	}
	obj.GetObjectKind().SetGroupVersionKind(gvks[0])
	return c.Patch(ctx, obj, client.Apply, fieldOwner, client.ForceOwnership)
}
