// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package x509trust

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"time"
)

// RevocationReason is the RFC 5280 reason a certificate was revoked, by name. "unspecified"
// stands for an entry that carries none.
type RevocationReason string

const (
	RevocationUnspecified          RevocationReason = "unspecified"
	RevocationKeyCompromise        RevocationReason = "keyCompromise"
	RevocationCACompromise         RevocationReason = "cACompromise"
	RevocationAffiliationChanged   RevocationReason = "affiliationChanged"
	RevocationSuperseded           RevocationReason = "superseded"
	RevocationCessationOfOperation RevocationReason = "cessationOfOperation"
	RevocationCertificateHold      RevocationReason = "certificateHold"
	RevocationPrivilegeWithdrawn   RevocationReason = "privilegeWithdrawn"
	RevocationAACompromise         RevocationReason = "aACompromise"
)

// Mode is an anchor's revocation mode: what an issuer with no current list means.
type Mode string

const (
	// ModeEntries refuses what is listed and admits the rest, whether or not a list exists or
	// is current: fail-closed on what is known, fail-open on completeness.
	ModeEntries Mode = "ENTRIES"
	// ModeRequireCurrent also refuses a path through an issuer with no list, or whose list is
	// older than nextUpdate plus the stale grace.
	ModeRequireCurrent Mode = "REQUIRE_CURRENT"
)

// IssuerID is the identity of an issuing CA for revocation: the SHA-256 of its
// SubjectPublicKeyInfo DER and the SHA-256 of its subject DER, both hex. BOTH are part of the
// identity. A key alone would let one key reused under two names share one list and one
// "current" coverage, so an empty list for one name could erase the other's entries. The name
// is the byte-exact DER (no DN normalisation): a CA whose re-encoded DN differs is a different
// issuer, which fails closed under ModeRequireCurrent rather than being guessed equal.
type IssuerID struct {
	Key  string
	Name string
}

// IssuerSerial identifies a certificate the way a CRL does: by its issuer and serial number.
// Serial is the minimal lowercase hex of the big-endian magnitude of a positive serial.
type IssuerSerial struct {
	Issuer IssuerID
	Serial string
}

// ListStatus is what the caller knows about an issuer's current list.
type ListStatus struct {
	NextUpdate time.Time
}

// RevocationQuery is every identity ApplyRevocation will look for on the verified paths, the
// anchor excluded: the keys of the one database read the caller makes. Each slice is sorted and
// free of duplicates.
type RevocationQuery struct {
	Fingerprints []string
	Serials      []IssuerSerial
	// Issuers are the issuers of the certificates on the paths (the anchor included, as the
	// issuer of the topmost certificate), whose lists decide completeness.
	Issuers []IssuerID
}

// RevocationState is what the caller read for a RevocationQuery. Presence in a map is what
// counts: the reason is for reporting.
type RevocationState struct {
	// RevokedFingerprints are the manual entries that matched a fingerprint.
	RevokedFingerprints map[string]RevocationReason
	// RevokedSerials are the CRL entries that matched an issuer and serial.
	RevokedSerials map[IssuerSerial]RevocationReason
	// Lists holds the current list of each issuer that has one.
	Lists map[IssuerID]ListStatus
	// Mode is the anchor's mode. The zero value is invalid and refused.
	Mode Mode
	// Now is the instant freshness is judged at. The zero value is invalid and refused.
	Now time.Time
	// StaleGrace is how long past nextUpdate a list still counts as current. Negative is invalid.
	StaleGrace time.Duration
}

// ErrRevocationState is returned (not as a ChainError, so ReasonOf is "") for a state or a
// Verified that ApplyRevocation cannot judge. It is a refusal like any other error: a caller
// treats every non-nil error from ApplyRevocation as "do not admit".
var ErrRevocationState = errors.New("x509trust: revocation state cannot be applied")

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// issuerIDOf is the issuer identity of child as issued by parent on one path: the key is the
// parent's, the name is the child's own RawIssuer (which path building requires to equal the
// parent's RawSubject, and which is what a connect path can hash without trusting anything
// else).
func issuerIDOf(child, parent *x509.Certificate) IssuerID {
	return IssuerID{Key: sha256Hex(parent.RawSubjectPublicKeyInfo), Name: sha256Hex(child.RawIssuer)}
}

