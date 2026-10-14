// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// Refusal codes of the signer registry and of signing.
const (
	CodeInvalidSigner      = "INVALID_SIGNER"
	CodeSignerKeyRefused   = "SIGNER_KEY_REFUSED"
	CodeSignerExists       = "SIGNER_ALREADY_REGISTERED"
	CodeSignerNotFound     = "SIGNER_NOT_FOUND"
	CodeSignerRevoked      = "SIGNER_REVOKED"
	CodeSignatureInvalid   = "SIGNATURE_INVALID"
	CodeInvalidTrustFilter = "INVALID_TRUST_FILTER"
)

const (
	maxSignerNameLen = 128
	maxPEMBytes      = 4 << 10
	// maxSignatureBytes bounds a decoded signature: an ECDSA P-256 ASN.1 signature is at most 72
	// bytes and an Ed25519 one is 64.
	maxSignatureBytes = 128
	// signerTokenKeyChars is how much of the key id names the signer. The token is only a
	// handle; the key id is the identity, and the unique index on it is what refuses a clash.
	signerTokenKeyChars = 16
)

// ParsedKey is a validated signer public key.
type ParsedKey struct {
	Pub       any
	Algorithm string
	KeyID     string
	// CanonicalPEM is the key re-encoded as a single SPKI "PUBLIC KEY" block, which is what is stored.
	CanonicalPEM string
}

// ParsePublicKey accepts exactly one PEM block holding an SPKI ECDSA P-256 or Ed25519 PUBLIC key.
//
// A PRIVATE key is refused WITHOUT being echoed: the message names no part of the input, and
// nothing here logs it. The platform must never be handed signing material, and a refusal that
// quoted it would put it in an error response, a log line and a client's history.
func ParsePublicKey(input string) (*ParsedKey, error) {
	if len(input) > maxPEMBytes {
		return nil, refuse(CodeInvalidSigner, "publicKeyPem exceeds %d bytes", maxPEMBytes)
	}
	// Any mention of a private key refuses, even in malformed input that would not decode: a
	// truncated or mangled private-key paste is still a private key paste.
	if strings.Contains(strings.ToUpper(input), "PRIVATE KEY") {
		return nil, refuse(CodeSignerKeyRefused,
			"the input contains a private key; register only the PUBLIC key (the platform must never hold signing material)")
	}
	block, rest := pem.Decode([]byte(input))
	if block == nil {
		return nil, refuse(CodeInvalidSigner, "publicKeyPem is not a PEM block")
	}
	if strings.TrimSpace(string(rest)) != "" {
		return nil, refuse(CodeInvalidSigner, "publicKeyPem must hold exactly one PEM block")
	}
	if block.Type != "PUBLIC KEY" {
		return nil, refuse(CodeInvalidSigner, "publicKeyPem must be an SPKI \"PUBLIC KEY\" block")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, refuse(CodeInvalidSigner, "publicKeyPem is not a valid SPKI public key")
	}
	var algorithm string
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		if k.Curve != elliptic.P256() {
			return nil, refuse(CodeSignerKeyRefused, "only ECDSA P-256 and Ed25519 keys are accepted; this is an ECDSA key on another curve")
		}
		algorithm = AlgorithmECDSAP256
	case ed25519.PublicKey:
		algorithm = AlgorithmEd25519
	default:
		return nil, refuse(CodeSignerKeyRefused, "only ECDSA P-256 and Ed25519 keys are accepted")
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, refuse(CodeInvalidSigner, "publicKeyPem is not a valid SPKI public key")
	}
	sum := sha256.Sum256(der)
	return &ParsedKey{
		Pub: pub, Algorithm: algorithm, KeyID: hex.EncodeToString(sum[:]),
		CanonicalPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})),
	}, nil
}

// VerifyDigestSignature checks sig over the raw 32-byte SHA-256 digest: ECDSA P-256 verifies the
// ASN.1 signature of the digest, and Ed25519 signs the digest bytes themselves.
func VerifyDigestSignature(pub any, digest, sig []byte) bool {
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		return ecdsa.VerifyASN1(k, digest, sig)
	case ed25519.PublicKey:
		return ed25519.Verify(k, digest, sig)
	}
	return false
}

