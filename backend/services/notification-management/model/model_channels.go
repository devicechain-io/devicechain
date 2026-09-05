// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
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

// DefaultOrder implements rdb.Sortable: the registry default — newest first, tiebroken
// on the entity's per-tenant token. created_at alone is not total (a bootstrap or an
// import writes several channels inside one clock tick, and rows that tie are free to
// reshuffle between pages), and token is unique per tenant, so it closes the order.
// Ascending on the tiebreak keeps a tie group's internal order stable and readable
// rather than merely deterministic.
func (NotificationChannel) DefaultOrder() string {
	return "notification_channels.created_at DESC, notification_channels.token ASC"
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

// NotificationChannelCreateRequest is the data required to CREATE a channel. Config is
// a JSON document (validated well-formed on write; deep per-adapter validation lands
// with the adapter in N.C). Secret is write-only: on create it sets the secret, and a
// nil or empty Secret stores none.
//
// It is no longer the update input. NotificationChannelUpdateRequest is, and it carries
// no token: a channel's RENAME is its own mutation now (RenameNotificationChannel), so
// the two tokens that used to disagree inside one payload cannot both exist.
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

// NotificationChannelUpdateRequest is the three-state update input: an OMITTED field
// leaves the stored value alone, an explicit NULL clears it, and a value sets it. The
// folds live in core's graphql package (ApplyTo for a nullable column, ApplyToRequired
// for one that cannot be cleared).
//
// It carries NO Token. A channel's token moves through RenameNotificationChannel, which
// says what it is doing in its own name; an update input carrying a second token could
// only ever disagree with the argument that names the record.
//
// # 🔴 THE SECRET'S THIRD STATE IS NEW, AND IT IS A CLEAR
//
// Under the create input, `secret` had two reachable meanings on update: nil PRESERVED
// (you cannot read a secret back to re-send it) and a non-nil value replaced, with the
// empty string as the only way to remove one. That made the field the exact inverse of
// every other field on the same request — null preserves, "" destroys — which is a trap
// worth a warning box in the API reference.
//
// Three states remove the inversion instead of documenting it:
//
//	ABSENT   -> the stored secret is PRESERVED (unchanged from before)
//	NULL     -> the stored secret is CLEARED   (the operation "" used to be the only
//	            spelling of; now it is spelled the way every other field spells it)
//	""       -> also cleared, unchanged from before, so a client that already sends the
//	            empty string to remove a secret keeps working
//	"value"  -> the secret is rotated to that value
//
// Both spellings of the clear are honoured because a secret is the one field where
// getting this wrong is silent: a channel whose delivery credential quietly went missing
// keeps its config, keeps returning success on every edit, and stops authenticating at
// the moment an alarm needed to reach a human.
type NotificationChannelUpdateRequest struct {
	Name        dcgraphql.OptionalString
	Description dcgraphql.OptionalString
	ChannelType dcgraphql.OptionalString
	Config      dcgraphql.OptionalString
	Secret      dcgraphql.OptionalString
	Enabled     dcgraphql.OptionalBool
	Metadata    dcgraphql.OptionalString
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
