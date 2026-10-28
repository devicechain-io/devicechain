// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// Who signed the Gateway's certificate.
//
// 🔴 READY IS NOT "SIGNED BY THE INSTANCE CA". On an instance upgraded across the switch from a
// self-signed leaf to the CA chain, cert-manager signed the Gateway's Certificate against the
// Issuer's old spec while the same apply was turning that Issuer into the CA Issuer. The
// Certificate was Ready, and its Secret held a self-signed leaf with an empty subject and
// CA:FALSE, which a client built on OpenSSL cannot anchor. The chart now signs under a name that
// never held the old spec, so a fresh issue cannot come out that way; this reads the Secret
// itself, because a Ready condition describes the request and not the certificate it left
// behind, and an instance already in that state would otherwise pass every gate.

var secretGVR = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}

const certManagerIssuerAnnotation = "cert-manager.io/issuer"

// gatewayCertificateIssuerProblem judges a certificate Secret's contents. It returns "" when the
// certificate is acceptable and otherwise says what is wrong with it.
//
// Two rules, and the first is only the chart's own:
//   - expectCA (the Gateway asks a namespaced cert-manager Issuer, which is the chart's CA chain):
//     the leaf must not be self-signed. A leaf whose issuer is its own subject, and that is not a CA,
//     is the defect above.
//   - whenever the Secret carries ca.crt: the leaf must verify against it. That holds for every
//     issuer that publishes its CA there, and says nothing for one that does not.
//
// A Secret that cannot be read as a certificate is a problem, never a pass.
func gatewayCertificateIssuerProblem(secret string, tlsCrt, caCrt []byte, expectCA bool) string {
	leaf, err := firstCertificate(tlsCrt)
	if err != nil {
		return fmt.Sprintf("Secret %s holds no readable certificate (%v)", secret, err)
	}
	if expectCA && !leaf.IsCA && bytes.Equal(leaf.RawIssuer, leaf.RawSubject) {
		return fmt.Sprintf("the certificate in Secret %s is self-signed (its issuer is its own subject, and it is not a CA), "+
			"not signed by the instance's CA, so a client built on OpenSSL cannot anchor it", secret)
	}
	if len(bytes.TrimSpace(caCrt)) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caCrt) {
			return fmt.Sprintf("Secret %s has a ca.crt that holds no readable certificate", secret)
		}
		// The time is the leaf's own start: whether it has expired is not the question here.
		if _, err := leaf.Verify(x509.VerifyOptions{
			Roots: pool, Intermediates: pool, CurrentTime: leaf.NotBefore.Add(time.Second),
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
		}); err != nil {
			return fmt.Sprintf("the certificate in Secret %s was not signed by the ca.crt beside it (%v)", secret, err)
		}
	}
	return ""
}

func firstCertificate(bundle []byte) (*x509.Certificate, error) {
	for {
		var block *pem.Block
		block, bundle = pem.Decode(bundle)
		if block == nil {
			return nil, fmt.Errorf("no CERTIFICATE block")
		}
		if block.Type == "CERTIFICATE" {
			return x509.ParseCertificate(block.Bytes)
		}
	}
}

// gatewayCertificateIssued reads the Secret a Ready Certificate left and judges who signed it.
// ok is false, with the reason, for a certificate that should not be served; err is for a
// cluster that could not be read.
func gatewayCertificateIssued(ctx context.Context, dyn dynamic.Interface, g *unstructured.Unstructured, secret string) (ok bool, why string, err error) {
	ns := g.GetNamespace()
	obj, err := dyn.Resource(secretGVR).Namespace(ns).Get(ctx, secret, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, fmt.Sprintf("Secret %s does not exist, although its Certificate reports Ready", secret), nil
	}
	if err != nil {
		return false, "", fmt.Errorf("reading Secret %s/%s: %w", ns, secret, err)
	}
	read := func(key string) []byte {
		enc, _, _ := unstructured.NestedString(obj.Object, "data", key)
		raw, derr := base64.StdEncoding.DecodeString(enc)
		if derr != nil {
			return nil
		}
		return raw
	}
	_, expectCA := g.GetAnnotations()[certManagerIssuerAnnotation]
	if p := gatewayCertificateIssuerProblem(secret, read("tls.crt"), read("ca.crt"), expectCA); p != "" {
		return false, p + fmt.Sprintf(". Delete the Secret (kubectl -n %s delete secret %s) and cert-manager issues it again "+
			"from the instance's CA", ns, secret), nil
	}
	return true, "", nil
}
