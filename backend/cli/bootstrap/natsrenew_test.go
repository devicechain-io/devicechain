// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"crypto/x509"
	"fmt"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

// An instance whose broker material was minted at a chosen moment, written the way a
// bootstrap writes it, and stored the way an API server stores it.
func anInstanceWithBrokerMaterialMintedAt(t *testing.T, mintedAt time.Time, replicas int) (*fake.Clientset, *State) {
	t.Helper()
	st := aWritableState()
	if replicas > 1 {
		st.HA = true
	}
	material, err := mintNATSTLS(natsReleaseName, InstanceNamespace(testInstance), replicas, mintedAt)
	if err != nil {
		t.Fatalf("minting the broker's certificate: %v", err)
	}
	st.NATSTLS = material

	// The broker itself. It is part of the fixture rather than omitted because a
	// renewal that writes a certificate and does not roll the broker onto it has
	// changed nothing an operator can observe — so "there was no StatefulSet to
	// restart" must not be a state any of these tests passes through quietly.
	n := int32(replicas)
	objs := []runtime.Object{&appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: natsStatefulSetName, Namespace: InstanceNamespace(testInstance)},
		Spec:       appsv1.StatefulSetSpec{Replicas: &n},
	}}
	for i := 0; i < replicas; i++ {
		objs = append(objs, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%d", natsStatefulSetName, i),
			Namespace: InstanceNamespace(testInstance),
			Labels:    map[string]string{"app.kubernetes.io/name": "nats", "app.kubernetes.io/instance": "dc-nats", "app.kubernetes.io/component": "nats"},
		}})
	}
	c := fake.NewSimpleClientset(objs...)
	if err := writeMintedSecrets(context.Background(), c, st); err != nil {
		t.Fatalf("writing the broker's material: %v", err)
	}
	settleStringDataLikeAnAPIServer(t, c)
	// A fake client cannot proxy to a pod; every renewal here reads the served certificate through this.
	podsServeTheSecret(t, c)
	return c, st
}

func brokerLeaf(t *testing.T, c *fake.Clientset) *x509.Certificate {
	t.Helper()
	s, err := c.CoreV1().Secrets(InstanceNamespace(testInstance)).Get(
		context.Background(), natsReleaseName+"-tls", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading the broker's certificate: %v", err)
	}
	leaf, err := parseFirstCertificate(string(s.Data["tls.crt"]))
	if err != nil {
		t.Fatalf("parsing the broker's certificate: %v", err)
	}
	return leaf
}

