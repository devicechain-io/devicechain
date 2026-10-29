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
	"strconv"
	"strings"
	"time"

	"github.com/fatih/color"
	appsv1 "k8s.io/api/apps/v1"
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
		Namespace: m.Namespace,
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
	ref := mintedCredentialRef{InstanceNamespace(instance), natsAuthoritySecretName, "tls.key"}

	foundKey, keyPEM, err := reuseMintedCredential(ctx, typed, instanceOwner(instance, instanceUID), ref)
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

	foundCert, certPEM, err := reuseMintedCredential(ctx, typed, instanceOwner(instance, instanceUID),
		mintedCredentialRef{InstanceNamespace(instance), natsAuthoritySecretName, "tls.crt"})
	if err != nil {
		return nil, nil, err
	}
	if foundCert != reuseRecovered {
		// Half of an authority is not an authority, and it is not an absence either:
		// something wrote this object and left it unusable.
		return nil, nil, fmt.Errorf(
			"Secret %s/%s holds the broker authority's private key but not its certificate, so "+
				"no certificate can be signed under it", InstanceNamespace(instance), natsAuthoritySecretName)
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

// brokerHotRenewTimeout bounds how long a hot-swapped certificate is waited for.
//
// The path is: API server -> kubelet projects the changed Secret into the pod volume (its sync
// period plus cache TTL, up to roughly two minutes) -> the config reloader notices and SIGHUPs
// the server -> the server swaps the listener's certificate. Five minutes covers that with
// margin; past it the swap is not coming and the broker is rolled instead.
var brokerHotRenewTimeout = 5 * time.Minute

// brokerHotRenewPoll is how often each broker pod's served certificate is read while waiting.
var brokerHotRenewPoll = 5 * time.Second

// brokerMetricsPort is the broker binary's own metrics port (DC_BROKER_METRICS_ADDR default).
const brokerMetricsPort = "7778"

// brokerServedNotAfter reads the expiry of the certificate a broker pod's MQTT listener is
// presenting NOW, from dc_broker_mqtt_tls_cert_not_after_seconds through the API server's pod
// proxy. It is a variable so a test can stand in for a pod.
//
// The gauge, not /varz: varz's tls_cert_not_after reads the wrapper's frozen first certificate
// and goes stale after a swap.
var brokerServedNotAfter = func(ctx context.Context, typed kubernetes.Interface, namespace, pod string) (time.Time, error) {
	raw, err := typed.CoreV1().Pods(namespace).ProxyGet("http", pod, brokerMetricsPort, "/metrics", nil).DoRaw(ctx)
	if err != nil {
		return time.Time{}, err
	}
	const series = "dc_broker_mqtt_tls_cert_not_after_seconds"
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, series+" ") {
			v, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, series)), 10, 64)
			if err != nil {
				return time.Time{}, fmt.Errorf("reading %s: %w", series, err)
			}
			return time.Unix(v, 0).UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("%s is not exported by %s", series, pod)
}

// hotRenewable says whether the broker can pick the new leaf up without being restarted, and
// otherwise why not.
//
// The broker's hot path (hottls.go) swaps the certificate on SIGHUP only when the server config
// text and the CA pools are unchanged; anything else falls back to a full reload, which drops
// the MQTT sessions this exists to keep, or needs pods that mount something new. So:
//   - the leaf must be re-issued for EXPIRY, not for missing names: names missing means the
//     topology grew, and a topology change is a config change (routes) that rolls the pods;
//   - the CA in the Secret the pods mount must be the one the leaf is signed under;
//   - the StatefulSet must already run the replica count the leaf is issued for.
//
// This only matters when the broker image is unchanged. An upgrade that also changes the image
// or the broker's rendered config rolls the broker in the infrastructure apply that follows,
// and the new pods mount the new Secret; no extra handling is warranted for that.
func hotRenewable(current *corev1.Secret, caPEM string, wanted []string,
	sts *appsv1.StatefulSet, replicas int) (bool, string) {
	leaf, err := parseFirstCertificate(string(current.Data["tls.crt"]))
	if err != nil {
		return false, "the current certificate cannot be read"
	}
	if missing := namesMissingFrom(leaf.DNSNames, wanted); len(missing) > 0 {
		return false, "the cluster's topology changed, which is a config change"
	}
	if strings.TrimSpace(string(current.Data["ca.crt"])) != strings.TrimSpace(caPEM) {
		return false, "the certificate authority changed"
	}
	if sts == nil || sts.Spec.Replicas == nil || int(*sts.Spec.Replicas) != replicas {
		return false, "the broker is not running the replica count the certificate is issued for"
	}
	return true, ""
}

