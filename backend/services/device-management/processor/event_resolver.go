// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/eventtime"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/proto"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// Worker used to resolve event entities.
type EventResolver struct {
	WorkerId   int
	Api        model.DeviceManagementApi
	AuthMode   string
	Unresolved <-chan messaging.Message
	Invalid    func(error, messaging.Message)
	// Resolved is handed the source message so its ack can be coordinated across
	// the 1->N resolved-event fan-out (the source is acked only once every
	// resolved event it produced has been durably published; ADR-022 review A3).
	Resolved func(messaging.Message, string, []EventResolutionResults)
	// Failed is handed the inbound message's correlation id (final argument) so
	// the outbound failed event it produces is stamped with it and stays
	// traceable end to end (ADR-022 review E15).
	Failed func(string, uint, esmodel.UnresolvedEvent, error, string)
	// metrics records RED instrumentation for the resolve loop (ADR-022 review
	// E13). It is shared across all workers and may be nil in tests; Start() is
	// nil-safe so a nil value no-ops.
	metrics *core.ProcessorMetrics
	// locationMemo bounds the undeclared-position warning to one line per (device,
	// profile version) (ADR-078). It is SHARED across the whole worker pool — see
	// newUndeclaredLocationMemo — so the bound is per process, not per worker.
	locationMemo *undeclaredLocationMemo
	// eventTime is the platform's event-time policy, applied HERE and nowhere else.
	eventTime EventTimePolicy
}

// EventTimePolicy is the resolution-time event-time policy: the tolerance a
// device-reported instant is bounded against, and the counter that reports how often
// the bound bites.
//
// 🔴 THIS IS THE ONLY PLACE IN THE PLATFORM THAT DECIDES WHAT INSTANT A READING
// HAPPENED AT. Resolution is upstream of the resolved-events stream, so a bounded time
// is what every consumer receives — the historian, both live projections, live
// detection, the streamed subscription and the replay preview all read one already-
// decided value. See package eventtime for why the rule is applied once rather than
// per-consumer, and what a device could do to the shared projections if it were not
// applied at all.
type EventTimePolicy struct {
	// MaxFutureSkew bounds how far a reported time may lead the server-stamped
	// processed time. Non-positive disables the bound.
	MaxFutureSkew time.Duration
	// Bounded counts readings whose reported time was refused and replaced with the
	// ceiling — one increment per bounded time, entry or envelope. It is label-free
	// (never per-tenant, ADR-023 G.3) and may be nil in tests.
	Bounded prometheus.Counter
}

// bound applies the policy to one reported instant, counting a bite.
func (p EventTimePolicy) bound(occurred, processed time.Time) time.Time {
	effective, bounded := eventtime.Effective(occurred, processed, p.MaxFutureSkew)
	if bounded && p.Bounded != nil {
		p.Bounded.Inc()
	}
	return effective
}

// boundEntry resolves one sample's instant — its own when it reported one, else the
// message envelope's — and bounds it.
func (p EventTimePolicy) boundEntry(entry *time.Time, envelope, processed time.Time) time.Time {
	effective, bounded := eventtime.ForEntry(entry, envelope, processed, p.MaxFutureSkew)
	if bounded && p.Bounded != nil {
		p.Bounded.Inc()
	}
	return effective
}

// Results of event resolution process.
type EventResolutionResults struct {
	Device   *model.Device
	Resolved *model.ResolvedEvent
}

// Create a new event resolver.
//
// locationMemo bounds the undeclared-position warning (ADR-078). The pool builds ONE
// and hands the same instance to every worker, which makes the "at most one warning
// per (device, profile version)" bound hold per PROCESS. Passing nil is supported and
// gives this resolver a private memo: still bounded, still never per-event, but the
// bound becomes per-worker — correct for a lone resolver (as in tests), wrong by a
// factor of the pool width for the real pool, which is why initializeEventResolvers
// shares one explicitly rather than letting each worker default.
func NewEventResolver(workerId int, api model.DeviceManagementApi, authMode string,
	eventTime EventTimePolicy,
	unrez <-chan messaging.Message,
	invalid func(error, messaging.Message),
	resolved func(messaging.Message, string, []EventResolutionResults),
	failed func(string, uint, esmodel.UnresolvedEvent, error, string),
	metrics *core.ProcessorMetrics,
	locationMemo *undeclaredLocationMemo) *EventResolver {
	if locationMemo == nil {
		locationMemo = newUndeclaredLocationMemo()
	}
	return &EventResolver{
		WorkerId:     workerId,
		Api:          api,
		AuthMode:     authMode,
		eventTime:    eventTime,
		Unresolved:   unrez,
		Invalid:      invalid,
		Resolved:     resolved,
		Failed:       failed,
		metrics:      metrics,
		locationMemo: locationMemo,
	}
}

