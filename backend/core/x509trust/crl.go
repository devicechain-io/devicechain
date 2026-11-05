// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package x509trust

import (
	"bytes"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"time"
)

const (
	// DefaultMaxCRLBytes bounds a CRL as submitted (PEM or DER), checked before any parsing.
	DefaultMaxCRLBytes = 8 << 20
	// DefaultMaxCRLEntries bounds the entries of one CRL.
	DefaultMaxCRLEntries = 500_000
	// maxCRLNumberBits is the RFC 5280 limit on a CRL number: 20 octets.
	maxCRLNumberBits = 160
	// maxIssuerSubjectBytes bounds the display subject kept with a list.
	maxIssuerSubjectBytes = 1024
)

// CRLLimits are the fixed-config bounds a CRL is held to.
type CRLLimits struct {
	MaxBytes   int
	MaxEntries int
}

// DefaultCRLLimits are the bounds ParseRevocationList applies.
var DefaultCRLLimits = CRLLimits{MaxBytes: DefaultMaxCRLBytes, MaxEntries: DefaultMaxCRLEntries}

// ErrRevocationListInvalid is the sentinel every CRL refusal wraps.
var ErrRevocationListInvalid = errors.New("x509trust: revocation list refused")

// ListRule names the acceptance rule a CRL broke. It is a closed vocabulary so an API can name
// the rule without quoting input.
type ListRule string

const (
	RuleTooLarge          ListRule = "too_large"
	RuleTooManyEntries    ListRule = "too_many_entries"
	RuleMalformed         ListRule = "malformed"
	RuleIssuerChainLength ListRule = "issuer_chain_too_long"
	RuleIssuerChain       ListRule = "issuer_chain_invalid"
	RuleNotCRLSigner      ListRule = "signer_not_crl_signer"
	RuleWrongIssuer       ListRule = "wrong_issuer"
	RuleBadSignature      ListRule = "bad_signature"
	RuleWeakSignature     ListRule = "weak_signature"
	RuleNoNextUpdate      ListRule = "no_next_update"
	RuleThisUpdateFuture  ListRule = "this_update_in_future"
	RuleUpdateOrder       ListRule = "next_update_not_after_this_update"
	RuleNoCRLNumber       ListRule = "no_crl_number"
	RuleBadCRLNumber      ListRule = "bad_crl_number"
	RuleDelta             ListRule = "delta_crl"
	RuleIssuingDistPoint  ListRule = "issuing_distribution_point"
	RuleDuplicateExt      ListRule = "duplicate_extension"
	RuleCriticalExt       ListRule = "unrecognised_critical_extension"
	RuleEntryCertIssuer   ListRule = "entry_certificate_issuer"
	RuleEntryCriticalExt  ListRule = "entry_unrecognised_critical_extension"
	RuleEntryReason       ListRule = "entry_reason"
	RuleEntrySerial       ListRule = "entry_serial"
	RuleDuplicateSerial   ListRule = "duplicate_serial"
)

// ListError is a CRL refusal with its rule. It satisfies errors.Is(err, ErrRevocationListInvalid)
// and its message never contains the CRL's content.
type ListError struct {
	Rule ListRule
}

func (e *ListError) Error() string { return fmt.Sprintf("%v: %s", ErrRevocationListInvalid, e.Rule) }
func (e *ListError) Unwrap() error { return ErrRevocationListInvalid }

// ListRuleOf returns the rule of a refusal, or "" when err is not a ListError.
func ListRuleOf(err error) ListRule {
	var le *ListError
	if errors.As(err, &le) {
		return le.Rule
	}
	return ""
}

func listRefuse(r ListRule) error { return &ListError{Rule: r} }

// RevokedEntry is one revoked certificate of a CRL: by serial under the list's issuer.
type RevokedEntry struct {
	// Serial is the minimal lowercase hex of the positive serial.
	Serial    string
	Reason    RevocationReason
	RevokedAt time.Time
}

