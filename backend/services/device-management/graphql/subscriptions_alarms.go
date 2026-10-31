// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	util "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/streams"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// alarmEventFilter is the set of narrowing predicates for an alarm subscription,
// resolved once at subscribe time. Every field is optional and they AND together;
// the originator token is pre-resolved to an id so the hot path filters on a cheap
// integer compare rather than a per-event token lookup.
type alarmEventFilter struct {
	originatorType *string
	originatorId   *uint
	state          *string
	severity       *string
	alarmKey       *string
	// anchor is the stamp ("area:floor-4") an alarm must carry, from model.ResolveAlarmAnchor.
	// Judged on EVERY event, against the stamp the event carries, beside the scope below.
	anchor *string
	// scope is the subscriber's scope (ADR-081). For a scoped subscriber an event passes
	// only when the alarm's scope stamp shares an anchor with it: the same rule the alarms
	// query applies in the database (Alarm.ScopedBy), applied here to the live feed, which
	// never touches the database. An unstamped alarm matches no scope.
	scope auth.Scope
}

// matches reports whether an alarm event passes every set predicate.
func (f alarmEventFilter) matches(ev *model.AlarmStateChangeEvent) bool {
	if f.originatorType != nil && ev.OriginatorType != *f.originatorType {
		return false
	}
	if f.originatorId != nil && ev.OriginatorId != *f.originatorId {
		return false
	}
	if f.state != nil && ev.State != *f.state {
		return false
	}
	if f.severity != nil && ev.Severity != *f.severity {
		return false
	}
	if f.alarmKey != nil && ev.AlarmKey != *f.alarmKey {
		return false
	}
	if f.anchor != nil && !slices.Contains(ev.ScopeAnchors, *f.anchor) {
		return false
	}
	if f.scope.IsScoped() && !sharesAnchor(f.scope.Anchors(), ev.ScopeAnchors) {
		return false
	}
	return true
}

// sharesAnchor reports whether the two anchor lists have one in common.
func sharesAnchor(scope, stamp []string) bool {
	for _, a := range scope {
		if slices.Contains(stamp, a) {
			return true
		}
	}
	return false
}

// alarmStreamArgs are alarmStream's arguments.
type alarmStreamArgs struct {
	OriginatorType  *string
	OriginatorToken *string
	State           *string
	Severity        *string
	AlarmKey        *string
	Anchor          *model.AlarmAnchor
}

// newAlarmEventFilter builds the subscription's filter from its arguments, everything but the
// originator (resolved against the store by the caller).
func newAlarmEventFilter(ctx context.Context, scope auth.Scope, args alarmStreamArgs) (alarmEventFilter, error) {
	// Reject an unknown state/severity up front rather than accept a filter that can
	// never match — a silently-empty stream is undebuggable, the same reason the
	// originator path surfaces an error instead of yielding nothing.
	if args.State != nil && !model.AlarmState(*args.State).Valid() {
		return alarmEventFilter{}, fmt.Errorf("invalid state %q", *args.State)
	}
	if args.Severity != nil && !model.AlarmSeverity(*args.Severity).Valid() {
		return alarmEventFilter{}, fmt.Errorf("invalid severity %q", *args.Severity)
	}
	filter := alarmEventFilter{
		originatorType: args.OriginatorType,
		state:          args.State,
		severity:       args.Severity,
		alarmKey:       args.AlarmKey,
		scope:          scope,
	}
	if args.Anchor != nil {
		// The same rule as the alarm queries: a customer outside the subscriber's scope is
		// refused at subscribe time, never filtered into a silently empty stream.
		stamp, err := model.ResolveAlarmAnchor(ctx, *args.Anchor)
		if err != nil {
			return alarmEventFilter{}, err
		}
		filter.anchor = &stamp
	}
	return filter, nil
}