// digestBytes decodes a stored "sha256:<64 hex>" digest.
func digestBytes(stored string) ([]byte, error) {
	b, err := hex.DecodeString(strings.TrimPrefix(stored, DigestPrefix))
	if err != nil || len(b) != sha256.Size {
		return nil, errors.New("the artifact's stored digest is not a SHA-256 digest")
	}
	return b, nil
}

// SignerByToken returns the tenant's signer, or nil when there is none (including another
// tenant's: the tenant scope confines the lookup).
func (api *Api) SignerByToken(ctx context.Context, token string) (*ArtifactSigner, error) {
	var found []*ArtifactSigner
	if err := rdb.FindByKeys(api.RDB.DB(ctx), &found, "token", []string{token}); err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, nil
	}
	return found[0], nil
}

// Signers searches the tenant's signers.
func (api *Api) Signers(ctx context.Context, state *string, pag rdb.Pagination) (*SignerSearchResults, error) {
	results := make([]ArtifactSigner, 0)
	db, p := api.RDB.ListOf(ctx, &ArtifactSigner{}, func(q *gorm.DB) *gorm.DB {
		if state != nil {
			q = q.Where("state = ?", *state)
		}
		return q
	}, pag)
	if err := db.Find(&results).Error; err != nil {
		return nil, err
	}
	return &SignerSearchResults{Results: results, Pagination: p}, nil
}

// RegisterSigner records a tenant's trusted public key.
func (api *Api) RegisterSigner(ctx context.Context, name, publicKeyPem string) (*ArtifactSigner, error) {
	if err := api.RefuseDeletedTenant(ctx); err != nil {
		return nil, err
	}
	if _, ok := core.TenantFromContext(ctx); !ok {
		return nil, errors.New("registering a signer requires a tenant")
	}
	key, err := ParsePublicKey(publicKeyPem)
	if err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > maxSignerNameLen {
		return nil, refuse(CodeInvalidSigner, "name must be 1 to %d characters", maxSignerNameLen)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return nil, refuse(CodeInvalidSigner, "name must not contain control characters")
		}
	}
	exists := func() (bool, error) {
		var n int64
		err := api.RDB.DB(ctx).Model(&ArtifactSigner{}).Where("key_id = ?", key.KeyID).Count(&n).Error
		return n > 0, err
	}
	if dup, err := exists(); err != nil {
		return nil, err
	} else if dup {
		return nil, refuse(CodeSignerExists, "a signer with this public key is already registered")
	}
	signer := &ArtifactSigner{
		TokenReference: rdb.TokenReference{Token: "signer-" + key.KeyID[:signerTokenKeyChars]},
		Name:           name, Algorithm: key.Algorithm, PublicKeyPem: key.CanonicalPEM, KeyId: key.KeyID,
		State: SignerActive, CreatedBy: actorOf(ctx),
	}
	if err := api.RDB.DB(ctx).Create(signer).Error; err != nil {
		// A concurrent registration of the same key loses on the unique index; say so by name.
		if dup, qerr := exists(); qerr == nil && dup {
			return nil, refuse(CodeSignerExists, "a signer with this public key is already registered")
		}
		return nil, err
	}
	return signer, nil
}

// RevokeSigner ends a signer's trust. It is terminal and idempotent. No artifact row is written:
// trust is derived, so every artifact the signer vouched for reads differently from this moment.
func (api *Api) RevokeSigner(ctx context.Context, token string) (*ArtifactSigner, error) {
	if err := api.RefuseDeletedTenant(ctx); err != nil {
		return nil, err
	}
	s, err := api.SignerByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, refuse(CodeSignerNotFound, "no signer with token %q", token)
	}
	if s.State == SignerRevoked {
		return s, nil
	}
	now := time.Now().UTC()
	if err := api.RDB.DB(ctx).Model(&ArtifactSigner{ID: s.ID, TokenReference: s.TokenReference}).
		Where("id = ? AND state = ?", s.ID, SignerActive).
		Updates(map[string]any{
			"state": SignerRevoked, "revoked_at": sql.NullTime{Time: now, Valid: true}, "revoked_by": actorOf(ctx),
		}).Error; err != nil {
		return nil, err
	}
	return api.SignerByToken(ctx, token)
}

