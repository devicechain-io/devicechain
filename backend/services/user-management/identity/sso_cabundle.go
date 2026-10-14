// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/egress"
	"github.com/devicechain-io/dc-user-management/iam"
)

// A connection may carry a private CA bundle: extra trust anchors for the TLS certificate of
// THAT provider, for an on-premises provider whose certificate a private CA signed.
//
//   - THE BUNDLE IS DATA THE ADMINISTRATOR SUPPLIES, never an address to fetch it from: a
//     fetched trust root is a trust root that whoever can answer the fetch chooses.
//   - IT ADDS TO THE SYSTEM ROOTS, it does not replace them. A public provider keeps working
//     with a bundle set, and an administrator who pastes the wrong file does not break a
//     connection that never needed one. The bundle is per connection, so a private root
//     is trusted for that provider's connections and no other.
//   - IT CHANGES WHO CAN VOUCH FOR THE PROVIDER, so a new bundle must verify the provider
//     before it is saved, and a discovery document fetched under it that names different
//     endpoints withdraws the snapshot's approval (see SetIdpCaBundle).
//   - IT NEVER TOUCHES THE EGRESS GUARD. The client built from a bundle is the guard's
//     transport with different roots; which address may be dialed is decided at the dial,
//     before any TLS, so no certificate makes a forbidden destination reachable.

// ErrCaBundleInvalid carries the reason a bundle was refused. The message never contains
// the submitted text: a private key pasted by mistake must not travel into a log or an
// error response.
var ErrCaBundleInvalid = fmt.Errorf("%w: invalid CA bundle", ErrIdpInvalid)

const (
	// maxCaBundleBytes bounds the bundle as submitted.
	maxCaBundleBytes = 64 << 10
	// maxCaBundleCerts bounds the trust anchors in one bundle.
	maxCaBundleCerts = 64
)

// systemCertPool is the seam for loading the system roots; a test replaces it.
var systemCertPool = x509.SystemCertPool

func caInvalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrCaBundleInvalid, fmt.Sprintf(format, a...))
}

// CaCertificate describes one certificate of a bundle for display.
type CaCertificate struct {
	// FingerprintSHA256 is the hex SHA-256 of the DER certificate.
	FingerprintSHA256 string
	Subject           string
	Issuer            string
	NotBefore         time.Time
	NotAfter          time.Time
	// IsCA is whether the certificate asserts it is a certificate authority. A bundle may
	// carry a non-CA certificate (pinning a provider's own self-signed certificate is
	// legitimate), so this is shown, not required.
	IsCA bool
}

// ParseCaBundle validates a PEM bundle and returns its canonical form (the CERTIFICATE
// blocks re-encoded, with anything outside them dropped) and the certificates it holds. An
// empty bundle is valid and means "no private CA": it returns "" and no certificates.
//
// Refused: more than 64 KiB; a private key anywhere in the text; any PEM block that is not a
// CERTIFICATE or that carries headers; a block that does not parse as X.509; no certificate
// at all. Errors name the reason and never quote the input.
func ParseCaBundle(text string) (string, []CaCertificate, error) {
	if strings.TrimSpace(text) == "" {
		return "", nil, nil
	}
	if len(text) > maxCaBundleBytes {
		return "", nil, caInvalid("the bundle is larger than %d KiB", maxCaBundleBytes>>10)
	}
	// A private key is refused wherever it sits, including in a block too damaged to decode.
	if strings.Contains(strings.ToUpper(text), "PRIVATE KEY") {
		return "", nil, caInvalid("the text contains a private key; supply certificates only, and keep the key private")
	}
	var (
		out    []byte
		certs  []CaCertificate
		seen   = map[string]bool{}
		rest   = []byte(text)
		blocks int
	)
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		blocks++
		if block.Type != "CERTIFICATE" {
			return "", nil, caInvalid("only CERTIFICATE blocks are accepted")
		}
		if len(block.Headers) != 0 {
			return "", nil, caInvalid("a certificate block must not carry headers")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return "", nil, caInvalid("a certificate block is not a valid X.509 certificate")
		}
		sum := sha256.Sum256(cert.Raw)
		fp := hex.EncodeToString(sum[:])
		if seen[fp] {
			continue
		}
		seen[fp] = true
		if len(certs) >= maxCaBundleCerts {
			return "", nil, caInvalid("at most %d certificates", maxCaBundleCerts)
		}
		out = append(out, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})...)
		certs = append(certs, CaCertificate{
			FingerprintSHA256: fp,
			Subject:           cert.Subject.String(),
			Issuer:            cert.Issuer.String(),
			NotBefore:         cert.NotBefore.UTC(),
			NotAfter:          cert.NotAfter.UTC(),
			IsCA:              cert.BasicConstraintsValid && cert.IsCA,
		})
	}
	// A block too damaged to decode is skipped by the PEM reader, which would silently drop
	// a trust anchor the administrator meant to supply.
	if markers := strings.Count(text, "-----BEGIN "); markers != blocks {
		return "", nil, caInvalid("%d certificate block(s) could not be read; fix or remove them", markers-blocks)
	}
	if len(certs) == 0 {
		return "", nil, caInvalid("no PEM certificate was found (expected -----BEGIN CERTIFICATE----- blocks)")
	}
	return string(out), certs, nil
}

