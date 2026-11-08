// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package x509trust

import (
	"bytes"
	"crypto"
	"crypto/sha1" //nolint:gosec // OCSP CertID and ResponderID key hashes are SHA-1 by RFC 6960; not a security boundary.
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"time"

	"golang.org/x/crypto/ocsp"
)

// OCSPMode is an anchor's OCSP mode: whether, and how strictly, the revocation status of a
// certificate is asked of its issuer's online responder at connect.
type OCSPMode string

const (
	// OCSPOff asks nothing. Every anchor that predates OCSP is OFF, and an OFF anchor runs no OCSP
	// code and sends no byte.
	OCSPOff OCSPMode = "OFF"
	// OCSPEnforceKnown refuses a certificate a responder says is revoked and ADMITS one whose status
	// could not be learned (timeout, unreachable responder, unusable answer): fail-closed on what
	// is known, fail-open on completeness.
	OCSPEnforceKnown OCSPMode = "ENFORCE_KNOWN"
	// OCSPRequireCurrent also refuses a certificate whose status could not be confirmed current.
	OCSPRequireCurrent OCSPMode = "REQUIRE_CURRENT"
)

// Valid reports whether m is one of the three modes.
func (m OCSPMode) Valid() bool {
	switch m {
	case OCSPOff, OCSPEnforceKnown, OCSPRequireCurrent:
		return true
	}
	return false
}

// OCSPStatus is the status a responder asserted for one certificate.
type OCSPStatus string

const (
	OCSPStatusGood    OCSPStatus = "good"
	OCSPStatusRevoked OCSPStatus = "revoked"
	// OCSPStatusUnknown is the responder saying it does not know the certificate. It is not
	// "good", and a caller treats it as no usable answer.
	OCSPStatusUnknown OCSPStatus = "unknown"
)

const (
	// MaxOCSPResponseBytes bounds a response VerifyOCSPResponse will parse.
	MaxOCSPResponseBytes = 128 << 10
	// DefaultOCSPSkew is how far into the future a thisUpdate may be (clock skew between us and
	// the responder) and still count as issued.
	DefaultOCSPSkew = time.Minute
	// ocspNoNextUpdateMax bounds how long a response with no nextUpdate is believed.
	ocspNoNextUpdateMax = time.Hour
	// maxOCSPEmbeddedCerts bounds the certificates of a response that are looked at.
	maxOCSPEmbeddedCerts = 4
)

// OCSPFailure classifies why a response was not accepted. Every failure is a refusal to TRUST the
// response; what the caller does about an unusable answer is the anchor's mode.
type OCSPFailure string

const (
	// OCSPMalformed: not a parseable OCSP response (or oversized).
	OCSPMalformed OCSPFailure = "malformed"
	// OCSPNotSuccessful: the responder answered with an error status (tryLater, unauthorized, ...).
	OCSPNotSuccessful OCSPFailure = "not_successful"
	// OCSPWrongCertificate: no single response names this certificate under this issuer (serial,
	// issuer name hash and issuer key hash must all match).
	OCSPWrongCertificate OCSPFailure = "wrong_certificate"
	// OCSPUntrustedSigner: the signer is neither the issuer nor a delegated responder that the
	// issuer vouches for.
	OCSPUntrustedSigner OCSPFailure = "untrusted_signer"
	// OCSPBadSignature: the signature does not verify under the signer's key, or its algorithm is
	// not one this verifier accepts.
	OCSPBadSignature OCSPFailure = "bad_signature"
	// OCSPStale: the response is past its nextUpdate, or older than the replay bound.
	OCSPStale OCSPFailure = "stale"
	// OCSPNotYetValid: thisUpdate is in the future beyond the skew.
	OCSPNotYetValid OCSPFailure = "not_yet_valid"
)

// ErrOCSP is the sentinel every OCSP verification refusal wraps.
var ErrOCSP = errors.New("x509trust: OCSP response refused")

// OCSPError is a refusal with its classification.
type OCSPError struct {
	Failure OCSPFailure
	Detail  string
}

func (e *OCSPError) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("%v: %s", ErrOCSP, e.Failure)
	}
	return fmt.Sprintf("%v: %s: %s", ErrOCSP, e.Failure, e.Detail)
}