// AddSignature verifies a signature over the artifact's STORED digest and records it. The
// signature is checked against the digest the artifact was declared (and upload-verified) with,
// never against anything the caller supplies, so a signature over some other digest cannot attach.
// Adding the same signer's signature to an artifact again returns the artifact unchanged.
func (api *Api) AddSignature(ctx context.Context, artifactToken, signerToken, signatureB64 string) (*Artifact, error) {
	if err := api.RefuseDeletedTenant(ctx); err != nil {
		return nil, err
	}
	artifact, err := api.ArtifactByToken(ctx, artifactToken)
	if err != nil {
		return nil, err
	}
	if artifact == nil {
		return nil, ErrNotFound(artifactToken)
	}
	signer, err := api.SignerByToken(ctx, signerToken)
	if err != nil {
		return nil, err
	}
	if signer == nil {
		return nil, refuse(CodeSignerNotFound, "no signer with token %q", signerToken)
	}
	if signer.State != SignerActive {
		return nil, refuse(CodeSignerRevoked, "signer %q is revoked and can no longer sign", signer.Token)
	}
	if len(signatureB64) > 4*maxSignatureBytes {
		return nil, refuse(CodeSignatureInvalid, "the signature is too long to be an ECDSA P-256 or Ed25519 signature")
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(signatureB64))
	if err != nil || len(sig) == 0 || len(sig) > maxSignatureBytes {
		return nil, refuse(CodeSignatureInvalid, "the signature is not valid standard base64 of a signature")
	}
	key, err := ParsePublicKey(signer.PublicKeyPem)
	if err != nil {
		return nil, errors.New("signer " + signer.Token + " holds a key that no longer parses")
	}
	digest, err := digestBytes(artifact.Digest)
	if err != nil {
		return nil, err
	}
	if !VerifyDigestSignature(key.Pub, digest, sig) {
		return nil, refuse(CodeSignatureInvalid,
			"the signature does not verify against artifact %q's digest with signer %q's key", artifact.Token, signer.Token)
	}
	have := func() (bool, error) {
		var n int64
		err := api.RDB.DB(ctx).Model(&ArtifactSignature{}).
			Where("artifact_id = ? AND signer_id = ?", artifact.ID, signer.ID).Count(&n).Error
		return n > 0, err
	}
	if present, err := have(); err != nil {
		return nil, err
	} else if !present {
		row := &ArtifactSignature{ArtifactId: artifact.ID, SignerId: signer.ID, Signature: sig, Actor: actorOf(ctx)}
		if err := api.RDB.DB(ctx).Create(row).Error; err != nil {
			// The same signer racing itself loses on the unique index: the signature is on record.
			if present, qerr := have(); qerr != nil || !present {
				return nil, err
			}
		}
	}
	return api.ArtifactByToken(ctx, artifactToken)
}

// activeSignerIDs is the subquery of the tenant's ACTIVE signers. The tenant predicate is written
// out because a subquery is never executed through the tenant-scope callback.
func (api *Api) activeSignerIDs(ctx context.Context) (*gorm.DB, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return nil, core.ErrNoTenant
	}
	return api.RDB.DB(ctx).Model(&ArtifactSigner{}).Select("id").
		Where("tenant_id = ? AND state = ?", tenant, SignerActive), nil
}

// signedArtifactIDs is the subquery of artifacts holding at least one signature by an ACTIVE signer.
func (api *Api) signedArtifactIDs(ctx context.Context) (*gorm.DB, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return nil, core.ErrNoTenant
	}
	active, err := api.activeSignerIDs(ctx)
	if err != nil {
		return nil, err
	}
	return api.RDB.DB(ctx).Model(&ArtifactSignature{}).Select("artifact_id").
		Where("tenant_id = ? AND signer_id IN (?)", tenant, active), nil
}

