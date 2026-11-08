// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admission

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const receiptDomain = "devicechain.admission.receipt.v1\n"

var (
	// ErrUnknownKey: the receipt names a key the verifier does not hold.
	ErrUnknownKey = errors.New("receipt signed under an unknown key")
	// ErrBadSignature: the signature does not verify over the receipt's content.
	ErrBadSignature = errors.New("receipt signature does not verify")
	// ErrProposalMismatch: the receipt is genuine but was issued for a different proposal.
	ErrProposalMismatch = errors.New("receipt does not match the proposal")
	// ErrNotAdmitted: the receipt is genuine and matches, but it is a refusal.
	ErrNotAdmitted = errors.New("receipt is a refusal")
	// ErrOutsideWindow: the receipt admits the proposal, but not at this time.
	ErrOutsideWindow = errors.New("outside the admitted window")
)

// Receipt is DeviceChain's signed answer to one proposal. Every field except Signature is
// signed; the copies of the proposal's submitter, tenant, action and window are there so a
// receipt can be read on its own, and Verify checks them against the proposal as well as the
// digest.
type Receipt struct {
	V              int       `json:"v"`
	ProposalDigest string    `json:"proposalDigest"`
	Submitter      string    `json:"submitter"`
	Tenant         string    `json:"tenant"`
	Action         string    `json:"action"`
	NotBefore      time.Time `json:"notBefore"`
	NotAfter       time.Time `json:"notAfter"`
	Verdict        Verdict   `json:"verdict"`
	Reasons        []Reason  `json:"reasons,omitempty"`
	PolicyVersion  string    `json:"policyVersion"`
	PolicyDigest   string    `json:"policyDigest"`
	IssuedAt       time.Time `json:"issuedAt"`
	KeyID          string    `json:"keyId"`
	Signature      []byte    `json:"signature,omitempty"`
}

// signedReceipt is the signed form: fixed field order, times as UTC text. Signing this rather
// than the Receipt's own JSON means a receipt that travelled through a parser that kept a
// different time zone still verifies, because the instant, not its spelling, is signed.
type signedReceipt struct {
	V              int      `json:"v"`
	ProposalDigest string   `json:"proposalDigest"`
	Submitter      string   `json:"submitter"`
	Tenant         string   `json:"tenant"`
	Action         string   `json:"action"`
	NotBefore      string   `json:"notBefore"`
	NotAfter       string   `json:"notAfter"`
	Verdict        Verdict  `json:"verdict"`
	Reasons        []Reason `json:"reasons"`
	PolicyVersion  string   `json:"policyVersion"`
	PolicyDigest   string   `json:"policyDigest"`
	IssuedAt       string   `json:"issuedAt"`
	KeyID          string   `json:"keyId"`
}

func (r Receipt) signingBytes() ([]byte, error) {
	reasons := r.Reasons
	if reasons == nil {
		reasons = []Reason{}
	}
	b, err := json.Marshal(signedReceipt{
		V: r.V, ProposalDigest: r.ProposalDigest, Submitter: r.Submitter, Tenant: r.Tenant,
		Action: r.Action, NotBefore: utcText(r.NotBefore), NotAfter: utcText(r.NotAfter),
		Verdict: r.Verdict, Reasons: reasons, PolicyVersion: r.PolicyVersion,
		PolicyDigest: r.PolicyDigest, IssuedAt: utcText(r.IssuedAt), KeyID: r.KeyID,
	})
	if err != nil {
		return nil, err
	}
	return append([]byte(receiptDomain), b...), nil
}

// ID identifies the receipt by its signed content: "sha256:" and the hex digest of the bytes
// the signature covers.
func (r Receipt) ID() string {
	b, err := r.signingBytes()
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return digestPrefix + hex.EncodeToString(sum[:])
}

// Issuer signs receipts under one named key.
type Issuer struct {
	keyID string
	key   ed25519.PrivateKey
}