func (e *OCSPError) Unwrap() error { return ErrOCSP }

// OCSPFailureOf is the classification of an error from VerifyOCSPResponse, "" for any other.
func OCSPFailureOf(err error) OCSPFailure {
	var oe *OCSPError
	if errors.As(err, &oe) {
		return oe.Failure
	}
	return ""
}

func ocspRefuse(f OCSPFailure, format string, args ...any) error {
	return &OCSPError{Failure: f, Detail: fmt.Sprintf(format, args...)}
}

// OCSPOptions are the bounds of one verification.
type OCSPOptions struct {
	// MaxAge bounds how old a response's thisUpdate may be, however far off its nextUpdate is. It
	// is the replay bound (a request carries no nonce, so a captured response is replayable until
	// then). Must be positive.
	MaxAge time.Duration
	// Skew is how far in the future thisUpdate may be. Zero means DefaultOCSPSkew.
	Skew time.Duration
}

// OCSPAnswer is a verified response for one certificate.
type OCSPAnswer struct {
	Status OCSPStatus
	// RevokedAt and Reason are set for a revoked answer.
	RevokedAt time.Time
	Reason    RevocationReason
	// ThisUpdate and NextUpdate are the signed times; NextUpdate is zero when the responder sent
	// none.
	ThisUpdate time.Time
	NextUpdate time.Time
	// ValidUntil is the instant the answer stops being usable: the earliest of nextUpdate,
	// thisUpdate plus MaxAge (or an hour, when there is no nextUpdate), and a delegated signer's
	// notAfter. It is computed from SIGNED times, never from when the response was fetched.
	ValidUntil time.Time
	// Delegated is whether a delegated responder certificate signed the response.
	Delegated bool
}

// BuildOCSPRequest is the DER OCSPRequest for cert as issued by issuer: one CertID (SHA-1, which
// RFC 6960 obliges every responder to accept) and no nonce, so a response is cacheable.
func BuildOCSPRequest(cert, issuer *x509.Certificate) ([]byte, error) {
	if cert == nil || issuer == nil {
		return nil, errors.New("x509trust: an OCSP request needs a certificate and its issuer")
	}
	if !bytes.Equal(cert.RawIssuer, issuer.RawSubject) {
		return nil, errors.New("x509trust: the issuer is not the certificate's issuer")
	}
	return ocsp.CreateRequest(cert, issuer, &ocsp.RequestOptions{Hash: crypto.SHA1})
}

// OCSPResponderURL is the first http or https id-ad-ocsp URL in the certificate's Authority
// Information Access, "" when it has none. The URL is the CA's choice, not the device's, but it is
// still not safe to dial unguarded: the caller dials it only through the egress guard.
func OCSPResponderURL(cert *x509.Certificate) string {
	for _, raw := range cert.OCSPServer {
		if u, err := url.Parse(raw); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
			return raw
		}
	}
	return ""
}

// ─── ASN.1 (RFC 6960). Owned, rather than the library's parser, because the library matches a
// single response by serial alone and does not expose the issuer hashes, and because it checks the
// signature against the FIRST embedded certificate whoever it is. The trust decision is ours. ───

var (
	oidOCSPBasic = asn1.ObjectIdentifier{1, 3, 6, 1, 5, 5, 7, 48, 1, 1}

	oidHashSHA1   = asn1.ObjectIdentifier{1, 3, 14, 3, 2, 26}
	oidHashSHA256 = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}
	oidHashSHA384 = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 2}
	oidHashSHA512 = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 3}

	oidSigSHA256WithRSA   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 11}
	oidSigSHA384WithRSA   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 12}
	oidSigSHA512WithRSA   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 13}
	oidSigECDSAWithSHA256 = asn1.ObjectIdentifier{1, 2, 840, 10045, 4, 3, 2}
	oidSigECDSAWithSHA384 = asn1.ObjectIdentifier{1, 2, 840, 10045, 4, 3, 3}
	oidSigECDSAWithSHA512 = asn1.ObjectIdentifier{1, 2, 840, 10045, 4, 3, 4}
	oidSigEd25519         = asn1.ObjectIdentifier{1, 3, 101, 112}
)

