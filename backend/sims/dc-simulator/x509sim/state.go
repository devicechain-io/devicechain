// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package x509sim

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/x509trust"
)

// Kind says how a minted device is meant to authenticate.
type Kind string

const (
	// KindPinned devices are created, with a pinned X509_CERTIFICATE credential, by Register.
	KindPinned Kind = "pinned"
	// KindJIT devices are minted and NEVER registered: they exist so a just-in-time
	// registration path has certificates to present for devices the platform has not seen.
	KindJIT Kind = "jit"
)

// JITTokenPrefix is the prefix of the device token the platform derives from a certificate's CN
// on the registration path (core.DeriveDeviceToken(CN, JITTokenPrefix)).
const JITTokenPrefix = "pv-"

// Entry is one minted device in the state manifest. No secret lives here: the key is in KeyFile.
type Entry struct {
	Kind        Kind      `json:"kind"`
	ExternalId  string    `json:"externalId"`
	Token       string    `json:"token"`
	CertFile    string    `json:"certFile"`
	KeyFile     string    `json:"keyFile"`
	Fingerprint string    `json:"fingerprintSha256"`
	NotAfter    time.Time `json:"notAfter"`
}

// Manifest is the state dir's index. The CA private key is NOT listed or embedded: it is the
// single file ca.key.
type Manifest struct {
	AnchorToken string  `json:"anchorToken"`
	Devices     []Entry `json:"devices"`
}

const (
	caCertFile   = "ca.pem"
	caKeyFile    = "ca.key"
	manifestFile = "manifest.json"
	devicesDir   = "devices"
)

// State is a state directory: the CA, its manifest, and the device certificates.
type State struct {
	Dir      string
	CA       *CA
	Manifest Manifest
}

// DeviceToken is the token a device with this externalId carries: the one the platform derives
// from a certificate's CN on the registration path. A pinned device uses the same rule, so every
// device's MQTT client id is a pure function of its CN.
func DeviceToken(externalId string) string { return core.DeriveDeviceToken(externalId, JITTokenPrefix) }

// Options sizes a state dir.
type Options struct {
	// Prefix names the minted devices: <Prefix>pin-0001 and <Prefix>jit-0001 (the CN, so the
	// externalId).
	Prefix string
	// AnchorToken is the trust anchor's token when it is registered.
	AnchorToken string
	// Pinned and JIT are how many of each to have. An existing state dir is topped up, never
	// shrunk or re-keyed.
	Pinned, JIT int
	// DeviceValidity overrides DefaultDeviceValidity.
	DeviceValidity time.Duration
	Now            time.Time
}

