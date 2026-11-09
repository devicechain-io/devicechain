// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package main is the OCSP responder hack/x509-rig.sh runs beside a real instance, so the
// platform's OCSP check of a certificate connect meets a live responder over the network.
//
// It answers RFC 6960 POST requests for ONE issuer. What it answers is chosen per request by the
// URL path, so a trust anchor's ocspResponderUrl selects the behaviour:
//
//	/ok        the status the status file names for the serial (good | revoked), and unknown for a
//	           serial it does not name. Signed by the issuer, thisUpdate a minute ago, nextUpdate an
//	           hour ahead: what a healthy CA's responder answers.
//	/wrongkey  the same status, signed by a key the issuer never certified (the signer certificate
//	           is embedded, as a delegated responder's would be). A planted defect: the answer must
//	           not be believed.
//	/stale     the same status, signed by the issuer, but thisUpdate three hours ago and nextUpdate
//	           an hour ago. A planted defect: the answer lapsed before it was sent.
//
// Every request is appended to the request log as one JSON line (path, serial, the status
// answered), BEFORE the response is written, so the rig can tell a connect the responder was
// asked about from one it never saw. The status file is read on every request, so the rig revokes
// a certificate by rewriting it, with no restart.
//
// It is a test instrument, not a responder to deploy: it holds the issuer's private key in a file
// and answers anyone who asks.
package main

import (
	"crypto"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ocsp"
)

// Status is what the status file may say about a serial.
type Status string

const (
	StatusGood    Status = "good"
	StatusRevoked Status = "revoked"
	StatusUnknown Status = "unknown"
)

// Responder answers OCSP requests for one issuer.
type Responder struct {
	Issuer     *x509.Certificate
	IssuerKey  crypto.Signer
	WrongCert  *x509.Certificate
	WrongKey   crypto.Signer
	StatusFile string
	// Log receives one JSON line per request. Nil logs nothing.
	Log io.Writer
	// Now is the clock; nil is time.Now.
	Now func() time.Time

	mu sync.Mutex
}

// LogLine is one request as the request log records it.
type LogLine struct {
	Time   string `json:"time"`
	Path   string `json:"path"`
	Serial string `json:"serial"`
	Status Status `json:"status"`
	Error  string `json:"error,omitempty"`
}

// SerialKey is how a serial is written in the status file and the log: lower-case hex, no
// leading zeros, no prefix.
func SerialKey(n *big.Int) string { return strings.ToLower(n.Text(16)) }

func (r *Responder) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// lookup reads the status file for one serial. A missing file names nothing; an unreadable or
// malformed one is an error, never "unknown": a typo in the rig's revocation must not read as a
// responder that simply does not know the certificate.
func (r *Responder) lookup(serial string) (Status, error) {
	raw, err := os.ReadFile(r.StatusFile)
	if errors.Is(err, os.ErrNotExist) {
		return StatusUnknown, nil
	}
	if err != nil {
		return "", err
	}
	var m map[string]Status
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", fmt.Errorf("status file %s: %w", r.StatusFile, err)
	}
	s, ok := m[serial]
	if !ok {
		return StatusUnknown, nil
	}
	switch s {
	case StatusGood, StatusRevoked, StatusUnknown:
		return s, nil
	}
	return "", fmt.Errorf("status file %s names %q for %s: want good, revoked or unknown", r.StatusFile, s, serial)
}

func (r *Responder) record(l LogLine) {
	if r.Log == nil {
		return
	}
	b, _ := json.Marshal(l)
	r.mu.Lock()
	defer r.mu.Unlock()
	_, _ = r.Log.Write(append(b, '\n'))
	if f, ok := r.Log.(interface{ Sync() error }); ok {
		_ = f.Sync()
	}
}