// NewIssuer builds an issuer. The key id is carried in every receipt so a verifier holding
// several keys knows which one to use.
func NewIssuer(keyID string, key ed25519.PrivateKey) (*Issuer, error) {
	if keyID == "" {
		return nil, errors.New("admission: key id is empty")
	}
	if len(key) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("admission: private key is %d bytes, want %d", len(key), ed25519.PrivateKeySize)
	}
	return &Issuer{keyID: keyID, key: key}, nil
}

// Issue evaluates the proposal against the policy at now and signs the answer. A refusal is a
// receipt too: it is evidence that the head-end was told no. Only a malformed proposal or an
// invalid policy returns an error and no receipt.
func (i *Issuer) Issue(now time.Time, p Proposal, pol Policy) (Receipt, error) {
	d, err := Admit(now, p, pol)
	if err != nil {
		return Receipt{}, err
	}
	pd, err := p.Digest()
	if err != nil {
		return Receipt{}, err
	}
	pold, err := pol.Digest()
	if err != nil {
		return Receipt{}, err
	}
	r := Receipt{
		V: 1, ProposalDigest: pd, Submitter: p.Submitter, Tenant: p.Tenant, Action: p.Action,
		NotBefore: p.NotBefore.UTC(), NotAfter: p.NotAfter.UTC(), Verdict: d.Verdict,
		Reasons: d.Reasons, PolicyVersion: pol.Version, PolicyDigest: pold,
		IssuedAt: now.UTC(), KeyID: i.keyID,
	}
	b, err := r.signingBytes()
	if err != nil {
		return Receipt{}, err
	}
	r.Signature = ed25519.Sign(i.key, b)
	return r, nil
}

// Verifier checks receipts against the public keys it was given.
type Verifier struct {
	keys map[string]ed25519.PublicKey
}

// NewVerifier builds a verifier over a set of named public keys.
func NewVerifier(keys map[string]ed25519.PublicKey) (*Verifier, error) {
	if len(keys) == 0 {
		return nil, errors.New("admission: a verifier needs at least one key")
	}
	held := make(map[string]ed25519.PublicKey, len(keys))
	for id, k := range keys {
		if id == "" {
			return nil, errors.New("admission: key id is empty")
		}
		if len(k) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("admission: public key %q is %d bytes, want %d", id, len(k), ed25519.PublicKeySize)
		}
		held[id] = append(ed25519.PublicKey(nil), k...)
	}
	return &Verifier{keys: held}, nil
}

// VerifySignature checks that the receipt is genuine, without reference to any proposal.
func (v *Verifier) VerifySignature(r Receipt) error {
	k, ok := v.keys[r.KeyID]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownKey, r.KeyID)
	}
	b, err := r.signingBytes()
	if err != nil {
		return err
	}
	if !ed25519.Verify(k, b, r.Signature) {
		return ErrBadSignature
	}
	return nil
}

// Verify checks that the receipt is genuine AND was issued for this proposal. It says nothing
// about the verdict or the time; Authorizes does.
func (v *Verifier) Verify(r Receipt, p Proposal) error {
	if err := v.VerifySignature(r); err != nil {
		return err
	}
	pd, err := p.Digest()
	if err != nil {
		return err
	}
	if pd != r.ProposalDigest {
		return fmt.Errorf("%w: digest", ErrProposalMismatch)
	}
	// The digest already covers these; the check makes a receipt whose readable copies
	// disagree with its own digest unusable, rather than trusting whichever a reader looks at.
	if r.Submitter != p.Submitter || r.Tenant != p.Tenant || r.Action != p.Action ||
		!r.NotBefore.Equal(p.NotBefore) || !r.NotAfter.Equal(p.NotAfter) {
		return fmt.Errorf("%w: readable fields", ErrProposalMismatch)
	}
	return nil
}

// Authorizes reports whether the receipt lets the proposal run at the instant at: genuine,
// for this proposal, admitted, and at inside [NotBefore, NotAfter). nil means yes.
func (v *Verifier) Authorizes(r Receipt, p Proposal, at time.Time) error {
	if err := v.Verify(r, p); err != nil {
		return err
	}
	if r.Verdict != Admitted {
		return ErrNotAdmitted
	}
	if at.Before(r.NotBefore) || !at.Before(r.NotAfter) {
		return ErrOutsideWindow
	}
	return nil
}