// RevocationList is a CRL that passed ParseRevocationList, in the shape to store.
type RevocationList struct {
	// Issuer is the identity of the signer: SHA-256 of its SubjectPublicKeyInfo and of its subject.
	Issuer IssuerID
	// Signer is the CA that signed the CRL; SignerPEM is its canonical PEM.
	Signer    *x509.Certificate
	SignerPEM string
	// IssuerSubject is the signer's subject, for display only.
	IssuerSubject string
	// Number is the CRL number (non-negative, at most 20 octets).
	Number     *big.Int
	ThisUpdate time.Time
	NextUpdate time.Time
	Entries    []RevokedEntry
	// SHA256 is the hex SHA-256 of the DER, for audit and idempotent re-push.
	SHA256 string
}

// Recognised extension identifiers (RFC 5280 5.2, 5.3).
var (
	oidExtAuthorityKeyID   = asn1.ObjectIdentifier{2, 5, 29, 35}
	oidExtCRLNumber        = asn1.ObjectIdentifier{2, 5, 29, 20}
	oidExtDeltaCRLIndic    = asn1.ObjectIdentifier{2, 5, 29, 27}
	oidExtIssuingDistPoint = asn1.ObjectIdentifier{2, 5, 29, 28}
	oidExtReasonCode       = asn1.ObjectIdentifier{2, 5, 29, 21}
	oidExtInvalidityDate   = asn1.ObjectIdentifier{2, 5, 29, 24}
	oidExtCertIssuer       = asn1.ObjectIdentifier{2, 5, 29, 29}
)

// ParseRevocationList validates a CRL for storage and returns it in storable form. It is pure
// and is called only on the operator's upload path: nothing on the connect path parses a CRL or
// checks a CRL signature.
//
// text is PEM (exactly one "X509 CRL" block and nothing else) or DER. issuerChain is the CRL
// signer first, then the CAs that link it to anchor (empty means the anchor itself signed it);
// at most MaxPresentedCerts-1 certificates. The signer is held to VerifyCAChain at now, must
// carry cRLSign, and its subject must equal the CRL's issuer byte for byte.
//
// Refused, each with a ListRule and never quoting input: larger than the limits (checked before
// parsing); anything but one CRL; a signature that does not verify under the signer, or a weak
// signature algorithm; no nextUpdate, nextUpdate not after thisUpdate, thisUpdate more than
// NotBeforeSkew in the future; no CRL number, a negative one or one over 20 octets; a delta CRL;
// ANY Issuing Distribution Point extension (a partial-scope CRL is refused as a whole rather
// than interpreted: indirect, partitioned, user/CA/attribute-only and reason-scoped CRLs
// alike); a duplicated extension; any unrecognised critical CRL extension; an entry with a
// certificateIssuer extension, an unrecognised critical extension, a reason that is not
// assignable on a base CRL (removeFromCRL and the unused value 7 included), a serial that is not
// positive, or a serial listed twice.
//
// An already-expired CRL is accepted: its entries are real revocations and are still enforced;
// staleness is the anchor mode's business. Rules that need storage (the CRL-number high-water
// mark, one identity per key) are the caller's.
func ParseRevocationList(text []byte, issuerChain []*x509.Certificate, anchor *x509.Certificate,
	now time.Time) (*RevocationList, error) {
	return ParseRevocationListLimits(text, issuerChain, anchor, now, DefaultCRLLimits)
}

