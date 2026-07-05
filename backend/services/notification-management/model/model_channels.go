// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// NotificationChannel is a tenant's configured delivery endpoint (ADR-017): a
// concrete instance of a ChannelType (smtp, webhook) with the connection config a
// routing policy delivers through. Config holds the type-specific, non-secret
// connection settings (SMTP host/port/from; webhook URL/method/headers) as opaque
// JSON so each adapter owns its own shape without a column per setting; Secret
// holds the one piece that must never be read back (SMTP password, webhook bearer
// token).
//
// Secret is stored reversibly, not hashed: unlike a device credential (verified by
// constant-time compare, ADR-014), the sender must present the actual secret to
// the SMTP server / HTTP endpoint at delivery time, so it has to be recoverable.
// It is kept out of the read API at the resolver layer (never selected away in
// SQL, so internal dispatch can load it): the GraphQL type exposes hasSecret, not
// the value. At-rest encryption of this column is the same future cross-cutting
// decision as the device-credential/provision-secret stores, not a notification
// concern to solve alone.
type NotificationChannel struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity

	ChannelType string
	Config      *datatypes.JSON
	// Secret is the write-only delivery secret; size caps it well above any realistic
	// SMTP password or bearer token. NULL means "no secret configured".
	Secret  sql.NullString `gorm:"size:4096"`
	Enabled bool
}

// NotificationChannelCreateRequest is the data required to create or update a
// channel. Config is a JSON document (validated well-formed on write; deep
// per-adapter validation lands with the adapter in N.C). Secret is write-only:
// on create it sets the secret; on update a nil Secret LEAVES THE EXISTING SECRET
// UNCHANGED (so editing a channel's other fields does not require re-entering the
// password), while a non-nil Secret replaces it — an explicit empty string clears
// it. This preserve-on-omit semantics is a deliberate improvement over the device
// credential update (which nulls the secret when omitted).
type NotificationChannelCreateRequest struct {
	Token       string
	Name        *string
	Description *string
	ChannelType string
	Config      *string
	Secret      *string
	Enabled     bool
	Metadata    *string
}

// NotificationChannelSearchCriteria locates channels by optional type/enabled
// filters.
type NotificationChannelSearchCriteria struct {
	rdb.Pagination
	ChannelType *string
	Enabled     *bool
}

// NotificationChannelSearchResults is a page of channel search results.
type NotificationChannelSearchResults struct {
	Results    []NotificationChannel
	Pagination rdb.SearchResultsPagination
}
