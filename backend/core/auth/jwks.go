// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
)

// b64 is the unpadded base64url encoding JWK uses for its big-endian integers
// (RFC 7517 §3) and for the RFC 7638 thumbprint.
var b64 = base64.RawURLEncoding

// JWK is a JSON Web Key (RFC 7517) restricted to the RSA public keys DeviceChain
// uses to verify RS256 tokens. Only the members a verifier needs are modeled.
type JWK struct {
	Kty string `json:"kty"`           // key type — always "RSA"
	N   string `json:"n"`             // base64url big-endian modulus
	E   string `json:"e"`             // base64url big-endian public exponent
	Kid string `json:"kid"`           // key id (RFC 7638 thumbprint)
	Use string `json:"use,omitempty"` // "sig"
	Alg string `json:"alg,omitempty"` // "RS256"
}

// JWKS is a JWK Set (RFC 7517 §5): the public half of every signing key the
// issuer will currently accept, including a key just rotated out but still inside
// its grace window so tokens it signed verify until they expire.
type JWKS struct {
	Keys []JWK `json:"keys"`
}

// Thumbprint computes the RFC 7638 JWK thumbprint of an RSA public key (the
// SHA-256 of the canonical {"e","kty","n"} JSON, base64url-encoded). It is a
// stable, collision-resistant key id derived purely from the key material, so
// issuer and verifier agree on a key's id without coordinating out of band.
func Thumbprint(pub *rsa.PublicKey) string {
	// Required members only, lexicographic order, no whitespace (RFC 7638 §3.2).
	canonical := fmt.Sprintf(`{"e":%q,"kty":"RSA","n":%q}`,
		b64.EncodeToString(bigEndianExponent(pub.E)),
		b64.EncodeToString(pub.N.Bytes()),
	)
	sum := sha256.Sum256([]byte(canonical))
	return b64.EncodeToString(sum[:])
}

// PublicKeyToJWK encodes an RSA public key as a JWK, deriving its kid from the
// RFC 7638 thumbprint so the same key always maps to the same id.
func PublicKeyToJWK(pub *rsa.PublicKey) JWK {
	return JWK{
		Kty: "RSA",
		N:   b64.EncodeToString(pub.N.Bytes()),
		E:   b64.EncodeToString(bigEndianExponent(pub.E)),
		Kid: Thumbprint(pub),
		Use: "sig",
		Alg: signingMethod,
	}
}

// PublicKey reconstructs the RSA public key from a JWK.
func (j JWK) PublicKey() (*rsa.PublicKey, error) {
	if j.Kty != "RSA" {
		return nil, fmt.Errorf("auth: unsupported JWK key type %q", j.Kty)
	}
	nBytes, err := b64.DecodeString(j.N)
	if err != nil {
		return nil, fmt.Errorf("auth: invalid JWK modulus: %w", err)
	}
	eBytes, err := b64.DecodeString(j.E)
	if err != nil {
		return nil, fmt.Errorf("auth: invalid JWK exponent: %w", err)
	}
	e := new(big.Int).SetBytes(eBytes)
	// A sane RSA exponent fits in an int and is at least 3; reject anything odd
	// so a malformed JWK cannot produce a degenerate key.
	if !e.IsInt64() || e.Int64() < 3 || e.Int64() > (1<<31-1) {
		return nil, errors.New("auth: JWK exponent out of range")
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: int(e.Int64())}, nil
}

// BuildJWKS encodes RSA public keys as a marshaled JWK Set document for serving
// from the issuer's JWKS endpoint.
func BuildJWKS(pubs []*rsa.PublicKey) ([]byte, error) {
	set := JWKS{Keys: make([]JWK, 0, len(pubs))}
	for _, pub := range pubs {
		set.Keys = append(set.Keys, PublicKeyToJWK(pub))
	}
	return json.Marshal(set)
}

// ParseJWKS parses a JWK Set document.
func ParseJWKS(data []byte) (*JWKS, error) {
	var set JWKS
	if err := json.Unmarshal(data, &set); err != nil {
		return nil, fmt.Errorf("auth: invalid JWKS document: %w", err)
	}
	return &set, nil
}

// keyMap reconstructs each JWK into an RSA public key, keyed by the key's true
// RFC 7638 thumbprint recomputed from the key material — the document's kid field
// is not trusted as the lookup key, so a tampered kid cannot misdirect lookups.
// An empty set is an error so a verifier never ends up holding zero keys.
func (s *JWKS) keyMap() (map[string]*rsa.PublicKey, error) {
	keys := make(map[string]*rsa.PublicKey, len(s.Keys))
	for _, jwk := range s.Keys {
		pub, err := jwk.PublicKey()
		if err != nil {
			return nil, err
		}
		keys[Thumbprint(pub)] = pub
	}
	if len(keys) == 0 {
		return nil, errors.New("auth: JWKS contains no keys")
	}
	return keys, nil
}

// bigEndianExponent renders the RSA public exponent as a minimal big-endian byte
// slice with no leading zero bytes, the form JWK and RFC 7638 require.
func bigEndianExponent(e int) []byte {
	return big.NewInt(int64(e)).Bytes()
}
