// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package x509sim is the simulator's X.509 (mTLS) device mode: it mints a throwaway test CA
// and device certificates, registers the CA and pinned credentials through the tenant GraphQL
// API, and connects to the MQTT gateway presenting those certificates.
//
// The certificates are the ones a real device PKI would produce as far as the platform's
// verifier (core/x509trust.VerifyClientChain) is concerned: ECDSA P-256, a clientAuth extended
// key usage, and a subject common name equal to the device's externalId. Nothing here is
// trusted by anything but the instance it is registered with.
package x509sim

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"time"
)

const (
	// DefaultCAValidity and DefaultDeviceValidity bound the throwaway PKI. A test rig is torn
	// down long before either lapses; expiry tests pass their own device validity.
	DefaultCAValidity     = 5 * 365 * 24 * time.Hour
	DefaultDeviceValidity = 365 * 24 * time.Hour

	// backdate puts notBefore slightly in the past so a certificate minted on a host whose clock
	// is a little ahead of the broker's is already valid there.
	backdate = 5 * time.Minute
)

// CA is a throwaway certificate authority: the certificate (public) and its signing key.
type CA struct {
	Cert *x509.Certificate
	Key  *ecdsa.PrivateKey
}

// Device is one minted device certificate and its key.
type Device struct {
	Cert *x509.Certificate
	Key  *ecdsa.PrivateKey
}

// NewCA mints a self-signed P-256 root that may sign certificates, valid for validity from now.
func NewCA(name string, now time.Time, validity time.Duration) (*CA, error) {
	if name == "" {
		return nil, errors.New("x509sim: a CA needs a name")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := newSerial()
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: name, Organization: []string{"dc-simulator test CA"}},
		NotBefore:             now.Add(-backdate),
		NotAfter:              now.Add(validity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &CA{Cert: cert, Key: key}, nil
}

// IssueDevice mints a device certificate signed by the CA: P-256, clientAuth only, not a CA,
// with the subject common name set to externalId (the identity the platform reads).
func (ca *CA) IssueDevice(externalId string, now time.Time, validity time.Duration) (*Device, error) {
	if externalId == "" {
		return nil, errors.New("x509sim: a device certificate needs an externalId for its CN")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := newSerial()
	if err != nil {
		return nil, err
	}
	notAfter := now.Add(validity)
	if notAfter.After(ca.Cert.NotAfter) {
		notAfter = ca.Cert.NotAfter
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: externalId},
		NotBefore:             now.Add(-backdate),
		NotAfter:              notAfter,
		BasicConstraintsValid: true,
		IsCA:                  false,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.Key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &Device{Cert: cert, Key: key}, nil
}

func newSerial() (*big.Int, error) {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, err
	}
	return n.Add(n, big.NewInt(1)), nil
}

// CertPEM is the certificate as one PEM block.
func CertPEM(c *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})
}

// KeyPEM is the private key as PKCS#8 PEM.
func KeyPEM(k *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return nil, fmt.Errorf("x509sim: marshal key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

func parseCertPEM(b []byte) (*x509.Certificate, error) {
	blk, _ := pem.Decode(b)
	if blk == nil || blk.Type != "CERTIFICATE" {
		return nil, errors.New("x509sim: no CERTIFICATE block")
	}
	return x509.ParseCertificate(blk.Bytes)
}

func parseKeyPEM(b []byte) (*ecdsa.PrivateKey, error) {
	blk, _ := pem.Decode(b)
	if blk == nil || blk.Type != "PRIVATE KEY" {
		return nil, errors.New("x509sim: no PRIVATE KEY block")
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, err
	}
	ec, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("x509sim: key is not ECDSA")
	}
	return ec, nil
}
