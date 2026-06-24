// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// User is a tenant-scoped identity that can authenticate to the platform. The
// username is globally unique so that login(username, password) can resolve the
// acting tenant from the user record (ADR-008) without the caller naming a
// tenant. The bcrypt password hash is stored inline; it is never exposed
// through the GraphQL API.
type User struct {
	gorm.Model
	rdb.TenantScoped
	Username     string `gorm:"uniqueIndex;not null;size:128"`
	Email        string `gorm:"size:256"`
	FirstName    string `gorm:"size:128"`
	LastName     string `gorm:"size:128"`
	Enabled      bool   `gorm:"not null;default:true"`
	PasswordHash string `gorm:"not null;size:256" json:"-"`
}

// SigningKey is the instance-global RSA keypair used to sign and verify the
// platform's RS256 JWTs (ADR-008). It is deliberately NOT tenant-scoped: a
// single keypair serves the whole instance, and it is read at startup before
// any tenant is known. Exactly one row is Active.
type SigningKey struct {
	gorm.Model
	Active        bool   `gorm:"not null;default:true;index"`
	PrivateKeyPem string `gorm:"not null;type:text" json:"-"`
	PublicKeyPem  string `gorm:"not null;type:text"`
}
