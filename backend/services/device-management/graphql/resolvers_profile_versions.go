// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	util "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
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

// ThingModel renders the version as a W3C WoT Thing Model. A snapshot that cannot be
// rendered is an error, never an empty model.
func (r *DeviceProfileVersionResolver) ThingModel() (string, error) {
	out, err := r.S.GetApi(r.C).DeviceProfileVersionThingModel(r.C, &r.M)
	if err != nil {
		return "", err
	}
	return string(out), nil
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

// Metrics are the metric definitions frozen into this version, read from the same snapshot
// the device resolution reads, so a client asking by profile is told what a device of the
// profile is told. A snapshot that cannot be decoded is an error, not an empty list.
func (r *DeviceProfileVersionResolver) Metrics() ([]*PublishedMetricResolver, error) {
	snap, err := model.ParseProfileSnapshot(r.M.Snapshot)
	if err != nil {
		return nil, err
	}
	result := make([]*PublishedMetricResolver, 0, len(snap.Metrics))
	for _, def := range snap.Metrics {
		if def == nil {
			continue
		}
		result = append(result, &PublishedMetricResolver{M: def.Resolved()})
	}
	return result, nil
}

// Commands are the command definitions frozen into this version, with the impact each was
// published with. Like Metrics, an undecodable snapshot is an error.
func (r *DeviceProfileVersionResolver) Commands() ([]*PublishedCommandResolver, error) {
	snap, err := model.ParseProfileSnapshot(r.M.Snapshot)
	if err != nil {
		return nil, err
	}
	result := make([]*PublishedCommandResolver, 0, len(snap.Commands))
	for _, cd := range snap.Commands {
		if cd == nil {
			continue
		}
		result = append(result, &PublishedCommandResolver{M: *cd})
	}
	return result, nil
}

// Activations reads the profile's activation log, newest first (see
// model.DeviceProfileActivation). It carries the same authority as reading the profile's
// versions: catalogue configuration, readable by any device:read member.
func (r *DeviceProfileResolver) Activations(args struct {
	First *int32
	After *string
}) (*DeviceProfileActivationPageResolver, error) {
	if err := auth.AuthorizeCatalogue(r.C, auth.DeviceRead); err != nil {
		return nil, err
	}
	limit := rdb.MaxPageSize
	if args.First != nil {
		if *args.First < 1 {
			return nil, &model.InvalidArgumentError{Msg: fmt.Sprintf("first must be at least 1, got %d", *args.First)}
		}
		limit = int(*args.First)
	}
	var after uint64
	if args.After != nil && *args.After != "" {
		var err error
		if after, err = parseCursor(*args.After); err != nil {
			return nil, err
		}
	}
	rows, next, err := r.S.GetApi(r.C).DeviceProfileActivations(r.C, r.M.Token, after, limit)
	if err != nil {
		return nil, err
	}
	out := &DeviceProfileActivationPageResolver{}
	for _, row := range rows {
		out.entries = append(out.entries, &DeviceProfileActivationResolver{M: *row})
	}
	if next != 0 {
		c := strconv.FormatUint(next, 10)
		out.next = &c
	}
	return out, nil
}

// DeviceProfileActivationPageResolver is one page of a profile's activation log.
type DeviceProfileActivationPageResolver struct {
	entries []*DeviceProfileActivationResolver
	next    *string
}

func (r *DeviceProfileActivationPageResolver) Entries() []*DeviceProfileActivationResolver {
	if r.entries == nil {
		return []*DeviceProfileActivationResolver{}
	}
	return r.entries
}

func (r *DeviceProfileActivationPageResolver) NextCursor() *string { return r.next }

// DeviceProfileActivationResolver is one entry of the log.
type DeviceProfileActivationResolver struct {
	M model.DeviceProfileActivation
}

func (r *DeviceProfileActivationResolver) Version() int32 { return r.M.Version }

func (r *DeviceProfileActivationResolver) Kind() string { return strings.ToUpper(r.M.Kind) }

func (r *DeviceProfileActivationResolver) ActivatedAt() string {
	return r.M.ActivatedAt.UTC().Format(time.RFC3339Nano)
}

func (r *DeviceProfileActivationResolver) ActivatedBy() *string {
	if r.M.ActivatedBy == "" {
		return nil
	}
	return &r.M.ActivatedBy
}
