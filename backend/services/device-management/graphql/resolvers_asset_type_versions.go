// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-device-management/model"
	util "github.com/devicechain-io/dc-microservice/graphql"
)

// -----------------------------
// Asset type version resolver
// -----------------------------

// AssetTypeVersionResolver exposes a published asset-type property contract
// (ADR-072).
//
// 🔴 Unlike DeviceProfileVersionResolver, this one DOES expose the frozen document.
// A profile version's snapshot is hidden because nothing outside the platform reads
// it — a device resolves the active version server-side. An asset-type version's
// contract is the thing an AUTHOR is deciding about: a rollback list showing only
// version numbers and labels asks somebody to pick between contracts they cannot
// see. It is also already public in draft form on the type itself, so hiding it
// here would conceal only the past.
type AssetTypeVersionResolver struct {
	M model.AssetTypeVersion
	S *SchemaResolver
	C context.Context
}

func (r *AssetTypeVersionResolver) Version() int32 {
	return r.M.Version
}

func (r *AssetTypeVersionResolver) Label() *string {
	return util.NullStr(r.M.Label)
}

func (r *AssetTypeVersionResolver) Description() *string {
	return util.NullStr(r.M.Description)
}

func (r *AssetTypeVersionResolver) PublishedAt() string {
	// publishedAt is non-null in the schema and CreatedAt is always set on a
	// persisted version, so a nil format (zero time) collapses to empty rather than
	// a resolver panic.
	if s := util.FormatTime(r.M.CreatedAt); s != nil {
		return *s
	}
	return ""
}

func (r *AssetTypeVersionResolver) PublishedBy() *string {
	if r.M.PublishedBy == "" {
		return nil
	}
	return &r.M.PublishedBy
}

// PropertySchema returns the frozen contract. The column is NOT NULL, so a version
// always carries a document — an empty array is a real, reachable value meaning "an
// asset of this type carries nothing", and it must not be collapsed into null.
func (r *AssetTypeVersionResolver) PropertySchema() string {
	return r.M.PropertySchema.String()
}
