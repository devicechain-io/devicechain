// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-device-management/model"
	util "github.com/devicechain-io/dc-microservice/graphql"
)

// ------------------------------
// Device profile version resolver
// ------------------------------

// DeviceProfileVersionResolver exposes a published profile version (ADR-045
// versioning). The snapshot blob is deliberately not exposed — the version list is
// metadata; a device resolves the active version server-side.
type DeviceProfileVersionResolver struct {
	M model.DeviceProfileVersion
	S *SchemaResolver
	C context.Context
}

func (r *DeviceProfileVersionResolver) Version() int32 {
	return r.M.Version
}

func (r *DeviceProfileVersionResolver) Label() *string {
	return util.NullStr(r.M.Label)
}

func (r *DeviceProfileVersionResolver) Description() *string {
	return util.NullStr(r.M.Description)
}

func (r *DeviceProfileVersionResolver) PublishedAt() string {
	// publishedAt is non-null in the schema and CreatedAt is always set on a
	// persisted version, so a nil format (zero time) collapses to empty rather than
	// a resolver panic.
	if s := util.FormatTime(r.M.CreatedAt); s != nil {
		return *s
	}
	return ""
}

func (r *DeviceProfileVersionResolver) PublishedBy() *string {
	if r.M.PublishedBy == "" {
		return nil
	}
	return &r.M.PublishedBy
}

// ConfigurationDeclaration is the device-visible configuration declaration frozen into
// this version. A snapshot that cannot be decoded is an error, not an empty declaration:
// "declares nothing" is a statement devices act on.
func (r *DeviceProfileVersionResolver) ConfigurationDeclaration() ([]*ConfigurationKeyResolver, error) {
	snap, err := model.ParseProfileSnapshot(r.M.Snapshot)
	if err != nil {
		return nil, err
	}
	result := make([]*ConfigurationKeyResolver, 0, len(snap.Configuration))
	for _, k := range snap.Configuration {
		result = append(result, &ConfigurationKeyResolver{M: k})
	}
	return result, nil
}