type ocspResponseASN1 struct {
	Status   asn1.Enumerated
	Response ocspResponseBytes `asn1:"explicit,tag:0,optional"`
}

type ocspResponseBytes struct {
	ResponseType asn1.ObjectIdentifier
	Response     []byte
}

type ocspBasicASN1 struct {
	TBS                ocspTBSASN1
	SignatureAlgorithm pkix.AlgorithmIdentifier
	Signature          asn1.BitString
	Certificates       []asn1.RawValue `asn1:"explicit,tag:0,optional"`
}

type ocspTBSASN1 struct {
	Raw            asn1.RawContent
	Version        int `asn1:"optional,default:0,explicit,tag:0"`
	RawResponderID asn1.RawValue
	ProducedAt     time.Time `asn1:"generalized"`
	Responses      []ocspSingleASN1
	// ResponseExtensions is read so that a critical one can be refused: encoding/asn1 would
	// otherwise skip a trailing field it was not given, and an extension the signer marked
	// critical would be silently ignored.
	ResponseExtensions []pkix.Extension `asn1:"explicit,tag:1,optional"`
}

type ocspSingleASN1 struct {
	CertID           ocspCertIDASN1
	Status           asn1.RawValue
	ThisUpdate       time.Time        `asn1:"generalized"`
	NextUpdate       time.Time        `asn1:"generalized,explicit,tag:0,optional"`
	SingleExtensions []pkix.Extension `asn1:"explicit,tag:1,optional"`
}

type ocspCertIDASN1 struct {
	HashAlgorithm pkix.AlgorithmIdentifier
	NameHash      []byte
	IssuerKeyHash []byte
	SerialNumber  *big.Int
}

func ocspHash(oid asn1.ObjectIdentifier) (crypto.Hash, bool) {
	switch {
	case oid.Equal(oidHashSHA1):
		return crypto.SHA1, true
	case oid.Equal(oidHashSHA256):
		return crypto.SHA256, true
	case oid.Equal(oidHashSHA384):
		return crypto.SHA384, true
	case oid.Equal(oidHashSHA512):
		return crypto.SHA512, true
	}
	return 0, false
}

func ocspDigest(h crypto.Hash, b []byte) []byte {
	switch h {
	case crypto.SHA1:
		s := sha1.Sum(b) //nolint:gosec // see the import.
		return s[:]
	case crypto.SHA256:
		s := sha256.Sum256(b)
		return s[:]
	case crypto.SHA384:
		s := sha512.Sum384(b)
		return s[:]
	case crypto.SHA512:
		s := sha512.Sum512(b)
		return s[:]
	}
	return nil
}

func ocspSigAlg(oid asn1.ObjectIdentifier) (x509.SignatureAlgorithm, bool) {
	switch {
	case oid.Equal(oidSigSHA256WithRSA):
		return x509.SHA256WithRSA, true
	case oid.Equal(oidSigSHA384WithRSA):
		return x509.SHA384WithRSA, true
	case oid.Equal(oidSigSHA512WithRSA):
		return x509.SHA512WithRSA, true
	case oid.Equal(oidSigECDSAWithSHA256):
		return x509.ECDSAWithSHA256, true
	case oid.Equal(oidSigECDSAWithSHA384):
		return x509.ECDSAWithSHA384, true
	case oid.Equal(oidSigECDSAWithSHA512):
		return x509.ECDSAWithSHA512, true
	case oid.Equal(oidSigEd25519):
		return x509.PureEd25519, true
	}
	return 0, false
}

// spkiKeyBytes is the subjectPublicKey BIT STRING contents of a certificate: what a CertID's
// issuerKeyHash and a ResponderID's byKey hash.
func spkiKeyBytes(c *x509.Certificate) ([]byte, bool) {
	var spki struct {
		Algorithm pkix.AlgorithmIdentifier
		Key       asn1.BitString
	}
	if rest, err := asn1.Unmarshal(c.RawSubjectPublicKeyInfo, &spki); err != nil || len(rest) != 0 {
		return nil, false
	}
	return spki.Key.RightAlign(), true
}

