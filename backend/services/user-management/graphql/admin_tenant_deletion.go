// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"errors"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/devicechain-io/dc-user-management/purge"
	"gorm.io/gorm"
)

// formatPurgeTime renders a ledger timestamp for the wire.
//
// 🔴 IT IS ONE FUNCTION ON PURPOSE, and AdminTenant.purgeEpoch goes through it too. The epoch
// is half of a deletion's IDENTITY, and a console correlating the badge on a tenant with the
// record in the deletion history compares these strings. Two call sites formatting "the same"
// way is exactly the arrangement in which one of them later gains a nanosecond or loses a
// timezone and the two stop matching — silently, because both still look like timestamps.
//
// 🔴 RFC3339**Nano**, and the precision is load-bearing rather than cosmetic. The epoch is
// `time.Now().UTC()` at the moment of the cut — not truncated anywhere — and it is looked up
// with an EXACT match (`WHERE epoch = ?`). Publishing it at second precision would therefore
// publish an identifier that does not identify anything: a client handed "…:56Z" and asking
// for that deletion by epoch would match no row, and the deletion-history page's own detail
// link would be the first thing to break. TestTheEpochRoundTripsThroughTheApi is the guard.
func formatPurgeTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

// formatPurgeTimePtr is formatPurgeTime for an optional timestamp, mapping both nil and the
// zero time to null. The zero time matters: a ledger line that has never been attempted
// carries one, and rendering it as "0001-01-01T00:00:00Z" would put a date on a thing that
// never happened.
func formatPurgeTimePtr(t *time.Time) *string {
	if t == nil || t.IsZero() {
		return nil
	}
	s := formatPurgeTime(*t)
	return &s
}

// AdminTenantDeletionStoreResolver resolves one storage system's ledger line.
type AdminTenantDeletionStoreResolver struct {
	M iam.TenantPurgeStore
}

func (r *AdminTenantDeletionStoreResolver) Store() string     { return r.M.Store }
func (r *AdminTenantDeletionStoreResolver) Complete() bool    { return r.M.Complete }
func (r *AdminTenantDeletionStoreResolver) RowsErased() int32 { return int32(r.M.Rows) }

// Retaining, LastError and Note stay THREE fields on the wire because they are three
// different claims about the future, and the ledger's own model comment argues why merging
// any two changes the answer to "can I claim this data was erased?".
func (r *AdminTenantDeletionStoreResolver) Retaining() *string { return emptyToNil(r.M.Deferred) }
func (r *AdminTenantDeletionStoreResolver) LastError() *string { return emptyToNil(r.M.Failure) }
func (r *AdminTenantDeletionStoreResolver) Note() *string      { return emptyToNil(r.M.Note) }

func (r *AdminTenantDeletionStoreResolver) AttemptedAt() *string {
	return formatPurgeTimePtr(&r.M.AttemptedAt)
}
func (r *AdminTenantDeletionStoreResolver) CleanSince() *string {
	return formatPurgeTimePtr(r.M.CleanSince)
}

// emptyToNil maps an empty ledger string to a null field rather than an empty one, so a
// client can test presence instead of having to know that "" means absent.
func emptyToNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// AdminTenantDeletionResolver resolves one deletion record.
//
// It carries the lines and the progress it was built with rather than fetching them per
// field: every field below is derived from the same read, and a per-field fetch would make
// `stores` and `awaiting` answer from two different moments — which is precisely the
// inconsistency an operator would report as a bug ("it says nothing is blocking but lists a
// blocked store").
type AdminTenantDeletionResolver struct {
	M        iam.TenantPurge
	lines    []iam.TenantPurgeStore
	progress purge.Progress
}

func (r *AdminTenantDeletionResolver) Token() string { return r.M.Token }
func (r *AdminTenantDeletionResolver) Epoch() string { return formatPurgeTime(r.M.Epoch) }
func (r *AdminTenantDeletionResolver) CompletedAt() *string {
	return formatPurgeTimePtr(r.M.CompletedAt)
}
func (r *AdminTenantDeletionResolver) RowsErased() int32 { return int32(r.M.Rows) }

