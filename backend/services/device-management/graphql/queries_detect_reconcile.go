// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"strconv"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
)

// The detection-engine reconcile reads (model/api_detect_reconcile.go): what event-processing's
// copy of a tenant's published rules, device roster and threshold attributes is checked against,
// at the start of its DETECT term and every five minutes.
//
// Tenancy and authority are those of every other device read: there is no argument through which
// a caller could name a tenant, the rows are tenant-scoped, and the scope callback refuses a
// tenant-scoped query with no tenant at all. device:read — the same authority the profile-version
// and detection-rule reads take, because this is the same material.

// ActiveProfileRules resolves one page of the tenant's published profiles with their active
// version's rules.
func (r *SchemaResolver) ActiveProfileRules(ctx context.Context, args struct {
	AfterId *string
	Limit   int32
}) (*ActiveProfileRulesPageResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	afterId, err := optionalCursor(args.AfterId)
	if err != nil {
		return nil, err
	}
	page, err := r.GetApi(ctx).ActiveProfileRules(ctx, afterId, int(args.Limit))
	if err != nil {
		return nil, err
	}
	return &ActiveProfileRulesPageResolver{M: *page}, nil
}

// DeviceRosterPage resolves one page of the device roster.
func (r *SchemaResolver) DeviceRosterPage(ctx context.Context, args struct {
	AfterId *string
	Limit   int32
}) (*DeviceRosterPageResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	afterId, err := optionalCursor(args.AfterId)
	if err != nil {
		return nil, err
	}
	page, err := r.GetApi(ctx).DeviceRosterPage(ctx, afterId, int(args.Limit))
	if err != nil {
		return nil, err
	}
	return &DeviceRosterPageResolver{M: *page}, nil
}

// DeviceThresholdAttributePage resolves one page of the threshold attributes.
func (r *SchemaResolver) DeviceThresholdAttributePage(ctx context.Context, args struct {
	AfterId *string
	Limit   int32
}) (*DeviceThresholdAttributePageResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	afterId, err := optionalCursor(args.AfterId)
	if err != nil {
		return nil, err
	}
	page, err := r.GetApi(ctx).DeviceThresholdAttributePage(ctx, afterId, int(args.Limit))
	if err != nil {
		return nil, err
	}
	return &DeviceThresholdAttributePageResolver{M: *page}, nil
}

// optionalCursor reads an absent or empty afterId as "begin" and anything else through
// parseCursor, which refuses a malformed one rather than restarting the walk from zero.
func optionalCursor(afterId *string) (uint64, error) {
	if afterId == nil || *afterId == "" {
		return 0, nil
	}
	return parseCursor(*afterId)
}

// formatCursor renders a page's next cursor, nil when the walk is complete.
func formatCursor(next *uint64) *string {
	if next == nil {
		return nil
	}
	s := strconv.FormatUint(*next, 10)
	return &s
}

// reconcileTime renders a stored instant for the reconcile reads: RFC3339 with every stored
// digit, in UTC, so the reader parses back exactly the value the fact would have carried.
func reconcileTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

// ActiveProfileRulesPageResolver exposes one page of active profiles.
type ActiveProfileRulesPageResolver struct {
	M model.ActiveProfileRulesPage
}

func (r *ActiveProfileRulesPageResolver) Entries() []*ActiveProfileRulesResolver {
	out := make([]*ActiveProfileRulesResolver, 0, len(r.M.Entries))
	for i := range r.M.Entries {
		out = append(out, &ActiveProfileRulesResolver{M: r.M.Entries[i]})
	}
	return out
}

func (r *ActiveProfileRulesPageResolver) NextCursor() *string { return formatCursor(r.M.NextCursor) }

// ActiveProfileRulesResolver exposes one published profile's active version.
type ActiveProfileRulesResolver struct {
	M model.ActiveProfileRules
}

func (r *ActiveProfileRulesResolver) ProfileToken() string { return r.M.ProfileToken }
func (r *ActiveProfileRulesResolver) VersionToken() string { return r.M.VersionToken }
func (r *ActiveProfileRulesResolver) ActiveSince() string  { return reconcileTime(r.M.ActiveSince) }

func (r *ActiveProfileRulesResolver) Rules() []*PublishedProfileRuleResolver {
	out := make([]*PublishedProfileRuleResolver, 0, len(r.M.Rules))
	for i := range r.M.Rules {
		out = append(out, &PublishedProfileRuleResolver{M: r.M.Rules[i]})
	}
	return out
}

// PublishedProfileRuleResolver exposes one enabled rule of a published version.
type PublishedProfileRuleResolver struct {
	M model.PublishedDetectionRule
}

func (r *PublishedProfileRuleResolver) Token() string            { return r.M.Token }
func (r *PublishedProfileRuleResolver) Definition() string       { return r.M.Definition }
func (r *PublishedProfileRuleResolver) EntityGroupToken() string { return r.M.EntityGroupToken }
func (r *PublishedProfileRuleResolver) EntityGroupVersion() int32 {
	return r.M.EntityGroupVersion
}

// DeviceRosterPageResolver exposes one page of the device roster.
type DeviceRosterPageResolver struct {
	M model.DeviceRosterPage
}

func (r *DeviceRosterPageResolver) Entries() []*DeviceRosterEntryResolver {
	out := make([]*DeviceRosterEntryResolver, 0, len(r.M.Entries))
	for i := range r.M.Entries {
		out = append(out, &DeviceRosterEntryResolver{M: r.M.Entries[i]})
	}
	return out
}

func (r *DeviceRosterPageResolver) NextCursor() *string { return formatCursor(r.M.NextCursor) }

// DeviceRosterEntryResolver exposes one roster entry.
type DeviceRosterEntryResolver struct {
	M model.DeviceRosterEntry
}

func (r *DeviceRosterEntryResolver) DeviceToken() string   { return r.M.DeviceToken }
func (r *DeviceRosterEntryResolver) ProfileToken() string  { return r.M.ProfileToken }
func (r *DeviceRosterEntryResolver) ExpectedSince() string { return reconcileTime(r.M.ExpectedSince) }

// DeviceThresholdAttributePageResolver exposes one page of threshold attributes.
type DeviceThresholdAttributePageResolver struct {
	M model.DeviceThresholdAttributePage
}

func (r *DeviceThresholdAttributePageResolver) Entries() []*DeviceThresholdAttributeResolver {
	out := make([]*DeviceThresholdAttributeResolver, 0, len(r.M.Entries))
	for i := range r.M.Entries {
		out = append(out, &DeviceThresholdAttributeResolver{M: r.M.Entries[i]})
	}
	return out
}

func (r *DeviceThresholdAttributePageResolver) NextCursor() *string {
	return formatCursor(r.M.NextCursor)
}

// DeviceThresholdAttributeResolver exposes one threshold attribute.
type DeviceThresholdAttributeResolver struct {
	M model.DeviceThresholdAttribute
}

func (r *DeviceThresholdAttributeResolver) DeviceToken() string { return r.M.DeviceToken }
func (r *DeviceThresholdAttributeResolver) Scope() string       { return r.M.Scope }
func (r *DeviceThresholdAttributeResolver) Key() string         { return r.M.AttrKey }
func (r *DeviceThresholdAttributeResolver) Value() float64      { return r.M.Value }
func (r *DeviceThresholdAttributeResolver) UpdatedAt() string   { return reconcileTime(r.M.UpdatedAt) }