// MergeToResolveEvent assembles a resolved event from the inbound event and the
// device's tracked-relationship targets, denormalized as a set of uniform
// (type, token) anchors (ADR-013/044). An empty anchor set yields an anchorless
// event — the device is unassigned — which still persists and projects, keyed on
// the device, rather than being dropped (ADR-013 addendum 2026-07-01). The source
// device travels as its token (ADR-044): the numeric row id stays inside
// device-management and never reaches event-management / device-state.
func (rez *EventResolver) MergeToResolveEvent(device *model.Device, anchors []model.ResolvedAnchor,
	memberships []model.GroupRef, event *esmodel.UnresolvedEvent, rezPayload interface{}, scope *model.ProfileScope) *EventResolutionResults {
	externalId := ""
	if device.ExternalId.Valid {
		externalId = device.ExternalId.String
	}
	resolved := &model.ResolvedEvent{
		Source:              event.Source,
		AltId:               event.AltId,
		SourceDeviceToken:   device.Token,
		ExternalId:          externalId,
		DeviceTypeToken:     scope.DeviceTypeToken,
		ProfileVersionToken: scope.ProfileVersionToken,
		Anchors:             anchors,
		ScopeMemberships:    memberships,
		// The envelope's time, bounded. This is the value the connectivity projection
		// advances last-activity on, so leaving it unbounded would let one event dated
		// 2099 pin a device's presence forever — its inactivity sweep would never fire
		// again and the device could never be seen to go offline, with no repair path
		// short of dropping the row. Bounding it here means presence cannot be frozen
		// by a device's own clock.
		OccurredTime:  rez.eventTime.bound(event.OccurredTime, event.ProcessedTime),
		ProcessedTime: event.ProcessedTime,
		EventType:     event.EventType,
		Payload:       rezPayload,
	}
	// The geofence stamp (ADR-078), at the same site and for the same reason as
	// ProfileVersionToken above: the fence set an event is evaluated against is frozen
	// into the event, so a replay a week later answers containment against the fences
	// that were live when the position was reported rather than the ones that exist now.
	//
	// 🔴 LOCATION ONLY. Nothing but a position can enter or leave a fence. This is the one
	// line that stops the stamp spreading to every event type, and it is guarded by a test
	// that anchors a location event carrying a version against sibling event types that
	// must not — an assertion on the absence alone would pass for the wrong reasons.
	if event.EventType == esmodel.Location {
		resolved.FenceSetVersion = scope.FenceSetVersion
	}
	return &EventResolutionResults{Device: device, Resolved: resolved}
}

// membershipTarget is one entity whose dynamic-group memberships contribute to an
// event's scope stamp — the reporting device or one of its tracked anchors.
type membershipTarget struct {
	Type string
	Id   uint
}

// unionMemberships resolves and de-duplicates the rule-scoped group memberships across
// the given targets (the device ∪ each tracked anchor) into the event's ScopeMemberships
// (ADR-062). Each read is served from the negative-caching membership cache, so a
// non-member target (the common case) is a cache hit returning empty. De-dup is by
// (group token, version): a device tracked into two arid areas is in scope once.
func (rez *EventResolver) unionMemberships(ctx context.Context, targets []membershipTarget) ([]model.GroupRef, error) {
	// Pay-nothing short-circuit (ADR-062 Decision 7): a tenant with no rule-scoped group
	// does zero per-target reads — one cached EXISTS check gates the whole union.
	any, err := rez.Api.AnyScopedGroups(ctx)
	if err != nil {
		return nil, err
	}
	if !any {
		return nil, nil
	}
	type mkey struct {
		token   string
		version int32
	}
	seen := make(map[mkey]struct{})
	out := make([]model.GroupRef, 0)
	for _, t := range targets {
		ms, err := rez.Api.MembershipsForEntity(ctx, t.Type, t.Id)
		if err != nil {
			return nil, err
		}
		for _, m := range ms {
			k := mkey{m.GroupToken, m.SelectorVersion}
			if _, dup := seen[k]; dup {
				continue
			}
			seen[k] = struct{}{}
			out = append(out, model.GroupRef{GroupToken: m.GroupToken, Version: m.SelectorVersion})
		}
	}
	return out, nil
}

// resolveScope denormalizes the device's rule-scoping identity (ADR-051) so
// event-processing's DETECT engine can select the applicable rules from the wire
// without a graph read. It is resolved through the same cached device→type→
// profile→active-version chain the metric resolution already uses (cheap). Callers
// resolve it BEFORE any state mutation so a transient lookup failure can never
// leave a committed side effect (e.g. a created relationship) that a redelivery
// would then duplicate.
func (rez *EventResolver) resolveScope(ctx context.Context, device *model.Device) (*model.ProfileScope, uint, error) {
	scope, err := rez.Api.ProfileScopeByDeviceType(ctx, device.DeviceTypeId)
	if err != nil {
		return nil, uint(dmproto.FailureReason_ApiCallFailed), fmt.Errorf("could not resolve profile scope: %w", err)
	}
	return scope, 0, nil
}

// Create a new relationship based on an inbound event. The source is the
// originating device; the target is a uniform (type, token) reference (ADR-013).
func (rez *EventResolver) CreateNewEntityRelationship(ctx context.Context, device *model.Device,
	relcreate esmodel.UnresolvedNewRelationshipPayload) (*model.EntityRelationship, uint, error) {
	create := &model.EntityRelationshipCreateRequest{
		Token:            uuid.New().String(),
		SourceType:       string(entity.TypeDevice),
		Source:           device.Token,
		TargetType:       relcreate.TargetType,
		Target:           relcreate.Target,
		RelationshipType: relcreate.RelationshipType,
		Metadata:         nil,
	}
	created, err := rez.Api.CreateEntityRelationship(ctx, create)
	if err != nil {
		return nil, uint(dmproto.FailureReason_ApiCallFailed), err
	}
	return created, 0, nil
}

