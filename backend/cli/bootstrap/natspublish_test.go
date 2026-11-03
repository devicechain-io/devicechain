// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

// The expiry on the declaration is the expiry of the certificate in the Secret, for the
// bootstrap's placement and for an upgrade's renewal alike.
func TestPublishedExpiryIsTheCertificateInTheSecret(t *testing.T) {
	minted := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	c, st := anInstanceWithBrokerMaterialMintedAt(t, minted, 1)
	dyn := declarationClient(declaredInstance(t, testInstance, nil))

	if err := publishBrokerCertificateExpiry(t.Context(), c, dyn, st); err != nil {
		t.Fatal(err)
	}
	got := readBack(t, dyn, testInstance)
	wantLeaf := minted.Add(natsLeafValidity).Format(time.RFC3339)
	wantCA := minted.Add(natsCAValidity).Format(time.RFC3339)
	if v := got.Annotations[dcv1beta1.AnnotationBrokerCertificateNotAfter]; v != wantLeaf {
		t.Errorf("leaf expiry = %q, want %q", v, wantLeaf)
	}
	if v := got.Annotations[dcv1beta1.AnnotationBrokerCANotAfter]; v != wantCA {
		t.Errorf("authority expiry = %q, want %q", v, wantCA)
	}
	if got.Annotations[dcv1beta1.AnnotationPhase] != dcv1beta1.PhaseReady {
		t.Errorf("publishing disturbed the phase: %v", got.Annotations)
	}
}

// 🔴 A RENEWAL MUST MOVE THE PUBLISHED DATE. The failure this guards is the quiet one: a
// renewal that re-issues the certificate and leaves the old date on the declaration
// reports an expiring instance for a certificate that was just replaced.
func TestARenewalMovesThePublishedExpiry(t *testing.T) {
	// Minted 340 days ago, so an upgrade now is inside the renewal window.
	minted := time.Now().UTC().Add(-340 * 24 * time.Hour).Truncate(time.Second)
	c, st := anInstanceWithBrokerMaterialMintedAt(t, minted, 1)
	dyn := declarationClient(declaredInstance(t, testInstance, nil))
	if err := publishBrokerCertificateExpiry(t.Context(), c, dyn, st); err != nil {
		t.Fatal(err)
	}
	before := readBack(t, dyn, testInstance).Annotations[dcv1beta1.AnnotationBrokerCertificateNotAfter]

	if _, err := renewBrokerCertificate(t.Context(), c, st); err != nil {
		t.Fatalf("renewing: %v", err)
	}
	settleStringDataLikeAnAPIServer(t, c)
	if err := publishBrokerCertificateExpiry(t.Context(), c, dyn, st); err != nil {
		t.Fatal(err)
	}
	after := readBack(t, dyn, testInstance).Annotations[dcv1beta1.AnnotationBrokerCertificateNotAfter]
	b, _ := time.Parse(time.RFC3339, before)
	a, err := time.Parse(time.RFC3339, after)
	if err != nil {
		t.Fatalf("published expiry %q: %v", after, err)
	}
	if !a.After(b.Add(300 * 24 * time.Hour)) {
		t.Errorf("published expiry went %s -> %s; a renewal should move it about a year on", before, after)
	}
}

// With nothing to publish from, or nowhere to publish it, nothing is written and nothing
// fails: the operator reports that as Unknown.
func TestPublishingWithNoSecretOrNoDeclarationIsQuiet(t *testing.T) {
	c, st := anInstanceWithBrokerMaterialMintedAt(t, time.Now().UTC(), 1)

	// No declaration.
	if err := publishBrokerCertificateExpiry(t.Context(), c, declarationClient(), st); err != nil {
		t.Errorf("no declaration: %v", err)
	}

	// No Secret.
	empty, st2 := anInstanceWithBrokerMaterialMintedAt(t, time.Now().UTC(), 1)
	if err := empty.CoreV1().Secrets(InstanceNamespace(st2.Instance)).Delete(t.Context(), natsReleaseName+"-tls", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	dyn := declarationClient(declaredInstance(t, testInstance, nil))
	if err := publishBrokerCertificateExpiry(t.Context(), empty, dyn, st2); err != nil {
		t.Errorf("no Secret: %v", err)
	}
	if _, has := readBack(t, dyn, testInstance).Annotations[dcv1beta1.AnnotationBrokerCertificateNotAfter]; has {
		t.Error("an expiry was published with no certificate to read it from")
	}
}
