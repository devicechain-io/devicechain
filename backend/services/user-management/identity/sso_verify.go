// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/devicechain-io/dc-user-management/iam"
)

// Verifying an ID token.
//
// go-oidc checks the signature, the algorithm, the issuer, the audience and the expiry.
// It does NOT check the nonce, the authorized party (azp), a provider's tenant-pinning
// claims (hd, tid), the assurance methods (amr), or whether the provider vouches for the
// email address. Those are this file's, written by hand and each pinned by a test that
// fails when the check is removed: the library cannot be trusted with what it does not
// claim to do.

var (
	// errSsoToken is the single error every ID-token failure reduces to for the person at
	// the browser. The precise cause is logged; the caller learns only that the provider's
	// answer was not accepted.
	errSsoToken = errors.New("the identity provider's answer was not accepted")
)

// ssoSigningAlgs are the signature algorithms an ID token may use. "none" is not among
// them and neither is any HMAC algorithm, so a token cannot be signed with the client
// secret (which the provider and this service both know) or with nothing at all.
var ssoSigningAlgs = []string{"RS256", "ES256", "PS256"}

// maxIdpResponseBytes bounds every response read from an identity provider's token endpoint
// and key set. Real ones are a few KiB; the bound is what stops a hostile endpoint from
// streaming this process out of memory.
const maxIdpResponseBytes = 512 << 10

// ssoClaims is what a verified ID token vouches for.
type ssoClaims struct {
	Subject string
	// Email is canonical (iam.CanonicalEmail) and Domain is its normalised domain.
	Email  string
	Domain string
	// Verified is whether the PROVIDER vouches that the person controls Email: the
	// email_verified claim, or for Microsoft Entra (which does not send one) xms_edov.
	Verified bool
	Raw      map[string]any
}

// ssoKeySetCache holds one go-oidc key set per connection, so a provider's keys are fetched
// once and refreshed on an unknown key id rather than on every sign-in. An entry is valid
// only for the exact (connection row version, key-set URL) it was built for: a change to the
// connection, made on any replica, bumps updated_at and the next sign-in on THIS replica
// builds a fresh set.
type ssoKeySetCache struct {
	mu      sync.Mutex
	entries map[uint]ssoKeySetEntry
}

type ssoKeySetEntry struct {
	updated time.Time
	jwksURI string
	// caBundle is the bundle the entry's fetches trust; a changed bundle is a new entry.
	caBundle string
	keys     *oidc.RemoteKeySet
}

func (c *ssoKeySetCache) get(conn *iam.IdpConnection, build func() *oidc.RemoteKeySet) *oidc.RemoteKeySet {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[uint]ssoKeySetEntry{}
	}
	uri := conn.DiscoverySnapshot.JwksURI
	if e, ok := c.entries[conn.ID]; ok && e.updated.Equal(conn.UpdatedAt) && e.jwksURI == uri && e.caBundle == conn.CaBundle {
		return e.keys
	}
	ks := build()
	c.entries[conn.ID] = ssoKeySetEntry{updated: conn.UpdatedAt, jwksURI: uri, caBundle: conn.CaBundle, keys: ks}
	return ks
}

// ssoContext returns ctx carrying the connection's identity-provider HTTP client for both
// go-oidc and oauth2. Both libraries fall back to http.DefaultClient -- which dials
// anywhere -- when the context carries none, so a Manager with no client refuses here
// rather than reaching them.
func (m *Manager) ssoContext(ctx context.Context, conn *iam.IdpConnection) (context.Context, error) {
	client, err := m.ssoClientFor(conn)
	if err != nil {
		return nil, err
	}
	return oidc.ClientContext(ctx, client), nil
}

