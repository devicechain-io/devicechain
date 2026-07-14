// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	util "github.com/devicechain-io/dc-microservice/graphql"
)

// -----------------------------
// Entity group version resolver
// -----------------------------

// EntityGroupVersionResolver exposes a published dynamic-group version (ADR-062 S1).
// Unlike a profile version (whose snapshot is an opaque capability blob), a group
// version's frozen selector is exposed — it is the authored membership rule, useful
// to a rule-authoring UI showing what a pinned {group}@{version} matches.
type EntityGroupVersionResolver struct {
	M model.EntityGroupVersion
	S *SchemaResolver
	C context.Context
}

func (r *EntityGroupVersionResolver) Version() int32 {
	return r.M.Version
}

func (r *EntityGroupVersionResolver) Selector() string {
	return r.M.Selector
}

func (r *EntityGroupVersionResolver) MemberType() string {
	return r.M.MemberType
}

func (r *EntityGroupVersionResolver) Label() *string {
	return util.NullStr(r.M.Label)
}

func (r *EntityGroupVersionResolver) Description() *string {
	return util.NullStr(r.M.Description)
}

func (r *EntityGroupVersionResolver) PublishedAt() string {
	// publishedAt is non-null in the schema and CreatedAt is always set on a
	// persisted version, so a nil format (zero time) collapses to empty rather than a
	// resolver panic.
	if s := util.FormatTime(r.M.CreatedAt); s != nil {
		return *s
	}
	return ""
}

func (r *EntityGroupVersionResolver) PublishedBy() *string {
	if r.M.PublishedBy == "" {
		return nil
	}
	return &r.M.PublishedBy
}

// -----------------------------------------------
// Entity group version fields on the group resolver
// -----------------------------------------------

// ActiveVersion is the published version a rule-scoped resolve reads (ADR-062 S1),
// or null for a group never published (always null for a static group).
func (r *EntityGroupResolver) ActiveVersion() *int32 {
	if !r.M.ActiveVersion.Valid {
		return nil
	}
	v := r.M.ActiveVersion.Int32
	return &v
}

// Versions lists this group's published versions, newest first (ADR-062 S1). It is
// gated on DeviceRead on its own: an EntityGroup can be produced by the
// DeviceWrite-only create/update mutations, so reading its version history through
// it must still require read (authorities are flat — write does not imply read),
// mirroring the Members nested field.
func (r *EntityGroupResolver) Versions(ctx context.Context) ([]*EntityGroupVersionResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	api := r.S.GetApi(ctx)
	versions, err := api.EntityGroupVersions(ctx, r.M.Token)
	if err != nil {
		return nil, err
	}
	result := make([]*EntityGroupVersionResolver, 0, len(versions))
	for _, v := range versions {
		result = append(result, &EntityGroupVersionResolver{M: *v, S: r.S, C: ctx})
	}
	return result, nil
}