// Handle a new relationship event.
func (rez *EventResolver) HandleNewRelationshipEvent(ctx context.Context,
	device *model.Device, event *esmodel.UnresolvedEvent) ([]EventResolutionResults, uint, error) {
	relcreate, ok := event.Payload.(*esmodel.UnresolvedNewRelationshipPayload)
	if !ok {
		return nil, uint(dmproto.FailureReason_Invalid), errors.New("new relationship payload was not of expected type")
	}

	// Resolve everything fallible BEFORE creating the relationship — the scope AND the
	// device's dynamic-group memberships (ADR-062) — so a transient lookup failure aborts
	// cleanly instead of leaving a committed relationship that a redelivery would create a
	// second time (a fresh token per attempt is NOT idempotent).
	//
	// Stamp the COMPLETE membership union — the device AND its EXISTING tracked anchors
	// (ADR-062 S5) — exactly as the standard telemetry path does. Every resolved event must
	// carry the authoritative membership set, because DETECT's descope path (runtime.Plan)
	// reads a group MISSING from the stamp as "this series left that group" and tears down its
	// keyed state + resolves any raised alarm. Stamping device-only here dropped the
	// memberships the device already holds through its tracked areas (e.g. an "arid areas"
	// geographic scope), so an ordinary assignment event spuriously descoped a live rule —
	// flapping the alarm and cancelling a running hold. The NEW target's own memberships are
	// still omitted (unknown until the relationship exists, and reading them post-create would
	// reintroduce a fallible call after the non-idempotent create); that is safe because the
	// device holds no prior state for a group it only now joined, so their absence tears down
	// nothing. They land on the device's next telemetry event, which anchors the now-tracked
	// relationship.
	scope, reason, err := rez.resolveScope(ctx, device)
	if err != nil {
		return nil, reason, err
	}
	_, memberships, reason, err := rez.deviceAnchors(ctx, device)
	if err != nil {
		return nil, reason, err
	}

	// Create new relationship from the event payload.
	created, reason, err := rez.CreateNewEntityRelationship(ctx, device, *relcreate)
	if err != nil {
		return nil, reason, errors.New("could not create relationship")
	}

	// Convert to resolved payload with the uniform (type, id) target.
	payload := &model.ResolvedNewRelationshipPayload{
		RelationshipTypeId: uint64(created.RelationshipTypeId),
		TargetType:         &created.TargetType,
		TargetId:           proto.NullUint64Of(&created.TargetId),
	}

	// Merge info from device and created assignment into event — the new
	// relationship is itself the event's single anchor, addressed by target token.
	anchors := []model.ResolvedAnchor{
		{AnchorType: created.TargetType, AnchorToken: created.TargetToken, RelationshipId: created.ID},
	}
	resolved := rez.MergeToResolveEvent(device, anchors, memberships, event, payload, scope)

	return []EventResolutionResults{*resolved}, 0, nil
}

// Resolve a locations event payload.
func (rez *EventResolver) ResolveLocationsEventPayload(ctx context.Context, device *model.Device,
	relation *model.EntityRelationship, event *esmodel.UnresolvedEvent) (interface{}, error) {
	if lpayload, ok := event.Payload.(*esmodel.UnresolvedLocationsPayload); ok {
		rlpayload := &model.ResolvedLocationsPayload{}
		rlentries := make([]model.ResolvedLocationEntry, 0)
		for _, ulentry := range lpayload.Entries {
			rlentry := model.ResolvedLocationEntry{
				Latitude:     ulentry.Latitude,
				Longitude:    ulentry.Longitude,
				Elevation:    ulentry.Elevation,
				Accuracy:     ulentry.Accuracy,
				Speed:        ulentry.Speed,
				Heading:      ulentry.Heading,
				OccurredTime: rez.eventTime.boundEntry(ulentry.OccurredTime, event.OccurredTime, event.ProcessedTime),
			}
			rlentries = append(rlentries, rlentry)
		}
		rlpayload.Entries = rlentries
		return rlpayload, nil
	}
	return nil, fmt.Errorf("can not resolve locations payload. invalid unresolved payload type")
}