// 🔴 THE PROPERTY THE WHOLE DESIGN EXISTS FOR: a renewed leaf still verifies against
// the authority the services already trust.
//
// Every service builds its trust pool from the CA in the configuration document it
// was started with. If a renewal changed the authority, that pool would reject the
// broker's new certificate while a pool built from the NEW authority would reject the
// leaf the broker is still serving — there is no ordering of that change which is not
// an outage. Keeping the authority is what turns a rotation into a renewal, and this
// is the assertion that says it worked.
func TestARenewedCertificateStillVerifiesAgainstTheAuthorityTheServicesTrust(t *testing.T) {
	// Minted eleven months ago, so it is inside its final thirty days.
	c, st := anInstanceWithBrokerMaterialMintedAt(t, time.Now().UTC().Add(-335*24*time.Hour), 1)
	trusted := x509.NewCertPool()
	if !trusted.AppendCertsFromPEM([]byte(st.NATSTLS.CACertPEM)) {
		t.Fatal("the authority the services would have been given is not a usable certificate")
	}
	before := brokerLeaf(t, c)

	if _, err := renewBrokerCertificate(context.Background(), c, st); err != nil {
		t.Fatalf("renewing the broker's certificate: %v", err)
	}
	settleStringDataLikeAnAPIServer(t, c)

	after := brokerLeaf(t, c)
	if after.SerialNumber.Cmp(before.SerialNumber) == 0 {
		t.Fatal("the certificate was not replaced, so the renewal did nothing")
	}
	if !after.NotAfter.After(before.NotAfter) {
		t.Error("the replacement expires no later than the certificate it replaced")
	}
	if _, err := after.Verify(x509.VerifyOptions{
		Roots:     trusted,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Errorf("the renewed certificate does not verify against the authority every service "+
			"was given: %v. Every service would reject the broker", err)
	}
}

// A certificate with most of its life left is left alone — and so is the broker. A
// renewal that fired on every upgrade would restart the broker on every upgrade.
func TestACertificateWellInsideItsLifeIsLeftAlone(t *testing.T) {
	c, st := anInstanceWithBrokerMaterialMintedAt(t, time.Now().UTC(), 1)
	before := brokerLeaf(t, c)

	reported, err := renewBrokerCertificate(context.Background(), c, st)
	if err != nil {
		t.Fatalf("checking a healthy certificate: %v", err)
	}
	if reported {
		t.Error("nothing was restarted but renewBrokerCertificate reported restarted=true")
	}
	settleStringDataLikeAnAPIServer(t, c)

	if after := brokerLeaf(t, c); after.SerialNumber.Cmp(before.SerialNumber) != 0 {
		t.Error("a certificate with most of its life left was replaced anyway, which restarts " +
			"the broker on every upgrade")
	}
	for _, a := range c.Actions() {
		if a.GetVerb() == "patch" && a.GetResource().Resource == "statefulsets" {
			t.Error("the broker was restarted for a certificate that did not need replacing")
		}
	}
}

// 🔴 THE READ IS THE LEAF'S EXPIRY, NOT THE AUTHORITY'S, AND THE TWO SIT IN ONE
// BUNDLE. tls.crt carries the leaf FOLLOWED BY the authority, because a client that
// trusts only the authority needs the chain presented to it. The leaf is good for a
// year and the authority for ten — so a reader that reached for the wrong block would
// conclude that nothing ever needs renewing, and would keep concluding it until the
// broker stopped accepting connections.
func TestTheExpiryConsultedIsTheLeafsAndNotTheAuthoritys(t *testing.T) {
	material, err := mintNATSTLS(natsReleaseName, InstanceNamespace(testInstance), 1,
		time.Now().UTC().Add(-335*24*time.Hour))
	if err != nil {
		t.Fatalf("minting: %v", err)
	}
	bundle := material.LeafCertPEM + material.CACertPEM

	reason, err := leafReissueReason(bundle, natsServerDNSNames(natsReleaseName, InstanceNamespace(testInstance), 1),
		time.Now().UTC())
	if err != nil {
		t.Fatalf("judging the certificate: %v", err)
	}
	// 🔴 THE REASON, NOT MERELY THAT THERE IS ONE. Asserting only that something came
	// back passes under the exact mutant this test exists to catch: a reader taking
	// the LAST block gets the authority, which carries no server names at all, so the
	// name check fires and returns a reason — for entirely the wrong cause. A reason
	// that says "expires on" is the only one that means the leaf was the certificate
	// consulted.
	if !strings.Contains(reason, "expires on") {
		t.Errorf("a leaf inside its last thirty days produced %q rather than an expiry: reading "+
			"the ten-year authority out of the same bundle looks exactly like this", reason)
	}

	// ...and the authority itself, read alone, is NOT near expiry — so the assertion
	// above cannot be passing because everything looks expiring.
	if r, err := leafReissueReason(material.CACertPEM, nil, time.Now().UTC()); err != nil || r != "" {
		t.Errorf("the ten-year authority was judged as needing renewal (%q, %v), so the test "+
			"above proves nothing", r, err)
	}
}

// 🔴 EXPIRY IS NOT THE ONLY REASON. The names a leaf covers are a function of the
// server count — one replica needs five, three need seventeen, because each server
// dials its peers by pod name. Scaling up invalidates a certificate that is still
// comfortably in date: the routes never form, no leader is elected, and it reads as a
// broken cluster rather than as a certificate a few names short.
func TestACertificateThatNoLongerCoversTheClusterIsReissued(t *testing.T) {
	material, err := mintNATSTLS(natsReleaseName, InstanceNamespace(testInstance), 1, time.Now().UTC())
	if err != nil {
		t.Fatalf("minting: %v", err)
	}

	reason, err := leafReissueReason(material.LeafCertPEM,
		natsServerDNSNames(natsReleaseName, InstanceNamespace(testInstance), 3), time.Now().UTC())
	if err != nil {
		t.Fatalf("judging the certificate: %v", err)
	}
	if reason == "" {
		t.Fatal("a single-server certificate was judged sufficient for a three-server cluster, " +
			"so the routes would never form and nothing would say why")
	}
	if !strings.Contains(reason, "dial each other by") {
		t.Errorf("the reason does not say what is actually wrong: %q", reason)
	}
}

// ...and the other direction is NOT a reason. A certificate covering more names than
// the topology needs is what an instance scaled back down looks like, and re-issuing
// for it would restart the broker to remove names nothing was using.
func TestACertificateCoveringMoreThanIsNeededIsLeftAlone(t *testing.T) {
	material, err := mintNATSTLS(natsReleaseName, InstanceNamespace(testInstance), 3, time.Now().UTC())
	if err != nil {
		t.Fatalf("minting: %v", err)
	}

	reason, err := leafReissueReason(material.LeafCertPEM,
		natsServerDNSNames(natsReleaseName, InstanceNamespace(testInstance), 1), time.Now().UTC())
	if err != nil {
		t.Fatalf("judging the certificate: %v", err)
	}
	if reason != "" {
		t.Errorf("a certificate covering more names than needed was replaced: %q", reason)
	}
}

// An instance built before the authority was kept cannot have its certificate
// re-issued, and that must be SAID rather than either failed or silently skipped.
// Failing would make a version bump impossible for that whole population; saying
// nothing would let the certificate lapse on an instance whose operator was told the
// upgrade succeeded.
func TestAnInstanceWithNoStoredAuthorityIsToldRatherThanFailed(t *testing.T) {
	c, st := anInstanceWithBrokerMaterialMintedAt(t, time.Now().UTC().Add(-335*24*time.Hour), 1)
	if err := c.CoreV1().Secrets(InstanceNamespace(testInstance)).Delete(context.Background(),
		natsAuthoritySecretName, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("removing the stored authority: %v", err)
	}
	before := brokerLeaf(t, c)

	if _, err := renewBrokerCertificate(context.Background(), c, st); err != nil {
		t.Fatalf("an upgrade failed because a certificate could not be renewed: %v", err)
	}
	settleStringDataLikeAnAPIServer(t, c)

	if after := brokerLeaf(t, c); after.SerialNumber.Cmp(before.SerialNumber) != 0 {
		t.Error("a certificate was issued under an authority that is not the one the services " +
			"trust, which no service would accept")
	}
}

// 🔴 THE AUTHORITY'S PRIVATE KEY GOES WHERE THE BROKER DOES NOT MOUNT IT. The broker
// needs its leaf and the CA certificate and has no use for the key that signs them;
// keeping them in one object would hand the ability to mint trusted certificates to
// anything that can read the broker's volume.
func TestTheStoredAuthorityIsNotTheObjectTheBrokerMounts(t *testing.T) {
	c, _ := anInstanceWithBrokerMaterialMintedAt(t, time.Now().UTC(), 1)

	if natsAuthoritySecretName == natsReleaseName+"-tls" {
		t.Fatal("the authority is kept in the object the broker mounts")
	}
	broker, err := c.CoreV1().Secrets(InstanceNamespace(testInstance)).Get(
		context.Background(), natsReleaseName+"-tls", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading the broker's Secret: %v", err)
	}
	authority, err := c.CoreV1().Secrets(InstanceNamespace(testInstance)).Get(
		context.Background(), natsAuthoritySecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading the stored authority: %v", err)
	}

	// The one value that must not be in both.
	caKey := string(authority.Data["tls.key"])
	if caKey == "" {
		t.Fatal("the authority's private key was not stored, so no certificate can ever be renewed")
	}
	for key, value := range broker.Data {
		if string(value) == caKey {
			t.Errorf("the authority's private key is in the broker's own Secret under %q", key)
		}
	}
}

// Stands in for the broker pods: each reports the expiry of whatever certificate the Secret holds
// at the moment it is asked, which is what a pod does once the kubelet has projected the Secret and
// the reloader has HUPed the server. Restored when the test ends.
func podsServeTheSecret(t *testing.T, c *fake.Clientset) {
	t.Helper()
	prev, prevTimeout, prevPoll := brokerServedNotAfter, brokerHotRenewTimeout, brokerHotRenewPoll
	brokerHotRenewTimeout, brokerHotRenewPoll = 2*time.Second, 5*time.Millisecond
	brokerServedNotAfter = func(ctx context.Context, _ kubernetes.Interface, ns, pod string) (time.Time, error) {
		settleStringDataLikeAnAPIServer(t, c)
		return brokerLeaf(t, c).NotAfter, nil
	}
	t.Cleanup(func() { brokerServedNotAfter, brokerHotRenewTimeout, brokerHotRenewPoll = prev, prevTimeout, prevPoll })
}

func statefulSetPatched(c *fake.Clientset) bool {
	for _, a := range c.Actions() {
		if a.GetVerb() == "patch" && a.GetResource().Resource == "statefulsets" {
			return true
		}
	}
	return false
}

// 🔴 A LEAF-ONLY RENEWAL MUST NOT ROLL THE BROKER. The broker hot-swaps its MQTT certificate on
// the reloader's SIGHUP when only the key material changed, keeping every session; a restart
// throws that away. The renewal waits for the pods to serve the new certificate instead.
func TestALeafOnlyRenewalDoesNotRestartTheBrokerAndVerifiesTheSwap(t *testing.T) {
	c, st := anInstanceWithBrokerMaterialMintedAt(t, time.Now().UTC().Add(-335*24*time.Hour), 3)
	podsServeTheSecret(t, c)
	probed := map[string]bool{}
	inner := brokerServedNotAfter
	brokerServedNotAfter = func(ctx context.Context, k kubernetes.Interface, ns, pod string) (time.Time, error) {
		probed[pod] = true
		return inner(ctx, k, ns, pod)
	}
	before := brokerLeaf(t, c)

	restarted, err := renewBrokerCertificate(context.Background(), c, st)
	if err != nil {
		t.Fatalf("renewing the broker's certificate: %v", err)
	}
	if restarted || statefulSetPatched(c) {
		t.Fatal("the broker was rolled for a renewal that changed only its leaf")
	}
	settleStringDataLikeAnAPIServer(t, c)
	if brokerLeaf(t, c).SerialNumber.Cmp(before.SerialNumber) == 0 {
		t.Fatal("the certificate was not replaced")
	}
	if len(probed) != 3 {
		t.Errorf("served certificate verified on %d of 3 pods: %v", len(probed), probed)
	}
}

// The authority changed under the leaf: the pods' CA pool would change, so the hot path would fall
// back to a full reload. Rolled, and the probe is never consulted.
func TestACertificateAuthorityChangeRestartsTheBroker(t *testing.T) {
	c, st := anInstanceWithBrokerMaterialMintedAt(t, time.Now().UTC().Add(-335*24*time.Hour), 1)
	podsServeTheSecret(t, c)
	brokerServedNotAfter = func(context.Context, kubernetes.Interface, string, string) (time.Time, error) {
		t.Error("the served certificate was probed although the CA changed")
		return time.Time{}, nil
	}
	secrets := c.CoreV1().Secrets(InstanceNamespace(testInstance))
	cur, err := secrets.Get(context.Background(), natsReleaseName+"-tls", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	other, err := mintNATSTLS(natsReleaseName, InstanceNamespace(testInstance), 1, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	cur.Data["ca.crt"] = []byte(other.CACertPEM)
	if _, err := secrets.Update(context.Background(), cur, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}

	restarted, err := renewBrokerCertificate(context.Background(), c, st)
	if err != nil {
		t.Fatal(err)
	}
	if !restarted || !statefulSetPatched(c) {
		t.Error("the broker was not restarted although the CA bundle it mounts changed")
	}
}

// A change to the broker's config (here the server count, which rewrites routes and the leaf's
// names) cannot be hot: the pods roll for it anyway.
func TestAConfigChangeRestartsTheBroker(t *testing.T) {
	c, st := anInstanceWithBrokerMaterialMintedAt(t, time.Now().UTC().Add(-335*24*time.Hour), 1)
	podsServeTheSecret(t, c)
	brokerServedNotAfter = func(context.Context, kubernetes.Interface, string, string) (time.Time, error) {
		t.Error("the served certificate was probed although the config changed")
		return time.Time{}, nil
	}
	st.HA = true // the instance is now scaled to three servers; the running StatefulSet has one.

	restarted, err := renewBrokerCertificate(context.Background(), c, st)
	if err != nil {
		t.Fatal(err)
	}
	if !restarted || !statefulSetPatched(c) {
		t.Error("the broker was not restarted although the topology changed")
	}
}

// 🔴 NEVER LEAVE THE OLD CERTIFICATE SERVING SILENTLY. A pod that has not switched within the
// bound means the swap is not coming (reloader not running, volume not projected, hot path
// refused): the renewal warns and rolls the broker.
func TestAnUnverifiedSwapFallsBackToARestart(t *testing.T) {
	c, st := anInstanceWithBrokerMaterialMintedAt(t, time.Now().UTC().Add(-335*24*time.Hour), 1)
	podsServeTheSecret(t, c)
	old := brokerLeaf(t, c).NotAfter
	brokerServedNotAfter = func(context.Context, kubernetes.Interface, string, string) (time.Time, error) {
		return old, nil // still the old certificate
	}
	brokerHotRenewTimeout = 100 * time.Millisecond

	restarted, err := renewBrokerCertificate(context.Background(), c, st)
	if err != nil {
		t.Fatal(err)
	}
	if !restarted || !statefulSetPatched(c) {
		t.Error("the broker kept serving the old certificate behind a green renewal")
	}
}