// AlarmStream streams alarm state-change events to the subscriber as transitions
// happen (ADR-041 / ADR-037). It taps the live alarm-events feed for the caller's
// tenant and pushes each event — the same envelope the alarm integrator and operator API
// emit (2.D) — optionally narrowed to one originator/state/severity/alarm key. The
// feed is torn down when the client unsubscribes or disconnects (ctx cancelled).
// Named distinctly from the alarm queries because both resolve off the one root
// resolver; unlike a query it streams from subscribe time onward with no backfill.
func (r *SchemaResolver) AlarmStream(ctx context.Context, args alarmStreamArgs) (<-chan *AlarmEventResolver, error) {
	// Open to a scoped subscriber (ADR-081): every event is matched against the scope
	// below, and an originator outside the scope is not found at subscribe time — the
	// device lookup runs under the subscriber's own context, so the scope callback hides
	// it exactly as the alarms query would.
	scope, err := auth.AuthorizeScoped(ctx, auth.AlarmRead)
	if err != nil {
		return nil, err
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return nil, core.ErrNoTenant
	}
	filter, err := newAlarmEventFilter(ctx, scope, args)
	if err != nil {
		return nil, err
	}
	// Resolve an originator token to its id once, at subscribe time, so per-event
	// filtering is a cheap int compare (mirrors the Alarms query criteria). An
	// unresolvable originator is a client error surfaced now, not a silently empty
	// stream that never yields.
	if args.OriginatorToken != nil {
		if args.OriginatorType == nil {
			return nil, errors.New("originatorType is required when filtering by originator")
		}
		id, err := r.GetApi(ctx).ResolveEntityToken(ctx, *args.OriginatorType, *args.OriginatorToken)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				// The miss stays in the chain: it is what the GraphQL boundary answers
				// with NOT_FOUND, as it does for every other service's miss.
				return nil, fmt.Errorf("originator %q: %w", *args.OriginatorToken, err)
			}
			return nil, err
		}
		filter.originatorId = &id
	}

	live, err := r.GetNats(ctx).SubscribeLive(ctx, tenant, streams.AlarmEvents)
	if err != nil {
		return nil, err
	}

	out := make(chan *AlarmEventResolver)
	go func() {
		defer close(out)
		for msg := range live {
			ev, err := dmproto.UnmarshalAlarmStateChangeEvent(msg.Value)
			if err != nil {
				log.Debug().Err(err).Msg("alarm subscription: skipping undecodable alarm event")
				continue
			}
			if !filter.matches(ev) {
				continue
			}
			select {
			case out <- &AlarmEventResolver{E: ev, S: r, C: ctx}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// -----------------
// AlarmEvent resolver
// -----------------

type AlarmEventResolver struct {
	E *model.AlarmStateChangeEvent
	S *SchemaResolver
	C context.Context
}

func (r *AlarmEventResolver) EventType() string { return string(r.E.EventType) }

func (r *AlarmEventResolver) AlarmToken() string { return r.E.AlarmToken }

func (r *AlarmEventResolver) OriginatorType() string { return r.E.OriginatorType }

func (r *AlarmEventResolver) OriginatorId() gql.ID { return gql.ID(fmt.Sprint(r.E.OriginatorId)) }

// OriginatorToken resolves the originator's token on demand — a device lookup by id,
// since alarms are raised on devices today. It returns nil when the
// originator is not a device or no longer exists, and surfaces a lookup failure as a
// field error (rather than an indistinguishable null). Lazy by design: the lookup
// runs only when the client selects this field, keeping it off the per-event fan-out
// path. It takes the injected per-event context (graphql-go's per-resolve subCtx),
// so it inherits that call's timeout and stays tenant-scoped.
func (r *AlarmEventResolver) OriginatorToken(ctx context.Context) (*string, error) {
	if r.E.OriginatorType != string(entity.TypeDevice) {
		return nil, nil
	}
	devices, err := r.S.GetApi(ctx).DevicesById(ctx, []uint{r.E.OriginatorId})
	if err != nil {
		return nil, err
	}
	if len(devices) == 0 {
		return nil, nil
	}
	t := devices[0].Token
	return &t, nil
}

func (r *AlarmEventResolver) AlarmKey() string { return r.E.AlarmKey }

func (r *AlarmEventResolver) MetricKey() string { return r.E.MetricKey }

func (r *AlarmEventResolver) State() string { return r.E.State }

func (r *AlarmEventResolver) Severity() string { return r.E.Severity }

// PreviousSeverity is present only for a severity move (ESCALATED/DEESCALATED).
func (r *AlarmEventResolver) PreviousSeverity() *string {
	if r.E.PreviousSeverity == "" {
		return nil
	}
	s := r.E.PreviousSeverity
	return &s
}

func (r *AlarmEventResolver) Acknowledged() bool { return r.E.Acknowledged }

func (r *AlarmEventResolver) AcknowledgedBy() *string { return r.E.AcknowledgedBy }

func (r *AlarmEventResolver) LastValue() *float64 { return r.E.LastValue }

func (r *AlarmEventResolver) RaisedTime() *string { return util.FormatTime(r.E.RaisedTime) }

// OccurredTime is always present (every transition has an event time); formatted with
// the GraphQL API's uniform RFC3339Nano convention.
//
// 🔴 This deliberately REVERSES the previous stance, which held that "the sub-second
// precision preserved on the NATS wire is a wire-ordering concern, not part of the
// API's time contract." That was wrong in a way that only shows up downstream: an event
// time is not decoration, it is part of how an event is ADDRESSED (occurred_time is in
// the base event's key, and the read cursor already had to use UnixNano because seconds
// could not order a page). Publishing a coarser clock than the one recorded meant two
// readings 200ms apart came back indistinguishable, and anything that round-tripped the
// value silently moved it. Nano drops trailing zeros, so whole-second times are byte
// identical to before.
func (r *AlarmEventResolver) OccurredTime() string {
	return r.E.OccurredTime.Format(time.RFC3339Nano)
}