// Resolve a measurements event payload. Each measurement is bound to its metric
// definition (ADR-016): the classifier FK plus the denormalized unit + data type
// make the stored value self-describing on read (no cross-service hop back to the
// definition), and a BOOLEAN metric is normalized to 0/1 so it stores in the
// numeric measurement column. An undeclared key resolves unclassified and
// unchanged when its value is numeric (lenient — matching validateMeasurements);
// an undeclared NON-numeric value cannot land in the numeric column, so that one
// entry is dropped (logged) rather than dead-lettering the whole event and losing
// its valid siblings. Callers must have run validateMeasurements first: this drops
// only undeclared non-numeric (and defensively, non-storable declared) values, so a
// DECLARED numeric metric carrying a non-numeric value relies on validation having
// already rejected it upstream.
func (rez *EventResolver) ResolveMeasurementsEventPayload(ctx context.Context, device *model.Device,
	relation *model.EntityRelationship, event *esmodel.UnresolvedEvent) (interface{}, error) {
	mpayload, ok := event.Payload.(*esmodel.UnresolvedMeasurementsPayload)
	if !ok {
		return nil, fmt.Errorf("can not resolve measurements payload. invalid unresolved payload type")
	}
	byKey, err := rez.metricDefsByKey(ctx, device)
	if err != nil {
		return nil, err
	}
	rmpayload := &model.ResolvedMeasurementsPayload{}
	rmsentries := make([]model.ResolvedMeasurementsEntry, 0)
	for _, umsentry := range mpayload.Entries {
		rmentries := make([]model.ResolvedMeasurementEntry, 0)
		for mxkey, mxvalue := range umsentry.Measurements {
			rmentry := model.ResolvedMeasurementEntry{Name: mxkey, Value: mxvalue}
			if def, declared := byKey[mxkey]; declared {
				if !model.MetricDataType(def.DataType).StorableAsMetric() {
					// Declared but non-storable (STRING is device state, not a
					// time-series metric — ADR-016). Creating such a definition is
					// already rejected, so this is a defensive backstop: drop the entry
					// rather than stamp a classifier onto a value that would then
					// dead-letter the whole batch at persist.
					dropMeasurement(device.Token, mxkey, mxvalue, "declared metric data type is not storable")
					continue
				}
				// The classifier is the metric definition's id AS OF the profile's
				// active PUBLISHED version (ADR-045 slice c) — the definitions come
				// from that version's snapshot, not the live draft. A later draft
				// edit/delete does not change already-stamped classifiers, and a
				// classifier can outlive its (hard-deleted) draft row. unit + data
				// type are denormalized from that same snapshot definition here, so
				// the persisted measurement is self-describing without ever resolving
				// the classifier back to a (possibly edited/deleted) live definition.
				id := uint64(def.ID)
				rmentry.Classifier = &id
				dataType := def.DataType
				rmentry.DataType = &dataType
				if def.Unit.Valid {
					unit := def.Unit.String
					rmentry.Unit = &unit
				}
				if model.MetricDataType(def.DataType) == model.MetricBoolean {
					rmentry.Value = normalizeBool(mxvalue)
				}
			} else if _, err := strconv.ParseFloat(mxvalue, 64); err != nil {
				// Undeclared and non-numeric: a measurement stores in the numeric
				// column, so this value cannot be persisted. Undeclared keys are
				// best-effort (ADR-016 lenient additive typing), so drop just this
				// entry instead of dead-lettering the whole event and discarding its
				// valid numeric siblings. Declare it as a metric to persist it.
				dropMeasurement(device.Token, mxkey, mxvalue, "undeclared and non-numeric")
				continue
			}
			rmentries = append(rmentries, rmentry)
		}
		// 🔴 SORT BY NAME, AND IT IS LOAD-BEARING — not a tidiness pass.
		//
		// The loop above ranges a MAP, and Go randomises map iteration, so without this
		// the same reading resolves into a differently-ordered slice on every attempt.
		// That order is not cosmetic: event-management derives the event's IDENTITY from
		// json.Marshal of this payload (DeriveEventId), and that id is the primary key
		// that makes a redelivery idempotent. A varying order means a varying id, so a
		// re-resolution misses ON CONFLICT and the event double-persists — parent row,
		// every measurement row (payload_id is scoped to the parent id), and a doubled
		// sum and count in the measurement_rollups aggregate.
		//
		// Measured, and the number is smaller than the theory: 400 resolutions of ONE
		// five-metric reading produced exactly 5 distinct ids, not the 120 that 5! would
		// suggest — Go rotates the iteration start within a bucket rather than permuting
		// freely, so a small map has about as many orders as it has keys. A redelivery
		// therefore reproduced the original id roughly 1 time in 5.
		//
		// Sorting HERE rather than in the digest is deliberate. There is no device order
		// being discarded: UnresolvedMeasurementsEntry.Measurements is a map[string]string
		// on the model AND a proto map field on the wire, so the device's JSON object
		// order was already gone one hop upstream. This loop does not preserve an order,
		// it invents one — so this is the single place where the arbitrariness enters, and
		// the digest (which sees only the payload) becomes stable as a result. Name is a
		// total order because map keys are unique, so no two entries here can share one.
		//
		// 🔴 WHAT THIS DOES NOT MAKE DETERMINISTIC, so nobody builds on a promise it does
		// not keep: the resolved EVENT's bytes as a whole. Anchors and ScopeMemberships are
		// filled from DB reads that carry no ORDER BY, so their order can still vary between
		// two resolutions. That is harmless for identity — neither reaches DeriveEventId,
		// which hashes the payload alone — but it means "the resolved event replays
		// byte-identically" is NOT a property this establishes.
		slices.SortFunc(rmentries, func(a, b model.ResolvedMeasurementEntry) int {
			return strings.Compare(a.Name, b.Name)
		})
		rmsentry := model.ResolvedMeasurementsEntry{
			Entries:      rmentries,
			OccurredTime: rez.eventTime.boundEntry(umsentry.OccurredTime, event.OccurredTime, event.ProcessedTime),
		}
		rmsentries = append(rmsentries, rmsentry)
	}
	rmpayload.Entries = rmsentries
	return rmpayload, nil
}

// metricDefsByKey loads the device profile's metric definitions keyed by MetricKey
// (cached through the API). Returns an empty map when none are declared.
func (rez *EventResolver) metricDefsByKey(ctx context.Context,
	device *model.Device) (map[string]*model.MetricDefinition, error) {
	defs, err := rez.Api.MetricDefinitionsByDeviceType(ctx, device.DeviceTypeId)
	if err != nil {
		return nil, err
	}
	byKey := make(map[string]*model.MetricDefinition, len(defs))
	for _, d := range defs {
		byKey[d.MetricKey] = d
	}
	return byKey, nil
}

// dropMeasurement logs an unstorable measurement entry that is being discarded
// during resolution. Discarding device data is a misconfiguration worth surfacing
// (the device sends something the numeric measurement column can never hold), so it
// warns rather than debugs — matching the anchor-skip warning elsewhere in this
// file. The remedy is in the message: declare the key as a numeric metric.
func dropMeasurement(deviceToken, metricKey, value, reason string) {
	log.Warn().Str("device", deviceToken).Str("metric", metricKey).Str("value", value).Str("reason", reason).
		Msg("Dropping unstorable measurement (declare it as a numeric metric to persist)")
}

// normalizeBool renders a validated boolean measurement as "1"/"0" so it stores in
// the numeric measurement column. A value that does not parse is left unchanged
// (validateMeasurements already rejected a non-boolean value upstream).
func normalizeBool(value string) string {
	b, err := strconv.ParseBool(value)
	if err != nil {
		return value
	}
	if b {
		return "1"
	}
	return "0"
}

// Resolve a alerts event payload.
func (rez *EventResolver) ResolveAlertsEventPayload(ctx context.Context, device *model.Device,
	relation *model.EntityRelationship, event *esmodel.UnresolvedEvent) (interface{}, error) {
	if apayload, ok := event.Payload.(*esmodel.UnresolvedAlertsPayload); ok {
		rapayload := &model.ResolvedAlertsPayload{}
		raentries := make([]model.ResolvedAlertEntry, 0)
		for _, uaentry := range apayload.Entries {
			raentry := model.ResolvedAlertEntry{
				Type:         uaentry.Type,
				Level:        uaentry.Level,
				Message:      uaentry.Message,
				Source:       uaentry.Source,
				OccurredTime: rez.eventTime.boundEntry(uaentry.OccurredTime, event.OccurredTime, event.ProcessedTime),
			}
			raentries = append(raentries, raentry)
		}
		rapayload.Entries = raentries
		return rapayload, nil
	}
	return nil, fmt.Errorf("can not resolve alerts payload. invalid unresolved payload type")
}

