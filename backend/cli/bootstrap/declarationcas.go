// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
)

// maxDeclarationStatusRetries bounds how many times a metadata write is retried over an
// operator status write. The operator writes .status a handful of times while an instance
// comes up; a writer that keeps losing past this is not losing to a status write alone.
const maxDeclarationStatusRetries = 5

// updateInstanceMetadata is the compare-and-swap for a dcctl write that touches only the
// declaration's METADATA (an annotation it owns): read, mutate, Update with the
// resourceVersion the read returned.
//
// 🔴 A STATUS WRITE IS NOT A CONFLICT, AND TREATING IT AS ONE FAILED FRESH BOOTSTRAPS. The
// Instance has a status subresource, so the operator's status write cannot touch anything
// this write is about -- but it still moves resourceVersion, and the API server refuses an
// Update that carries the old one. On a fresh instance the operator's first status write
// lands exactly while dcctl is recording its overlay, so the record lost the race in 2 of 3
// fresh `dcctl bootstrap --values` runs, reported as "something else is editing it".
//
// So on a Conflict this re-reads and compares what a concurrent EDITOR could have changed
// -- identity, generation, spec, annotations, labels, finalizers, deletion -- against the
// FIRST read, not the latest one. Equal means only status (or server bookkeeping) moved:
// the mutation is re-applied to the fresh object and the write retried. Anything else is
// refused exactly as before, because a retry against a real concurrent editor is how their
// edit gets silently reverted. Comparing against the first read keeps that true across
// retries: an edit landing between two retries is still measured from where dcctl started.
//
// mutate reports whether it changed anything; false writes nothing.
func updateInstanceMetadata(ctx context.Context, dyn dynamic.Interface, instance, what string,
	mutate func(*dcv1beta1.Instance) (bool, error)) error {
	var first *dcv1beta1.Instance
	for attempt := 0; ; attempt++ {
		inst, err := readInstanceCR(ctx, dyn, instance)
		if err != nil {
			return err
		}
		if inst == nil {
			return fmt.Errorf("the declaration for instance %q disappeared before %s could be recorded; "+
				"re-run to start from a clean read", instance, what)
		}
		if first == nil {
			first = inst.DeepCopy()
		} else if !onlyStatusMoved(first, inst) {
			return declarationEditedError(instance, what)
		}
		changed, err := mutate(inst)
		if err != nil {
			return err
		}
		if !changed {
			return nil
		}
		obj, err := instanceToUnstructured(inst)
		if err != nil {
			return err
		}
		_, err = dyn.Resource(instanceGVR).Update(ctx, obj, metav1.UpdateOptions{})
		if err == nil {
			return nil
		}
		if !apierrors.IsConflict(err) {
			return fmt.Errorf("recording %s on instance %q: %w", what, instance, err)
		}
		if attempt >= maxDeclarationStatusRetries {
			return declarationEditedError(instance, what)
		}
	}
}

func declarationEditedError(instance, what string) error {
	return fmt.Errorf("the declaration for instance %q changed while %s was being recorded; "+
		"something else is editing it -- re-run once you know what", instance, what)
}

// onlyStatusMoved is whether b differs from a in nothing a metadata write could clobber or
// a concurrent editor could mean: what is left to differ is status and server bookkeeping
// (resourceVersion, managedFields).
func onlyStatusMoved(a, b *dcv1beta1.Instance) bool {
	return a.UID == b.UID &&
		a.Generation == b.Generation &&
		equality.Semantic.DeepEqual(a.Spec, b.Spec) &&
		equality.Semantic.DeepEqual(a.Annotations, b.Annotations) &&
		equality.Semantic.DeepEqual(a.Labels, b.Labels) &&
		equality.Semantic.DeepEqual(a.Finalizers, b.Finalizers) &&
		equality.Semantic.DeepEqual(a.OwnerReferences, b.OwnerReferences) &&
		a.DeletionTimestamp.Equal(b.DeletionTimestamp)
}
