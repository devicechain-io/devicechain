// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// The address OpenTofu knows the shared infrastructure namespace by, and the labels
// its module stamps on it (deploy/opentofu/modules/namespace/main.tf).
//
// Both are restated here because dcctl now creates that namespace, and a namespace
// created with different labels than the module declares is one the next apply
// rewrites — a diff on every run, reported as drift, on an object nothing is drifting.
const (
	infraNamespaceStateAddress = "module.namespace.kubernetes_namespace_v1.this[0]"
	tofuManagedByLabel         = "app.kubernetes.io/managed-by"
	tofuManagedByValue         = "opentofu"
	tofuComponentLabel         = "devicechain.io/component"
	tofuComponentValue         = "infrastructure"
)

// stateLister and stateImporter are the two halves of terraform-exec this file
// needs, named separately so the decision can be exercised without a real binary.
type stateLister interface {
	Show(ctx context.Context, opts ...tfexec.ShowOption) (*tfjson.State, error)
}

type stateImporter interface {
	Import(ctx context.Context, address, id string, opts ...tfexec.ImportOption) error
}

// ensureInfraNamespace creates the shared infrastructure namespace if it is not
// there.
//
// 🔴 SOMETHING HAS TO CREATE IT BEFORE THE APPLY, AND IT CANNOT BE THE APPLY. The
// credentials the databases and the broker are built with are Secrets in this
// namespace, and dcctl writes them BEFORE OpenTofu runs — CloudNativePG reads the
// credentials Secret when it CREATES a Cluster (modules/cnpg-cluster/main.tf: "Left
// unset, CNPG mints a password of its own"), so a Secret written afterwards leaves the
// role on one password and every service on another. A Secret cannot be written into a
// namespace that does not exist, so the namespace comes first.
//
// The labels are the module's own, so that importing this object into state produces
// no diff. See adoptInfraNamespace for why import rather than a toggle.
func ensureInfraNamespace(ctx context.Context, typed kubernetes.Interface, namespace string) error {
	api := typed.CoreV1().Namespaces()
	existing, err := api.Get(ctx, namespace, metav1.GetOptions{})
	switch {
	case err == nil:
		if existing.DeletionTimestamp != nil {
			return fmt.Errorf("namespace %q is still being deleted, so this instance's "+
				"infrastructure cannot be built into it yet: a previous `dcctl destroy` has not "+
				"finished. Wait for the namespace to go and run this again", namespace)
		}
		return nil
	case !apierrors.IsNotFound(err):
		return fmt.Errorf("reading namespace %q: %w", namespace, err)
	}

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: namespace,
		Labels: map[string]string{
			tofuManagedByLabel: tofuManagedByValue,
			tofuComponentLabel: tofuComponentValue,
		},
	}}
	if _, err := api.Create(ctx, ns, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("creating namespace %q: %w", namespace, err)
	}
	return nil
}

// adoptInfraNamespace makes OpenTofu manage a namespace dcctl created, by importing
// it into state.
//
// 🔴 IMPORT IS THE ONLY DOOR THAT LEAVES THE RESOURCE COUNT WHERE IT IS, and the
// count is the whole hazard. The two obvious alternatives both move it:
//
//   - Leave `create_namespace` true and create the namespace first. The Kubernetes
//     provider's namespace resource DOES NOT ADOPT — resourceKubernetesNamespaceV1Create
//     has no already-exists branch — so a namespace that is already there is an apply
//     FAILURE, not a no-op.
//   - Set `create_namespace` false. Measured: with the resource in state that plans
//     `0 to add, 0 to change, 1 to DESTROY`, and destroying this namespace takes every
//     database, the broker and every credential in it. This is the orphan-destroy
//     class, and `prevent_destroy` does not protect against it — a resource removed
//     from the configuration is not a resource the configuration can guard.
//
// Importing leaves the toggle alone on every path, so neither direction of the flag is
// ever the thing that decides whether a namespace lives.
//
// Doing nothing when the resource is already managed is the ordinary case: only the
// FIRST run after this change has a namespace OpenTofu does not know about. `tofu
// import` refuses an address that is already in state, so asking first is not
// defensive padding — it is the difference between idempotent and once-only.
func adoptInfraNamespace(ctx context.Context, tf interface {
	stateLister
	stateImporter
}, namespace string, vars []string) error {
	managed, err := stateHasAddress(ctx, tf, infraNamespaceStateAddress)
	if err != nil {
		return err
	}
	if managed {
		return nil
	}
	opts := make([]tfexec.ImportOption, 0, len(vars))
	for _, v := range vars {
		opts = append(opts, tfexec.Var(v))
	}
	if err := tf.Import(ctx, infraNamespaceStateAddress, namespace, opts...); err != nil {
		return fmt.Errorf("handing namespace %q to OpenTofu (import %s): %w. Without it the "+
			"apply would try to CREATE a namespace that is already there, and the Kubernetes "+
			"provider treats that as a failure rather than adopting it",
			namespace, infraNamespaceStateAddress, err)
	}
	return nil
}

// stateHasAddress reports whether the current state manages a resource address.
//
// 🔴 A READ FAILURE IS NOT "NOT MANAGED". Reading it that way would import over an
// instance whose state exists but could not be parsed, and a double-managed namespace
// is a namespace two runs can each decide to delete. Only an ANSWER counts; anything
// else is returned. The one absence that is genuinely an answer is a state file with
// no values at all, which is what a first bootstrap has.
func stateHasAddress(ctx context.Context, tf stateLister, address string) (bool, error) {
	state, err := tf.Show(ctx)
	if err != nil {
		return false, fmt.Errorf("reading the infrastructure state: %w", err)
	}
	if state == nil || state.Values == nil || state.Values.RootModule == nil {
		return false, nil
	}
	return moduleHasAddress(state.Values.RootModule, address), nil
}

// moduleHasAddress walks a state module tree looking for one resource address.
//
// Recursive because the addresses that matter are inside modules — the namespace is
// `module.namespace.…`, the database credentials are `module.cnpg_rdb.…` — and
// tfjson nests a child module per level rather than flattening them.
func moduleHasAddress(m *tfjson.StateModule, address string) bool {
	if m == nil {
		return false
	}
	for _, r := range m.Resources {
		if r.Address == address {
			return true
		}
	}
	for _, child := range m.ChildModules {
		if moduleHasAddress(child, address) {
			return true
		}
	}
	return false
}
