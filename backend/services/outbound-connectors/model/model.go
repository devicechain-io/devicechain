// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/secrets"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Connector is a tenant-scoped, versioned outbound-connector definition (ADR-060
// Tier 2). It is the registered target a `publish` REACT action delivers through:
// a {type, config} the service turns into a bounded single-message send (the Bento
// output config is generated from it — the tenant never writes Bento YAML), plus an
// optional write-only credential sealed in the ADR-059 secret store (never a column,
// never returned across the API). It mirrors the ADR-039 versioned-resource pattern
// (Dashboard): the mutable row is the DRAFT; publishing freezes it into an immutable
// ConnectorVersion; rollback re-drafts a snapshot. Dispatch resolves the connector by
// token to its latest published version (slice C4b) — the draft is work-in-progress.
type Connector struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	// Type is the connector kind, one of the registered ConnectorType vocabulary
	// (mqtt/kafka/aws_sns/aws_sqs/gcp_pubsub). It selects the Bento output the send
	// runs through; validated against the vocabulary at write.
	Type string `gorm:"not null;size:64"`
	// Config is the opaque, per-type connection configuration (broker URL, topic,
	// region, …) stored as a JSON object. The backend validates it is a well-formed
	// JSON object at write; the per-type field validation lives with each output
	// generator (slices C4b/C4c). Credentials are NEVER stored here — they live in
	// the secret store under the connector's handle (see ConnectorSecretRef).
	Config datatypes.JSON `gorm:"not null"`
}

// ConnectorVersion is an immutable, published SNAPSHOT of a connector's definition
// (ADR-039 versioning). The mutable working copy is the parent Connector row (the
// "draft"); publishing freezes {type, config} into a new version (N+1). History is
// append-only — rollback re-drafts a snapshot into the parent, it never deletes a
// version. There is no token: a version is addressed by its parent + monotonic
// integer, so it embeds TenantScoped (for isolation) but NOT TokenReference.
//
// A version snapshots {type, config} only, NOT the credential: the secret is keyed by
// the parent connector's immutable id (ConnectorSecretRef), so rotating a credential
// applies across every version without republishing, and dispatch resolves the live
// secret for whichever version it runs. The publish timestamp is the row's CreatedAt.
type ConnectorVersion struct {
	gorm.Model
	rdb.TenantScoped
	// Parent connector + monotonic-per-connector version number; unique together so
	// two concurrent publishes can't mint the same version (the loser's insert fails).
	ConnectorID uint  `gorm:"not null;uniqueIndex:uix_connector_versions_connector_version,priority:1"`
	Version     int32 `gorm:"not null;uniqueIndex:uix_connector_versions_connector_version,priority:2"`
	// Snapshot of the connector's kind + config at publish time.
	Type   string         `gorm:"not null;size:64"`
	Config datatypes.JSON `gorm:"not null"`
	// User-supplied label/description for the version (MAY embed a semver string; the
	// platform does not parse it). Optional.
	Label       sql.NullString `gorm:"size:128"`
	Description sql.NullString `gorm:"size:1024"`
	// The identity that published this version (claims username, falling back to email).
	PublishedBy string `gorm:"size:256"`
}

// ConnectorCreateRequest is the data required to create or update a connector. Config
// is the raw JSON config document (validated by the API layer). Secret is write-only:
// a nil value preserves the stored secret on update (the caller cannot read it back to
// resend it); a non-nil value replaces it, and an explicit empty string clears it.
type ConnectorCreateRequest struct {
	Token       string
	Name        *string
	Description *string
	Type        string
	Config      string
	Secret      *string
}

// ConnectorSearchCriteria is the filter/pagination for a connector search.
type ConnectorSearchCriteria struct {
	rdb.Pagination
	Type *string
}

// ConnectorSearchResults is a page of connectors plus its pagination info.
type ConnectorSearchResults struct {
	Results    []Connector
	Pagination rdb.SearchResultsPagination
}

// ConnectorSecretName is the stable per-connector secret handle name, keyed by the
// connector's IMMUTABLE numeric id (not its token) so a token rename keeps the same
// credential bound (rename-safe, mirroring notification's ChannelSecretName).
func ConnectorSecretName(id uint) string {
	return fmt.Sprintf("connector/%d", id)
}

// ConnectorSecretRef builds the tenant-scoped SecretRef for a connector's outbound
// credential from the tenant in context. Fails closed with core.ErrNoTenant when no
// tenant is in context, so a secret can never be written or resolved unscoped.
func ConnectorSecretRef(ctx context.Context, id uint) (secrets.SecretRef, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return secrets.SecretRef{}, core.ErrNoTenant
	}
	return secrets.SecretRef{Scope: secrets.ScopeTenant, Tenant: tenant, Name: ConnectorSecretName(id)}, nil
}
