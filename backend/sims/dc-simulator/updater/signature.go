// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
)

// The signature half of the device contract. A SIGNED assignment carries a detached signature
// over the artifact's RAW 32-byte SHA-256 digest (not its hex or "sha256:" text), the algorithm,
// and the keyId: the lowercase hex SHA-256 of the signer's public key as DER
// SubjectPublicKeyInfo. The device holds its own trust set of public keys and decides; the
// platform only relays the signature.
//
// Like everything in this package the literals are the device's own, not imported from the
// platform's contract package.
const (
	trustUnverified = "UNVERIFIED"
	trustSigned     = "SIGNED"

	algECDSAP256SHA256 = "ECDSA_P256_SHA256"
	algEd25519         = "ED25519"

	// The failure codes (stage FAILED, failing at DOWNLOADED like VERIFICATION_FAILED).
	codeSignatureInvalid = "SIGNATURE_INVALID"
	codeUnknownSigner    = "UNKNOWN_SIGNER"
)

// RequireSignatureOption is a convenience for Config.RequireSignature.
func RequireSignatureOption(b bool) *bool { return &b }

// ErrBadTrustedKey is returned by Open for a TrustedKeys entry that is not a usable public key.
// It never carries the offending PEM: a private key pasted by mistake must not be echoed.
var ErrBadTrustedKey = errors.New("updater: TrustedKeys entry is not a PEM ECDSA P-256 or Ed25519 public key")

// parseTrustedKeys maps keyId to public key for every PEM block in the entries.
func parseTrustedKeys(pems []string) (map[string]any, error) {
	keys := map[string]any{}
	for i, p := range pems {
		rest := []byte(p)
		found := 0
		for {
			var blk *pem.Block
			blk, rest = pem.Decode(rest)
			if blk == nil {
				break
			}
			found++
			if blk.Type != "PUBLIC KEY" {
				return nil, fmt.Errorf("%w (entry %d: block type %q)", ErrBadTrustedKey, i, blk.Type)
			}
			pub, err := x509.ParsePKIXPublicKey(blk.Bytes)
			if err != nil {
				return nil, fmt.Errorf("%w (entry %d)", ErrBadTrustedKey, i)
			}
			switch k := pub.(type) {
			case *ecdsa.PublicKey:
				if k.Curve != elliptic.P256() {
					return nil, fmt.Errorf("%w (entry %d: not P-256)", ErrBadTrustedKey, i)
				}
			case ed25519.PublicKey:
			default:
				return nil, fmt.Errorf("%w (entry %d)", ErrBadTrustedKey, i)
			}
			// The canonical DER of the parsed key, not the bytes as the PEM happened to carry them.
			der, err := x509.MarshalPKIXPublicKey(pub)
			if err != nil {
				return nil, fmt.Errorf("%w (entry %d)", ErrBadTrustedKey, i)
			}
			sum := sha256.Sum256(der)
			keys[hex.EncodeToString(sum[:])] = pub
		}
		if found == 0 {
			return nil, fmt.Errorf("%w (entry %d: no PEM block)", ErrBadTrustedKey, i)
		}
	}
	return keys, nil
}

// requireSignature is the effective policy: signatures are required unless the config says not.
func (u *Updater) requireSignature() bool {
	return u.cfg.RequireSignature == nil || *u.cfg.RequireSignature
}

// checkSignature is the device's authenticity decision over the digest it computed from the bytes
// it downloaded. It returns an empty code when the update may proceed.
func (u *Updater) checkSignature(a *attemptState, digest []byte) (code, detail string) {
	switch a.Trust {
	case "", trustUnverified:
		if u.requireSignature() {
			return codeUnknownSigner, "unsigned update refused: this device requires a signature"
		}
		return "", ""
	case trustSigned:
	default:
		return codeSignatureInvalid, "unsupported trust " + a.Trust
	}
	pub, ok := u.keys[a.KeyID]
	if !ok {
		return codeUnknownSigner, "signer " + a.KeyID + " is not in the trust set"
	}
	sig, err := base64.StdEncoding.Strict().DecodeString(a.Signature)
	if err != nil || len(sig) == 0 {
		return codeSignatureInvalid, "signature is not standard base64"
	}
	if u.faults.CorruptSignature {
		sig = append([]byte(nil), sig...)
		sig[len(sig)-1] ^= 0x01
	}
	switch a.Algorithm {
	case algECDSAP256SHA256:
		if k, ok := pub.(*ecdsa.PublicKey); ok && ecdsa.VerifyASN1(k, digest, sig) {
			return "", ""
		}
	case algEd25519:
		if k, ok := pub.(ed25519.PublicKey); ok && ed25519.Verify(k, digest, sig) {
			return "", ""
		}
	default:
		return codeSignatureInvalid, "unsupported algorithm " + a.Algorithm
	}
	return codeSignatureInvalid, "signature does not verify against signer " + a.KeyID
}
