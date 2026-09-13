// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"sort"
	"time"

	"github.com/fatih/color"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// natsLeafRenewBefore is how close to expiry the broker's certificate is re-issued.
//
// Thirty days, which is the number the retired infrastructure module used
// (`early_renewal_hours = 720` on both certificates). It is kept rather than chosen
// afresh because the behaviour being restored is that module's: an apply inside the
// last thirty days re-issued the leaf, and an apply outside it did nothing.
const natsLeafRenewBefore = 30 * 24 * time.Hour

// natsAuthoritySecretName holds the authority that signs the broker's leaf.
//
// 🔴 A SEPARATE OBJECT FROM THE BROKER'S OWN TLS SECRET, AND THE SEPARATION IS THE
// WHOLE DESIGN. The broker needs its leaf and the CA certificate; it has no use for
// the authority's PRIVATE key, and putting one where it is mounted would hand the
// ability to mint trusted certificates to anything that can read the broker's volume.
// TestTheBrokerSecretNeverCarriesTheAuthorityKey says so and stays exactly as it is.
//
// 🔴 AND IT IS NOT NAMED dc-nats-ca. That name is already taken, by the ConfigMap the
// infrastructure module renders the PUBLIC certificate into. Two objects a letter
// apart, one of which is safe to read and one of which is not, is a mistake waiting
// for whoever greps.
const natsAuthoritySecretName = natsReleaseName + "-ca-keypair"

// natsAuthoritySecret keeps the authority so its leaf can be re-issued later.
//
// 🔴 THIS IS A DELIBERATE REVERSAL, AND THE REASON IT IS SAFE IS NOT "a Secret is
// fine". The authority key used to be written in CLEARTEXT INTO THE OPENTOFU STATE
// FILE, beside the instance directory on whoever ran the bootstrap — that is the leak
// natstls.go was written to close, and nothing here walks it back. What changes is
// where the key lives: inside the cluster, under the same RBAC and the same
// encryption at rest as every other credential this instance runs on, rather than on
// a laptop.
//
// 🔑 AND WITHOUT IT THERE IS NO RENEWAL AT ALL, only rotation. Discarding the
// authority meant a leaf could never be re-issued under it, so the only way to
// replace an expiring certificate was to replace the CA every service trusts — and
// since a service builds its trust pool from the document it was started with, the
// new pool rejects the running broker's old leaf while the old pool rejects the new
// one. There is no ordering of that change which is not an outage. Keeping the
// authority makes the routine case routine: a new leaf, the same CA, nothing to
// re-trust.
func natsAuthoritySecret(releaseName string, m *natsTLSMaterial) ownedSecret {
	return ownedSecret{
		Name:      releaseName + "-ca-keypair",
		Namespace: infraNamespace,
		Type:      corev1.SecretTypeTLS,
		Labels: map[string]string{
			"app.kubernetes.io/name":      releaseName,
			"app.kubernetes.io/component": "broker-authority",
		},
		Data: map[string]string{
			"tls.crt": m.CACertPEM,
			"tls.key": m.CAKeyPEM,
		},
	}
}

