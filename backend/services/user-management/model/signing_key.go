// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"time"

	"gorm.io/gorm"
)

// SigningKey is an instance-global RSA keypair used to sign and verify the
// platform's RS256 JWTs (ADR-008). It is deliberately NOT tenant-scoped: a
// single keypair serves the whole instance, and it is read at startup before
// any tenant is known.
//
// The row holds the PUBLIC half only, and there is no field that could hold the
// private one. The private half is sealed in the instance's secret store
// (envelope-encrypted under the instance root key, like every other stored
// credential), under a handle derived from the public key — so a database read, a
// backup or a WAL archive yields no key that can sign a token, and a cleartext
// private key cannot be persisted here by mistake.
//
// Exactly one row is Active — the key currently signing new tokens, and the only
// key with a private half at all. A rotation generates a new Active key and
// demotes the previous one (Active=false, RetiredAt set) and deletes the demoted
// key's private half in the same transaction: a retired key only ever verifies.
// Its public half is still served in the JWKS so tokens it signed verify until
// they expire, and the row is pruned once past the retention window. The key id
// (kid) is not stored — it is derived from the public key (RFC 7638 thumbprint)
// wherever needed, and so is the secret-store handle of the private half.
type SigningKey struct {
	gorm.Model
	Active       bool       `gorm:"not null;default:true;index"`
	PublicKeyPem string     `gorm:"not null;type:text"`
	RetiredAt    *time.Time `gorm:"index"`
}
