// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"github.com/devicechain-io/dc-user-management/iam"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewOAuthClientConfidential adds the secret_hash column to iam_oauth_clients
// (ADR-047 confidential-client fold-in): a confidential client authenticates to
// the token endpoint with a client secret whose bcrypt hash lives here (empty ⇔ a
// public PKCE client). Additive — AutoMigrate adds the new column to the existing
// table; existing/public clients keep an empty hash and are unaffected.
func NewOAuthClientConfidential() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260715120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&iam.OAuthClient{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropColumn(&iam.OAuthClient{}, "secret_hash")
		},
	}
}
