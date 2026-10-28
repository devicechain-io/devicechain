// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package x509trust

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"time"
)

const (
	// MaxPresentedCerts is the most certificates a device may present: the leaf and up to four
	// intermediates.
	MaxPresentedCerts = 5
	// MaxPresentedDERBytes bounds the presented chain's total DER size.
	MaxPresentedDERBytes = 16 << 10
	// NotBeforeSkew is how far in the future a leaf's notBefore may sit and still be accepted:
	// room for a freshly issued certificate on a device whose clock runs slightly behind the
	// issuer's. The leaf is then verified at its notBefore; notAfter is still enforced.
	NotBeforeSkew = 5 * time.Minute
)

// Reason classifies why a chain was refused. It is a closed vocabulary so a caller can count
// refusals by cause without parsing messages.
type Reason string

const (
	ReasonNoCertificate Reason = "no_certificate"
	ReasonTooLong       Reason = "chain_too_long"
	ReasonTooLarge      Reason = "chain_too_large"
	ReasonMalformed     Reason = "malformed"
	ReasonAnchorInvalid Reason = "anchor_invalid"
	ReasonLeafIsCA      Reason = "leaf_is_ca"
	ReasonNotClientAuth Reason = "not_client_auth"
	ReasonWeakSignature Reason = "weak_signature"
	ReasonKeyPolicy     Reason = "key_policy"
	ReasonExpired       Reason = "expired"
	ReasonNotYetValid   Reason = "not_yet_valid"
	ReasonUntrusted     Reason = "untrusted"
)

// ErrChainInvalid is the sentinel every refusal wraps.
var ErrChainInvalid = errors.New("x509trust: client certificate chain refused")

// ChainError is a refusal with its Reason. It satisfies errors.Is(err, ErrChainInvalid). Its
// message never contains certificate content.
type ChainError struct {
	Reason Reason
	Detail string
}

func (e *ChainError) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("%v: %s", ErrChainInvalid, e.Reason)
	}
	return fmt.Sprintf("%v: %s: %s", ErrChainInvalid, e.Reason, e.Detail)
}

func (e *ChainError) Unwrap() error { return ErrChainInvalid }

// ReasonOf returns the Reason of a refusal, or "" when err is not a ChainError.
func ReasonOf(err error) Reason {
	var ce *ChainError
	if errors.As(err, &ce) {
		return ce.Reason
	}
	return ""
}

func refuse(r Reason, detail string) error { return &ChainError{Reason: r, Detail: detail} }

// Verified is a chain that passed VerifyClientChain.
type Verified struct {
	// Leaf is the device certificate.
	Leaf *x509.Certificate
	// Fingerprint is the hex SHA-256 of the leaf DER: the value a credential pins.
	Fingerprint string
	// Chain is the verified path, leaf first, ending in the anchor.
	Chain []*x509.Certificate
	// NotAfter is the earliest notAfter over the whole verified path (leaf, intermediates and
	// the anchor): the instant after which the chain no longer authenticates. A session must not
	// outlive it.
	NotAfter time.Time
}

// ParseChainPEM parses the PEM certificates a TLS peer presented, leaf first. The count and
// size bounds are checked before any parsing, so an oversized presentation costs nothing.
func ParseChainPEM(pems []string) ([]*x509.Certificate, error) {
	if len(pems) == 0 {
		return nil, refuse(ReasonNoCertificate, "")
	}
	if len(pems) > MaxPresentedCerts {
		return nil, refuse(ReasonTooLong, fmt.Sprintf("more than %d certificates", MaxPresentedCerts))
	}
	// PEM is larger than the DER it carries (base64), so this bound is safe to apply early.
	total := 0
	for _, p := range pems {
		total += len(p)
	}
	if total > 2*MaxPresentedDERBytes {
		return nil, refuse(ReasonTooLarge, "")
	}
	out := make([]*x509.Certificate, 0, len(pems))
	for _, p := range pems {
		block, rest := pem.Decode([]byte(p))
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 || hasPEMBlock(rest) {
			return nil, refuse(ReasonMalformed, "a presented certificate is not a single CERTIFICATE block")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, refuse(ReasonMalformed, "a presented certificate does not parse")
		}
		out = append(out, cert)
	}
	return out, nil
}

func hasPEMBlock(b []byte) bool {
	blk, _ := pem.Decode(b)
	return blk != nil
}

