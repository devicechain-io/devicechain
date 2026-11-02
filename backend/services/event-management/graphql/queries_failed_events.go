// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/devicechain-io/dc-microservice/streams"
)

// The failed-events index (tenant plane). Every field requires event:read through
// auth.Authorize, which refuses a customer-scoped caller outright: a record of a refusal often
// names a device that does not exist, so it has no customer to scope it to, and rev 1 serves
// the tenant's administrators only.

// reasonUnknown is the enum value of a reason this build does not name.
const reasonUnknown = "UNKNOWN"

// reasonEnum is the GraphQL enum value of a FailureReason number: the protobuf name in
// SCREAMING_SNAKE, or UNKNOWN for a number this build does not know (and for 0, which is "no
// reason given"). The number travels beside it as reasonCode, so a reason a newer writer adds
// is never mapped to a neighbour that looks plausible.
func reasonEnum(code int32) string {
	if code == int32(resolvedevent.FailureReason_Unknown) {
		return reasonUnknown
	}
	if name, ok := model.ReasonName(code); ok {
		return strings.ToUpper(name)
	}
	return reasonUnknown
}

// reasonCodeOf is the inverse of reasonEnum for a filter: the number a named reason selects.
// UNKNOWN has no single number and is handled by the caller.
func reasonCodeOf(enum string) (int32, bool) {
	for _, code := range model.KnownReasonCodes() {
		if reasonEnum(code) == enum {
			return code, true
		}
	}
	return 0, false
}

func parseInstant(field, value string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, integrity.NewRefusal(integrity.ClassInvalid,
			fmt.Sprintf("%s is not an RFC 3339 timestamp: %v", field, err))
	}
	return t, nil
}

// FailedEventSearchCriteriaInput is the FailedEventSearchCriteria input object.
type FailedEventSearchCriteriaInput struct {
	PageNumber  int32
	PageSize    int32
	Reason      *string
	DeviceToken *string
	Since       *string
	Until       *string
}

func toFailedEventCriteria(in FailedEventSearchCriteriaInput) (model.FailedEventSearchCriteria, error) {
	c := model.FailedEventSearchCriteria{DeviceToken: in.DeviceToken, KnownReasons: model.KnownReasonCodes()}
	c.PageNumber, c.PageSize = in.PageNumber, in.PageSize
	if in.Reason != nil {
		if *in.Reason == reasonUnknown {
			c.UnknownReasons = true
		} else if code, ok := reasonCodeOf(*in.Reason); ok {
			c.Reason = &code
		} else {
			return c, integrity.NewRefusal(integrity.ClassInvalid, fmt.Sprintf("%q is not a failure reason", *in.Reason))
		}
	}
	if in.Since != nil {
		t, err := parseInstant("since", *in.Since)
		if err != nil {
			return c, err
		}
		c.Since = &t
	}
	if in.Until != nil {
		t, err := parseInstant("until", *in.Until)
		if err != nil {
			return c, err
		}
		c.Until = &t
	}
	return c, nil
}

// FailedEvents lists the tenant's failed events, newest first.
func (r *SchemaResolver) FailedEvents(ctx context.Context, args struct {
	Criteria FailedEventSearchCriteriaInput
}) (*FailedEventSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.EventRead); err != nil {
		return nil, err
	}
	criteria, err := toFailedEventCriteria(args.Criteria)
	if err != nil {
		return nil, err
	}
	found, err := r.GetApi(ctx).FailedEvents(ctx, criteria)
	if err != nil {
		return nil, err
	}
	return &FailedEventSearchResultsResolver{M: *found, S: r, C: ctx, W: r.payloadWatermark(ctx, len(found.Results))}, nil
}

// payloadWatermark is where the failure stream's retained history begins, asked once per request
// and only when there is a record to report on.
type payloadWatermark struct {
	first uint64
	known bool
}

// firstSequencer is the one question put to the broker.
type firstSequencer interface {
	StreamFirstSequence(ctx context.Context, suffix string) (uint64, error)
}

func (r *SchemaResolver) payloadWatermark(ctx context.Context, records int) payloadWatermark {
	if records == 0 {
		return payloadWatermark{}
	}
	nats, _ := ctx.Value(gqlcore.ContextNatsKey).(*messaging.NatsManager)
	if nats == nil {
		return payloadWatermark{}
	}
	return askWatermark(ctx, nats)
}

func askWatermark(ctx context.Context, q firstSequencer) payloadWatermark {
	wctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	first, err := q.StreamFirstSequence(wctx, streams.FailedEvents)
	if err != nil {
		return payloadWatermark{}
	}
	return payloadWatermark{first: first, known: true}
}

// payloadStatusOf says whether the stream still holds the record at seq. When the stream could
// not be asked it says UNKNOWN: neither "held" nor "gone" is claimed for a record nobody looked at.
func payloadStatusOf(w payloadWatermark, seq int64) string {
	switch {
	case !w.known || seq <= 0:
		return "UNKNOWN"
	case uint64(seq) < w.first:
		return "EXPIRED"
	default:
		return "AVAILABLE"
	}
}

