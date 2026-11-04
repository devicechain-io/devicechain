// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

// The broker's certificate authority and the one leaf it signs.
//
// 🔴 THIS MOVED HERE BECAUSE THE CA PRIVATE KEY WAS THE ONE CONFIRMED LEAK. Generated
// during the infrastructure apply, it was recorded in that tool's state file in
// cleartext, along with the leaf's key — six resources' worth of private key material
// sitting in a file beside the instance directory. Nothing else measured in that file
// turned out to be a credential; this was.
//
// 🔴 AND IT HAD TO MOVE FOR A SECOND REASON THAT IS NOT ABOUT SECRECY. The CA was an
// OUTPUT of the apply, consumed afterwards as a chart value — so the document every
// service reads could not be composed until the apply had run. "Entropy is minted
// once, before anything is applied" is not achievable while a value flows in that
// direction. Minting it here reverses the arrow: the apply receives the public half
// as an input, and the private half never reaches it.
const (
	natsReleaseName = "dc-nats"

	// Ten years for the authority, one for the leaf, matching what the
	// infrastructure module issued. The CA outliving the leaf by an order of
	// magnitude is the point: re-issuing a leaf is routine, and replacing the CA
	// means re-trusting it everywhere at once.
	natsCAValidity   = 10 * 365 * 24 * time.Hour
	natsLeafValidity = 365 * 24 * time.Hour

	natsCACommonName = "DeviceChain NATS CA"
	certOrganization = "The DeviceChain Authors"

	natsTLSKeyBits = 2048
)

// natsTLSMaterial is one authority and the single leaf it signs.
type natsTLSMaterial struct {
	// Namespace is the instance namespace the broker runs in — the one the leaf names,
	// and so the one its Secrets belong in.
	Namespace   string
	CACertPEM   string
	CAKeyPEM    string
	LeafCertPEM string
	LeafKeyPEM  string
}

// natsServerDNSNames lists every name a peer or a client may dial the broker by.
//
// 🔴 THE PER-NODE ROUTE NAMES ARE NOT OPTIONAL ABOVE ONE REPLICA, AND THEIR ABSENCE
// IS SILENT. With more than one server each dials its peers by pod name, and a
// certificate that does not cover those names fails that handshake — routes retry,
// the stream layer never elects a leader, and the symptom reads as a broken cluster
// rather than as a certificate that is a few names short. A single-server install
// never opens a route, so it cannot detect this at all.
//
// All four resolution depths are named because which one a server dials with is
// decided by the chart's own templating, not here. Naming all four costs nothing and
// removes the dependency.
func natsServerDNSNames(releaseName, namespace string, replicas int, extra ...string) []string {
	names := []string{
		releaseName,
		releaseName + "." + namespace,
		releaseName + "." + namespace + ".svc",
		releaseName + "." + namespace + ".svc.cluster.local",
		// In-pod tooling connects over the loopback name.
		"localhost",
	}
	// The names devices dial from OUTSIDE the cluster (--mqtt-host), when MQTT is exposed.
	names = append(names, extra...)
	if replicas <= 1 {
		return names
	}
	headless := releaseName + "-headless"
	for i := 0; i < replicas; i++ {
		pod := fmt.Sprintf("%s-%d.%s", releaseName, i, headless)
		names = append(names,
			pod,
			pod+"."+namespace,
			pod+"."+namespace+".svc",
			pod+"."+namespace+".svc.cluster.local",
		)
	}
	return names
}

