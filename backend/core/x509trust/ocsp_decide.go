// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package x509trust

import (
	"crypto/x509"
	"fmt"
)

// OCSPTarget is one certificate below the anchor on a verified path, with the issuer it is
// checked against on that path.
type OCSPTarget struct {
	Cert   *x509.Certificate
	Issuer *x509.Certificate
}

// OCSPTargetKey identifies a target: the SHA-256 fingerprints of the certificate and of its issuer.
// The same intermediate under the same parent on two paths is one target.
type OCSPTargetKey struct {
	Cert   string
	Issuer string
}

// Key is the target's identity.
func (t OCSPTarget) Key() OCSPTargetKey {
	return OCSPTargetKey{Cert: Fingerprint(t.Cert), Issuer: Fingerprint(t.Issuer)}
}

// OCSPOutcome is what the caller learned about one target.
type OCSPOutcome int

const (
	// OCSPNoAnswer is the zero value and what a missing entry reads as: nothing usable was learned
	// (timeout, unreachable, saturated, an unusable or "unknown" answer, or never asked).
	OCSPNoAnswer OCSPOutcome = iota
	// OCSPGood: a verified, current answer said good.
	OCSPGood
	// OCSPRevoked: a verified answer said revoked.
	OCSPRevoked
)

// OCSPResults is what the caller learned, by target. Absence is OCSPNoAnswer.
type OCSPResults map[OCSPTargetKey]OCSPOutcome

// OCSPTargets lists every distinct (certificate, issuer) pair below the anchor over every VERIFIED
// path, leaf first within a path, in a stable order. Like RevocationKeys it reads Chains and
// nothing the device presented.
func (v *Verified) OCSPTargets() []OCSPTarget {
	var out []OCSPTarget
	seen := map[OCSPTargetKey]struct{}{}
	for _, chain := range v.Chains {
		if len(chain) < 2 {
			continue
		}
		for i := 0; i < len(chain)-1; i++ {
			if chain[i] == nil || chain[i+1] == nil {
				break
			}
			t := OCSPTarget{Cert: chain[i], Issuer: chain[i+1]}
			if _, dup := seen[t.Key()]; dup {
				continue
			}
			seen[t.Key()] = struct{}{}
			out = append(out, t)
		}
	}
	return out
}

// OCSPDecision is the outcome of ApplyOCSP.
type OCSPDecision struct {
	// Survivor holds the paths that survive, and is what the session is clamped to.
	Survivor *Verified
	// Incomplete is true when the connect is admitted although some target on the surviving path
	// has no usable answer (ENFORCE_KNOWN's fail-open): the caller counts it.
	Incomplete bool
}

// ApplyOCSP applies what was learned to the verified paths.
//
// REVOKED WINS, and over every path: a verified revoked answer for ANY certificate below the
// anchor on ANY verified path refuses the connect, before completeness is considered. A revoked
// answer a leaf's alternate path could have avoided is still a CA saying the certificate is
// revoked; and an early failure on one target never hides a revocation on another, because the
// caller has gathered every target's outcome first.
//
// Otherwise ENFORCE_KNOWN admits (preferring a path with an answer for every target, and
// reporting Incomplete when none has), and REQUIRE_CURRENT admits only a path on which every
// target is good, else refuses with ReasonRevocationUnknown.
//
// 🔴 It REFUSES BY DEFAULT: a mode that is OFF or unknown, or a Verified with no path, is an
// error, never a pass. ANY non-nil error means "do not admit".
func (v *Verified) ApplyOCSP(mode OCSPMode, res OCSPResults) (OCSPDecision, error) {
	if v == nil || len(v.Chains) == 0 || v.Leaf == nil {
		return OCSPDecision{}, fmt.Errorf("%w: no verified path", ErrRevocationState)
	}
	switch mode {
	case OCSPEnforceKnown, OCSPRequireCurrent:
	default:
		return OCSPDecision{}, fmt.Errorf("%w: OCSP mode %q is not enforcing", ErrRevocationState, mode)
	}
	for _, t := range v.OCSPTargets() {
		if res[t.Key()] == OCSPRevoked {
			return OCSPDecision{}, refuse(ReasonRevoked, "")
		}
	}
	var complete, answered [][]*x509.Certificate
	for _, chain := range v.Chains {
		if len(chain) < 2 {
			continue
		}
		all, ok := true, true
		for i := 0; i < len(chain)-1; i++ {
			if chain[i] == nil || chain[i+1] == nil {
				ok = false
				break
			}
			if res[OCSPTarget{Cert: chain[i], Issuer: chain[i+1]}.Key()] != OCSPGood {
				all = false
			}
		}
		if !ok {
			continue
		}
		answered = append(answered, chain)
		if all {
			complete = append(complete, chain)
		}
	}
	if len(complete) > 0 {
		return OCSPDecision{Survivor: pickLongestLived(v.Leaf, complete)}, nil
	}
	if mode == OCSPRequireCurrent || len(answered) == 0 {
		return OCSPDecision{}, refuse(ReasonRevocationUnknown, "")
	}
	return OCSPDecision{Survivor: pickLongestLived(v.Leaf, answered), Incomplete: true}, nil
}