// renewBrokerCertificate re-issues the broker's leaf when it is near expiry or no
// longer covers the cluster, under the authority the instance already has.
//
// 🔴 THE NEW CERTIFICATE HAS TO ACTUALLY BE SERVED, AND SOMETHING MUST CHECK. The NATS
// chart rolls its pods on a checksum of the rendered CONFIGMAP, not of the TLS Secret, so
// a Secret write alone rolls nothing; what picks it up is the config reloader sidecar
// (SIGHUP) and the broker's hot TLS swap, which keeps MQTT sessions. When only the leaf
// changed (same CA, same config, same replica count) this waits until every broker pod
// reports the new expiry on dc_broker_mqtt_tls_cert_not_after_seconds, and does NOT
// restart. If a pod has not switched within brokerHotRenewTimeout, or the probe cannot be
// made, it warns and rolls the broker, so an old certificate is never left in service
// behind a green renewal. Every other case rolls it, as before.
//
// restarted reports that it restarted the broker, so the caller can wait for the
// servers to come back before it moves anything else.
func renewBrokerCertificate(ctx context.Context, typed kubernetes.Interface, st *State) (restarted bool, err error) {
	ns := InstanceNamespace(st.Instance)
	current, err := typed.CoreV1().Secrets(ns).Get(ctx, natsReleaseName+"-tls", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		fmt.Println(color.YellowString(
			"  The broker has no certificate Secret, so there is nothing to renew. This instance " +
				"predates dcctl owning it; its certificate is renewed by rebuilding the instance."))
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading the broker's certificate to see whether it needs renewing: %w", err)
	}

	replicas := haFor(st.HA).ServerReplicas
	wanted := natsServerDNSNames(natsReleaseName, ns, replicas)
	reason, err := leafReissueReason(string(current.Data["tls.crt"]), wanted, time.Now().UTC())
	if err != nil {
		return false, err
	}
	if reason == "" {
		return false, nil
	}

	caCert, caKey, err := readNATSAuthority(ctx, typed, st.Instance, st.InstanceUID)
	if err != nil {
		return false, err
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
		return false, nil
	}

	leafCertPEM, leafKeyPEM, err := issueNATSLeaf(caCert, caKey, natsReleaseName, ns, replicas, time.Now().UTC())
	if err != nil {
		return false, err
	}
	newLeaf, err := parseFirstCertificate(leafCertPEM)
	if err != nil {
		return false, fmt.Errorf("reading the certificate just issued: %w", err)
	}
	material := &natsTLSMaterial{
		Namespace:   ns,
		CACertPEM:   encodePEM("CERTIFICATE", caCert.Raw),
		LeafCertPEM: leafCertPEM,
		LeafKeyPEM:  leafKeyPEM,
	}

	// Decided BEFORE the write, against the Secret and StatefulSet as they were.
	sts, stsErr := typed.AppsV1().StatefulSets(ns).Get(ctx, natsStatefulSetName, metav1.GetOptions{})
	if stsErr != nil {
		sts = nil
	}
	hot, whyNot := hotRenewable(current, material.CACertPEM, wanted, sts, replicas)

	if err := writeOwnedSecret(ctx, typed, instanceOwner(st.Instance, st.InstanceUID),
		natsTLSSecret(natsReleaseName, material), time.Now); err != nil {
		return false, err
	}

	if hot {
		fmt.Println(color.WhiteString(
			"  Broker certificate re-issued under the same authority (%s). Waiting for the brokers to\n"+
				"  switch to it without restarting (up to %s).", reason, brokerHotRenewTimeout))
		verr := waitForBrokersToServe(ctx, typed, ns, newLeaf.NotAfter)
		if verr == nil {
			fmt.Println(color.WhiteString(
				"  Every broker now serves the new certificate; no broker was restarted and no MQTT session dropped."))
			return false, nil
		}
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		fmt.Println(color.YellowString(
			"  ⚠️  The brokers did not switch to the new certificate on their own (%v).\n"+
				"     Restarting the broker so it presents it.", verr))
	} else {
		fmt.Println(color.WhiteString("  Rolling the broker rather than swapping the certificate in place: %s.", whyNot))
	}

	if err := restartBroker(ctx, typed, ns); err != nil {
		return false, err
	}
	fmt.Println(color.WhiteString(
		"  Broker certificate re-issued under the same authority (%s), and the broker restarted\n"+
			"  to present it. Nothing had to re-trust anything.", reason))
	return true, nil
}

// waitForBrokersToServe waits until every broker pod's MQTT listener presents a certificate
// expiring at want (the new leaf's NotAfter, which is strictly later than the one it replaces).
func waitForBrokersToServe(ctx context.Context, typed kubernetes.Interface, ns string, want time.Time) error {
	_, err := pollUntil(ctx, brokerHotRenewTimeout, brokerHotRenewPoll, 1, func() (bool, string, error) {
		list, err := typed.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: natsPodSelector})
		if err != nil {
			return false, "", err
		}
		if len(list.Items) == 0 {
			return false, "", fmt.Errorf("no broker pods found to verify")
		}
		for _, p := range list.Items {
			got, err := brokerServedNotAfter(ctx, typed, ns, p.Name)
			if err != nil {
				return false, "", fmt.Errorf("%s: %w", p.Name, err)
			}
			if got.Unix() != want.Unix() {
				return false, fmt.Sprintf("%s still serves a certificate expiring %s", p.Name, got.Format(time.RFC3339)), nil
			}
		}
		return true, "", nil
	})
	return err
}

// restartBroker rolls the broker onto a certificate it has already been handed.
//
// A template annotation, which is what `kubectl rollout restart` does: it changes the
// pod template, so the StatefulSet controller replaces the pods in order and waits for
// each to be ready, rather than the pods all going at once.
func restartBroker(ctx context.Context, typed kubernetes.Interface, namespace string) error {
	patch := fmt.Sprintf(
		`{"spec":{"template":{"metadata":{"annotations":{"devicechain.io/restarted-at":%q}}}}}`,
		time.Now().UTC().Format(time.RFC3339))
	_, err := typed.AppsV1().StatefulSets(namespace).Patch(
		ctx, natsStatefulSetName, types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("restarting the broker so it presents its new certificate: %w. The "+
			"certificate was written, so restarting %s/%s by hand completes the renewal",
			err, namespace, natsStatefulSetName)
	}
	return nil
}