// Resolve a state-change (presence) event payload (ADR-067). Validates the state
// enum and parses the producer-supplied session id into a uint64. A malformed
// payload is a deterministic failure (a bad producer), surfaced as an error so
// the caller dead-letters it rather than retrying it forever.
func (rez *EventResolver) ResolveStateChangeEventPayload(ctx context.Context, device *model.Device,
	relation *model.EntityRelationship, event *esmodel.UnresolvedEvent) (interface{}, error) {
	payload, ok := event.Payload.(*esmodel.UnresolvedStateChangePayload)
	if !ok {
		return nil, fmt.Errorf("can not resolve state-change payload. invalid unresolved payload type")
	}
	switch payload.State {
	case esmodel.PresenceConnected, esmodel.PresenceDisconnected, esmodel.PresenceDemoted:
	default:
		return nil, fmt.Errorf("invalid presence state %q in state-change event", payload.State)
	}
	sessionId, err := parseSessionId(payload.SessionId, "session id")
	if err != nil {
		return nil, err
	}
	// The SAME guarded parse, deliberately not a second hand-written one. A compare-and-set
	// precondition is matched against a stored session id, so a rule that accepted a range
	// the session field rejects could never match anything — and would fail as a silent
	// no-repair rather than as an error.
	expectedSessionId, err := parseSessionId(payload.ExpectedSessionId, "expected session id")
	if err != nil {
		return nil, err
	}
	// A demotion releases a NAMED session, and both of these refusals exist because the
	// alternative is a silent no-op rather than an error. presence.Decide accepts a
	// demotion only when its session equals the one the row already holds, so a
	// session-less demotion can never match anything: it would be admitted here, travel
	// the whole pipeline, and be dropped by the ordering guard as "stale" — indis-
	// tinguishable from a genuinely late echo, and leaving the row exactly as wedged as
	// before. And a compare-and-set precondition on a demotion is incoherent by
	// construction: the demotion is ALREADY matched against the stored session, so an
	// ExpectedSessionId either restates that or contradicts it.
	if payload.State == esmodel.PresenceDemoted {
		if sessionId == 0 {
			return nil, fmt.Errorf("state-change event demotes with no session id; a demotion must name the session it releases")
		}
		if expectedSessionId != 0 {
			return nil, fmt.Errorf("state-change event carries expected session id %d on a demotion; a demotion is already matched against the stored session", expectedSessionId)
		}
		// The third silent no-op, and the reason it is refused here rather than guarded in
		// one emitter: a demotion is accepted only when its stamp is strictly AFTER the
		// row's, so an unstamped one is rejected downstream as "stale" and the row it was
		// releasing stays frozen. Every other producer defect in this family was made loud
		// for the same reason. This is the boundary all producers cross — the transports'
		// emitter and the operator mutation, which does not share it.
		//
		// Only the ZERO case is knowable here. A demotion stamped at or before the stored
		// PresenceTime is the same failure and cannot be seen without reading the
		// projection, so a producer must stamp a fresh clock rather than echo a value it
		// read.
		//
		// 🔑 IT GUARDS THE ENVELOPE'S TIME, NOT THE PAYLOAD'S. The payload carries a
		// descriptive copy; the ordering stamp both consumers hand to presence.Decide is
		// ResolvedEvent.OccurredTime, which comes from the envelope. And event-time
		// bounding does not rescue this: eventtime.Effective bounds the FUTURE direction
		// only, so a zero instant passes through it untouched.
		if event.OccurredTime.IsZero() {
			return nil, fmt.Errorf("state-change event demotes with no occurred time; a demotion is applied only when it is newer than the row it releases")
		}
	}
	return &model.ResolvedStateChangePayload{
		State:             string(payload.State),
		Reason:            payload.Reason,
		ExpectedSessionId: expectedSessionId,
		SessionId:         sessionId,
		OccurredTime:      payload.OccurredTime,
	}, nil
}

// parseSessionId parses a wire-format session id. Empty is absent (zero), which for
// SessionId means "the producer sent none" and for ExpectedSessionId means "no
// compare-and-set claim".
//
// 🔴 THE UPPER BOUND IS NOT COSMETIC. Both sinks (device-state, event-management) store a
// session id in a signed bigint, so a value above MaxInt64 is unstorable. Rejecting it
// HERE makes it a deterministic failure the caller dead-letters; left to the database, the
// pgx "greater than maximum int8" error is not classified deterministic and the message
// burns every MaxDeliver redelivery first — a poison loop.
func parseSessionId(raw, field string) (uint64, error) {
	if raw == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q in state-change event: %w", field, raw, err)
	}
	if parsed > math.MaxInt64 {
		return 0, fmt.Errorf("%s %q in state-change event exceeds the storable range (max %d)",
			field, raw, int64(math.MaxInt64))
	}
	return parsed, nil
}

// Convert an unresolved event payload into a resolved payload.
func (rez *EventResolver) ResolveEventPayload(ctx context.Context, device *model.Device,
	relation *model.EntityRelationship, event *esmodel.UnresolvedEvent) (interface{}, error) {
	switch event.EventType {
	case esmodel.Location:
		return rez.ResolveLocationsEventPayload(ctx, device, relation, event)
	case esmodel.Measurement:
		return rez.ResolveMeasurementsEventPayload(ctx, device, relation, event)
	case esmodel.Alert:
		return rez.ResolveAlertsEventPayload(ctx, device, relation, event)
	case esmodel.StateChange:
		return rez.ResolveStateChangeEventPayload(ctx, device, relation, event)
	default:
		return nil, fmt.Errorf("unable to handle resolution for payload type: %s", event.EventType.String())
	}
}

