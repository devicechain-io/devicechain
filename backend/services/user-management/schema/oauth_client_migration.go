// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"github.com/devicechain-io/dc-user-management/iam"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewOAuthClientSchema creates the iam_oauth_clients table (ADR-047): the OAuth
// 2.1 client registry the Authorization Server validates the authorization-code
// flow against. Additive — no existing table is touched.
func NewOAuthClientSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260712150000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&iam.OAuthClient{})
		},
		Rollback: func(tx *gorm.DB) error {
			return dropTables(tx, []string{"iam_oauth_clients"})
		},
	}
}
