// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package smoke

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// issued is a certificate and the key it was issued for.
type issued struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newKey() (*ecdsa.PrivateKey, error) { return ecdsa.GenerateKey(elliptic.P256(), rand.Reader) }

func serial() (*big.Int, error) { return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120)) }

// issue signs a certificate for tmpl. parent nil means self-signed.
func issue(tmpl *x509.Certificate, parent *issued) (*issued, error) {
	key, err := newKey()
	if err != nil {
		return nil, err
	}
	if tmpl.SerialNumber, err = serial(); err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl.NotBefore, tmpl.NotAfter = now.Add(-time.Hour), now.Add(24*time.Hour)
	signerCert, signerKey := tmpl, key
	if parent != nil {
		signerCert, signerKey = parent.cert, parent.key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signerCert, &key.PublicKey, signerKey)
	if err != nil {
		return nil, err
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &issued{cert: c, key: key}, nil
}

func caTemplate(cn string) *x509.Certificate {
	return &x509.Certificate{
		Subject:               pkix.Name{CommonName: cn},
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
}

func certPEM(c *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})
}

func keyPEM(k *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// writePair writes <name>.crt (the chain, leaf first) and <name>.key.
func writePair(dir, name string, key *ecdsa.PrivateKey, chain ...*x509.Certificate) error {
	var buf []byte
	for _, c := range chain {
		buf = append(buf, certPEM(c)...)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".crt"), buf, 0o644); err != nil {
		return err
	}
	kp, err := keyPEM(key)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name+".key"), kp, 0o644)
}

// mintPKI writes everything the broker and the clients need:
//
//	server-ca.crt              the broker's CA, which clients trust
//	server.crt / server.key    the broker's leaf, valid for the given names
//	device.crt / device.key    a device leaf (clientAuth EKU) from a separate device CA
//	big.crt / big.key          a device leaf with intermediates padded to bigChainBytes of DER
//	wrongkey.crt / wrongkey.key  device.crt paired with a key it was NOT issued for
//
// The device CA is never given to the broker: under the request-only mode nothing is verified
// there, which is the point.
func mintPKI(dir string, sans []string, bigChainBytes int) error {
	serverCA, err := issue(caTemplate("brokersmoke server CA"), nil)
	if err != nil {
		return err
	}
	st := &x509.Certificate{
		Subject:     pkix.Name{CommonName: "brokersmoke server"},
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, n := range sans {
		if ip := net.ParseIP(n); ip != nil {
			st.IPAddresses = append(st.IPAddresses, ip)
		} else {
			st.DNSNames = append(st.DNSNames, n)
		}
	}
	srv, err := issue(st, serverCA)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "server-ca.crt"), certPEM(serverCA.cert), 0o644); err != nil {
		return err
	}
	if err := writePair(dir, "server", srv.key, srv.cert); err != nil {
		return err
	}
	// The CA key lets RotateServerCert re-issue the broker's leaf under the same CA, as a
	// cert-manager renewal does. Test material only; it is never referenced by nats.conf.
	if kp, err := keyPEM(serverCA.key); err != nil {
		return err
	} else if err := os.WriteFile(filepath.Join(dir, "server-ca.key"), kp, 0o600); err != nil {
		return err
	}

	deviceCA, err := issue(caTemplate("brokersmoke device CA"), nil)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "device-ca.crt"), certPEM(deviceCA.cert), 0o644); err != nil {
		return err
	}
	dev, err := issue(deviceLeaf("sensor-0001"), deviceCA)
	if err != nil {
		return err
	}
	if err := writePair(dir, "device", dev.key, dev.cert); err != nil {
		return err
	}
	other, err := newKey()
	if err != nil {
		return err
	}
	if err := writePair(dir, "wrongkey", other, dev.cert); err != nil {
		return err
	}

	// The large chain: intermediates whose subjects carry padding until the DER of the whole
	// presented chain (leaf + intermediates) reaches bigChainBytes. Four intermediates, the most
	// the device path accepts, each signed by the one above it.
	chain, leaf, err := paddedChain(deviceCA, bigChainBytes)
	if err != nil {
		return err
	}
	return writePair(dir, "big", leaf.key, chain...)
}

