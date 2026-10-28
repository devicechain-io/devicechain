// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// issuedMaterial is the contents of a certificate Secret.
type issuedMaterial struct{ tlsCrt, caCrt []byte }

func pemOf(der []byte) []byte { return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}) }

func mustKey() *ecdsa.PrivateKey {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	return k
}

// caSignedMaterial is what the chart's CA chain issues: a leaf with a subject, signed by a CA
// whose certificate is the Secret's ca.crt.
func caSignedMaterial() issuedMaterial {
	caKey, leafKey := mustKey(), mustKey()
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "prod DeviceChain CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		panic(err)
	}
	caCert, _ := x509.ParseCertificate(caDER)
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "prod.example.com"}, DNSNames: []string{"prod.example.com"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		panic(err)
	}
	return issuedMaterial{tlsCrt: pemOf(leafDER), caCrt: pemOf(caDER)}
}

// staleMaterial is the defect: a self-signed leaf with an empty subject and CA:FALSE, as a
// SelfSigned Issuer signs it, and a ca.crt that is the leaf itself.
func staleMaterial() issuedMaterial {
	k := mustKey()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(3), DNSNames: []string{"prod.example.com"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		panic(err)
	}
	return issuedMaterial{tlsCrt: pemOf(der), caCrt: pemOf(der)}
}

// goodIssuedMaterial is one shared CA-signed leaf, so a fake cluster is cheap to build.
var goodIssuedMaterial = sync.OnceValue(caSignedMaterial)

func issuedSecret(ns, name string, m issuedMaterial) *unstructured.Unstructured {
	enc := func(b []byte) interface{} { return base64.StdEncoding.EncodeToString(b) }
	data := map[string]interface{}{"tls.crt": enc(m.tlsCrt)}
	if m.caCrt != nil {
		data["ca.crt"] = enc(m.caCrt)
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]interface{}{"name": name, "namespace": ns},
		"data":     data,
	}}
}

func TestGatewayCertificateIssuerProblem(t *testing.T) {
	good, stale := caSignedMaterial(), staleMaterial()
	other := caSignedMaterial() // a different CA: its ca.crt did not sign good's leaf

	for _, c := range []struct {
		name     string
		m        issuedMaterial
		expectCA bool
		want     string // a substring of the problem, or "" for acceptable
	}{
		{"signed by the CA beside it", good, true, ""},
		{"signed by the CA, the Gateway asking no namespaced Issuer", good, false, ""},
		{"self-signed under the chart's CA chain", stale, true, "self-signed"},
		{"self-signed leaf with no ca.crt under the chart's CA chain", issuedMaterial{tlsCrt: stale.tlsCrt}, true, "self-signed"},
		{"self-signed from the operator's own ClusterIssuer, no ca.crt", issuedMaterial{tlsCrt: stale.tlsCrt}, false, ""},
		{"a leaf its ca.crt did not sign", issuedMaterial{tlsCrt: good.tlsCrt, caCrt: other.caCrt}, false, "not signed by the ca.crt"},
		{"a leaf with no ca.crt at all (an ACME issuer)", issuedMaterial{tlsCrt: good.tlsCrt}, false, ""},
		{"not a certificate", issuedMaterial{tlsCrt: []byte("nope")}, true, "no readable certificate"},
		{"an empty Secret", issuedMaterial{}, false, "no readable certificate"},
		{"an unreadable ca.crt", issuedMaterial{tlsCrt: good.tlsCrt, caCrt: []byte("junk")}, false, "ca.crt that holds no readable"},
	} {
		got := gatewayCertificateIssuerProblem("devicechain-tls-gateway", c.m.tlsCrt, c.m.caCrt, c.expectCA)
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%s: problem %q, want one containing %q", c.name, got, c.want)
		}
	}
}