// readNATSAuthority recovers the authority so a fresh leaf can be signed under it.
func readNATSAuthority(
	ctx context.Context,
	typed kubernetes.Interface,
	instance, instanceUID string,
) (*x509.Certificate, *rsa.PrivateKey, error) {
	ref := mintedCredentialRef{infraNamespace, natsAuthoritySecretName, "tls.key"}

	foundKey, keyPEM, err := reuseMintedCredential(ctx, typed, instance, instanceUID, ref)
	if err != nil {
		return nil, nil, err
	}
	if foundKey != reuseRecovered {
		// 🔴 NOT AN ERROR, AND NOT A RE-MINT EITHER. An instance bootstrapped before
		// the authority was kept simply does not have this, and the honest answer is
		// that its certificate cannot be renewed in place — not that this upgrade
		// should mint a new authority and change what every service trusts as a side
		// effect of a version bump. The caller reports it and carries on.
		return nil, nil, nil
	}

	foundCert, certPEM, err := reuseMintedCredential(ctx, typed, instance, instanceUID,
		mintedCredentialRef{infraNamespace, natsAuthoritySecretName, "tls.crt"})
	if err != nil {
		return nil, nil, err
	}
	if foundCert != reuseRecovered {
		// Half of an authority is not an authority, and it is not an absence either:
		// something wrote this object and left it unusable.
		return nil, nil, fmt.Errorf(
			"Secret %s/%s holds the broker authority's private key but not its certificate, so "+
				"no certificate can be signed under it", infraNamespace, natsAuthoritySecretName)
	}

	cert, err := parseFirstCertificate(certPEM)
	if err != nil {
		return nil, nil, fmt.Errorf("reading the broker's certificate authority: %w", err)
	}
	block, _ := pem.Decode([]byte(keyPEM))
	if block == nil {
		return nil, nil, fmt.Errorf("the broker authority's private key is not PEM")
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("reading the broker authority's private key: %w", err)
	}
	return cert, key, nil
}

// parseFirstCertificate reads the leading certificate out of a PEM bundle.
//
// 🔴 THE FIRST BLOCK, AND WHICH ONE THAT IS MATTERS. The broker's tls.crt carries the
// LEAF followed by the AUTHORITY, because a client that trusts only the authority
// needs the chain presented to it. So a reader that took the last block, or that
// concatenated them, would be asking the ten-year authority when it expires instead
// of the one-year leaf — and would conclude that nothing ever needs renewing.
func parseFirstCertificate(bundle string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(bundle))
	if block == nil {
		return nil, fmt.Errorf("no PEM certificate found")
	}
	return x509.ParseCertificate(block.Bytes)
}

// leafReissueReason says why the broker's certificate has to be replaced, or "" when
// it does not.
//
// 🔴 EXPIRY IS NOT THE ONLY REASON, AND THE OTHER ONE IS THE QUIET ONE. The names a
// leaf covers are a function of how many servers there are: one replica needs five,
// three need seventeen, because each server dials its peers by pod name. Scaling an
// instance up therefore invalidates a certificate that is still comfortably in date —
// the routes never form, no leader is elected, and the symptom reads as a broken
// cluster rather than as a certificate a few names short.
func leafReissueReason(leafPEM string, wanted []string, now time.Time) (string, error) {
	leaf, err := parseFirstCertificate(leafPEM)
	if err != nil {
		return "", fmt.Errorf("reading the broker's current certificate: %w", err)
	}

	if remaining := leaf.NotAfter.Sub(now); remaining <= natsLeafRenewBefore {
		if remaining <= 0 {
			return fmt.Sprintf("it expired on %s", leaf.NotAfter.Format(time.RFC3339)), nil
		}
		return fmt.Sprintf("it expires on %s, within its last %d days",
			leaf.NotAfter.Format(time.RFC3339), int(natsLeafRenewBefore.Hours()/24)), nil
	}

	if missing := namesMissingFrom(leaf.DNSNames, wanted); len(missing) > 0 {
		return fmt.Sprintf("it does not cover %v, which this instance's brokers dial each other by",
			missing), nil
	}

	return "", nil
}

// namesMissingFrom reports which wanted names a certificate does not carry.
//
// One-directional on purpose. A certificate covering MORE names than the current
// topology needs is what an instance scaled back DOWN looks like, and re-issuing for
// that would restart the broker to remove names nothing was using.
func namesMissingFrom(have, wanted []string) []string {
	present := make(map[string]bool, len(have))
	for _, n := range have {
		present[n] = true
	}
	var missing []string
	for _, n := range wanted {
		if !present[n] {
			missing = append(missing, n)
		}
	}
	sort.Strings(missing)
	return missing
}