// ParseRevocationListLimits is ParseRevocationList with explicit bounds.
func ParseRevocationListLimits(text []byte, issuerChain []*x509.Certificate, anchor *x509.Certificate,
	now time.Time, lim CRLLimits) (*RevocationList, error) {
	if lim.MaxBytes <= 0 || lim.MaxEntries <= 0 {
		return nil, listRefuse(RuleMalformed) // unconfigured limits are not "unbounded"
	}
	if len(text) > lim.MaxBytes {
		return nil, listRefuse(RuleTooLarge)
	}
	der, err := crlDER(text)
	if err != nil {
		return nil, err
	}
	rl, perr := x509.ParseRevocationList(der)
	if perr != nil || len(rl.Raw) != len(der) || !strictCRLStructure(der) {
		return nil, listRefuse(RuleMalformed)
	}
	if len(rl.RevokedCertificateEntries) > lim.MaxEntries {
		return nil, listRefuse(RuleTooManyEntries)
	}

	signer, err := crlSigner(issuerChain, anchor, now)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(rl.RawIssuer, signer.RawSubject) {
		return nil, listRefuse(RuleWrongIssuer)
	}
	if weakSigAlg(rl.SignatureAlgorithm) {
		return nil, listRefuse(RuleWeakSignature)
	}
	if rl.CheckSignatureFrom(signer) != nil {
		return nil, listRefuse(RuleBadSignature)
	}

	if rl.NextUpdate.IsZero() {
		return nil, listRefuse(RuleNoNextUpdate)
	}
	if rl.ThisUpdate.Sub(now) > NotBeforeSkew {
		return nil, listRefuse(RuleThisUpdateFuture)
	}
	if !rl.NextUpdate.After(rl.ThisUpdate) {
		return nil, listRefuse(RuleUpdateOrder)
	}
	if err := checkListExtensions(rl.Extensions); err != nil {
		return nil, err
	}
	if rl.Number == nil {
		return nil, listRefuse(RuleNoCRLNumber)
	}
	if rl.Number.Sign() < 0 || rl.Number.BitLen() > maxCRLNumberBits {
		return nil, listRefuse(RuleBadCRLNumber)
	}

	entries, err := convertEntries(rl.RevokedCertificateEntries)
	if err != nil {
		return nil, err
	}
	subject := signer.Subject.String()
	if len(subject) > maxIssuerSubjectBytes {
		subject = subject[:maxIssuerSubjectBytes]
	}
	return &RevocationList{
		Issuer:        IssuerIDOfCA(signer),
		Signer:        signer,
		SignerPEM:     string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: signer.Raw})),
		IssuerSubject: subject,
		Number:        new(big.Int).Set(rl.Number),
		ThisUpdate:    rl.ThisUpdate.UTC(),
		NextUpdate:    rl.NextUpdate.UTC(),
		Entries:       entries,
		SHA256:        sha256Hex(der),
	}, nil
}

// crlDER returns the DER of a CRL given as PEM or DER.
func crlDER(text []byte) ([]byte, error) {
	trimmed := bytes.TrimLeft(text, " \t\r\n")
	if !bytes.HasPrefix(trimmed, []byte("-----BEGIN")) {
		return text, nil
	}
	// pem.Decode skips ahead past a malformed block to the next well-formed one, so a malformed
	// first block followed by a valid CRL would decode cleanly. Require the one block to be the
	// whole input: it starts at the first byte (the prefix) and no other BEGIN marker exists.
	if !bytes.HasPrefix(trimmed, []byte("-----BEGIN X509 CRL-----")) ||
		bytes.Count(trimmed, []byte("-----BEGIN")) != 1 {
		return nil, listRefuse(RuleMalformed)
	}
	block, rest := pem.Decode(trimmed)
	if block == nil || block.Type != "X509 CRL" || len(block.Headers) != 0 ||
		len(bytes.TrimSpace(rest)) != 0 {
		return nil, listRefuse(RuleMalformed)
	}
	return block.Bytes, nil
}

// crlSigner resolves and validates the CRL signer: the anchor, or issuerChain[0] once the chain
// verifies to the anchor.
func crlSigner(issuerChain []*x509.Certificate, anchor *x509.Certificate, now time.Time) (*x509.Certificate, error) {
	if len(issuerChain) > MaxPresentedCerts-1 {
		return nil, listRefuse(RuleIssuerChainLength)
	}
	chain := issuerChain
	if len(chain) == 0 {
		if anchor == nil {
			return nil, listRefuse(RuleIssuerChain)
		}
		chain = []*x509.Certificate{anchor}
	}
	if err := VerifyCAChain(anchor, chain, now); err != nil {
		// The signer may be refused for lacking cRLSign: say so rather than "chain invalid".
		if ReasonOf(err) == ReasonNoCRLSign || ReasonOf(err) == ReasonNotCA {
			return nil, listRefuse(RuleNotCRLSigner)
		}
		return nil, listRefuse(RuleIssuerChain)
	}
	return chain[0], nil
}

