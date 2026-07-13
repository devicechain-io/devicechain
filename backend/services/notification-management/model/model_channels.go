// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/secrets"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// NotificationChannel is a tenant's configured delivery endpoint (ADR-017): a
// concrete instance of a ChannelType (smtp, webhook) with the connection config a
// routing policy delivers through. Config holds the type-specific, non-secret
// connection settings (SMTP host/port/from; webhook URL/method/headers) as opaque
// JSON so each adapter owns its own shape without a column per setting.
//
// The one piece that must never be read back — the SMTP password / webhook bearer
// token — is NOT a column here. As of ADR-059 (S3) it lives in the envelope-
// encrypted secret store, keyed by the channel's tenant-scoped handle
// (ChannelSecretRef): the write path Puts it, dispatch Resolves it server-internal
// at delivery time, and the read API exposes only hasSecret (store.Exists). This
// replaces the earlier reversible plaintext column, gaining encryption-at-rest and
// removing the log-leak footgun, without changing what the client sees.
type NotificationChannel struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity

	ChannelType string
	Config      *datatypes.JSON
	Enabled     bool
}

// ChannelSecretName is the stable secret-store handle name for a channel's delivery
// secret: "channel/{id}/secret". It is keyed by the channel's immutable numeric ID,
// NOT its token: a channel's token is mutable (an update may rename it), and keying
// the secret by a mutable token would silently orphan the secret on a rename (the
// SMTP/webhook auth would then fail with no configuration change). The ID never
// changes, so the handle is stable for the channel's whole life.
func ChannelSecretName(id uint) string { return fmt.Sprintf("channel/%d/secret", id) }

// ChannelSecretRef builds the tenant-scoped SecretRef for a channel's delivery
// secret from the acting tenant in ctx and the channel's immutable id. It fails
// closed (ErrNoTenant) when no tenant is bound, so a secret operation can never
// cross a tenant boundary (ADR-059).
func ChannelSecretRef(ctx context.Context, id uint) (secrets.SecretRef, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		return secrets.SecretRef{}, core.ErrNoTenant
	}
	return secrets.SecretRef{Scope: secrets.ScopeTenant, Tenant: tenant, Name: ChannelSecretName(id)}, nil
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
