// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"errors"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	util "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// DeviceConfiguration reads one device's configuration: latest revision, last report and
// the derived pending / stale flags. Null for an unknown device token. Gated on
// device:read — the same authority that already reads the device's SHARED attributes,
// which is all the document is built from.
func (r *SchemaResolver) DeviceConfiguration(ctx context.Context, args struct {
	DeviceToken string
}) (*DeviceConfigurationResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	found, err := r.GetApi(ctx).DeviceConfigurationByToken(ctx, args.DeviceToken)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &DeviceConfigurationResolver{M: *found}, nil
}

// DeviceConfigurationRevisions pages a device's revisions, newest first; an unknown
// device token is an empty page.
func (r *SchemaResolver) DeviceConfigurationRevisions(ctx context.Context, args struct {
	DeviceToken string
	Pagination  PaginationInput
}) (*DeviceConfigurationRevisionSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	found, err := r.GetApi(ctx).DeviceConfigurationRevisions(ctx, args.DeviceToken, rdbPagination(args.Pagination))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		found = &model.DeviceConfigurationRevisionSearchResults{
			Results: make([]model.DeviceConfigurationRevision, 0), Pagination: rdb.SearchResultsPagination{}}
		err = nil
	}
	if err != nil {
		return nil, err
	}
	return &DeviceConfigurationRevisionSearchResultsResolver{M: *found, S: r, C: ctx}, nil
}

type DeviceConfigurationResolver struct {
	M model.DeviceConfiguration
}

func (r *DeviceConfigurationResolver) Desired() *DeviceConfigurationRevisionResolver {
	if r.M.Desired == nil {
		return nil
	}
	return &DeviceConfigurationRevisionResolver{M: *r.M.Desired}
}

func (r *DeviceConfigurationResolver) Reported() *DeviceConfigurationReportResolver {
	if r.M.Reported == nil {
		return nil
	}
	return &DeviceConfigurationReportResolver{M: *r.M.Reported}
}

func (r *DeviceConfigurationResolver) Pending() bool        { return r.M.Pending }
func (r *DeviceConfigurationResolver) Stale() bool          { return r.M.Stale }
func (r *DeviceConfigurationResolver) Undeclared() []string { return nonNilStrings(r.M.Undeclared) }
func (r *DeviceConfigurationResolver) Invalid() []string    { return nonNilStrings(r.M.Invalid) }

func nonNilStrings(in []string) []string {
	if in == nil {
		return make([]string, 0)
	}
	return in
}

type DeviceConfigurationRevisionResolver struct {
	M model.DeviceConfigurationRevision
}

func (r *DeviceConfigurationRevisionResolver) Revision() (int32, error) {
	return util.StoredInt32("revision", r.M.Revision)
}

func (r *DeviceConfigurationRevisionResolver) Digest() string { return r.M.Digest }

// Document returns the stored canonical bytes verbatim: they are what the digest covers,
// so they are never re-encoded on the way out.
func (r *DeviceConfigurationRevisionResolver) Document() string { return string(r.M.Document) }

func (r *DeviceConfigurationRevisionResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *DeviceConfigurationRevisionResolver) Actor() string { return r.M.Actor }

type DeviceConfigurationReportResolver struct {
	M model.DeviceConfigurationState
}

func (r *DeviceConfigurationReportResolver) Revision() (*int32, error) {
	return util.NullInt32("revision", r.M.ReportedRevision)
}

func (r *DeviceConfigurationReportResolver) Digest() *string { return util.NullStr(r.M.ReportedDigest) }
func (r *DeviceConfigurationReportResolver) Status() *string { return util.NullStr(r.M.ReportedStatus) }
func (r *DeviceConfigurationReportResolver) Errors() []string {
	return r.M.ReportedErrorList()
}

func (r *DeviceConfigurationReportResolver) ReportedAt() *string {
	if !r.M.ReportedAt.Valid {
		return nil
	}
	return util.FormatTime(r.M.ReportedAt.Time)
}

func (r *DeviceConfigurationReportResolver) LastSyncAt() *string {
	if !r.M.LastSyncAt.Valid {
		return nil
	}
	return util.FormatTime(r.M.LastSyncAt.Time)
}

type DeviceConfigurationRevisionSearchResultsResolver struct {
	M model.DeviceConfigurationRevisionSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *DeviceConfigurationRevisionSearchResultsResolver) Results() []*DeviceConfigurationRevisionResolver {
	out := make([]*DeviceConfigurationRevisionResolver, 0, len(r.M.Results))
	for _, rev := range r.M.Results {
		out = append(out, &DeviceConfigurationRevisionResolver{M: rev})
	}
	return out
}

func (r *DeviceConfigurationRevisionSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.M.Pagination, S: r.S, C: r.C}
}