func checkListExtensions(exts []pkix.Extension) error {
	seen := map[string]bool{}
	for _, e := range exts {
		id := e.Id.String()
		if seen[id] {
			return listRefuse(RuleDuplicateExt)
		}
		seen[id] = true
		switch {
		case e.Id.Equal(oidExtIssuingDistPoint):
			return listRefuse(RuleIssuingDistPoint)
		case e.Id.Equal(oidExtDeltaCRLIndic):
			return listRefuse(RuleDelta)
		case e.Id.Equal(oidExtCRLNumber):
			var n *big.Int
			if rest, err := asn1.Unmarshal(e.Value, &n); err != nil || len(rest) != 0 {
				return listRefuse(RuleBadCRLNumber)
			}
		case e.Id.Equal(oidExtAuthorityKeyID):
		default:
			if e.Critical {
				return listRefuse(RuleCriticalExt)
			}
		}
	}
	return nil
}

// reasonNames maps the RFC 5280 CRLReason codes a base CRL may carry. 7 is unassigned and 8
// (removeFromCRL) is delta-only; neither is here, so both are refused.
var reasonNames = map[int]RevocationReason{
	0:  RevocationUnspecified,
	1:  RevocationKeyCompromise,
	2:  RevocationCACompromise,
	3:  RevocationAffiliationChanged,
	4:  RevocationSuperseded,
	5:  RevocationCessationOfOperation,
	6:  RevocationCertificateHold,
	9:  RevocationPrivilegeWithdrawn,
	10: RevocationAACompromise,
}

func convertEntries(in []x509.RevocationListEntry) ([]RevokedEntry, error) {
	out := make([]RevokedEntry, 0, len(in))
	seenSerial := make(map[string]struct{}, len(in))
	for _, e := range in {
		serial, ok := SerialHex(e.SerialNumber)
		if !ok {
			return nil, listRefuse(RuleEntrySerial)
		}
		if _, dup := seenSerial[serial]; dup {
			return nil, listRefuse(RuleDuplicateSerial)
		}
		seenSerial[serial] = struct{}{}

		seenExt := map[string]bool{}
		for _, x := range e.Extensions {
			id := x.Id.String()
			if seenExt[id] {
				return nil, listRefuse(RuleDuplicateExt)
			}
			seenExt[id] = true
			switch {
			case x.Id.Equal(oidExtCertIssuer):
				return nil, listRefuse(RuleEntryCertIssuer)
			case x.Id.Equal(oidExtReasonCode):
				var enum asn1.Enumerated
				if rest, err := asn1.Unmarshal(x.Value, &enum); err != nil || len(rest) != 0 {
					return nil, listRefuse(RuleMalformed)
				}
			case x.Id.Equal(oidExtInvalidityDate):
				// Understood, so it is validated rather than waved through; it is
				// non-critical by definition, so a critical one is not this extension.
				var when time.Time
				if x.Critical {
					return nil, listRefuse(RuleEntryCriticalExt)
				}
				if rest, err := asn1.Unmarshal(x.Value, &when); err != nil || len(rest) != 0 {
					return nil, listRefuse(RuleMalformed)
				}
			default:
				if x.Critical {
					return nil, listRefuse(RuleEntryCriticalExt)
				}
			}
		}
		reason, ok := reasonNames[e.ReasonCode]
		if !ok {
			return nil, listRefuse(RuleEntryReason)
		}
		out = append(out, RevokedEntry{Serial: serial, Reason: reason, RevokedAt: e.RevocationTime.UTC()})
	}
	return out, nil
}

