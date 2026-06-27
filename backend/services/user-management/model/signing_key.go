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
// Exactly one row is Active — the key currently signing new tokens. A rotation
// generates a new Active key and demotes the previous one (Active=false,
// RetiredAt set); retired keys are kept and their public halves are still served
// in the JWKS so tokens they signed verify until they expire, then pruned once
// past the retention window. The key id (kid) is not stored — it is derived from
// the public key (RFC 7638 thumbprint) wherever needed.
type SigningKey struct {
	gorm.Model
	Active        bool       `gorm:"not null;default:true;index"`
	PrivateKeyPem string     `gorm:"not null;type:text" json:"-"`
	PublicKeyPem  string     `gorm:"not null;type:text"`
	RetiredAt     *time.Time `gorm:"index"`
}