// mintNATSTLS creates the authority and its leaf.
func mintNATSTLS(releaseName, namespace string, replicas int, now time.Time, extraNames ...string) (*natsTLSMaterial, error) {
	caKey, err := rsa.GenerateKey(rand.Reader, natsTLSKeyBits)
	if err != nil {
		return nil, fmt.Errorf("generating the broker certificate authority key: %w", err)
	}
	caSerial, err := certSerial()
	if err != nil {
		return nil, err
	}
	caTemplate := &x509.Certificate{
		SerialNumber: caSerial,
		Subject: pkix.Name{
			CommonName:   natsCACommonName,
			Organization: []string{certOrganization},
		},
		NotBefore:             now.Add(-certClockSkewAllowance),
		NotAfter:              now.Add(natsCAValidity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("self-signing the broker certificate authority: %w", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, fmt.Errorf("re-reading the certificate authority just created: %w", err)
	}

	leafCertPEM, leafKeyPEM, err := issueNATSLeaf(caCert, caKey, releaseName, namespace, replicas, now, extraNames...)
	if err != nil {
		return nil, err
	}

	return &natsTLSMaterial{
		Namespace:   namespace,
		CACertPEM:   encodePEM("CERTIFICATE", caDER),
		CAKeyPEM:    encodePEM("RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(caKey)),
		LeafCertPEM: leafCertPEM,
		LeafKeyPEM:  leafKeyPEM,
	}, nil
}

// settleNATSTLS is the broker's authority and leaf for a bootstrap run: the authority the instance
// already keeps, with a fresh leaf, else a newly minted pair.
//
// 🔴 A RE-RUN MUST NOT ROTATE THE AUTHORITY. Devices pin it (the instance CA is shipped in firmware
// and configuration), and the services trust it from the document they started with, so a new CA
// on a re-run silently breaks every pinned device while the run reports success. Only an instance
// with no authority of its own (a fresh namespace) gets a new one.
//
// An authority that EXISTS but cannot be read is an error, never a mint: "could not tell" must not
// resolve to the answer that cannot be undone. typed is nil in a dry run, which mints a
// throwaway pair it will never deploy.
func settleNATSTLS(ctx context.Context, typed kubernetes.Interface, st *State, now time.Time) (*natsTLSMaterial, error) {
	ns := InstanceNamespace(st.Instance)
	replicas := haFor(st.HA).ServerReplicas
	if typed == nil {
		return mintNATSTLS(natsReleaseName, ns, replicas, now, mqttBrokerNames(st)...)
	}
	caCert, caKey, err := readNATSAuthority(ctx, typed, st.Instance, st.InstanceUID)
	if err != nil {
		return nil, fmt.Errorf("the instance already has a broker authority that cannot be read, and minting a "+
			"new one would break every device that pins it: %w", err)
	}
	if caCert == nil {
		return mintNATSTLS(natsReleaseName, ns, replicas, now, mqttBrokerNames(st)...)
	}
	certPEM, keyPEM, err := issueNATSLeaf(caCert, caKey, natsReleaseName, ns, replicas, now, mqttBrokerNames(st)...)
	if err != nil {
		return nil, err
	}
	return &natsTLSMaterial{
		Namespace:   ns,
		CACertPEM:   encodePEM("CERTIFICATE", caCert.Raw),
		CAKeyPEM:    encodePEM("RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(caKey)),
		LeafCertPEM: certPEM,
		LeafKeyPEM:  keyPEM,
	}, nil
}

// issueNATSLeaf signs one server certificate under an authority.
//
// 🔴 SEPARATED SO RENEWAL AND MINTING CANNOT ISSUE DIFFERENT CERTIFICATES. A renewal
// that restated this template from memory is how a leaf comes back missing its client
// role, or a name — and both failures are invisible until they are not: the client
// role only matters above one replica, and a missing name only matters to whichever
// peer dials by it. Two issuers that must agree are one issuer with two callers.
func issueNATSLeaf(
	caCert *x509.Certificate,
	caKey *rsa.PrivateKey,
	releaseName, namespace string,
	replicas int,
	now time.Time,
	extraNames ...string,
) (certPEM, keyPEM string, err error) {
	leafKey, err := rsa.GenerateKey(rand.Reader, natsTLSKeyBits)
	if err != nil {
		return "", "", fmt.Errorf("generating the broker server key: %w", err)
	}
	leafSerial, err := certSerial()
	if err != nil {
		return "", "", err
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: leafSerial,
		Subject: pkix.Name{
			CommonName:   releaseName + "." + namespace,
			Organization: []string{certOrganization},
		},
		NotBefore: now.Add(-certClockSkewAllowance),
		NotAfter:  now.Add(natsLeafValidity),
		DNSNames:  natsServerDNSNames(releaseName, namespace, replicas, extraNames...),
		KeyUsage:  x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		// 🔴 BOTH ROLES, AND THE SECOND ONE IS THE ONE THAT IS EASY TO DROP. One leaf
		// plays both: on the client and messaging listeners the server presents it as
		// a server, and on the route listener, with peer verification on, each server
		// presents the SAME certificate as a CLIENT to its peers. Without the client
		// role that handshake is rejected and the cluster never forms — quietly, and
		// only above one replica.
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
			x509.ExtKeyUsageClientAuth,
		},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		return "", "", fmt.Errorf("signing the broker server certificate: %w", err)
	}

	return encodePEM("CERTIFICATE", leafDER),
		encodePEM("RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(leafKey)),
		nil
}

// certClockSkewAllowance backdates the start of validity.
//
// A certificate whose validity begins at the instant it is issued is not yet valid on
// any machine whose clock is a moment behind the one that issued it, and the pods
// that reject it do so at startup — which reads as a broken broker, minutes after a
// bootstrap that reported success.
const certClockSkewAllowance = 5 * time.Minute

// natsTLSSecret places the material where the broker's chart mounts it from.
//
// 🔴 tls.crt CARRIES THE LEAF FOLLOWED BY THE AUTHORITY, not the leaf alone. A client
// that trusts only the authority needs the chain presented to it; a leaf on its own
// verifies for anything holding the CA already and fails for everything else, which
// is a failure that depends on which client is asking.
func natsTLSSecret(releaseName string, m *natsTLSMaterial) ownedSecret {
	return ownedSecret{
		Name:      releaseName + "-tls",
		Namespace: m.Namespace,
		Type:      corev1.SecretTypeTLS,
		Labels: map[string]string{
			"app.kubernetes.io/name":      releaseName,
			"app.kubernetes.io/component": "broker",
		},
		Data: map[string]string{
			"tls.crt": m.LeafCertPEM + m.CACertPEM,
			"tls.key": m.LeafKeyPEM,
			"ca.crt":  m.CACertPEM,
		},
	}
}

func certSerial() (*big.Int, error) {
	// 127 bits, so the value is always positive — a negative serial is malformed and
	// some verifiers refuse it.
	limit := new(big.Int).Lsh(big.NewInt(1), 127)
	n, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("choosing a certificate serial number: %w", err)
	}
	return n, nil
}

func encodePEM(blockType string, der []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}))
}