// IssuerIDOfCA is the identity under which a CA's CRL is stored: its own key and subject.
func IssuerIDOfCA(ca *x509.Certificate) IssuerID {
	return IssuerID{Key: sha256Hex(ca.RawSubjectPublicKeyInfo), Name: sha256Hex(ca.RawSubject)}
}

// SerialHex is the canonical text of a certificate serial: minimal lowercase hex of the
// magnitude. A zero, negative or absent serial has none (RFC 5280 requires a positive one).
func SerialHex(n *big.Int) (string, bool) {
	if n == nil || n.Sign() <= 0 {
		return "", false
	}
	return n.Text(16), true
}

// pathEntry is one non-anchor certificate of a path with the identities revocation looks up.
type pathEntry struct {
	fingerprint string
	issuer      IssuerID
	serial      string
	serialOK    bool
}

// pathEntries lists every certificate of a path except the anchor (the last), leaf first. A
// path shorter than two is not a verified path.
func pathEntries(chain []*x509.Certificate) ([]pathEntry, bool) {
	if len(chain) < 2 {
		return nil, false
	}
	out := make([]pathEntry, 0, len(chain)-1)
	for i := 0; i < len(chain)-1; i++ {
		if chain[i] == nil || chain[i+1] == nil {
			return nil, false
		}
		s, ok := SerialHex(chain[i].SerialNumber)
		out = append(out, pathEntry{
			fingerprint: Fingerprint(chain[i]),
			issuer:      issuerIDOf(chain[i], chain[i+1]),
			serial:      s,
			serialOK:    ok,
		})
	}
	return out, true
}

// RevocationKeys lists every identity on every VERIFIED path, the anchor excluded: each
// certificate's fingerprint, its (issuer, serial) against its parent ON THAT PATH, and the
// issuers whose lists matter. It reads Chains and nothing the device presented, so a revoked
// certificate that merely rode along in the presentation, on no verified path, is not in it.
// A certificate whose serial is not a positive integer has no serial key (it cannot be named
// by a CRL); ApplyRevocation refuses any path through one.
func (v *Verified) RevocationKeys() RevocationQuery {
	fps := map[string]struct{}{}
	sers := map[IssuerSerial]struct{}{}
	iss := map[IssuerID]struct{}{}
	for _, chain := range v.Chains {
		entries, ok := pathEntries(chain)
		if !ok {
			continue
		}
		for _, e := range entries {
			fps[e.fingerprint] = struct{}{}
			iss[e.issuer] = struct{}{}
			if e.serialOK {
				sers[IssuerSerial{Issuer: e.issuer, Serial: e.serial}] = struct{}{}
			}
		}
	}
	q := RevocationQuery{}
	for f := range fps {
		q.Fingerprints = append(q.Fingerprints, f)
	}
	for s := range sers {
		q.Serials = append(q.Serials, s)
	}
	for i := range iss {
		q.Issuers = append(q.Issuers, i)
	}
	sort.Strings(q.Fingerprints)
	sort.Slice(q.Serials, func(a, b int) bool {
		x, y := q.Serials[a], q.Serials[b]
		if x.Issuer != y.Issuer {
			return lessIssuer(x.Issuer, y.Issuer)
		}
		return x.Serial < y.Serial
	})
	sort.Slice(q.Issuers, func(a, b int) bool { return lessIssuer(q.Issuers[a], q.Issuers[b]) })
	return q
}

func lessIssuer(a, b IssuerID) bool {
	if a.Key != b.Key {
		return a.Key < b.Key
	}
	return a.Name < b.Name
}

// pathVerdict is the outcome of judging one path.
type pathVerdict int

const (
	pathClear pathVerdict = iota
	pathRevoked
	pathUnknown
)