// responderMatches is whether the ResponderID names cert: byName by byte-exact subject, byKey by
// the SHA-1 of its public key.
func responderMatches(id asn1.RawValue, cert *x509.Certificate) bool {
	if id.Class != asn1.ClassContextSpecific {
		return false
	}
	switch id.Tag {
	case 1:
		return bytes.Equal(id.Bytes, cert.RawSubject)
	case 2:
		var keyHash []byte
		if rest, err := asn1.Unmarshal(id.Bytes, &keyHash); err != nil || len(rest) != 0 {
			return false
		}
		key, ok := spkiKeyBytes(cert)
		return ok && bytes.Equal(keyHash, ocspDigest(crypto.SHA1, key))
	}
	return false
}

// VerifyOCSPResponse decides whether raw is a trustworthy statement of cert's status, issued by
// issuer's OCSP service, at instant now. It does no I/O.
//
// The response is trusted only when ALL of these hold:
//   - it is a successful basic response of at most MaxOCSPResponseBytes;
//   - exactly one single response names cert, by serial AND issuer name hash AND issuer key hash
//     under the hash algorithm it states (status, times and extensions are all read from that one);
//   - it is signed by the issuer itself, or by a delegated responder certificate EMBEDDED in the
//     response that is within its validity, was issued DIRECTLY by issuer (signature verified),
//     carries id-kp-OCSPSigning, and is the one the ResponderID names;
//   - the signature verifies, with an algorithm on the short list (RSA PKCS#1 or ECDSA with
//     SHA-256/384/512, Ed25519);
//   - thisUpdate is not in the future beyond the skew, is not after nextUpdate, now is before
//     nextUpdate, and now is within MaxAge of thisUpdate. A response with no nextUpdate is believed
//     for min(MaxAge, one hour) from thisUpdate.
//
// A critical single-response or response extension is refused (none is understood). A delegated
// responder certificate with an unhandled critical extension, or a key usage that excludes
// digitalSignature, is not a signer. A delegated responder is
// not itself revocation-checked (the id-pkix-ocsp-nocheck posture); an answer it signed is not
// usable past its notAfter (ValidUntil).
//
// Any refusal is an *OCSPError whose Failure classifies it. The result for status "unknown" is a
// successful verification of an answer that is not good: callers treat it as unusable.
func VerifyOCSPResponse(raw []byte, cert, issuer *x509.Certificate, now time.Time, opts OCSPOptions) (*OCSPAnswer, error) {
	if cert == nil || issuer == nil || cert.SerialNumber == nil || cert.SerialNumber.Sign() <= 0 {
		return nil, ocspRefuse(OCSPWrongCertificate, "no certificate to check")
	}
	if opts.MaxAge <= 0 {
		return nil, ocspRefuse(OCSPStale, "no replay bound configured")
	}
	skew := opts.Skew
	if skew <= 0 {
		skew = DefaultOCSPSkew
	}
	if now.IsZero() {
		return nil, ocspRefuse(OCSPStale, "no clock")
	}
	if len(raw) == 0 || len(raw) > MaxOCSPResponseBytes {
		return nil, ocspRefuse(OCSPMalformed, "response size %d", len(raw))
	}
	if !bytes.Equal(cert.RawIssuer, issuer.RawSubject) {
		return nil, ocspRefuse(OCSPWrongCertificate, "the issuer is not the certificate's issuer")
	}

	var outer ocspResponseASN1
	if rest, err := asn1.Unmarshal(raw, &outer); err != nil || len(rest) != 0 {
		return nil, ocspRefuse(OCSPMalformed, "response does not parse")
	}
	if ocsp.ResponseStatus(outer.Status) != ocsp.Success {
		return nil, ocspRefuse(OCSPNotSuccessful, "responder status %d", int(outer.Status))
	}
	if !outer.Response.ResponseType.Equal(oidOCSPBasic) {
		return nil, ocspRefuse(OCSPMalformed, "not a basic response")
	}
	var basic ocspBasicASN1
	if rest, err := asn1.Unmarshal(outer.Response.Response, &basic); err != nil || len(rest) != 0 {
		return nil, ocspRefuse(OCSPMalformed, "basic response does not parse")
	}

	// The one single response for this certificate.
	var single *ocspSingleASN1
	for i := range basic.TBS.Responses {
		r := &basic.TBS.Responses[i]
		if r.CertID.SerialNumber == nil || r.CertID.SerialNumber.Cmp(cert.SerialNumber) != 0 {
			continue
		}
		h, ok := ocspHash(r.CertID.HashAlgorithm.Algorithm)
		if !ok {
			continue
		}
		keyBytes, ok := spkiKeyBytes(issuer)
		if !ok {
			return nil, ocspRefuse(OCSPWrongCertificate, "issuer key unreadable")
		}
		if !bytes.Equal(r.CertID.NameHash, ocspDigest(h, issuer.RawSubject)) ||
			!bytes.Equal(r.CertID.IssuerKeyHash, ocspDigest(h, keyBytes)) {
			continue
		}
		if single != nil {
			return nil, ocspRefuse(OCSPWrongCertificate, "more than one single response names the certificate")
		}
		single = r
	}
	if single == nil {
		return nil, ocspRefuse(OCSPWrongCertificate, "no single response names this certificate under this issuer")
	}
	for _, ext := range single.SingleExtensions {
		if ext.Critical {
			return nil, ocspRefuse(OCSPMalformed, "critical single-response extension")
		}
	}
	for _, ext := range basic.TBS.ResponseExtensions {
		if ext.Critical {
			return nil, ocspRefuse(OCSPMalformed, "critical response extension")
		}
	}

	// The signer.
	sigAlg, ok := ocspSigAlg(basic.SignatureAlgorithm.Algorithm)
	if !ok {
		return nil, ocspRefuse(OCSPBadSignature, "signature algorithm %v is not accepted", basic.SignatureAlgorithm.Algorithm)
	}
	signer, delegated, err := ocspSigner(&basic, issuer, now, skew, sigAlg)
	if err != nil {
		return nil, err
	}

	// The window, from SIGNED times.
	ans := &OCSPAnswer{ThisUpdate: single.ThisUpdate.UTC(), Delegated: delegated}
	if !single.NextUpdate.IsZero() {
		ans.NextUpdate = single.NextUpdate.UTC()
	}
	if single.ThisUpdate.After(now.Add(skew)) {
		return nil, ocspRefuse(OCSPNotYetValid, "thisUpdate %s", single.ThisUpdate.UTC().Format(time.RFC3339))
	}
	validity := opts.MaxAge
	if single.NextUpdate.IsZero() {
		validity = min(validity, ocspNoNextUpdateMax)
	} else if single.ThisUpdate.After(single.NextUpdate) {
		return nil, ocspRefuse(OCSPMalformed, "thisUpdate is after nextUpdate")
	}
	until := single.ThisUpdate.Add(validity)
	if !single.NextUpdate.IsZero() && single.NextUpdate.Before(until) {
		until = single.NextUpdate
	}
	if delegated && signer.NotAfter.Before(until) {
		until = signer.NotAfter
	}
	if !now.Before(until) {
		return nil, ocspRefuse(OCSPStale, "usable until %s", until.UTC().Format(time.RFC3339))
	}
	ans.ValidUntil = until.UTC()

	switch single.Status.Tag {
	case 0:
		ans.Status = OCSPStatusGood
	case 2:
		ans.Status = OCSPStatusUnknown
	case 1:
		ans.Status = OCSPStatusRevoked
		at, reason, err := parseOCSPRevokedInfo(single.Status.Bytes)
		if err != nil {
			return nil, ocspRefuse(OCSPMalformed, "revoked info: %v", err)
		}
		ans.RevokedAt, ans.Reason = at, reason
	default:
		return nil, ocspRefuse(OCSPMalformed, "certificate status tag %d", single.Status.Tag)
	}
	if single.Status.Class != asn1.ClassContextSpecific {
		return nil, ocspRefuse(OCSPMalformed, "certificate status class")
	}
	return ans, nil
}

