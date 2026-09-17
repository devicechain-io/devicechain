// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"strings"

	tfjson "github.com/hashicorp/terraform-json"
)

// instanceNamespacedAddresses are the instance root's releases whose namespace moved when
// each instance got a namespace of its own.
var instanceNamespacedAddresses = []string{
	"module.nats.helm_release.nats",
	"module.cnpg_tsdb.helm_release.cluster",
}

// checkInstanceInItsOwnNamespace refuses to apply the instance root over state whose
// broker or event store runs somewhere other than the instance's own namespace.
//
// 🔴 A NAMESPACE CHANGE ON A HELM RELEASE IS A REPLACEMENT, AND FOR THE EVENT STORE A
// REPLACEMENT IS ITS HISTORY. OpenTofu cannot move a release between namespaces; it
// destroys and recreates it. The event store's release carries prevent_destroy, so the
// apply would stop on it — with an error about a lifecycle rule, not about the move —
// and the broker's, which does not, would be destroyed with its JetStream state. Before
// either, this says what actually happened and what to do.
//
// Keyed on the STATE, like the other fences: it is what the apply acts on. Fails closed
// on an unreadable state.
func checkInstanceInItsOwnNamespace(ctx context.Context, tf stateLister, instance string) error {
	state, err := tf.Show(ctx)
	if err != nil {
		return fmt.Errorf("reading the infrastructure state: %w", err)
	}
	if state == nil || state.Values == nil || state.Values.RootModule == nil {
		return nil
	}
	want := instanceNamespace(instance)
	var elsewhere []string
	for _, address := range instanceNamespacedAddresses {
		r := findStateResource(state.Values.RootModule, address)
		if r == nil {
			continue
		}
		ns, _ := r.AttributeValues["namespace"].(string)
		if ns != want {
			elsewhere = append(elsewhere, fmt.Sprintf("%s in namespace %q", address, ns))
		}
	}
	if len(elsewhere) == 0 {
		return nil
	}
	return fmt.Errorf(
		"instance %q was built before each instance had a namespace of its own: its infrastructure "+
			"runs outside namespace %q —\n  %s\n"+
			"A Helm release cannot move between namespaces in place; OpenTofu would destroy and "+
			"recreate it, which for the event store is its history. There is no in-place upgrade: "+
			"the instance has to be rebuilt. On a cluster dcctl created, `dcctl destroy %s` deletes the "+
			"cluster with it; then `dcctl bootstrap %s`. `--keep-cluster` is NOT enough: it leaves this "+
			"infrastructure running in the shared namespace and this state in place, and this refusal "+
			"returns. Back up anything you need first — rebuilding takes the instance's data with it",
		instance, want, strings.Join(elsewhere, "\n  "), instance, instance)
}

// findStateResource walks a state module tree for one resource address.
func findStateResource(m *tfjson.StateModule, address string) *tfjson.StateResource {
	if m == nil {
		return nil
	}
	for _, r := range m.Resources {
		if r.Address == address {
			return r
		}
	}
	for _, child := range m.ChildModules {
		if r := findStateResource(child, address); r != nil {
			return r
		}
	}
	return nil
}