// strictCRLStructure walks the DER of a CRL and requires every constructed element to be
// consumed exactly: Go's parser stops reading a sequence when it has what it wants and ignores
// what follows, so a second extensions field (or trailing fields in the TBS, an entry or an
// extension) would sit inside the signed bytes unexamined, out of reach of the extension rules.
// It checks the RFC 5280 shape and nothing else: tags and lengths, not meaning.
//
//	CertificateList  ::= SEQUENCE { tbsCertList, signatureAlgorithm SEQUENCE, signature BIT STRING }
//	TBSCertList      ::= SEQUENCE { version INTEGER, signature SEQUENCE, issuer SEQUENCE,
//	                     thisUpdate Time, nextUpdate Time OPTIONAL, revokedCertificates SEQUENCE OPTIONAL,
//	                     crlExtensions [0] EXPLICIT SEQUENCE OPTIONAL }
//	revoked entry    ::= SEQUENCE { serial INTEGER, revocationDate Time, entryExtensions SEQUENCE OPTIONAL }
//	Extension        ::= SEQUENCE { extnID OID, critical BOOLEAN OPTIONAL, extnValue OCTET STRING }
func strictCRLStructure(der []byte) bool {
	outer, ok := elements(der)
	if !ok || len(outer) != 1 || !isSeq(outer[0]) {
		return false
	}
	top, ok := elements(outer[0].Bytes)
	if !ok || len(top) != 3 || !isSeq(top[0]) || !isSeq(top[1]) ||
		top[2].Class != asn1.ClassUniversal || top[2].Tag != asn1.TagBitString || top[2].IsCompound {
		return false
	}
	tbs, ok := elements(top[0].Bytes)
	if !ok || len(tbs) < 4 {
		return false
	}
	if !isUniversal(tbs[0], asn1.TagInteger) || !isSeq(tbs[1]) || !isSeq(tbs[2]) || !isTime(tbs[3]) {
		return false
	}
	rest := tbs[4:]
	if len(rest) > 0 && isTime(rest[0]) {
		rest = rest[1:]
	}
	if len(rest) > 0 && isSeq(rest[0]) {
		entries, ok := elements(rest[0].Bytes)
		if !ok {
			return false
		}
		for _, e := range entries {
			if !isSeq(e) {
				return false
			}
			parts, ok := elements(e.Bytes)
			if !ok || len(parts) < 2 || len(parts) > 3 ||
				!isUniversal(parts[0], asn1.TagInteger) || !isTime(parts[1]) {
				return false
			}
			if len(parts) == 3 && !strictExtensionSeq(parts[2]) {
				return false
			}
		}
		rest = rest[1:]
	}
	if len(rest) > 0 {
		// The only thing that may follow is the one explicit [0] wrapper around the extensions.
		if len(rest) != 1 || rest[0].Class != asn1.ClassContextSpecific || rest[0].Tag != 0 || !rest[0].IsCompound {
			return false
		}
		inner, ok := elements(rest[0].Bytes)
		if !ok || len(inner) != 1 || !strictExtensionSeq(inner[0]) {
			return false
		}
	}
	return true
}

func strictExtensionSeq(seq asn1.RawValue) bool {
	if !isSeq(seq) {
		return false
	}
	exts, ok := elements(seq.Bytes)
	if !ok {
		return false
	}
	for _, e := range exts {
		if !isSeq(e) {
			return false
		}
		parts, ok := elements(e.Bytes)
		if !ok || len(parts) < 2 || len(parts) > 3 || !isUniversal(parts[0], asn1.TagOID) {
			return false
		}
		if len(parts) == 3 && !isUniversal(parts[1], asn1.TagBoolean) {
			return false
		}
		if !isUniversal(parts[len(parts)-1], asn1.TagOctetString) {
			return false
		}
	}
	return true
}

// elements splits b into its consecutive top-level DER elements; false when anything does not
// parse or bytes are left over.
func elements(b []byte) ([]asn1.RawValue, bool) {
	var out []asn1.RawValue
	for len(b) > 0 {
		var rv asn1.RawValue
		rest, err := asn1.Unmarshal(b, &rv)
		if err != nil {
			return nil, false
		}
		out = append(out, rv)
		b = rest
	}
	return out, true
}

func isUniversal(v asn1.RawValue, tag int) bool {
	return v.Class == asn1.ClassUniversal && v.Tag == tag && !v.IsCompound
}

func isSeq(v asn1.RawValue) bool {
	return v.Class == asn1.ClassUniversal && v.Tag == asn1.TagSequence && v.IsCompound
}

func isTime(v asn1.RawValue) bool {
	return v.Class == asn1.ClassUniversal && !v.IsCompound && (v.Tag == asn1.TagUTCTime || v.Tag == asn1.TagGeneralizedTime)
}