// VerifyClientChain decides whether presented (leaf first, then any intermediates) chains to
// anchor as a TLS client certificate at now. It is the single definition of that decision.
//
// Verified against exactly one anchor (the caller chooses it; this function never searches),
// with the usage pinned to ClientAuth so a certificate that merely allows "any" usage is not
// accepted for it.
//
// Refused: no certificate; more than 5 certificates or more than 16 KiB of DER; an anchor that
// is not a valid CA; a leaf that is itself a CA; a leaf without an explicit clientAuth usage; a
// SHA-1 (or weaker) signature on any presented certificate; a key outside the key policy
// anywhere on the path, anchor included; a chain that is expired or not yet valid; anything
// that does not build to the anchor.
//
// Time: now, except that a leaf whose notBefore lies within NotBeforeSkew in the future is
// verified at its notBefore.
func VerifyClientChain(anchor *x509.Certificate, presented []*x509.Certificate, now time.Time) (*Verified, error) {
	if len(presented) == 0 {
		return nil, refuse(ReasonNoCertificate, "")
	}
	if len(presented) > MaxPresentedCerts {
		return nil, refuse(ReasonTooLong, fmt.Sprintf("more than %d certificates", MaxPresentedCerts))
	}
	size := 0
	for _, c := range presented {
		size += len(c.Raw)
	}
	if size > MaxPresentedDERBytes {
		return nil, refuse(ReasonTooLarge, "")
	}
	if anchor == nil {
		return nil, refuse(ReasonAnchorInvalid, "no anchor")
	}
	// The anchor was checked when it was stored; it is checked again here because a stored row
	// outlives the rules, and because this function must not trust its caller's bookkeeping.
	if err := CheckAnchor(anchor, now); err != nil {
		return nil, refuse(ReasonAnchorInvalid, "")
	}

	leaf := presented[0]
	if leaf.BasicConstraintsValid && leaf.IsCA {
		return nil, refuse(ReasonLeafIsCA, "")
	}
	if !hasClientAuth(leaf) {
		return nil, refuse(ReasonNotClientAuth, "")
	}
	for _, c := range presented {
		if weakSignature(c) {
			return nil, refuse(ReasonWeakSignature, "")
		}
		if err := CheckPublicKey(c.PublicKey); err != nil {
			return nil, refuse(ReasonKeyPolicy, "")
		}
	}

	at := now
	if leaf.NotBefore.After(now) && leaf.NotBefore.Sub(now) <= NotBeforeSkew {
		at = leaf.NotBefore
	}
	roots := x509.NewCertPool()
	roots.AddCert(anchor)
	inters := x509.NewCertPool()
	for _, c := range presented[1:] {
		inters.AddCert(c)
	}
	chains, err := leaf.Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: inters,
		CurrentTime:   at,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	if err != nil {
		return nil, refuse(classify(err, presented, anchor, now), "")
	}

	var best *Verified
	for _, chain := range chains {
		ok := true
		for _, c := range chain {
			if CheckPublicKey(c.PublicKey) != nil {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		na := leaf.NotAfter
		for _, c := range chain[1:] {
			if c.NotAfter.Before(na) {
				na = c.NotAfter
			}
		}
		// Any verified path authenticates the device; report the one that lasts longest.
		if best == nil || na.After(best.NotAfter) {
			best = &Verified{Leaf: leaf, Fingerprint: Fingerprint(leaf), Chain: chain, NotAfter: na.UTC()}
		}
	}
	if best == nil {
		return nil, refuse(ReasonKeyPolicy, "")
	}
	return best, nil
}

func hasClientAuth(c *x509.Certificate) bool {
	for _, u := range c.ExtKeyUsage {
		if u == x509.ExtKeyUsageClientAuth {
			return true
		}
	}
	return false
}

// classify maps the verifier's error to a Reason. Anything it cannot place is Untrusted.
func classify(err error, presented []*x509.Certificate, anchor *x509.Certificate, now time.Time) Reason {
	var inv x509.CertificateInvalidError
	if errors.As(err, &inv) {
		switch inv.Reason {
		case x509.Expired:
			for _, c := range append(append([]*x509.Certificate{}, presented...), anchor) {
				if now.Before(c.NotBefore) {
					return ReasonNotYetValid
				}
			}
			return ReasonExpired
		case x509.IncompatibleUsage:
			return ReasonNotClientAuth
		}
	}
	return ReasonUntrusted
}