// OpenOrMint loads the state dir, creating the CA when there is none, and mints whatever
// devices Options asks for beyond those already present.
func OpenOrMint(dir string, o Options) (*State, error) {
	if o.Pinned < 0 || o.JIT < 0 {
		return nil, errors.New("x509sim: device counts must not be negative")
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	if o.DeviceValidity == 0 {
		o.DeviceValidity = DefaultDeviceValidity
	}
	if err := os.MkdirAll(filepath.Join(dir, devicesDir), 0o700); err != nil {
		return nil, err
	}
	st := &State{Dir: dir}
	if _, err := os.Stat(filepath.Join(dir, caKeyFile)); err == nil {
		if err := st.load(); err != nil {
			return nil, err
		}
	} else if errors.Is(err, os.ErrNotExist) {
		ca, err := NewCA("dc-simulator test CA "+o.Now.UTC().Format("20060102T150405Z"), o.Now, DefaultCAValidity)
		if err != nil {
			return nil, err
		}
		st.CA = ca
		keyPEM, err := KeyPEM(ca.Key)
		if err != nil {
			return nil, err
		}
		if err := writeFile(filepath.Join(dir, caKeyFile), keyPEM, 0o600); err != nil {
			return nil, err
		}
		if err := writeFile(filepath.Join(dir, caCertFile), CertPEM(ca.Cert), 0o644); err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}
	if o.AnchorToken != "" {
		st.Manifest.AnchorToken = o.AnchorToken
	}
	if st.Manifest.AnchorToken == "" {
		st.Manifest.AnchorToken = "dc-sim-x509-ca"
	}
	for _, spec := range []struct {
		kind Kind
		n    int
		tag  string
	}{{KindPinned, o.Pinned, "pin"}, {KindJIT, o.JIT, "jit"}} {
		for i := st.count(spec.kind) + 1; i <= spec.n; i++ {
			if err := st.mint(spec.kind, fmt.Sprintf("%s%s-%04d", o.Prefix, spec.tag, i), o); err != nil {
				return nil, err
			}
		}
	}
	return st, st.save()
}

func (s *State) count(k Kind) int { return len(s.Entries(k)) }

func (s *State) mint(kind Kind, externalId string, o Options) error {
	for _, e := range s.Manifest.Devices {
		if e.ExternalId == externalId {
			return fmt.Errorf("x509sim: %q is already minted as %s: use another prefix or state dir", externalId, e.Kind)
		}
	}
	d, err := s.CA.IssueDevice(externalId, o.Now, o.DeviceValidity)
	if err != nil {
		return err
	}
	token := DeviceToken(externalId)
	keyPEM, err := KeyPEM(d.Key)
	if err != nil {
		return err
	}
	e := Entry{
		Kind: kind, ExternalId: externalId, Token: token,
		CertFile:    filepath.Join(devicesDir, token+".crt"),
		KeyFile:     filepath.Join(devicesDir, token+".key"),
		Fingerprint: x509trust.Fingerprint(d.Cert),
		NotAfter:    d.Cert.NotAfter,
	}
	if err := writeFile(filepath.Join(s.Dir, e.KeyFile), keyPEM, 0o600); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(s.Dir, e.CertFile), CertPEM(d.Cert), 0o644); err != nil {
		return err
	}
	s.Manifest.Devices = append(s.Manifest.Devices, e)
	return nil
}

func (s *State) load() error {
	keyPEM, err := os.ReadFile(filepath.Join(s.Dir, caKeyFile))
	if err != nil {
		return err
	}
	certPEM, err := os.ReadFile(filepath.Join(s.Dir, caCertFile))
	if err != nil {
		return err
	}
	key, err := parseKeyPEM(keyPEM)
	if err != nil {
		return fmt.Errorf("x509sim: %s: %w", caKeyFile, err)
	}
	cert, err := parseCertPEM(certPEM)
	if err != nil {
		return fmt.Errorf("x509sim: %s: %w", caCertFile, err)
	}
	s.CA = &CA{Cert: cert, Key: key}
	raw, err := os.ReadFile(filepath.Join(s.Dir, manifestFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, &s.Manifest)
}

func (s *State) save() error {
	raw, err := json.MarshalIndent(s.Manifest, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(s.Dir, manifestFile), append(raw, '\n'), 0o644)
}

// CAPEM is the CA certificate, the public text a trust anchor is created from.
func (s *State) CAPEM() string { return string(CertPEM(s.CA.Cert)) }

// Entries returns the manifest entries of one kind.
func (s *State) Entries(k Kind) []Entry {
	var out []Entry
	for _, e := range s.Manifest.Devices {
		if e.Kind == k {
			out = append(out, e)
		}
	}
	return out
}

// Pair loads an entry's certificate and key as a TLS client certificate.
func (s *State) Pair(e Entry) (tls.Certificate, error) {
	return tls.LoadX509KeyPair(filepath.Join(s.Dir, e.CertFile), filepath.Join(s.Dir, e.KeyFile))
}

// Leaf parses an entry's certificate.
func (s *State) Leaf(e Entry) (*x509.Certificate, error) {
	b, err := os.ReadFile(filepath.Join(s.Dir, e.CertFile))
	if err != nil {
		return nil, err
	}
	return parseCertPEM(b)
}

// writeFile writes with the given mode, then Chmods so a pre-existing looser file is tightened.
// Windows has no POSIX modes; there the bits are advisory and this is best effort.
func writeFile(path string, b []byte, mode os.FileMode) error {
	if err := os.WriteFile(path, b, mode); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}