// ApplyRevocation drops every verified path that contains a revoked certificate and, under
// ModeRequireCurrent, every path with an issuer that has no list or whose list is past
// nextUpdate plus StaleGrace. It then re-picks the longest-lived survivor by the rule
// VerifyClientChain uses, and returns a Verified holding only the surviving paths. v is not
// modified.
//
// When no path survives the error is a *ChainError: ReasonRevoked when every path was dropped
// for a revoked certificate, otherwise ReasonRevocationUnknown (some path could be shown
// neither revoked nor current: the actionable cause, since a current list may admit it).
//
// 🔴 It REFUSES BY DEFAULT. A zero or unknown Mode, a zero Now, a negative StaleGrace, or a
// Verified with no verified path returns ErrRevocationState, never a pass. A path through a
// certificate whose serial is not a positive integer cannot be checked against any CRL and is
// dropped as unknown in every mode. ANY non-nil error means "do not admit"; the two classified
// reasons are for counting, and a caller's switch on ReasonOf must refuse in its default arm
// (an error whose ReasonOf is "" included).
func (v *Verified) ApplyRevocation(s RevocationState) (*Verified, error) {
	if v == nil || len(v.Chains) == 0 || v.Leaf == nil {
		return nil, fmt.Errorf("%w: no verified path", ErrRevocationState)
	}
	switch s.Mode {
	case ModeEntries, ModeRequireCurrent:
	default:
		return nil, fmt.Errorf("%w: unknown mode", ErrRevocationState)
	}
	if s.Now.IsZero() {
		return nil, fmt.Errorf("%w: no clock", ErrRevocationState)
	}
	if s.StaleGrace < 0 {
		return nil, fmt.Errorf("%w: negative grace", ErrRevocationState)
	}

	var survivors [][]*x509.Certificate
	revoked, unknown := 0, 0
	for _, chain := range v.Chains {
		switch judgePath(chain, s) {
		case pathClear:
			survivors = append(survivors, chain)
		case pathRevoked:
			revoked++
		default:
			unknown++
		}
	}
	if len(survivors) == 0 {
		if unknown == 0 && revoked > 0 {
			return nil, refuse(ReasonRevoked, "")
		}
		return nil, refuse(ReasonRevocationUnknown, "")
	}
	return pickLongestLived(v.Leaf, survivors), nil
}

func judgePath(chain []*x509.Certificate, s RevocationState) pathVerdict {
	entries, ok := pathEntries(chain)
	if !ok {
		return pathUnknown
	}
	for _, e := range entries {
		if _, hit := s.RevokedFingerprints[e.fingerprint]; hit {
			return pathRevoked
		}
		if e.serialOK {
			if _, hit := s.RevokedSerials[IssuerSerial{Issuer: e.issuer, Serial: e.serial}]; hit {
				return pathRevoked
			}
		}
	}
	for _, e := range entries {
		if s.Mode != ModeRequireCurrent {
			// ENTRIES changes nothing until an entry exists: a serial no CRL can name is not
			// grounds to refuse a certificate the verifier admits.
			continue
		}
		if !e.serialOK {
			return pathUnknown
		}
		l, have := s.Lists[e.issuer]
		if !have || s.Now.After(l.NextUpdate.Add(s.StaleGrace)) {
			return pathUnknown
		}
	}
	return pathClear
}

// PathLink is the revocation identity of one non-anchor certificate on a path: what a stored
// chain link records so a later revocation can find the credentials that relied on it. Issuer is
// the certificate's issuer against its parent ON THAT PATH.
type PathLink struct {
	Fingerprint string
	Issuer      IssuerID
	// Serial is the minimal lowercase hex of a positive serial; SerialOK is false for a serial a
	// CRL cannot name (zero or negative), in which case Serial is empty.
	Serial   string
	SerialOK bool
}

// PathLinks lists the identities of every certificate of one verified path except the anchor
// (the last), leaf first. ok is false for a path shorter than two or holding a nil certificate:
// not a verified path, and nothing to record.
func PathLinks(chain []*x509.Certificate) (links []PathLink, ok bool) {
	entries, ok := pathEntries(chain)
	if !ok {
		return nil, false
	}
	links = make([]PathLink, len(entries))
	for i, e := range entries {
		links[i] = PathLink{Fingerprint: e.fingerprint, Issuer: e.issuer, Serial: e.serial, SerialOK: e.serialOK}
	}
	return links, true
}

// IssuerNameHash is the hex SHA-256 of a certificate's byte-exact RawIssuer: the half of an
// issuer identity that is derivable from the certificate alone. A credential recorded before
// links existed has only this (its issuer's key is not in the leaf).
func IssuerNameHash(cert *x509.Certificate) string { return sha256Hex(cert.RawIssuer) }