// VerifySsoIDToken verifies rawIDToken against a connection's snapshotted issuer and key-set
// URL, fetching keys through the Manager's egress-guarded client. It checks everything
// described at the top of this file except the connection's own claim requirements
// (checkSsoClaims), and returns the verified claims.
func (m *Manager) VerifySsoIDToken(ctx context.Context, conn *iam.IdpConnection, rawIDToken, nonce string) (*ssoClaims, error) {
	if conn.DiscoverySnapshot == nil {
		return nil, fmt.Errorf("%w: the connection has no discovery snapshot", errSsoToken)
	}
	snap := conn.DiscoverySnapshot
	cctx, err := m.ssoContext(ctx, conn)
	if err != nil {
		return nil, err
	}
	// The key set outlives this request (its keys are cached), so it is built over a context
	// that carries the client but no deadline of this request.
	keyCtx, err := m.ssoContext(context.Background(), conn)
	if err != nil {
		return nil, err
	}
	keys := m.ssoKeySets.get(conn, func() *oidc.RemoteKeySet { return oidc.NewRemoteKeySet(keyCtx, snap.JwksURI) })
	rec := &fetchRecordingKeySet{inner: keys}
	verifier := oidc.NewVerifier(snap.Issuer, rec, &oidc.Config{
		ClientID:             conn.ClientID,
		SupportedSigningAlgs: ssoSigningAlgs,
	})
	tok, err := verifier.Verify(cctx, rawIDToken)
	if err != nil {
		// A failure to REACH the key set is returned as such, so the caller can tell an
		// unreachable provider (an alert) from a token that was simply wrong. go-oidc's
		// Verify flattens its cause to text, which would lose an egress refusal, so the
		// key set is wrapped to keep the original error.
		if rec.fetchErr != nil {
			return nil, &idpFetchError{err: rec.fetchErr}
		}
		return nil, fmt.Errorf("%w: %w", errSsoToken, err)
	}

	// The issuer is pinned to the connection, not merely to whatever the library was handed:
	// a token's iss must be exactly the issuer this connection was created for.
	if tok.Issuer != conn.Issuer || tok.Issuer != snap.Issuer {
		return nil, fmt.Errorf("%w: issuer %q is not the connection's", errSsoToken, tok.Issuer)
	}

	var raw map[string]any
	if err := tok.Claims(&raw); err != nil {
		return nil, fmt.Errorf("%w: %v", errSsoToken, err)
	}

	// The nonce binds this ID token to the sign-in this browser started. An empty one on
	// either side is a failure, not a match. (An ordinary comparison: the nonce is not a secret
	// a caller presents for a verdict, it is a value inside a token whose signature has already
	// verified.)
	if nonce == "" || tok.Nonce == "" || tok.Nonce != nonce {
		return nil, fmt.Errorf("%w: nonce does not match", errSsoToken)
	}

	// The authorized party. A token addressed to several audiences MUST name its authorized
	// party (OpenID Connect Core 3.1.3.7), and when one is named it must be this client.
	azp, hasAzp := raw["azp"].(string)
	if _, present := raw["azp"]; present && !hasAzp {
		return nil, fmt.Errorf("%w: azp is not a string", errSsoToken)
	}
	if len(tok.Audience) > 1 && !hasAzp {
		return nil, fmt.Errorf("%w: several audiences and no azp", errSsoToken)
	}
	if hasAzp && azp != conn.ClientID {
		return nil, fmt.Errorf("%w: azp %q is not this client", errSsoToken, azp)
	}

	if tok.Subject == "" {
		return nil, fmt.Errorf("%w: no subject", errSsoToken)
	}
	return &ssoClaims{Subject: tok.Subject, Raw: raw}, nil
}

// bindEmail reads the email the connection says to read and decides whether the provider
// vouches for it. It is separate from verification because a sign-in whose subject is
// already linked does not depend on the email at all.
func bindEmail(conn *iam.IdpConnection, c *ssoClaims) error {
	name := conn.EmailClaim
	if name == "" {
		name = "email"
	}
	raw, _ := c.Raw[name].(string)
	email, err := iam.CanonicalEmail(raw)
	if err != nil {
		return fmt.Errorf("%w: no usable %s claim", errSsoToken, name)
	}
	domain, err := NormalizeDomain(iam.EmailDomain(email))
	if err != nil {
		return fmt.Errorf("%w: the %s claim has no usable domain", errSsoToken, name)
	}
	c.Email, c.Domain = email, domain
	c.Verified = providerVouchesForEmail(conn, c.Raw)
	return nil
}

