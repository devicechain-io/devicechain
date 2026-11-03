// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

// publishBrokerCertificateExpiry records, on the Instance, when the broker's TLS
// certificate and its authority expire, so the operator can report on them without
// being given access to Secrets (see dcv1beta1.AnnotationBrokerCertificateNotAfter).
//
// 🔴 IT PUBLISHES WHAT THE SECRET HOLDS NOW, NOT WHAT THE CALLER MEANT TO WRITE. Both
// callers (the bootstrap, right after it places the material, and the upgrade, right
// after the renewal step) call this once the Secret is settled, and reading it back
// makes the published date the date of the certificate the broker will load whatever
// path got it there: re-issued, left alone because it is still in date, or unrenewable
// because the authority was discarded. The last two matter: an instance that predates
// this publication, and an upgrade that renews nothing, would otherwise never publish.
//
// A missing Secret (an instance that predates dcctl owning the certificate) or a
// missing declaration publishes nothing and is not an error: there is nothing true to
// say, and the operator reports that as Unknown rather than as a date.
//
// The write is a merge patch of the two annotations, for the reason setInstancePhase
// gives: the operator writes this object's status concurrently, and a compare-and-swap
// of the whole object would let a status update abort a bootstrap.
func publishBrokerCertificateExpiry(ctx context.Context, typed kubernetes.Interface, dyn dynamic.Interface, st *State) error {
	secret, err := typed.CoreV1().Secrets(InstanceNamespace(st.Instance)).Get(ctx, natsReleaseName+"-tls", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading the broker's certificate to publish its expiry: %w", err)
	}
	leaf, err := parseFirstCertificate(string(secret.Data["tls.crt"]))
	if err != nil {
		return fmt.Errorf("reading the broker's certificate to publish its expiry: %w", err)
	}
	annotations := map[string]string{
		dcv1beta1.AnnotationBrokerCertificateNotAfter: leaf.NotAfter.UTC().Format(time.RFC3339),
	}
	// The authority is published beside it when the Secret carries one it can read.
	// A Secret without a readable ca.crt is not an error here: the leaf is the date
	// that matters first, and the operator judges whichever it is given.
	if ca, err := parseFirstCertificate(string(secret.Data["ca.crt"])); err == nil {
		annotations[dcv1beta1.AnnotationBrokerCANotAfter] = ca.NotAfter.UTC().Format(time.RFC3339)
	}

	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": annotations}})
	if err != nil {
		return err
	}
	if _, err := dyn.Resource(instanceGVR).Patch(ctx, st.Instance, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		if isInstanceNotFound(err, st.Instance) {
			return nil
		}
		return fmt.Errorf("publishing the broker certificate's expiry on instance %q: %w", st.Instance, err)
	}
	return nil
}