// fillTrust sets Trust on every artifact with ONE query however many there are: the ids among them
// that hold a signature by an ACTIVE signer are SIGNED, the rest UNVERIFIED.
func (api *Api) fillTrust(ctx context.Context, artifacts ...*Artifact) error {
	if len(artifacts) == 0 {
		return nil
	}
	ids := make([]uint, 0, len(artifacts))
	for _, a := range artifacts {
		ids = append(ids, a.ID)
	}
	signed, err := api.signedArtifactIDs(ctx)
	if err != nil {
		return err
	}
	var hit []uint
	if err := api.RDB.DB(ctx).Model(&ArtifactSignature{}).Distinct().
		Where("artifact_id IN ? AND artifact_id IN (?)", ids, signed).
		Pluck("artifact_id", &hit).Error; err != nil {
		return err
	}
	signedSet := make(map[uint]bool, len(hit))
	for _, id := range hit {
		signedSet[id] = true
	}
	for _, a := range artifacts {
		a.Trust = TrustUnverified
		if signedSet[a.ID] {
			a.Trust = TrustSigned
		}
	}
	return nil
}

// SoleSignedArtifactCount is how many artifacts would LOSE their only active-signer signature if
// this signer were revoked: the revoke preview. A signer that is already revoked vouches for
// nothing, so it is 0.
func (api *Api) SoleSignedArtifactCount(ctx context.Context, s *ArtifactSigner) (int64, error) {
	if s.State != SignerActive {
		return 0, nil
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return 0, core.ErrNoTenant
	}
	active, err := api.activeSignerIDs(ctx)
	if err != nil {
		return 0, err
	}
	others := api.RDB.DB(ctx).Model(&ArtifactSignature{}).Select("artifact_id").
		Where("tenant_id = ? AND signer_id <> ? AND signer_id IN (?)", tenant, s.ID, active)
	var n int64
	err = api.RDB.DB(ctx).Model(&ArtifactSignature{}).
		Where("signer_id = ? AND artifact_id NOT IN (?)", s.ID, others).Count(&n).Error
	return n, err
}

// SignatureView is one signature on record with the signer that made it.
type SignatureView struct {
	Signature ArtifactSignature
	Signer    ArtifactSigner
}

// CountsTowardTrust reports whether the signature currently vouches for its artifact: its signer
// is ACTIVE. A revoked signer's signatures stay on record and stop counting.
func (v SignatureView) CountsTowardTrust() bool { return v.Signer.State == SignerActive }

// SignaturesOf returns the signatures on the given artifacts, grouped by artifact id, oldest
// first, with their signers. It issues two queries however many artifacts there are, both through
// the tenant-scoped handle, so another tenant's rows are never returned.
func (api *Api) SignaturesOf(ctx context.Context, artifactIDs ...uint) (map[uint][]SignatureView, error) {
	out := make(map[uint][]SignatureView, len(artifactIDs))
	if len(artifactIDs) == 0 {
		return out, nil
	}
	var sigs []ArtifactSignature
	if err := api.RDB.DB(ctx).Where("artifact_id IN ?", artifactIDs).
		Order("created_at ASC, id ASC").Find(&sigs).Error; err != nil {
		return nil, err
	}
	if len(sigs) == 0 {
		return out, nil
	}
	signerIDs := make([]uint, 0, len(sigs))
	for _, sg := range sigs {
		signerIDs = append(signerIDs, sg.SignerId)
	}
	var signers []ArtifactSigner
	if err := api.RDB.DB(ctx).Where("id IN ?", signerIDs).Find(&signers).Error; err != nil {
		return nil, err
	}
	byID := make(map[uint]ArtifactSigner, len(signers))
	for _, sn := range signers {
		byID[sn.ID] = sn
	}
	for _, sg := range sigs {
		sn, ok := byID[sg.SignerId]
		if !ok {
			// A signature whose signer cannot be read is an error, not a signature that
			// quietly does not count.
			return nil, fmt.Errorf("signature %d refers to a signer that cannot be read", sg.ID)
		}
		out[sg.ArtifactId] = append(out[sg.ArtifactId], SignatureView{Signature: sg, Signer: sn})
	}
	return out, nil
}