// HandleStandardEvent resolves a location/measurement/alert event into exactly one
// resolved event. The event belongs to the device; each of the device's tracked
// relationships is denormalized as an anchor so the event is queryable by every
// assignment dimension, and an unassigned device resolves anchorless rather than
// being dropped (ADR-013 addendum 2026-07-01).
func (rez *EventResolver) HandleStandardEvent(ctx context.Context,
	device *model.Device, event *esmodel.UnresolvedEvent) ([]EventResolutionResults, uint, error) {
	// Validate measurements against the device's metric definitions (resolved via
	// its type's profile, ADR-016/ADR-045): a non-conforming value routes the whole event to the dead-letter
	// path rather than persisting bad data.
	if event.EventType == esmodel.Measurement {
		if reason, err := rez.validateMeasurements(ctx, device, event); err != nil {
			return nil, reason, err
		}
	}

	// Resolve the payload once — it does not depend on the anchors.
	resolved, err := rez.ResolveEventPayload(ctx, device, nil, event)
	if err != nil {
		return nil, uint(dmproto.FailureReason_ApiCallFailed), err
	}

	// Denormalize the full set of the device's tracked relationships as anchors, and the
	// device+anchor dynamic-group memberships (ADR-062) stamped alongside them.
	anchors, memberships, reason, err := rez.deviceAnchors(ctx, device)
	if err != nil {
		return nil, reason, err
	}
	if len(anchors) == 0 {
		log.Debug().Str("device", device.Token).
			Msg("Resolving event with no anchors (device has no tracked relationship)")
	}

	// Denormalize the rule-scoping identity (ADR-051) onto the event.
	scope, reason, err := rez.resolveScope(ctx, device)
	if err != nil {
		return nil, reason, err
	}

	// Surface a position reported by a device whose profile never declared one
	// (ADR-078). Deliberately AFTER the payload has already resolved and with no
	// return value: it observes, it never gates. Note it does not appear next to the
	// measurement validation above, which CAN fail the event — putting it there would
	// invite the next reader to give it the same power.
	if event.EventType == esmodel.Location {
		rez.warnIfLocationUndeclared(ctx, device, scope)
	}

	result := rez.MergeToResolveEvent(device, anchors, memberships, event, resolved, scope)
	return []EventResolutionResults{*result}, 0, nil
}

// warnIfLocationUndeclared reports, at most once per (device, profile version), that a
// device is sending positions its profile does not declare (ADR-078).
//
// 🔴 It cannot reject, filter or alter anything, and that is the point. An undeclared
// position is a description gap, never a permission failure: the fix is stored in
// full, exactly as a declared one would be, because a device that started reporting
// its position before someone got around to editing the profile must not lose data
// over an unedited form. Everything here is therefore side-effect-free apart from a
// log line — no error is returned, and a failure to even determine the answer is
// swallowed at debug level rather than escalated.
//
// Precedent note, because it is easy to copy the wrong one: the nearest existing
// warning, dropMeasurement, fires only when an entry is DISCARDED — an undeclared but
// numeric measurement is stored SILENTLY, with no warning at all. So there is no
// "stored with a warning" behaviour in this file to imitate; this adds one, and the
// per-(device, version) bound is what makes adding it safe.
func (rez *EventResolver) warnIfLocationUndeclared(ctx context.Context,
	device *model.Device, scope *model.ProfileScope) {
	versionToken := ""
	if scope != nil {
		versionToken = scope.ProfileVersionToken
	}
	// Tenant is part of the key because device tokens are unique per tenant, not
	// globally. No tenant in context means no key that could isolate two tenants'
	// identically-named devices, so skip the memo's bookkeeping rather than risk one
	// tenant's device muting another's.
	tenant, hasTenant := core.TenantFromContext(ctx)
	if !hasTenant {
		return
	}

	// The cheap gate first: a (device, version) already reported never reaches the
	// declaration lookup, so the steady-state cost of a chatty undeclared device is a
	// map probe, not a query.
	if rez.locationMemo.alreadyWarned(tenant, device.Token, versionToken) {
		return
	}

	declared, cached := rez.locationMemo.declared(device.DeviceTypeId, versionToken)
	if !cached {
		decl, err := rez.Api.LocationDeclarationByDeviceType(ctx, device.DeviceTypeId)
		if err != nil {
			// Nothing is cached and nothing is claimed, so a transient failure is retried
			// on the device's next fix instead of permanently suppressing the warning.
			// Debug, not warn: failing to describe an event is not worth a warning of its
			// own on a path whose job is to reduce log volume.
			log.Debug().Err(err).Str("device", device.Token).
				Msg("Unable to resolve the profile's location declaration; skipping the undeclared-position check")
			return
		}
		declared = decl != nil
		rez.locationMemo.rememberDeclared(device.DeviceTypeId, versionToken, declared)
	}
	if declared {
		return
	}

	// Claim under one lock so two workers handling two fixes from the same device
	// concurrently produce one warning between them, not one each.
	if !rez.locationMemo.claimWarning(tenant, device.Token, versionToken) {
		return
	}
	log.Warn().Str("device", device.Token).Str("profileVersion", versionToken).
		Msg("Device reported a position its profile does not declare; the position was stored — declare a location on the profile and publish it to describe the expected accuracy and update rate (this is reported once per device per profile version)")
}