// FailedEventSummary counts the tenant's failures per reason.
func (r *SchemaResolver) FailedEventSummary(ctx context.Context, args struct {
	Since string
}) ([]*FailedEventReasonCountResolver, error) {
	if err := auth.Authorize(ctx, auth.EventRead); err != nil {
		return nil, err
	}
	since, err := parseInstant("since", args.Since)
	if err != nil {
		return nil, err
	}
	if floor := time.Now().Add(-model.MaxFailedEventSummarySpan); since.Before(floor) {
		since = floor
	}
	lines, err := r.GetApi(ctx).FailedEventSummary(ctx, since)
	if err != nil {
		return nil, err
	}
	out := make([]*FailedEventReasonCountResolver, len(lines))
	for i := range lines {
		out[i] = &FailedEventReasonCountResolver{M: lines[i]}
	}
	return out, nil
}

// FailedEventCount counts the records refused for one claimed device token.
func (r *SchemaResolver) FailedEventCount(ctx context.Context, args struct {
	DeviceToken string
	Since       string
}) (int32, error) {
	if err := auth.Authorize(ctx, auth.EventRead); err != nil {
		return 0, err
	}
	since, err := parseInstant("since", args.Since)
	if err != nil {
		return 0, err
	}
	n, err := r.GetApi(ctx).DeviceFailedEventCount(ctx, model.ClaimedDeviceToken(args.DeviceToken), since)
	if err != nil {
		return 0, err
	}
	return saturate(n), nil
}

// saturate fits a count into GraphQL's 32-bit Int without wrapping.
func saturate(n int64) int32 {
	const max = int64(1<<31 - 1)
	if n > max {
		return int32(max)
	}
	if n < 0 {
		return 0
	}
	return int32(n)
}

// FailedEventSearchResultsResolver resolves FailedEventSearchResults.
type FailedEventSearchResultsResolver struct {
	M model.FailedEventSearchResults
	S *SchemaResolver
	C context.Context
	W payloadWatermark
}

func (r *FailedEventSearchResultsResolver) Results() []*FailedEventResolver {
	return mapResolvers(r.M.Results, func(m model.FailedEventRecord) *FailedEventResolver {
		return &FailedEventResolver{M: m, W: r.W}
	})
}

func (r *FailedEventSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.M.Pagination, S: r.S, C: r.C}
}

// FailedEventResolver resolves FailedEvent.
type FailedEventResolver struct {
	M model.FailedEventRecord
	W payloadWatermark
}

func (r *FailedEventResolver) PayloadStatus() string { return payloadStatusOf(r.W, r.M.RecordSeq) }

func (r *FailedEventResolver) RecordSeq() string { return fmt.Sprintf("%d", r.M.RecordSeq) }
func (r *FailedEventResolver) OccurredTime() string {
	return r.M.OccurredTime.UTC().Format(time.RFC3339Nano)
}
func (r *FailedEventResolver) SourceArea() string { return r.M.SourceArea }
func (r *FailedEventResolver) Reason() string     { return reasonEnum(r.M.Reason) }
func (r *FailedEventResolver) ReasonCode() int32  { return r.M.Reason }
func (r *FailedEventResolver) DeviceToken() *string {
	if r.M.DeviceToken == "" || r.M.DeviceToken == model.OverflowDeviceToken {
		return nil
	}
	return &r.M.DeviceToken
}
func (r *FailedEventResolver) EventType() *int32 {
	if r.M.EventType == nil {
		return nil
	}
	v := saturate(*r.M.EventType)
	return &v
}
func (r *FailedEventResolver) Error() string       { return r.M.Error }
func (r *FailedEventResolver) Correlation() string { return r.M.Correlation }
func (r *FailedEventResolver) Suppressed() int32   { return saturate(r.M.Suppressed) }
func (r *FailedEventResolver) Overflow() bool      { return r.M.DeviceToken == model.OverflowDeviceToken }

// FailedEventReasonCountResolver resolves FailedEventReasonCount.
type FailedEventReasonCountResolver struct{ M model.FailedEventReasonCount }

func (r *FailedEventReasonCountResolver) Reason() string    { return reasonEnum(r.M.Reason) }
func (r *FailedEventReasonCountResolver) ReasonCode() int32 { return r.M.Reason }
func (r *FailedEventReasonCountResolver) Records() int32    { return saturate(r.M.Records) }
func (r *FailedEventReasonCountResolver) Suppressed() int32 { return saturate(r.M.Suppressed) }
func (r *FailedEventReasonCountResolver) Devices() []*FailedEventDeviceCountResolver {
	out := make([]*FailedEventDeviceCountResolver, len(r.M.Devices))
	for i := range r.M.Devices {
		out[i] = &FailedEventDeviceCountResolver{M: r.M.Devices[i]}
	}
	return out
}

// FailedEventDeviceCountResolver resolves FailedEventDeviceCount.
type FailedEventDeviceCountResolver struct{ M model.FailedEventDeviceCount }

func (r *FailedEventDeviceCountResolver) DeviceToken() string { return r.M.DeviceToken }
func (r *FailedEventDeviceCountResolver) Records() int32      { return saturate(r.M.Records) }