// 🔴 THE REPORTED DEFECT, THROUGH THE REAL READS. A Certificate that is Ready and controlled by
// the Gateway, whose Secret holds a self-signed leaf, passed the gate before it looked at the
// Secret. It must now refuse --drop-legacy-ingress, say why and what to do, and accept the same
// cluster once the Secret holds a certificate the CA signed.
func TestAReadyCertificateSignedByTheWrongIssuerIsNotUsable(t *testing.T) {
	const secret = "devicechain-tls-gateway"
	gw := gwObj("dci-prod", map[string]string{"cert-manager.io/issuer": "devicechain-selfsigned-signer"}, secret)
	ready := cert("gw", secret, true, "True")

	stale := issuedSecret("dci-prod", secret, staleMaterial())
	vs, err := gatewayCertificatesFrom(t.Context(), newFakeCertDynamic(gw, &ready, stale), "prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 || vs[0].Ready || !strings.Contains(vs[0].Why, "self-signed") ||
		!strings.Contains(vs[0].Why, "kubectl -n dci-prod delete secret "+secret) {
		t.Fatalf("a Ready Certificate over a self-signed leaf was accepted, or refused without the fix: %+v", vs)
	}
	err = refuseUnlessGatewayCertificatesReady(vs, true, "--drop-legacy-ingress is refused", "remedy")
	if err == nil || !strings.Contains(err.Error(), "self-signed") {
		t.Fatalf("the drop gate did not refuse: %v", err)
	}

	// The wait that follows an upgrade says the same, rather than reporting the Gateway ready.
	prevT, prevP, prevL := gatewayCertTimeout, gatewayCertPoll, gatewayCertLegacyTimeout
	gatewayCertTimeout, gatewayCertPoll, gatewayCertLegacyTimeout = 50*time.Millisecond, 5*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { gatewayCertTimeout, gatewayCertPoll, gatewayCertLegacyTimeout = prevT, prevP, prevL })
	st := compactState(false)
	st.Instance, st.IngressController, st.GatewayTLSSecret = "prod", frontEndGateway, secret
	st.LegacyIngress = true
	out := captureStdout(t, func() {
		err = waitForGatewayCertificate(t.Context(), newFakeCertDynamic(gw, &ready, stale), st)
	})
	if err != nil || !strings.Contains(out, "self-signed") {
		t.Fatalf("the window's wait should warn naming the issuer (err=%v):\n%s", err, out)
	}
	st.LegacyIngress = false
	if err = waitForGatewayCertificate(t.Context(), newFakeCertDynamic(gw, &ready, stale), st); err == nil ||
		!strings.Contains(err.Error(), "self-signed") {
		t.Fatalf("outside the window the wait must fail naming the issuer: %v", err)
	}

	// The counterweight: the same objects with a CA-signed Secret pass.
	okSecret := issuedSecret("dci-prod", secret, caSignedMaterial())
	vs, err = gatewayCertificatesFrom(t.Context(), newFakeCertDynamic(gw, &ready, okSecret), "prod")
	if err != nil || len(vs) != 1 || !vs[0].Ready {
		t.Fatalf("a CA-signed certificate was refused: %+v %v", vs, err)
	}
}

func TestAReadyCertificateWithNoSecretIsNotUsable(t *testing.T) {
	const secret = "devicechain-tls-gateway"
	gw := gwObj("dci-prod", map[string]string{"cert-manager.io/issuer": "x-signer"}, secret)
	ready := cert("gw", secret, true, "True")
	dyn := newFakeCertDynamic(gw)
	if _, err := dyn.Resource(certificateGVR).Namespace("dci-prod").Create(t.Context(), &ready, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	vs, err := gatewayCertificatesFrom(t.Context(), dyn, "prod")
	if err != nil || len(vs) != 1 || vs[0].Ready || !strings.Contains(vs[0].Why, "does not exist") {
		t.Fatalf("a Ready Certificate whose Secret is missing passed: %+v %v", vs, err)
	}
}