// providerVouchesForEmail is whether the provider asserts the person controls the address.
// Microsoft Entra sends no email_verified: it sends xms_edov, and only when the application
// asks for it, so a token without it is NOT verified. There is no setting that trusts an
// unverified address.
func providerVouchesForEmail(conn *iam.IdpConnection, raw map[string]any) bool {
	if isEntraIssuer(conn.Issuer) {
		return claimIsTrue(raw["xms_edov"])
	}
	return claimIsTrue(raw["email_verified"])
}

// claimIsTrue accepts a JSON true or the string "true", which some providers send.
func claimIsTrue(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true"
	}
	return false
}

// isEntraIssuer reports whether issuer is a Microsoft Entra issuer.
func isEntraIssuer(issuer string) bool {
	u, err := url.Parse(issuer)
	if err != nil {
		return false
	}
	_, ok := microsoftIssuerHosts[strings.ToLower(u.Hostname())]
	return ok
}

// isGoogleIssuer reports whether issuer is Google's shared issuer.
func isGoogleIssuer(issuer string) bool {
	u, err := url.Parse(issuer)
	return err == nil && strings.EqualFold(u.Hostname(), "accounts.google.com")
}

// checkSsoClaims applies the connection's own requirements to a verified token: every
// required claim must be present and equal, an Entra token must be for the pinned tenant,
// a Google token must be for the Workspace domain the email is in, and when the connection
// requires assurance methods the token must report at least one of them.
//
// These hold for EVERY sign-in through the connection, linked or not: they say whose
// token this is, which is what stops a shared multi-tenant issuer from vouching for
// people who are not the customer's.
func checkSsoClaims(conn *iam.IdpConnection, c *ssoClaims) error {
	for name, want := range conn.RequiredClaims {
		got, ok := c.Raw[name].(string)
		if !ok || got != want {
			return fmt.Errorf("required claim %q is missing or different", name)
		}
	}
	if isGoogleIssuer(conn.Issuer) {
		// Google signs for every Google account, consumer ones registered on a corporate
		// address included. Only a Workspace account carries hd, and it must be the domain
		// of the address the sign-in rests on.
		hd, _ := c.Raw["hd"].(string)
		if c.Domain == "" || !strings.EqualFold(hd, c.Domain) {
			return errors.New("the Google account is not a Workspace account for the email's domain")
		}
	}
	if len(conn.RequiredAmr) > 0 {
		have, _ := c.Raw["amr"].([]any)
		ok := false
		for _, h := range have {
			s, _ := h.(string)
			for _, want := range conn.RequiredAmr {
				if s == want {
					ok = true
				}
			}
		}
		if !ok {
			return errors.New("the sign-in did not report a required authentication method")
		}
	}
	return nil
}

// idpFetchError is a failure to reach an identity provider's key set (a refused address, a
// timeout, an oversized or unreadable response), as opposed to a judgement of a token.
type idpFetchError struct{ err error }

func (e *idpFetchError) Error() string {
	return "the identity provider's key set could not be fetched: " + e.err.Error()
}
func (e *idpFetchError) Unwrap() error { return e.err }

// fetchRecordingKeySet wraps a key set and remembers a failure to fetch keys. go-oidc's
// RemoteKeySet reports that failure with a stable "fetching keys" prefix and a wrapped
// cause; its verifier then flattens the error to text, so the cause is captured here.
type fetchRecordingKeySet struct {
	inner    oidc.KeySet
	fetchErr error
}

func (k *fetchRecordingKeySet) VerifySignature(ctx context.Context, jwt string) ([]byte, error) {
	payload, err := k.inner.VerifySignature(ctx, jwt)
	if err != nil && strings.HasPrefix(err.Error(), "fetching keys") {
		k.fetchErr = err
	}
	return payload, err
}