func deviceLeaf(cn string) *x509.Certificate {
	return &x509.Certificate{
		Subject:     pkix.Name{CommonName: cn},
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
}

// paddedChain returns [leaf, int4, int3, int2, int1] (leaf first, as TLS sends it) with a total DER
// size of at least target bytes, by padding each intermediate's subject with OrganizationalUnit
// values.
func paddedChain(root *issued, target int) ([]*x509.Certificate, *issued, error) {
	const intermediates = 4
	per := target / (intermediates + 1)
	parent := root
	var ints []*x509.Certificate
	for i := 0; i < intermediates; i++ {
		t := caTemplate(fmt.Sprintf("brokersmoke intermediate %d", i+1))
		t.Subject.OrganizationalUnit = padding(per)
		c, err := issue(t, parent)
		if err != nil {
			return nil, nil, err
		}
		ints = append(ints, c.cert)
		parent = c
	}
	lt := deviceLeaf("sensor-big")
	lt.Subject.OrganizationalUnit = padding(per)
	leaf, err := issue(lt, parent)
	if err != nil {
		return nil, nil, err
	}
	chain := []*x509.Certificate{leaf.cert}
	for i := len(ints) - 1; i >= 0; i-- {
		chain = append(chain, ints[i])
	}
	return chain, leaf, nil
}

// padding is enough 60-byte OU values to make roughly n bytes of subject. A certificate's subject
// appears twice in the next one down (as its issuer), so this slightly overshoots, which is the
// safe direction for a size test.
func padding(n int) []string {
	var out []string
	for size := 0; size < n/2; size += 64 {
		out = append(out, strings.Repeat("p", 60))
	}
	return out
}

// RotateServerCert re-issues the broker's leaf (same names, new key, new serial) under the same CA
// and rewrites server.crt and server.key in place, the way a cert-manager renewal rewrites the
// mounted Secret. It returns the new serial in hex. Clients that trust server-ca.crt keep trusting.
func RotateServerCert(dir string) (string, error) {
	caPEM, err := os.ReadFile(filepath.Join(dir, "server-ca.crt"))
	if err != nil {
		return "", err
	}
	caBlk, _ := pem.Decode(caPEM)
	if caBlk == nil {
		return "", fmt.Errorf("server-ca.crt holds no certificate")
	}
	caCert, err := x509.ParseCertificate(caBlk.Bytes)
	if err != nil {
		return "", err
	}
	kb, err := os.ReadFile(filepath.Join(dir, "server-ca.key"))
	if err != nil {
		return "", err
	}
	kblk, _ := pem.Decode(kb)
	if kblk == nil {
		return "", fmt.Errorf("server-ca.key holds no PEM block")
	}
	k, err := x509.ParsePKCS8PrivateKey(kblk.Bytes)
	if err != nil {
		return "", err
	}
	ek, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return "", fmt.Errorf("server-ca.key is not an ECDSA key")
	}
	oldPEM, err := os.ReadFile(filepath.Join(dir, "server.crt"))
	if err != nil {
		return "", err
	}
	oldBlk, _ := pem.Decode(oldPEM)
	if oldBlk == nil {
		return "", fmt.Errorf("server.crt holds no certificate")
	}
	old, err := x509.ParseCertificate(oldBlk.Bytes)
	if err != nil {
		return "", err
	}
	tmpl := &x509.Certificate{
		Subject:     old.Subject,
		DNSNames:    old.DNSNames,
		IPAddresses: old.IPAddresses,
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leaf, err := issue(tmpl, &issued{cert: caCert, key: ek})
	if err != nil {
		return "", err
	}
	if err := writePair(dir, "server", leaf.key, leaf.cert); err != nil {
		return "", err
	}
	return leaf.cert.SerialNumber.Text(16), nil
}

// ServedSerial dials addr (a TLS listener), completes a handshake trusting dir's server CA, and
// returns the serial, in hex, of the leaf the listener presented.
func ServedSerial(dir, addr string) (string, error) {
	cfg, err := ClientTLS(dir, "")
	if err != nil {
		return "", err
	}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", addr, cfg)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	return conn.ConnectionState().PeerCertificates[0].SerialNumber.Text(16), nil
}