// renewBrokerCertificate re-issues the broker's leaf when it is near expiry or no
// longer covers the cluster, under the authority the instance already has.
//
// 🔴 IT RESTARTS THE BROKER, AND LEAVING THAT OUT WOULD MAKE THE WHOLE THING A NO-OP
// THAT REPORTS SUCCESS. The NATS chart rolls its pods on a checksum of the rendered
// CONFIGMAP, not of the TLS Secret — so a new certificate written into the Secret is
// picked up by nothing. The old leaf keeps being served until something unrelated
// happens to roll the StatefulSet, which on a healthy instance may be never, and the
// certificate would expire anyway with a green renewal in the log.
func renewBrokerCertificate(ctx context.Context, typed kubernetes.Interface, st *State) error {
	current, err := typed.CoreV1().Secrets(infraNamespace).Get(
		ctx, natsReleaseName+"-tls", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		fmt.Println(color.YellowString(
			"  The broker has no certificate Secret, so there is nothing to renew. This instance " +
				"predates dcctl owning it; its certificate is renewed by rebuilding the instance."))
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading the broker's certificate to see whether it needs renewing: %w", err)
	}

	replicas := haFor(st.HA).ServerReplicas
	wanted := natsServerDNSNames(natsReleaseName, infraNamespace, replicas)
	reason, err := leafReissueReason(string(current.Data["tls.crt"]), wanted, time.Now().UTC())
	if err != nil {
		return err
	}
	if reason == "" {
		return nil
	}

	caCert, caKey, err := readNATSAuthority(ctx, typed, st.Instance, st.InstanceUID)
	if err != nil {
		return err
	}
	if caCert == nil {
		// 🔴 REPORTED LOUDLY AND NOT FAILED. This instance needs a new certificate and
		// this dcctl cannot issue one, because the authority that signed the old one
		// was discarded at bootstrap — instances built before the authority was kept
		// are all in this position. Failing the upgrade would make a version bump
		// impossible for exactly that population; saying nothing would let the
		// certificate expire on an instance whose operator was told the upgrade
		// succeeded.
		fmt.Println(color.YellowString(
			"  ⚠️  The broker's certificate needs replacing (%s), and this instance does not\n"+
				"     keep the authority that signed it — so it cannot be re-issued in place.\n"+
				"     Rebuilding the instance mints a fresh authority and certificate; until then\n"+
				"     the broker will refuse every connection once the certificate lapses.", reason))
		return nil
	}

	leafCertPEM, leafKeyPEM, err := issueNATSLeaf(
		caCert, caKey, natsReleaseName, infraNamespace, replicas, time.Now().UTC())
	if err != nil {
		return err
	}
	material := &natsTLSMaterial{
		CACertPEM:   encodePEM("CERTIFICATE", caCert.Raw),
		LeafCertPEM: leafCertPEM,
		LeafKeyPEM:  leafKeyPEM,
	}
	if err := writeOwnedSecret(ctx, typed, st.Instance, st.InstanceUID,
		natsTLSSecret(natsReleaseName, material), time.Now); err != nil {
		return err
	}

	if err := restartBroker(ctx, typed); err != nil {
		return err
	}
	fmt.Println(color.WhiteString(
		"  Broker certificate re-issued under the same authority (%s), and the broker restarted\n"+
			"  to present it. Nothing had to re-trust anything.", reason))
	return nil
}

// restartBroker rolls the broker onto a certificate it has already been handed.
//
// A template annotation, which is what `kubectl rollout restart` does: it changes the
// pod template, so the StatefulSet controller replaces the pods in order and waits for
// each to be ready, rather than the pods all going at once.
func restartBroker(ctx context.Context, typed kubernetes.Interface) error {
	patch := fmt.Sprintf(
		`{"spec":{"template":{"metadata":{"annotations":{"devicechain.io/restarted-at":%q}}}}}`,
		time.Now().UTC().Format(time.RFC3339))
	_, err := typed.AppsV1().StatefulSets(infraNamespace).Patch(
		ctx, natsStatefulSetName, types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("restarting the broker so it presents its new certificate: %w. The "+
			"certificate was written, so restarting %s/%s by hand completes the renewal",
			err, infraNamespace, natsStatefulSetName)
	}
	return nil
}
