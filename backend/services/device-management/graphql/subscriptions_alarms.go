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
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// Factories are test seams, replaced only before creating a hub.
var alarmLiveOpener = func(nmgr *messaging.NatsManager) messaging.LiveOpener {
	return func(ctx context.Context, tenant string) (<-chan messaging.Message, error) {
		return nmgr.SubscribeLive(ctx, tenant, streams.AlarmEvents)
	}
}
var alarmDecode = decodeAlarms
var alarmDropHook func()

// InitLiveSubscriptionMetrics runs once during process initialization, never per connection.
func InitLiveSubscriptionMetrics(ms *core.Microservice) {
	counter := ms.NewCounterVec("live_subscription_dropped_total", "Live values dropped for slow subscribers.", []string{"suffix"}).WithLabelValues(streams.AlarmEvents)
	alarmDropHook = counter.Inc
}

func alarmHub(nmgr *messaging.NatsManager) *messaging.LiveHub[*model.AlarmStateChangeEvent] {
	return messaging.SharedLiveHub(nmgr, streams.AlarmEvents, func() *messaging.LiveHub[*model.AlarmStateChangeEvent] {
		hub := messaging.NewLiveHub(streams.AlarmEvents, alarmLiveOpener(nmgr), alarmDecode, messaging.DefaultMaxLiveSubscribersPerTenant)
		hub.OnDrop(alarmDropHook)
		return hub
	})
}

func alarmOriginatorKey(originatorType string, id uint) string {
	return fmt.Sprintf("%s:%d", originatorType, id)
}

func decodeAlarms(msg messaging.Message, _ string) []messaging.Keyed[*model.AlarmStateChangeEvent] {
	ev, err := dmproto.UnmarshalAlarmStateChangeEvent(msg.Value)
	if err != nil {
		log.Debug().Err(err).Msg("alarm subscription: skipping undecodable alarm event")
		return nil
	}
	return []messaging.Keyed[*model.AlarmStateChangeEvent]{{Key: alarmOriginatorKey(ev.OriginatorType, ev.OriginatorId), Value: ev}}
}

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
	// position is set for a subscriber without location:read: an event of an alarm a geofence
	// rule raised is withheld, as every alarm read withholds it (model/position_withholding.go)
	// and as detectionStream withholds the rule's detections. nil for a subscriber holding it.
	// Judged last, after every predicate that needs no store read.
	position *positionGate
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
	if f.position != nil && !f.position.allows(ev.AlarmToken) {
		return false
	}
	return true
}

// maxWithheldAlarms bounds a subscription's memory of withheld alarms.
const maxWithheldAlarms = 1024

// positionGate decides, for a subscriber without location:read, whether an alarm's events may
// be pushed. The event does not say whether its alarm is position-derived, so the gate reads the
// alarm's classification. Only an alarm classified NOT position-derived is delivered; a
// position-derived one, one not classified yet, one that cannot be read and one that no longer
// exists are withheld (fail closed).
//
// Only a position-derived verdict is remembered. That verdict is sticky (never cleared), so it
// never goes stale; every other answer can change (a visible alarm can become position-derived
// when a geofence rule first contributes to it, an unclassified one is classified within a
// minute), so it is asked again on the alarm's next event. A failed read is not remembered either,
// so a store error does not silence an alarm for the rest of the subscription. The memory is
// bounded (FIFO); an evicted alarm is simply read again.
//
// One gate per subscription, used from that subscription's goroutine only.
type positionGate struct {
	derived  func(token string) (derived *bool, found bool, err error)
	withheld map[string]struct{}
	order    []string
	next     int
}

// positionGateFor returns the gate for a subscriber, or nil for one holding location:read, which
// receives every event its other predicates pass. derived is Api.AlarmPositionDerived.
func positionGateFor(ctx context.Context, derived func(context.Context, string) (*bool, bool, error)) *positionGate {
	if model.CallerHoldsLocationRead(ctx) {
		return nil
	}
	return &positionGate{derived: func(token string) (*bool, bool, error) { return derived(ctx, token) }}
}

func (g *positionGate) allows(token string) bool {
	if _, ok := g.withheld[token]; ok {
		return false
	}
	derived, found, err := g.derived(token)
	if err != nil {
		log.Debug().Err(err).Str("alarm", token).Msg("alarm subscription: alarm unreadable; withholding event")
		return false
	}
	if !found || derived == nil {
		return false
	}
	if !*derived {
		return true
	}
	g.remember(token)
	return false
}

func (g *positionGate) remember(token string) {
	if g.withheld == nil {
		g.withheld = make(map[string]struct{})
	}
	if len(g.withheld) == maxWithheldAlarms {
		delete(g.withheld, g.order[g.next])
		g.order[g.next] = token
		g.next = (g.next + 1) % maxWithheldAlarms
	} else {
		g.order = append(g.order, token)
	}
	g.withheld[token] = struct{}{}
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
// shared feed is torn down when its last client disconnects (ctx cancelled).
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
	filter.position = positionGateFor(ctx, r.GetApi(ctx).AlarmPositionDerived)
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

	var keys []string
	if filter.originatorId != nil {
		keys = []string{alarmOriginatorKey(*args.OriginatorType, *filter.originatorId)}
	}
	nmgr, err := r.GetNats(ctx)
	if err != nil {
		return nil, err
	}
	live, err := alarmHub(nmgr).Subscribe(ctx, tenant, keys)
	if err != nil {
		return nil, err
	}

	out := make(chan *AlarmEventResolver)
	go func() {
		defer close(out)
		for batch := range live {
			for _, ev := range batch {
				if !filter.matches(ev) {
					continue
				}
				select {
				case out <- &AlarmEventResolver{E: ev, S: r, C: ctx}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

// -----------------
// AlarmEvent resolver
// -----------------

// AlarmEventResolver and all field resolvers treat E (including pointer fields)
// as read-only: the decoded event is shared across live subscribers.
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

func (r *AlarmEventResolver) ClearReason() *string {
	if r.E.ClearReason != model.AlarmClearReasonOriginatorDeleted {
		return nil
	}
	return &r.E.ClearReason
}

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

// SuppressedUntil preserves the precision carried by the event.
func (r *AlarmEventResolver) SuppressedUntil() *string {
	if r.E.SuppressedUntil == nil {
		return nil
	}
	return util.FormatTime(*r.E.SuppressedUntil)
}
func (r *AlarmEventResolver) SuppressionWindowToken(ctx context.Context) (*string, error) {
	if r.E.SuppressionWindowToken == "" {
		return nil, nil
	}
	return r.visibleSuppressionToken(ctx, r.E.SuppressionWindowToken)
}
func (r *AlarmEventResolver) RaisedDuringSuppression(ctx context.Context) (*string, error) {
	if r.E.RaisedDuringSuppressionToken == "" {
		return nil, nil
	}
	return r.visibleSuppressionToken(ctx, r.E.RaisedDuringSuppressionToken)
}

// Resolve with each subscriber's context, never mutating the shared event.
func (r *AlarmEventResolver) visibleSuppressionToken(ctx context.Context, token string) (*string, error) {
	if _, scoped := auth.ScopeFromContext(ctx); !scoped {
		return &token, nil
	}
	visible, err := r.S.GetApi(ctx).VisibleSuppressionWindowToken(ctx, token)
	if err != nil {
		return nil, err
	}
	return &visible, nil
}