// deviceAnchors returns the device's tracked-relationship targets as anchors —
// one per tracked relationship — or an empty set when the device has no tracked
// relationship. Every anchor is denormalized onto the event (ADR-013 addendum
// 2026-07-01), so a device assigned to several targets is queryable by each.
func (rez *EventResolver) deviceAnchors(ctx context.Context, device *model.Device) ([]model.ResolvedAnchor, []model.GroupRef, uint, error) {
	tracked := true
	sourceType := string(entity.TypeDevice)
	criteria := model.EntityRelationshipSearchCriteria{
		// A device's tracked-relationship set is denormalized in full onto every
		// event, so this genuinely needs all rows — the explicit internal unbounded
		// path, not the (now bounded) default (ADR-029).
		Pagination: rdb.Pagination{Unbounded: true},
		SourceType: &sourceType,
		SourceId:   &device.ID,
		Tracked:    &tracked,
	}
	drels, err := rez.Api.EntityRelationships(ctx, criteria)
	if err != nil {
		return nil, nil, uint(dmproto.FailureReason_ApiCallFailed), err
	}
	anchors := make([]model.ResolvedAnchor, 0, len(drels.Results))
	// The membership stamp (ADR-062) is the union over the device itself and every
	// emitted anchor: a device-facet rule matches on the device's memberships, a
	// geographic rule ("arid areas") matches on an area anchor's.
	targets := []membershipTarget{{Type: string(entity.TypeDevice), Id: device.ID}}
	for i := range drels.Results {
		r := &drels.Results[i]
		// TargetToken is denormalized at relationship-create time (ADR-044). An empty
		// value means a row predating that column on a non-fresh cluster (the migration
		// does not backfill) — emitting it would write an unqueryable empty-token anchor
		// and make the sweep churn on it, so skip it loudly rather than corrupt silently.
		if r.TargetToken == "" {
			log.Warn().Str("device", device.Token).Str("relationship", r.Token).
				Str("targetType", r.TargetType).
				Msg("Skipping anchor with empty target token (relationship predates the ADR-044 denormalization; recreate it)")
			continue
		}
		anchors = append(anchors, model.ResolvedAnchor{
			AnchorType:     r.TargetType,
			AnchorToken:    r.TargetToken,
			RelationshipId: r.ID,
		})
		targets = append(targets, membershipTarget{Type: r.TargetType, Id: r.TargetId})
	}
	memberships, err := rez.unionMemberships(ctx, targets)
	if err != nil {
		return nil, nil, uint(dmproto.FailureReason_ApiCallFailed), err
	}
	return anchors, memberships, 0, nil
}

// validateMeasurements enforces the device's metric definitions (resolved via its
// type's profile, ADR-045) against an inbound measurement event (ADR-016). It returns (0, nil) when the
// event conforms or the device type declares no metrics. A transient
// definition-lookup failure returns FailureReason_ApiCallFailed (retryable); a
// non-conforming value returns FailureReason_Invalid, routing the event to the
// dead-letter path. Validation is lenient: an undeclared metric key passes
// through (model.ValidateMeasurement), so the metric model is an additive typing
// layer, not a strict allow-list.
func (rez *EventResolver) validateMeasurements(ctx context.Context, device *model.Device,
	event *esmodel.UnresolvedEvent) (uint, error) {
	payload, ok := event.Payload.(*esmodel.UnresolvedMeasurementsPayload)
	if !ok {
		return 0, nil
	}
	byKey, err := rez.metricDefsByKey(ctx, device)
	if err != nil {
		return uint(dmproto.FailureReason_ApiCallFailed), err
	}
	if len(byKey) == 0 {
		return 0, nil
	}
	for _, entry := range payload.Entries {
		for name, value := range entry.Measurements {
			if verr := model.ValidateMeasurement(byKey, name, value); verr != nil {
				return uint(dmproto.FailureReason_Invalid), verr
			}
		}
	}
	return 0, nil
}

// Route event to handlers based on event type.
func (rez *EventResolver) HandleEvent(ctx context.Context,
	device *model.Device, unresolved *esmodel.UnresolvedEvent) ([]EventResolutionResults, uint, error) {
	switch unresolved.EventType {
	case esmodel.NewRelationship:
		return rez.HandleNewRelationshipEvent(ctx, device, unresolved)
	case esmodel.Location, esmodel.Measurement, esmodel.Alert, esmodel.StateChange:
		return rez.HandleStandardEvent(ctx, device, unresolved)
	default:
		return nil, uint(dmproto.FailureReason_Invalid), fmt.Errorf("unhandled event type: %s", unresolved.EventType.String())
	}
}

// presentedCredential extracts the credential a device offered on the inbound
// event (ADR-014), or nil when none was presented. An empty credential id counts
// as none so an event carrying blank fields falls through to the configured
// no-credential behaviour rather than failing authentication.
func presentedCredential(unrez *esmodel.UnresolvedEvent) *model.PresentedCredential {
	if unrez.CredentialType == nil || unrez.CredentialId == nil || *unrez.CredentialId == "" {
		return nil
	}
	return &model.PresentedCredential{
		CredentialType: *unrez.CredentialType,
		CredentialId:   *unrez.CredentialId,
		Secret:         unrez.CredentialSecret,
	}
}

// transportAuthenticatedBypass reports whether an event may skip the required-mode
// credential check because a trusted internal ingest source authenticated the device
// at the TRANSPORT (LwM2M DTLS-PSK / Sparkplug broker) upstream and marked the event.
// The event then resolves on its self-asserted device token, exactly as the
// disabled/optional transports already do (config.AuthMode doc, ADR-014/025).
//
// This is safe because the marker is NOT device-forgeable: ADR-025 confines a
// device's NATS publish to its own devices.{token}.events subject, and the
// device->inbound-events gateway (event-sources JsonDecoder) copies only named
// payload fields — it has no field for this marker — so only the trusted service
// account can ever set it. LwM2M binds the device token to the authenticated PSK
// identity (per-device, ADR-075 D1). Sparkplug's token is topic-derived, so this is
// BROKER-level not per-device — `required` does NOT close intra-tenant device-token
// spoofing for Sparkplug (it still does for HTTP/MQTT credential paths); cross-tenant
// stays closed via connection-scoped tenancy. Real per-device Sparkplug auth is a
// tracked gap.
//
// The bypass is confined to the event types the transport-authenticated ingest path
// actually emits (Measurement, StateChange). A marked event of ANY other type does
// not bypass the credential check, so a future emit path for a more dangerous type
// cannot silently inherit this trust.
func transportAuthenticatedBypass(unrez *esmodel.UnresolvedEvent) bool {
	if !unrez.AuthenticatedTransport {
		return false
	}
	switch unrez.EventType {
	case esmodel.Measurement, esmodel.StateChange:
		return true
	default:
		return false
	}
}

