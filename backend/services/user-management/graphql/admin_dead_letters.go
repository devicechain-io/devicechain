// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"
	"strconv"
	"time"

	gql "github.com/graph-gophers/graphql-go"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-user-management/deadletters"
)

// AdminDeadLetterResolver renders one stored dead letter.
type AdminDeadLetterResolver struct {
	M deadletters.DeadLetter
	S *AdminResolver
	C context.Context
}

func (r *AdminDeadLetterResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}
func (r *AdminDeadLetterResolver) Tenant() string { return r.M.TenantId }

// OccurredTime is RFC3339Nano, matching every other timestamp this plane publishes. The
// precision costs nothing and a truncated one is a value that cannot be used to correlate.
func (r *AdminDeadLetterResolver) OccurredTime() string {
	return r.M.OccurredTime.UTC().Format(time.RFC3339Nano)
}
func (r *AdminDeadLetterResolver) Kind() string     { return r.M.Kind }
func (r *AdminDeadLetterResolver) Reason() string   { return r.M.Reason }
func (r *AdminDeadLetterResolver) Source() string   { return r.M.Source }
func (r *AdminDeadLetterResolver) Summary() string  { return r.M.Summary }
func (r *AdminDeadLetterResolver) Attempts() int32  { return int32(r.M.Attempts) }
func (r *AdminDeadLetterResolver) Detail() *string  { return emptyToNil(r.M.Detail) }
func (r *AdminDeadLetterResolver) Subject() *string { return emptyToNil(r.M.Subject) }

// Sequence is a STRING, not an Int, and the reason is the type rather than taste: it is a
// JetStream stream sequence, a uint64, and GraphQL's Int is a signed 32-bit value. A
// long-lived instance passes 2^31 and would start publishing a negative or truncated
// position — for the one field whose whole job is to locate the original message.
func (r *AdminDeadLetterResolver) Sequence() *string {
	if r.M.Sequence == 0 {
		return nil
	}
	s := strconv.FormatUint(r.M.Sequence, 10)
	return &s
}
func (r *AdminDeadLetterResolver) Correlation() *string { return emptyToNil(r.M.Correlation) }
func (r *AdminDeadLetterResolver) Reference() *string   { return emptyToNil(r.M.Reference) }

// AdminDeadLetterSearchResultsResolver is one page.
type AdminDeadLetterSearchResultsResolver struct {
	M deadletters.SearchResults
	S *AdminResolver
	C context.Context
}

func (r *AdminDeadLetterSearchResultsResolver) Results() []*AdminDeadLetterResolver {
	out := make([]*AdminDeadLetterResolver, 0, len(r.M.Results))
	for i := range r.M.Results {
		out = append(out, &AdminDeadLetterResolver{M: r.M.Results[i], S: r.S, C: r.C})
	}
	return out
}

func (r *AdminDeadLetterSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.M.Pagination}
}

// deadLetterCriteriaInput is the wire shape of DeadLetterSearchCriteria.
type deadLetterCriteriaInput struct {
	PageNumber int32
	PageSize   int32
	Tenant     *string
	Kind       *string
	Source     *string
	Since      *string
	Until      *string
}

// DeadLetters returns a page of work the platform gave up on.
//
// 🔑 IT IS GATED ON audit:read RATHER THAN A NEW AUTHORITY, and the reason is what the two
// surfaces are. Both are the operator's record of things that happened and cannot be
// changed through the API, both are instance-wide on this plane, and both are read by the
// same person doing the same job. A separate authority would have to be granted alongside
// this one everywhere it is granted at all, which is how an authority becomes decoration.
func (r *AdminResolver) DeadLetters(ctx context.Context, args struct {
	Criteria deadLetterCriteriaInput
}) (*AdminDeadLetterSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.AuditRead); err != nil {
		return nil, err
	}
	criteria, err := toDeadLetterCriteria(args.Criteria)
	if err != nil {
		return nil, err
	}
	found, err := r.getAdminService(ctx).DeadLetters(ctx, criteria)
	if err != nil {
		return nil, err
	}
	return &AdminDeadLetterSearchResultsResolver{M: *found, S: r, C: ctx}, nil
}

// DeadLetter returns one record by id, or null when there is none.
func (r *AdminResolver) DeadLetter(ctx context.Context, args struct {
	Id gql.ID
}) (*AdminDeadLetterResolver, error) {
	if err := auth.Authorize(ctx, auth.AuditRead); err != nil {
		return nil, err
	}
	id, err := strconv.ParseUint(string(args.Id), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("dead letter id %q is not a number", string(args.Id))
	}
	found, err := r.getAdminService(ctx).DeadLetter(ctx, uint(id))
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, nil
	}
	return &AdminDeadLetterResolver{M: *found, S: r, C: ctx}, nil
}

// toDeadLetterCriteria maps the wire input onto the store's criteria.
//
// 🔴 AN UNPARSEABLE TIME BOUND IS AN ERROR, NOT AN IGNORED FILTER. A caller narrowing to an
// incident window and getting the whole table back because their timestamp was malformed
// would read the extra rows as findings.
func toDeadLetterCriteria(in deadLetterCriteriaInput) (deadletters.SearchCriteria, error) {
	criteria := deadletters.SearchCriteria{}
	criteria.PageNumber = in.PageNumber
	criteria.PageSize = in.PageSize
	criteria.Tenant = strValue(in.Tenant)
	criteria.Kind = strValue(in.Kind)
	criteria.Source = strValue(in.Source)

	for _, bound := range []struct {
		name string
		in   *string
		out  **time.Time
	}{
		{"since", in.Since, &criteria.Since},
		{"until", in.Until, &criteria.Until},
	} {
		if bound.in == nil || *bound.in == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, *bound.in)
		if err != nil {
			return criteria, fmt.Errorf("%s must be an RFC3339 timestamp: %w", bound.name, err)
		}
		utc := t.UTC()
		*bound.out = &utc
	}
	return criteria, nil
}

// strValue dereferences an optional string, mapping nil to empty.
func strValue(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