// parseOCSPRevokedInfo reads the contents of an implicitly tagged RevokedInfo: a GeneralizedTime
// and an optional [0] EXPLICIT CRLReason.
func parseOCSPRevokedInfo(b []byte) (time.Time, RevocationReason, error) {
	var at time.Time
	rest, err := asn1.UnmarshalWithParams(b, &at, "generalized")
	if err != nil {
		return time.Time{}, "", err
	}
	reason := RevocationUnspecified
	if len(rest) > 0 {
		var wrapper asn1.RawValue
		if r, err := asn1.Unmarshal(rest, &wrapper); err != nil || len(r) != 0 ||
			wrapper.Class != asn1.ClassContextSpecific || wrapper.Tag != 0 {
			return time.Time{}, "", errors.New("trailing data")
		}
		var code asn1.Enumerated
		if r, err := asn1.Unmarshal(wrapper.Bytes, &code); err != nil || len(r) != 0 {
			return time.Time{}, "", errors.New("reason")
		}
		if name, ok := reasonNames[int(code)]; ok {
			reason = name
		}
	}
	return at.UTC(), reason, nil
}

// ocspSigner picks the certificate whose key signed the response, or refuses. Every certificate
// the ResponderID names and that is eligible (the issuer itself, or a delegate the issuer vouches
// for) is a CANDIDATE, and the signature is tried under each: a name does not identify a key (a
// delegate may share the issuer's name, rollover delegates may share one another's), so the first
// match is not necessarily the signer.
func ocspSigner(basic *ocspBasicASN1, issuer *x509.Certificate, now time.Time, skew time.Duration,
	sigAlg x509.SignatureAlgorithm) (*x509.Certificate, bool, error) {
	type candidate struct {
		cert      *x509.Certificate
		delegated bool
	}
	var cands []candidate
	if responderMatches(basic.TBS.RawResponderID, issuer) {
		cands = append(cands, candidate{issuer, false})
	}
	n := len(basic.Certificates)
	if n > maxOCSPEmbeddedCerts {
		n = maxOCSPEmbeddedCerts
	}
	var why string
	for i := 0; i < n; i++ {
		del, err := x509.ParseCertificate(basic.Certificates[i].FullBytes)
		if err != nil {
			continue
		}
		if !responderMatches(basic.TBS.RawResponderID, del) {
			continue
		}
		switch {
		case !bytes.Equal(del.RawIssuer, issuer.RawSubject):
			why = "the delegated responder was not issued by the checked certificate's issuer"
		case weakSignature(del):
			why = "the delegated responder is signed with a hash that must not carry trust"
		case CheckPublicKey(del.PublicKey) != nil:
			why = "the delegated responder's key is outside the key policy"
		case issuer.CheckSignature(del.SignatureAlgorithm, del.RawTBSCertificate, del.Signature) != nil:
			why = "the delegated responder's issuer signature does not verify"
		case now.Add(skew).Before(del.NotBefore) || !now.Before(del.NotAfter):
			why = "the delegated responder is outside its validity"
		case !hasOCSPSigning(del):
			why = "the delegated responder lacks id-kp-OCSPSigning"
		case len(del.UnhandledCriticalExtensions) > 0:
			why = "the delegated responder carries a critical extension this verifier does not understand"
		case del.KeyUsage != 0 && del.KeyUsage&x509.KeyUsageDigitalSignature == 0:
			why = "the delegated responder's key usage does not allow digital signatures"
		default:
			cands = append(cands, candidate{del, true})
		}
	}
	if len(cands) == 0 {
		if why == "" {
			why = "the response names no signer the issuer vouches for"
		}
		return nil, false, ocspRefuse(OCSPUntrustedSigner, "%s", why)
	}
	var sigErr error
	for _, c := range cands {
		if sigErr = c.cert.CheckSignature(sigAlg, basic.TBS.Raw, basic.Signature.RightAlign()); sigErr == nil {
			return c.cert, c.delegated, nil
		}
	}
	return nil, false, ocspRefuse(OCSPBadSignature, "%v", sigErr)
}

func hasOCSPSigning(c *x509.Certificate) bool {
	for _, u := range c.ExtKeyUsage {
		if u == x509.ExtKeyUsageOCSPSigning {
			return true
		}
	}
	return false
}