// Answer builds the DER response a path gives for a request. It is the whole of the responder's
// decision, separate from HTTP so the tests can read it directly.
func (r *Responder) Answer(path string, req *ocsp.Request) ([]byte, Status, error) {
	serial := SerialKey(req.SerialNumber)
	status, err := r.lookup(serial)
	if err != nil {
		return nil, "", err
	}
	now := r.now().UTC().Truncate(time.Second)
	tmpl := ocsp.Response{
		SerialNumber: req.SerialNumber,
		IssuerHash:   req.HashAlgorithm,
		ThisUpdate:   now.Add(-time.Minute),
		NextUpdate:   now.Add(time.Hour),
	}
	switch status {
	case StatusGood:
		tmpl.Status = ocsp.Good
	case StatusRevoked:
		tmpl.Status = ocsp.Revoked
		tmpl.RevokedAt = now.Add(-2 * time.Minute)
		tmpl.RevocationReason = ocsp.KeyCompromise
	default:
		tmpl.Status = ocsp.Unknown
	}
	signerCert, signerKey := r.Issuer, r.IssuerKey
	switch path {
	case "/ok":
	case "/wrongkey":
		if r.WrongCert == nil || r.WrongKey == nil {
			return nil, "", errors.New("/wrongkey needs -wrong-cert and -wrong-key")
		}
		signerCert, signerKey = r.WrongCert, r.WrongKey
		// Embedded, as a delegated responder's certificate is: the signature is valid for the key
		// it names, and only the issuer never certified that key.
		tmpl.Certificate = r.WrongCert
	case "/stale":
		tmpl.ThisUpdate = now.Add(-3 * time.Hour)
		tmpl.NextUpdate = now.Add(-time.Hour)
		if status == StatusRevoked {
			tmpl.RevokedAt = now.Add(-4 * time.Hour)
		}
	default:
		return nil, "", fmt.Errorf("no behaviour at path %q (want /ok, /wrongkey or /stale)", path)
	}
	der, err := ocsp.CreateResponse(r.Issuer, signerCert, tmpl, signerKey)
	if err != nil {
		return nil, "", err
	}
	return der, status, nil
}

// ServeHTTP answers a POST application/ocsp-request. A request it cannot answer is a 400 or a
// 500 and is logged with its error, so a broken fixture shows as such rather than as a quiet
// responder.
func (r *Responder) ServeHTTP(w http.ResponseWriter, hr *http.Request) {
	line := LogLine{Time: r.now().UTC().Format(time.RFC3339Nano), Path: hr.URL.Path}
	if hr.Method == http.MethodGet && hr.URL.Path == "/healthz" {
		_, _ = io.WriteString(w, "ok\n")
		return
	}
	if hr.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(hr.Body, 64<<10))
	if err != nil {
		line.Error = err.Error()
		r.record(line)
		http.Error(w, "read", http.StatusBadRequest)
		return
	}
	req, err := ocsp.ParseRequest(body)
	if err != nil {
		line.Error = "parse: " + err.Error()
		r.record(line)
		http.Error(w, "malformed request", http.StatusBadRequest)
		return
	}
	line.Serial = SerialKey(req.SerialNumber)
	der, status, err := r.Answer(hr.URL.Path, req)
	line.Status = status
	if err != nil {
		line.Error = err.Error()
		r.record(line)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	r.record(line)
	w.Header().Set("Content-Type", "application/ocsp-response")
	_, _ = w.Write(der)
}

// LoadCert reads the first certificate of a PEM file.
func LoadCert(path string) (*x509.Certificate, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	for {
		var b *pem.Block
		b, raw = pem.Decode(raw)
		if b == nil {
			return nil, fmt.Errorf("%s holds no certificate", path)
		}
		if b.Type == "CERTIFICATE" {
			return x509.ParseCertificate(b.Bytes)
		}
	}
}

// LoadKey reads a PKCS#8 (or SEC 1 EC) private key from a PEM file.
func LoadKey(path string) (crypto.Signer, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	for {
		var b *pem.Block
		b, raw = pem.Decode(raw)
		if b == nil {
			return nil, fmt.Errorf("%s holds no private key", path)
		}
		switch b.Type {
		case "PRIVATE KEY":
			k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
			if err != nil {
				return nil, err
			}
			s, ok := k.(crypto.Signer)
			if !ok {
				return nil, fmt.Errorf("%s: the key cannot sign", path)
			}
			return s, nil
		case "EC PRIVATE KEY":
			return x509.ParseECPrivateKey(b.Bytes)
		}
	}
}