// resolveDevice determines the originating device for an event, enforcing the
// configured device authentication policy (transport security, ADR-014):
//   - disabled: the self-asserted device token is trusted (legacy path).
//   - optional: a presented credential is authenticated and authoritative; with
//     no credential the device token is trusted.
//   - required: a valid credential must be presented and is authoritative — UNLESS
//     the event was marked transport-authenticated by a trusted internal ingest
//     source, which resolves on its self-asserted token (transportAuthenticatedBypass).
//
// When a credential authenticates, the resolved device is authoritative: a
// self-asserted token naming a different device is rejected so one authenticated
// device can not impersonate another.
func (rez *EventResolver) resolveDevice(ctx context.Context, unrez *esmodel.UnresolvedEvent) (*model.Device, uint, error) {
	if rez.AuthMode != config.AuthModeDisabled {
		if presented := presentedCredential(unrez); presented != nil {
			device, err := rez.Api.AuthenticateDevice(ctx, presented, time.Now())
			if err != nil {
				return nil, uint(dmproto.FailureReason_Unauthenticated), err
			}
			if unrez.Device != "" && unrez.Device != device.Token {
				return nil, uint(dmproto.FailureReason_Unauthenticated),
					fmt.Errorf("event device token %q does not match authenticated device %q", unrez.Device, device.Token)
			}
			return device, 0, nil
		}
		if rez.AuthMode == config.AuthModeRequired && !transportAuthenticatedBypass(unrez) {
			return nil, uint(dmproto.FailureReason_Unauthenticated),
				errors.New("device authentication required but no credential was presented")
		}
	}

	matches, err := rez.Api.DevicesByToken(ctx, []string{unrez.Device})
	if err != nil {
		return nil, uint(dmproto.FailureReason_ApiCallFailed), err
	}
	if len(matches) == 0 {
		// A missing token is DevicesByToken returning ([], nil): gorm's Find reports
		// no error on an empty result set, so this branch MUST synthesize the error
		// itself. Without it resolveDevice returns a nil *Device AND a nil error;
		// ResolveEvent only checks err != nil, hands the nil device to HandleEvent,
		// and the resolver nil-derefs — which JetStream then crash-loops on
		// redelivery. Returning an error routes it to the designed retry-then-
		// dead-letter path (Process loop: a not-yet-registered device may appear).
		return nil, uint(dmproto.FailureReason_DeviceNotFound),
			fmt.Errorf("event device token %q is not registered", unrez.Device)
	}
	return matches[0], 0, nil
}

// Execute logic to resolve event.
func (rez *EventResolver) ResolveEvent(ctx context.Context, unrez *esmodel.UnresolvedEvent) ([]EventResolutionResults, uint, error) {
	device, reason, err := rez.resolveDevice(ctx, unrez)
	if err != nil {
		return nil, reason, err
	}
	return rez.HandleEvent(ctx, device, unrez)
}

// Converts unresolved events into resolved events.
func (rez *EventResolver) Process(ctx context.Context) {
	for {
		unresolved, more := <-rez.Unresolved
		if more {
			// RED instrumentation for the resolve loop (E13): Start marks the
			// message in-flight and starts its timer; done(result) is called
			// exactly once on every disposition path below. correlation ties the
			// inbound message into the per-message log context (E15).
			done := rez.metrics.Start()
			correlation := unresolved.CorrelationID()
			log.Debug().Str("correlation", correlation).Msg(fmt.Sprintf("Event resolution handled by resolver id %d", rez.WorkerId))

			// Derive the per-message tenant from the message subject and build a
			// tenant-scoped context. Without a parseable tenant the message can
			// not be processed safely (fail-closed) so it is skipped. The tenant
			// string is carried onto the resolved/failed channels so the
			// downstream producer can publish to the same tenant's subject.
			msgctx, tenant, ok := messaging.TenantContextFromSubject(ctx, unresolved.Subject)
			if !ok {
				log.Warn().Str("correlation", correlation).Msg(fmt.Sprintf("Skipping message with no parseable tenant in subject %q", unresolved.Subject))
				// No tenant means the message can never be processed; ack it so it
				// is not redelivered (A3 — drop poison).
				_ = unresolved.Ack()
				done(core.ResultInvalid)
				continue
			}

			// Attempt to unmarshal event.
			event, err := esproto.UnmarshalUnresolvedEvent(unresolved.Value)
			if err != nil {
				// Unparseable payload routes to the failed-events dead-letter path
				// and is acked (terminal; redelivery cannot help).
				rez.Invalid(err, unresolved)
				_ = unresolved.Ack()
				done(core.ResultInvalid)
				continue
			}

			if log.Debug().Enabled() {
				jevent, err := json.MarshalIndent(event, "", "  ")
				if err == nil {
					log.Debug().Str("correlation", correlation).Msg(fmt.Sprintf("Received %s event:\n%s", event.EventType.String(), jevent))
				}
			}

			// Attempt to resolve event using the per-message tenant context.
			resolved, reason, err := rez.ResolveEvent(msgctx, event)
			if err != nil {
				// Resolution failed. Retry via redelivery (a transient lookup error
				// may clear, and a not-yet-registered device may appear) until the
				// delivery cap, then route to the failed-events dead-letter path and
				// ack so a permanently-unresolvable event stops looping (A4).
				if unresolved.NumDelivered >= messaging.MaxDeliver {
					rez.Failed(tenant, reason, *event, err, correlation)
					_ = unresolved.Ack()
					done(core.ResultFailed)
				} else {
					// Transient: leave it UNACKED (do not nak) so AckWait paces
					// redelivery — an immediate nak would burn MaxDeliver in ~1.4ms
					// inside an outage. Reference: event-sources' settler (ADR-030).
					done(core.ResultRetry)
				}
			} else {
				// On success the source is acked only after every resolved event it
				// produced has been durably published (handled via the fan-out ack
				// coordinator in OnResolvedEvent / ProcessResolvedEvent).
				rez.Resolved(unresolved, tenant, resolved)
				done(core.ResultOK)
			}
		} else {
			log.Debug().Msg("Event resolver received shutdown signal.")
			return
		}
	}
}