func (r *AdminTenantDeletionResolver) Stores() []*AdminTenantDeletionStoreResolver {
	out := make([]*AdminTenantDeletionStoreResolver, 0, len(r.lines))
	for i := range r.lines {
		out = append(out, &AdminTenantDeletionStoreResolver{M: r.lines[i]})
	}
	return out
}

// BlockedBy returns a NON-NIL empty slice when nothing is blocking, because the field is
// [String!]! and a nil slice would serialize as null against a non-null type.
func (r *AdminTenantDeletionResolver) BlockedBy() []string {
	if r.progress.BlockedBy == nil {
		return []string{}
	}
	return r.progress.BlockedBy
}

func (r *AdminTenantDeletionResolver) Awaiting() string { return string(r.progress.Awaiting) }
func (r *AdminTenantDeletionResolver) ElapsesAt() *string {
	return formatPurgeTimePtr(r.progress.ElapsesAt)
}

// TenantDeletion resolves one deletion record with its ledger (requires tenant:read).
//
// A record that does not exist is null rather than an error: "has this token ever been
// deleted?" is a legitimate question with a legitimate negative answer, and an error would
// make a console distinguish "no such record" from "the query failed".
func (r *AdminResolver) TenantDeletion(ctx context.Context, args struct {
	Token string
	Epoch *string
}) (*AdminTenantDeletionResolver, error) {
	if err := auth.Authorize(ctx, auth.TenantRead); err != nil {
		return nil, err
	}
	var epoch *time.Time
	if args.Epoch != nil {
		// RFC3339Nano parses a plain RFC3339 string too, so a caller that trims the
		// fractional part is still understood — it simply will not match a record whose
		// epoch has one.
		parsed, err := time.Parse(time.RFC3339Nano, *args.Epoch)
		if err != nil {
			return nil, err
		}
		utc := parsed.UTC()
		epoch = &utc
	}
	svc := r.getAdminService(ctx)
	rec, lines, err := svc.TenantDeletion(ctx, args.Token, epoch)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &AdminTenantDeletionResolver{
		M: *rec, lines: lines, progress: svc.TenantDeletionProgress(rec, lines),
	}, nil
}

// TenantDeletions lists deletion records, newest cut first (requires tenant:read).
func (r *AdminResolver) TenantDeletions(ctx context.Context, args struct {
	Completed *bool
	Limit     *int32
	Offset    *int32
}) ([]*AdminTenantDeletionResolver, error) {
	if err := auth.Authorize(ctx, auth.TenantRead); err != nil {
		return nil, err
	}
	svc := r.getAdminService(ctx)
	recs, err := svc.TenantDeletions(ctx, args.Completed, intArg(args.Limit), intArg(args.Offset))
	if err != nil {
		return nil, err
	}
	out := make([]*AdminTenantDeletionResolver, 0, len(recs))
	for i := range recs {
		// Each record's lines are read individually. That is one query per record and it is
		// the right trade at this size: an instance accumulates one record per deletion, the
		// list is paged, and the alternative — one query over every line and a group-by in
		// Go — buys nothing until the page is large enough that nobody would read it.
		lines, err := svc.TenantDeletionStores(ctx, recs[i].ID)
		if err != nil {
			return nil, err
		}
		out = append(out, &AdminTenantDeletionResolver{
			M: recs[i], lines: lines, progress: svc.TenantDeletionProgress(&recs[i], lines),
		})
	}
	return out, nil
}

// intArg adapts an optional GraphQL Int to the store's int, mapping absent to 0 — which the
// store reads as "unbounded" for a limit and "from the start" for an offset.
func intArg(v *int32) int {
	if v == nil {
		return 0
	}
	return int(*v)
}