// caAuditLabel names the connection and the fingerprints a change added to and removed from
// its bundle, for the audit journal. The certificates themselves are not recorded.
func caAuditLabel(token, oldBundle, newBundle string) string {
	fps := func(b string) map[string]bool {
		out := map[string]bool{}
		_, certs, _ := ParseCaBundle(b)
		for _, c := range certs {
			out[c.FingerprintSHA256] = true
		}
		return out
	}
	oldSet, newSet := fps(oldBundle), fps(newBundle)
	var added, removed []string
	for f := range newSet {
		if !oldSet[f] {
			added = append(added, f)
		}
	}
	for f := range oldSet {
		if !newSet[f] {
			removed = append(removed, f)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return token + " added=[" + strings.Join(added, ",") + "] removed=[" + strings.Join(removed, ",") + "]"
}

// caPool is the system roots plus the (already canonical) bundle. If the system pool cannot
// be loaded the pool is the bundle alone, which trusts strictly less.
func caPool(bundle string) (*x509.CertPool, error) {
	pool, err := systemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM([]byte(bundle)) {
		return nil, caInvalid("the stored bundle holds no usable certificate")
	}
	return pool, nil
}

// SsoClientFactory builds an identity-provider HTTP client over the egress guard. A nil
// pool means the system roots; otherwise the pool is the connection's roots.
type SsoClientFactory func(roots *x509.CertPool) *http.Client

// NewSsoClientFactory is NewSsoHTTPClient's guard, kept so a client can be built per
// connection with that connection's trust anchors: same guard, same bounds, same refusal of
// redirects and proxies; only the TLS roots differ.
func NewSsoClientFactory(infra mscfg.InfrastructureConfiguration) (SsoClientFactory, error) {
	guard, err := egress.FromConfig(infra.Egress)
	if err != nil {
		return nil, err
	}
	return func(roots *x509.CertPool) *http.Client {
		tr := guard.Transport()
		if roots != nil {
			tr.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
		}
		return &http.Client{
			Transport: boundResponses(tr, maxIdpResponseBytes),
			Timeout:   discoveryTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}, nil
}

// SetSsoClientFactory installs the builder of per-connection clients. Without one a
// connection that carries a bundle REFUSES to reach its provider (it never falls back to
// the system roots, which would quietly ignore the bundle, nor to a client that skips the
// guard).
func (m *Manager) SetSsoClientFactory(f SsoClientFactory) { m.ssoClientFactory = f }

var errCaNoFactory = errors.New("no identity provider client factory is configured for connections with a private CA bundle")

// ssoClientCache holds one client per connection, valid for the exact connection row
// version it was built for: a change to the connection on any replica bumps updated_at and
// the next use on THIS replica builds a fresh client from the new bundle.
type ssoClientCache struct {
	mu      sync.Mutex
	entries map[uint]ssoClientEntry
}

type ssoClientEntry struct {
	updated time.Time
	bundle  string
	client  *http.Client
}

// ssoClientFor returns the client for a connection: the Manager's own (system roots) when
// the connection has no bundle, otherwise one trusting the bundle too.
func (m *Manager) ssoClientFor(conn *iam.IdpConnection) (*http.Client, error) {
	if m.ssoClient == nil {
		return nil, ErrDiscoveryNoClient
	}
	if conn.CaBundle == "" {
		return m.ssoClient, nil
	}
	c := &m.ssoClients
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[conn.ID]; ok && e.updated.Equal(conn.UpdatedAt) && e.bundle == conn.CaBundle {
		return e.client, nil
	}
	client, err := m.clientForBundle(conn.CaBundle)
	if err != nil {
		return nil, err
	}
	if c.entries == nil {
		c.entries = map[uint]ssoClientEntry{}
	}
	c.entries[conn.ID] = ssoClientEntry{updated: conn.UpdatedAt, bundle: conn.CaBundle, client: client}
	return client, nil
}

// clientForBundle builds an uncached client for a canonical bundle; "" is the Manager's own.
func (m *Manager) clientForBundle(bundle string) (*http.Client, error) {
	if m.ssoClient == nil {
		return nil, ErrDiscoveryNoClient
	}
	if bundle == "" {
		return m.ssoClient, nil
	}
	if m.ssoClientFactory == nil {
		return nil, errCaNoFactory
	}
	pool, err := caPool(bundle)
	if err != nil {
		return nil, err
	}
	return m.ssoClientFactory(pool), nil
}
