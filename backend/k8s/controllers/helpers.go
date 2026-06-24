/**
 * Copyright © 2022 DeviceChain
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

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
