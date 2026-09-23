// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"crypto/rand"
	"encoding/base64"

	"gorm.io/gorm"
)

// NewSessionEpoch returns a fresh session value: 128 bits from crypto/rand,
// base64url-encoded (22 characters). It is not a secret — it rides signed tokens in
// the clear — but it must be unpredictable and never repeat, because a value that
// came round again would revive every token minted under it.
//
// crypto/rand.Read never returns an error on the platforms Go supports (it panics
// if the system source is unusable), so there is no error to hand back.
func NewSessionEpoch() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// BeforeCreate gives every new identity row a fresh session epoch, OVERWRITING any
// value the caller set. A new row never inherits a session: that one rule is what
// makes deleting an identity and creating it again with the same email end the old
// person's refresh tokens, and it covers CreateIdentity, SeedSuperuser and every
// test fixture without any of them having to remember.
//
// Only inserts reach it. The association writes on an existing identity
// (ReplaceSystemRoles, membership changes) save through an UPDATE, so they never
// rotate the epoch — which the store tests pin.
func (i *Identity) BeforeCreate(*gorm.DB) error {
	i.SessionEpoch = NewSessionEpoch()
	return nil
}
