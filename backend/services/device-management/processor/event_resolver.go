// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/devicechain-io/dc-microservice/presence"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-device-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/eventtime"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/proto"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// Worker used to resolve event entities.
type EventResolver struct {
	WorkerId int
	Api      model.DeviceManagementApi
	AuthMode string
	// SecretTransit opens the sealed credential secret an inbound event carries
	// (event-sources seals it; the stream never holds a password as sent). Nil opens
	// nothing, so every event presenting a secret fails to authenticate.
	SecretTransit *credential.SecretTransit
	Unresolved    <-chan messaging.Message
	// Invalid is handed a message that could not be decoded. It owns the message's ack
	// from then on: the source is acked once its failed-event record is stored.
	Invalid func(error, messaging.Message)
	// Resolved is handed the source message so its ack can be coordinated across
	// the 1->N resolved-event fan-out (the source is acked only once every
	// resolved event it produced has been durably published; ADR-022 review A3).
	Resolved func(messaging.Message, string, []EventResolutionResults)
	// Failed is handed the inbound message (first argument) and owns its ack from
	// then on, exactly as Invalid does. The message also supplies the correlation id
	// the outbound failed event is stamped with, so it stays traceable end to end
	// (ADR-022 review E15).
	Failed func(messaging.Message, string, uint, esmodel.UnresolvedEvent, error)
	// metrics records RED instrumentation for the resolve loop (ADR-022 review
	// E13). It is shared across all workers and may be nil in tests; Start() is
	// nil-safe so a nil value no-ops.
	metrics *core.ProcessorMetrics
	// locationMemo bounds the undeclared-position warning to one line per (device,
	// profile version) (ADR-078). It is SHARED across the whole worker pool — see
	// newUndeclaredLocationMemo — so the bound is per process, not per worker.
	locationMemo *undeclaredLocationMemo
	// slow reports resolves that take seconds. Like locationMemo it is SHARED across the
	// pool (initializeEventResolvers replaces the private one NewEventResolver gives).
	slow *slowResolveReporter
	// eventTime is the platform's event-time policy. The instant a reading is resolved to is
	// decided HERE and nowhere else; the age refusal also runs upstream, from the same
	// constant (see package eventtime).
	eventTime EventTimePolicy
	// tenantDeleted reports a tenant that has been through the delete door (the pool sets it
	// to the processor's, which asks the dead-letter producer). Nil reads "not deleted". It is
	// asked only when resolution FAILS: a deleted tenant's event usually fails before any
	// write the erasure fence could refuse — its device and credentials are among the first
	// rows the deletion removes — and would otherwise be retried to the cap and recorded on
	// failed-events, a new record of the data being erased.
	tenantDeleted func(string) bool
	// deviceDisabled counts events refused because their device is disabled. ONE counter
	// for the pool (initializeEventResolvers sets it), label-free; nil in tests that do not
	// look at it.
	deviceDisabled prometheus.Counter
	// gateway is what a refused gateway publish does besides being dead-lettered: count it, log
	// it and tell the gateway. SHARED across the pool (initializeEventResolvers replaces the
	// private one NewEventResolver gives); see gateway_refusal.go.
	gateway *gatewayRefusals
	// calcObs reports calculated-metric outcomes (ADR-082). Its metrics are the pool's
	// (initializeEventResolvers sets them; nil in tests, which counts nothing); the rest is
	// pointed at each event as it is resolved, which is safe because a resolver resolves one
	// event at a time.
	calcObs calcObserver
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
	// TooOld counts events refused because a reported time is more than eventtime.MaxAge
	// before the platform received it — one increment per refused event. Label-free like
	// Bounded, and may be nil in tests.
	TooOld prometheus.Counter
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
	Resolved *resolvedevent.ResolvedEvent
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
	failed func(messaging.Message, string, uint, esmodel.UnresolvedEvent, error),
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
		slow:         newSlowResolveReporter(),
		gateway:      newGatewayRefusals(),
	}
}

// MergeToResolveEvent assembles a resolved event from the inbound event and the
// device's tracked-relationship targets, denormalized as a set of uniform
// (type, token) anchors (ADR-013/044). An empty anchor set yields an anchorless
// event — the device is unassigned — which still persists and projects, keyed on
// the device, rather than being dropped (ADR-013 addendum 2026-07-01). The source
// device travels as its token (ADR-044): the numeric row id stays inside
// device-management and never reaches event-management / device-state.
func (rez *EventResolver) MergeToResolveEvent(device *model.Device, anchors []resolvedevent.ResolvedAnchor,
	memberships []resolvedevent.GroupRef, event *esmodel.UnresolvedEvent, rezPayload interface{}, scope *model.ProfileScope) *EventResolutionResults {
	externalId := ""
	if device.ExternalId.Valid {
		externalId = device.ExternalId.String
	}
	resolved := &resolvedevent.ResolvedEvent{
		Source:              event.Source,
		AltId:               event.AltId,
		SourceDeviceToken:   device.Token,
		ExternalId:          externalId,
		DeviceTypeToken:     scope.DeviceTypeToken,
		ProfileVersionToken: scope.ProfileVersionToken,
		Anchors:             anchors,
		ScopeMemberships:    memberships,
		// Who carried the event, when a gateway did: the broker authenticated it, and the
		// child's assignment named it (resolveGatewayChild refuses the event before this
		// otherwise, so a token here has been checked). SourceDeviceToken stays the child.
		GatewayToken: event.Gateway,
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
	if event.EventType == eventtype.Location {
		resolved.FenceSetVersion = scope.FenceSetVersion
	}
	return &EventResolutionResults{Device: device, Resolved: resolved}
}

// unionMemberships resolves and de-duplicates the rule-scoped group memberships across
// the given targets (the device ∪ each tracked anchor) into the event's ScopeMemberships
// (ADR-062). Each read is served from the negative-caching membership cache, so a
// non-member target (the common case) is a cache hit returning empty. De-dup is by
// (group token, version): a device tracked into two arid areas is in scope once.
//
// api is the event's own (model.ReadAheadForEvent). The membership caches of all the
// targets are read at the same time once the gate below has opened; the database is still
// read one target after another, in target order, for whatever they could not answer.
//
// It also returns the device's ACCESS-SCOPE anchors (ADR-081): "group:<token>" for each
// access scope the device itself (targets[0]) is in at the group's published version. Only
// the device's own memberships count — an access scope collects devices, and a device is
// not in a region because an area it is tracked to is. Access scopes are enrolled through
// the same read-model, so the same gate covers them: a tenant with neither pays nothing.
func (rez *EventResolver) unionMemberships(ctx context.Context, api model.DeviceManagementApi,
	targets []model.MembershipTarget) ([]resolvedevent.GroupRef, []resolvedevent.ResolvedAnchor, error) {
	// Pay-nothing short-circuit (ADR-062 Decision 7): a tenant with no rule-scoped group
	// does zero per-target reads — one cached EXISTS check gates the whole union.
	any, err := api.AnyScopedGroups(ctx)
	if err != nil {
		return nil, nil, err
	}
	if !any {
		return nil, nil, nil
	}
	model.ReadMembershipsAhead(ctx, api, targets)
	type mkey struct {
		token   string
		version int32
	}
	seen := make(map[mkey]struct{})
	out := make([]resolvedevent.GroupRef, 0)
	var access []resolvedevent.ResolvedAnchor
	for i, t := range targets {
		ms, err := api.MembershipsForEntity(ctx, t.Type, t.Id)
		if err != nil {
			return nil, nil, err
		}
		for _, m := range ms {
			if i == 0 && m.AccessScope && t.Type == string(entity.TypeDevice) {
				access = append(access, resolvedevent.ResolvedAnchor{AnchorType: auth.AnchorKindGroup, AnchorToken: m.GroupToken})
			}
			k := mkey{m.GroupToken, m.SelectorVersion}
			if _, dup := seen[k]; dup {
				continue
			}
			seen[k] = struct{}{}
			out = append(out, resolvedevent.GroupRef{GroupToken: m.GroupToken, Version: m.SelectorVersion})
		}
	}
	return out, access, nil
}

// resolveProfile reads the device type's active published profile ONCE per event: the
// metric definitions that validate a measurement and stamp its classifiers, and the
// rule-scoping identity (ADR-051) that lets event-processing's DETECT engine select the
// applicable rules from the wire without a graph read. Everything an event takes from
// the profile comes from this one value, so a publish landing mid-resolution cannot
// give one event two versions — reading it twice is exactly the defect this replaces.
//
// Callers resolve it BEFORE any state mutation so a transient lookup failure can never
// leave a committed side effect (e.g. a created relationship) that a redelivery would
// then duplicate.
func (rez *EventResolver) resolveProfile(ctx context.Context, api model.DeviceManagementApi,
	device *model.Device) (model.ProfileResolution, uint, error) {
	res, err := api.ProfileResolutionByDeviceType(ctx, device.DeviceTypeId)
	if err != nil {
		return model.ProfileResolution{}, uint(resolvedevent.FailureReason_ApiCallFailed), fmt.Errorf("could not resolve device profile: %w", err)
	}
	return res, 0, nil
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
		return nil, uint(resolvedevent.FailureReason_ApiCallFailed), err
	}
	return created, 0, nil
}

// Handle a new relationship event.
func (rez *EventResolver) HandleNewRelationshipEvent(ctx context.Context,
	device *model.Device, event *esmodel.UnresolvedEvent) ([]EventResolutionResults, uint, error) {
	relcreate, ok := event.Payload.(*esmodel.UnresolvedNewRelationshipPayload)
	if !ok {
		return nil, uint(resolvedevent.FailureReason_Invalid), errors.New("new relationship payload was not of expected type")
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
	api := model.ReadAheadForEvent(ctx, rez.Api, device)
	res, reason, err := rez.resolveProfile(ctx, api, device)
	if err != nil {
		return nil, reason, err
	}
	existing, memberships, reason, err := rez.deviceAnchors(ctx, api, device)
	if err != nil {
		return nil, reason, err
	}

	// Create new relationship from the event payload.
	created, reason, err := rez.CreateNewEntityRelationship(ctx, device, *relcreate)
	if err != nil {
		// Wrapped, not replaced: the cause decides the message's disposition — an erased
		// tenant's refusal is settled at once rather than retried into a failure report.
		return nil, reason, fmt.Errorf("could not create relationship: %w", err)
	}

	// Convert to resolved payload with the uniform (type, id) target.
	payload := &resolvedevent.ResolvedNewRelationshipPayload{
		RelationshipTypeId: uint64(created.RelationshipTypeId),
		TargetType:         &created.TargetType,
		TargetId:           proto.NullUint64Of(&created.TargetId),
	}

	// Merge info from device and created assignment into event. The event's anchors are the
	// device's anchors AFTER the write, exactly as its next telemetry event will carry them:
	// every tracked relationship it already had, the access scopes it is in, and the new
	// relationship, addressed by target token. Stamping only the new one left this event —
	// in the history, and in the live view until the ownership fact arrived — visible to
	// the new target alone, and denied to the device's other owners (ADR-081).
	anchors := existing
	newAnchor := resolvedevent.ResolvedAnchor{AnchorType: created.TargetType, AnchorToken: created.TargetToken, RelationshipId: created.ID}
	if !slices.ContainsFunc(anchors, func(a resolvedevent.ResolvedAnchor) bool {
		return a.AnchorType == newAnchor.AnchorType && a.AnchorToken == newAnchor.AnchorToken
	}) {
		anchors = append(anchors, newAnchor)
	}
	scope := res.Scope()
	resolved := rez.MergeToResolveEvent(device, anchors, memberships, event, payload, &scope)

	return []EventResolutionResults{*resolved}, 0, nil
}

// Resolve a locations event payload.
func (rez *EventResolver) ResolveLocationsEventPayload(ctx context.Context, device *model.Device,
	relation *model.EntityRelationship, event *esmodel.UnresolvedEvent) (interface{}, error) {
	if lpayload, ok := event.Payload.(*esmodel.UnresolvedLocationsPayload); ok {
		rlpayload := &resolvedevent.ResolvedLocationsPayload{}
		rlentries := make([]resolvedevent.ResolvedLocationEntry, 0)
		for _, ulentry := range lpayload.Entries {
			rlentry := resolvedevent.ResolvedLocationEntry{
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

// deviceConstants reads the device constants a version's formulas use. The device a
// credential authenticated is the narrow row of the credential lookup, which does not read
// metadata (so a miss costs the same however much a device stores), and metadata is where
// the constants live. The row says so (model.Device.MetadataRead): when the formulas read
// any constant and the row in hand never read its metadata, the device is read by token
// through the cached lookup the token path uses, which a device edit evicts on every
// replica. A row that did read its metadata is used as is, empty or not, and a version
// whose formulas read no constant reads nothing.
//
// The by-token row must be the device the credential authenticated: a token that names
// another row now (the device was deleted and its token reused since the credential row
// was cached) is refused, not computed from a stranger's constants.
func (rez *EventResolver) deviceConstants(ctx context.Context, device *model.Device,
	profile model.ProfileResolution) ([]calcfield.Constant, error) {
	if !profile.ReadsDeviceConstants() {
		return nil, nil
	}
	metadata := device.Metadata
	if !device.MetadataRead() {
		matches, err := rez.Api.DevicesByToken(ctx, []string{device.Token})
		if err != nil {
			return nil, fmt.Errorf("reading the metadata of device %q for its calculated metrics: %w", device.Token, err)
		}
		if len(matches) == 1 && matches[0] != nil {
			if matches[0].ID != device.ID {
				return nil, fmt.Errorf("reading the metadata of device %q for its calculated metrics: the token names row %d, the credential row %d",
					device.Token, matches[0].ID, device.ID)
			}
			metadata = matches[0].Metadata
		}
	}
	return profile.DeviceConstants(metadata), nil
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
// already rejected it upstream. profile is the event's one profile read, the same one
// validation read.
//
// A version with calculated metrics (ADR-082) also has each sample's formulas
// evaluated against that sample (the message's entries sharing an instant) and appended as Derived entries, and a value a
// device sent under a calculated key dropped in favour of the computed one. See
// model/profile_calculation.go; a version with none pays one compare per event.
func (rez *EventResolver) ResolveMeasurementsEventPayload(ctx context.Context, device *model.Device,
	profile model.ProfileResolution, event *esmodel.UnresolvedEvent) (interface{}, error) {
	mpayload, ok := event.Payload.(*esmodel.UnresolvedMeasurementsPayload)
	if !ok {
		return nil, fmt.Errorf("can not resolve measurements payload. invalid unresolved payload type")
	}
	rmpayload := &resolvedevent.ResolvedMeasurementsPayload{}
	rmsentries := make([]resolvedevent.ResolvedMeasurementsEntry, 0)
	// Calculated metrics (ADR-082). A version with none — every profile without formulas —
	// pays this one compare per event and nothing else: no sample is taken, no value parsed.
	calculated := profile.CalculatedCount() > 0
	var constants []calcfield.Constant
	if calculated {
		rez.calcObs.ctx, rez.calcObs.device, rez.calcObs.version = ctx, device.Token, profile.Scope().ProfileVersionToken
		// The device's numeric constants (`device.name` in a formula), read ONCE for the event
		// from the device record in hand when it read its metadata and through the cached
		// by-token read when it did not (a credential's narrow row; see deviceConstants),
		// and nil, with the metadata untouched, for a version whose formulas read none. Each
		// computed value is frozen with the constants as they are now: a later edit to the
		// device never rewrites it.
		var err error
		constants, err = rez.deviceConstants(ctx, device, profile)
		if err != nil {
			return nil, err
		}
	}
	// Calculated metrics read a SAMPLE, and a sample is every measurement of this message that
	// shares an instant — not one payload entry. Sparkplug and LwM2M ingest emit one entry per
	// metric, so a formula over two of them would never see both if each entry were its own
	// sample. groups holds one open Calculation per distinct instant, in first-seen order.
	var groups []calcGroup
	for _, umsentry := range mpayload.Entries {
		occurred := rez.eventTime.boundEntry(umsentry.OccurredTime, event.OccurredTime, event.ProcessedTime)
		rmentries := make([]resolvedevent.ResolvedMeasurementEntry, 0, len(umsentry.Measurements))
		var grp *calcGroup
		if calculated {
			grp = findCalcGroup(&groups, occurred, len(rmsentries), profile, constants)
		}
		for mxkey, mxvalue := range umsentry.Measurements {
			rmentry := resolvedevent.ResolvedMeasurementEntry{Name: mxkey, Value: mxvalue}
			if def, declared := profile.Metric(mxkey); declared {
				if def.Calculated() {
					// A device-sent value under a calculated key: the computed value is
					// authoritative and a device cannot forge one, so this one is dropped
					// (and counted) and the formula's value, if it produces one, is stored
					// under the key instead. Validation skipped it for the same reason.
					rez.calcObs.deviceKeyDropped(mxkey, mxvalue)
					continue
				}
				if !model.MetricDataType(def.DataType()).StorableAsMetric() {
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
				id := uint64(def.ID())
				rmentry.Classifier = &id
				dataType := def.DataType()
				rmentry.DataType = &dataType
				if unit, ok := def.Unit(); ok {
					rmentry.Unit = &unit
				}
				if model.MetricDataType(dataType) == model.MetricBoolean {
					rmentry.Value = normalizeBool(mxvalue)
				}
				if calculated {
					// The value as the device sent it, parsed once as its declared type.
					grp.set(&def, mxkey, mxvalue)
				}
			} else if f, err := strconv.ParseFloat(mxvalue, 64); err != nil {
				// Undeclared and non-numeric: a measurement stores in the numeric
				// column, so this value cannot be persisted. Undeclared keys are
				// best-effort (ADR-016 lenient additive typing), so drop just this
				// entry instead of dead-lettering the whole event and discarding its
				// valid numeric siblings. Declare it as a metric to persist it.
				dropMeasurement(device.Token, mxkey, mxvalue, "undeclared and non-numeric")
				continue
			} else if err := model.CheckStorableMeasurement(f); err != nil {
				// Numeric but not storable: NaN, +/-Inf, or beyond decimal(20,8). Dropped like
				// the non-numeric case, so a lenient key costs only its own entry.
				dropMeasurement(device.Token, mxkey, mxvalue, "undeclared and not storable: "+err.Error())
				continue
			}
			rmentries = append(rmentries, rmentry)
		}
		rmsentry := resolvedevent.ResolvedMeasurementsEntry{
			Entries:      rmentries,
			OccurredTime: occurred,
		}
		rmsentries = append(rmsentries, rmsentry)
	}
	// Computed from the SAMPLE (every entry of this message at one instant), under the event's
	// own profile version, and appended before the sort so a calculated value takes its place
	// in the canonical order like any other entry. A derived value rides on the FIRST entry of
	// its sample, so a message of per-metric entries gains them without gaining an entry.
	// Event identity does not see them (ResolvedMeasurementsPayload.IdentityView), so the event
	// id stays a function of what the device sent alone.
	for i := range groups {
		g := &groups[i]
		rmsentries[g.first].Entries = g.calc.Append(rmsentries[g.first].Entries, &rez.calcObs)
		g.calc.Release()
	}
	for i := range rmsentries {
		rmentries := rmsentries[i].Entries
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
		slices.SortFunc(rmentries, func(a, b resolvedevent.ResolvedMeasurementEntry) int {
			return strings.Compare(a.Name, b.Name)
		})
	}
	rmpayload.Entries = rmsentries
	return rmpayload, nil
}

// instantKey is a sample's instant as a comparable map key.
type instantKey struct {
	sec  int64
	nsec int
}

// calcGroup is one open sample: the Calculation for every measurement of a message that shares
// an instant, and the index (in the resolved entries) of the first entry that carries it,
// which is where the derived values are appended.
type calcGroup struct {
	at    instantKey
	first int
	calc  model.Calculation
	// names is the names already recorded into this sample. Nil until a second entry joins the
	// group (the common one-entry sample pays nothing); then it holds the first entry's names.
	names map[string]struct{}
	// firstNames is the first entry's measured names, kept to seed set.
	firstNames []string
}

// findCalcGroup returns the open sample for instant occurred, opening it (as the entry at
// index idx) if this is the first entry at that instant.
func findCalcGroup(groups *[]calcGroup, occurred time.Time, idx int, profile model.ProfileResolution,
	constants []calcfield.Constant) *calcGroup {
	k := instantKey{sec: occurred.Unix(), nsec: occurred.Nanosecond()}
	for i := range *groups {
		if (*groups)[i].at == k {
			g := &(*groups)[i]
			if g.names == nil {
				g.names = make(map[string]struct{}, len(g.firstNames)+4)
				for _, n := range g.firstNames {
					g.names[n] = struct{}{}
				}
			}
			return g
		}
	}
	*groups = append(*groups, calcGroup{at: k, first: idx, calc: profile.BeginCalculation(constants)})
	return &(*groups)[len(*groups)-1]
}

// set records a measured input into the sample. A name already recorded at this instant keeps
// its FIRST value, in message order. That is this sample's rule, not storage's: both raw values
// are still stored (a raw row's identity includes its value), and the preview, which reads stored
// rows back in payload-id order, can pick the other one. Only a device that sends one metric
// twice at one instant with two different values sees the difference.
func (g *calcGroup) set(def *model.ResolvedMetric, name, raw string) {
	if g.names != nil {
		if _, dup := g.names[name]; dup {
			return
		}
		g.names[name] = struct{}{}
	} else {
		g.firstNames = append(g.firstNames, name)
	}
	g.calc.Set(def, raw)
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
		rapayload := &resolvedevent.ResolvedAlertsPayload{}
		raentries := make([]resolvedevent.ResolvedAlertEntry, 0)
		for _, uaentry := range apayload.Entries {
			raentry := resolvedevent.ResolvedAlertEntry{
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
	case presence.StateConnected, presence.StateDisconnected, presence.StateDemoted:
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
	// These refusals exist because the alternative is a silent no-op rather than an error:
	// a demotion the ordering guard drops is indistinguishable from a genuinely late echo,
	// and it leaves the row exactly as wedged as it was.
	//
	// 🔴 A ZERO SESSION ID IS NOT ONE OF THEM, AND THE REASONING THAT SAID IT WAS IS
	// WRONG. The claim was that a session-less demotion can never match anything, because
	// a demotion applies only against the session the row already holds. But a row asserted
	// by a producer that sent no session id HOLDS ZERO — parseSessionId documents empty as
	// absent, and that is a supported way to report presence — so zero matches zero and the
	// demotion applies exactly where it should. Refusing it would make those rows the one
	// population that can never be handed back to inferred: permanently frozen, by the
	// guard written to stop rows freezing. The blast radius is bounded by the same equality
	// that makes it work, since a zero-session demotion can reach nothing else, and an
	// INFERRED row is refused on the stamp instead.
	if payload.State == presence.StateDemoted {
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
	return &resolvedevent.ResolvedStateChangePayload{
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

// Convert an unresolved event payload into a resolved payload. profile is the event's
// one profile read (see resolveProfile); only a measurement reads its declared metric
// definitions.
func (rez *EventResolver) ResolveEventPayload(ctx context.Context, device *model.Device,
	profile model.ProfileResolution, event *esmodel.UnresolvedEvent) (interface{}, error) {
	switch event.EventType {
	case eventtype.Location:
		return rez.ResolveLocationsEventPayload(ctx, device, nil, event)
	case eventtype.Measurement:
		return rez.ResolveMeasurementsEventPayload(ctx, device, profile, event)
	case eventtype.Alert:
		return rez.ResolveAlertsEventPayload(ctx, device, nil, event)
	case eventtype.StateChange:
		return rez.ResolveStateChangeEventPayload(ctx, device, nil, event)
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
	// The event's cache reads of its profile, relationships and scoped-group gate are
	// made here, at the same time (model.ReadAheadForEvent). Each is still USED where it
	// always was, and any database read for what the caches could not answer is still
	// made there, one after another, so every failure is reported in the order it was
	// before: a measurement that fails validation reports Invalid even when its
	// relationships could not be read, and never reads them from the database.
	api := model.ReadAheadForEvent(ctx, rez.Api, device)

	// The event's ONE read of its type's published profile. Validation, the classifier
	// and unit stamp, and the version token below all come from this value, so they all
	// name the same profile version. Reading it first changes nothing else: every read
	// here is a read, and every failure maps to ApiCallFailed as each one did before.
	res, reason, err := rez.resolveProfile(ctx, api, device)
	if err != nil {
		return nil, reason, err
	}
	scope := res.Scope()

	// Validate measurements against the device's metric definitions (resolved via
	// its type's profile, ADR-016/ADR-045): a non-conforming value routes the whole event to the dead-letter
	// path rather than persisting bad data. Only a measurement reads the definitions;
	// every other event type uses the read for its scope alone.
	if event.EventType == eventtype.Measurement {
		if reason, err := validateMeasurements(res, event); err != nil {
			return nil, reason, err
		}
	}

	// Resolve the payload once — it does not depend on the anchors.
	resolved, err := rez.ResolveEventPayload(ctx, device, res, event)
	if err != nil {
		return nil, uint(resolvedevent.FailureReason_ApiCallFailed), err
	}

	// Denormalize the full set of the device's tracked relationships as anchors, and the
	// device+anchor dynamic-group memberships (ADR-062) stamped alongside them.
	anchors, memberships, reason, err := rez.deviceAnchors(ctx, api, device)
	if err != nil {
		return nil, reason, err
	}
	if len(anchors) == 0 {
		log.Debug().Str("device", device.Token).
			Msg("Resolving event with no anchors (device has no tracked relationship)")
	}

	// Surface a position reported by a device whose profile never declared one
	// (ADR-078). Deliberately AFTER the payload has already resolved and with no
	// return value: it observes, it never gates. Note it does not appear next to the
	// measurement validation above, which CAN fail the event — putting it there would
	// invite the next reader to give it the same power.
	if event.EventType == eventtype.Location {
		rez.warnIfLocationUndeclared(ctx, device, &scope)
	}

	// Denormalize the rule-scoping identity (ADR-051) onto the event.
	result := rez.MergeToResolveEvent(device, anchors, memberships, event, resolved, &scope)
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
func (rez *EventResolver) deviceAnchors(ctx context.Context, api model.DeviceManagementApi,
	device *model.Device) ([]resolvedevent.ResolvedAnchor, []resolvedevent.GroupRef, uint, error) {
	// A device's tracked-relationship set is denormalized in full onto every event, so
	// this genuinely needs all rows. TrackedRelationshipsForDevice is the named full-set
	// read (ADR-029); the search criteria this used to assemble — and the flag it set on
	// their pagination to lift the LIMIT — are both gone.
	drels, err := api.TrackedRelationshipsForDevice(ctx, device.ID)
	if err != nil {
		return nil, nil, uint(resolvedevent.FailureReason_ApiCallFailed), err
	}
	anchors := make([]resolvedevent.ResolvedAnchor, 0, len(drels.Results))
	// The membership stamp (ADR-062) is the union over the device itself and every
	// emitted anchor: a device-facet rule matches on the device's memberships, a
	// geographic rule ("arid areas") matches on an area anchor's.
	targets := []model.MembershipTarget{{Type: string(entity.TypeDevice), Id: device.ID}}
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
		anchors = append(anchors, resolvedevent.ResolvedAnchor{
			AnchorType:     r.TargetType,
			AnchorToken:    r.TargetToken,
			RelationshipId: r.ID,
		})
		targets = append(targets, model.MembershipTarget{Type: r.TargetType, Id: r.TargetId})
	}
	memberships, access, err := rez.unionMemberships(ctx, api, targets)
	if err != nil {
		return nil, nil, uint(resolvedevent.FailureReason_ApiCallFailed), err
	}
	// The access scopes the device is in are anchors too (ADR-081): an event written while
	// the device is in "north" is visible to a member scoped to group:north, as one written
	// while it is assigned to acme is to a member scoped to customer:acme. They carry no
	// relationship. A tracked edge from the device to the same group already anchored it.
	for _, a := range access {
		if !slices.ContainsFunc(anchors, func(x resolvedevent.ResolvedAnchor) bool {
			return x.AnchorType == a.AnchorType && x.AnchorToken == a.AnchorToken
		}) {
			anchors = append(anchors, a)
		}
	}
	return anchors, memberships, 0, nil
}

// validateMeasurements enforces the device's metric definitions (resolved via its
// type's profile, ADR-045) against an inbound measurement event (ADR-016). profile is the
// event's one profile read; the read itself, and its transient failure, belong to the
// caller. It returns (0, nil) when the event conforms or the
// device type declares no metrics; a non-conforming value returns
// FailureReason_Invalid, routing the event to the dead-letter path. Validation is
// lenient: an undeclared metric key passes through (model.ValidateMeasurement), so the
// metric model is an additive typing layer, not a strict allow-list.
func validateMeasurements(profile model.ProfileResolution, event *esmodel.UnresolvedEvent) (uint, error) {
	payload, ok := event.Payload.(*esmodel.UnresolvedMeasurementsPayload)
	if !ok {
		return 0, nil
	}
	if profile.MetricCount() == 0 {
		return 0, nil
	}
	for _, entry := range payload.Entries {
		for name, value := range entry.Measurements {
			if verr := profile.ValidateMeasurement(name, value); verr != nil {
				return uint(resolvedevent.FailureReason_Invalid), verr
			}
		}
	}
	return 0, nil
}

// Route event to handlers based on event type.
func (rez *EventResolver) HandleEvent(ctx context.Context,
	device *model.Device, unresolved *esmodel.UnresolvedEvent) ([]EventResolutionResults, uint, error) {
	switch unresolved.EventType {
	case eventtype.NewRelationship:
		return rez.HandleNewRelationshipEvent(ctx, device, unresolved)
	case eventtype.Location, eventtype.Measurement, eventtype.Alert, eventtype.StateChange:
		return rez.HandleStandardEvent(ctx, device, unresolved)
	default:
		return nil, uint(resolvedevent.FailureReason_Invalid), fmt.Errorf("unhandled event type: %s", unresolved.EventType.String())
	}
}

// presentedCredential extracts the credential a device offered on the inbound
// event (ADR-014), or nil when none was presented. An empty credential id counts
// as none so an event carrying blank fields falls through to the configured
// no-credential behaviour rather than failing authentication.
//
// Two things arrive SEALED and are opened here, each bound to the tenant it was sealed for.
// A password (SealedCredentialSecret) is bound to its credential id. A secret that will not
// open, a plaintext left in a message from before sealing included, is presented as an
// EMPTY secret: it matches no stored digest, so the event fails to authenticate like a
// wrong password and never as an absent credential, which optional mode would let through.
// An access token (SealedCredentialId) is bound to its credential type and is opened before
// any lookup; one that will not open is refused, never read as an absent credential.
func (rez *EventResolver) presentedCredential(ctx context.Context, unrez *esmodel.UnresolvedEvent) (*model.PresentedCredential, error) {
	if unrez.CredentialType == nil {
		if unrez.SealedCredentialId != nil {
			return nil, errAccessTokenNotSealed
		}
		return nil, nil
	}
	credentialId := ""
	if unrez.CredentialId != nil {
		credentialId = *unrez.CredentialId
	}
	// An access token is the credential itself, so it arrives SEALED and is opened here,
	// bound to the tenant and credential type, before any lookup. A token that will not
	// open, or one that arrives as sent (a message from before sealing), is an
	// authentication failure and never an absent credential, which optional mode would
	// let through.
	if unrez.SealedCredentialId != nil || (esproto.IsBearerCredentialType(unrez.CredentialType) && credentialId != "") {
		tenant, ok := core.TenantFromContext(ctx)
		if !ok || unrez.SealedCredentialId == nil || rez.SecretTransit == nil {
			return nil, errAccessTokenNotSealed
		}
		// A bearer token is the whole credential, so event-sources never publishes a
		// secret beside one. That shape is refused rather than read.
		if unrez.SealedCredentialSecret != nil {
			return nil, errAccessTokenNotSealed
		}
		token, err := rez.SecretTransit.OpenToken(tenant, *unrez.CredentialType, unrez.SealedCredentialId)
		if err != nil || token == "" {
			return nil, errAccessTokenNotSealed
		}
		credentialId = token
	}
	if credentialId == "" {
		return nil, nil
	}
	presented := &model.PresentedCredential{
		CredentialType: *unrez.CredentialType,
		CredentialId:   credentialId,
	}
	if unrez.SealedCredentialSecret != nil {
		opened := ""
		if tenant, ok := core.TenantFromContext(ctx); ok {
			if secret, err := rez.SecretTransit.Open(tenant, credentialId, unrez.SealedCredentialSecret); err == nil {
				opened = secret
			}
		}
		presented.Secret = &opened
	}
	return presented, nil
}

// errAccessTokenNotSealed is the authentication failure for an access token that did not
// arrive sealed for this tenant and credential type.
var errAccessTokenNotSealed = errors.New("the access token was not sealed for this tenant and credential type")

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
	case eventtype.Measurement, eventtype.StateChange:
		return true
	default:
		return false
	}
}

// A BrokerAsserted event is the other way past the required-mode credential check, and it
// is not confined to an event type: the device is the one the platform's own MQTT broker
// authenticated at CONNECT and confined to this topic, so every type a device can publish
// there is attributable to it. Only event-sources' capture source sets the marker, and it
// refuses any event whose body carries a credential, so a marked event never reaches this
// function with one to authenticate. A credential that IS presented is still authenticated
// above, marker or not: the marker only removes the demand for one.

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
		presented, perr := rez.presentedCredential(ctx, unrez)
		if perr != nil {
			return nil, uint(resolvedevent.FailureReason_Unauthenticated), perr
		}
		if presented != nil {
			device, err := rez.Api.AuthenticateDevice(ctx, presented, time.Now())
			if err != nil {
				return nil, uint(resolvedevent.FailureReason_Unauthenticated), err
			}
			if unrez.Device != "" && unrez.Device != device.Token {
				return nil, uint(resolvedevent.FailureReason_Unauthenticated),
					fmt.Errorf("event device token %q does not match authenticated device %q", unrez.Device, device.Token)
			}
			return device, 0, nil
		}
		if rez.AuthMode == config.AuthModeRequired && !unrez.BrokerAsserted && !transportAuthenticatedBypass(unrez) {
			return nil, uint(resolvedevent.FailureReason_Unauthenticated),
				errors.New("device authentication required but no credential was presented")
		}
	}

	matches, err := rez.Api.DevicesByToken(ctx, []string{unrez.Device})
	if err != nil {
		return nil, uint(resolvedevent.FailureReason_ApiCallFailed), err
	}
	if len(matches) == 0 {
		// A missing token is DevicesByToken returning ([], nil): gorm's Find reports
		// no error on an empty result set, so this branch MUST synthesize the error
		// itself. Without it resolveDevice returns a nil *Device AND a nil error;
		// ResolveEvent only checks err != nil, hands the nil device to HandleEvent,
		// and the resolver nil-derefs — which JetStream then crash-loops on
		// redelivery. Returning an error routes it to the designed retry-then-
		// dead-letter path (Process loop: a not-yet-registered device may appear).
		return nil, uint(resolvedevent.FailureReason_DeviceNotFound),
			fmt.Errorf("event device token %q is not registered", unrez.Device)
	}
	return matches[0], 0, nil
}

// Execute logic to resolve event.
func (rez *EventResolver) ResolveEvent(ctx context.Context, unrez *esmodel.UnresolvedEvent) ([]EventResolutionResults, uint, error) {
	// The age floor, before the device is read: it needs no device, so a stream of backdated
	// events costs no database read. It reads the same constant and the same ProcessedTime as
	// the event-sources decoder, so for a JSON transport it never refuses what the door let
	// through; it is what covers every other producer (the gateway emitter drops old samples
	// one by one before this, see adapter.Emitter.Emit) and any producer added later.
	//
	// Running first changes one outcome on purpose: a backdated event that would ALSO fail
	// authentication is dead-lettered as Invalid rather than Unauthenticated. Its age alone
	// decides that nothing will store it, and saying so costs no credential lookup.
	if err := esmodel.CheckEventAge(unrez.OccurredTime, unrez.ProcessedTime, unrez.Payload); err != nil {
		if rez.eventTime.TooOld != nil && errors.Is(err, eventtime.ErrTooOld) {
			rez.eventTime.TooOld.Inc()
		}
		return nil, uint(resolvedevent.FailureReason_Invalid), err
	}
	// A gateway's publish for a child resolves to the CHILD, after the platform has checked the
	// child is the gateway's (gateway_refusal.go). That check replaces the credential policy for
	// this event rather than following it: the identity is the broker's (the gateway) plus the
	// assignment (the child), and a gateway event carries no credential to authenticate.
	resolve := rez.resolveDevice
	if unrez.Gateway != "" {
		resolve = rez.resolveGatewayChild
	}
	device, reason, err := resolve(ctx, unrez)
	if err != nil {
		return nil, reason, err
	}
	// A disabled device's events are refused here, on every path that resolved it — a
	// credential, a trusted token, a transport-authenticated event — and after the device
	// is known, so the refusal names a device that exists. The credential path refused
	// first when the presented credential was bad: a disabled device does not turn a
	// failed authentication into a different answer.
	//
	// 🔴 EXCEPT A PRESENCE TRANSITION, and the exception is what keeps a quarantined
	// device's status true. Disabling a device does not close a connection it already has:
	// that connection stays open until it ends. A StateChange is the platform's own report
	// of that connection — the broker, the LwM2M server and the Sparkplug host make it, and
	// so does an operator's demotion (device-state's demoteAssertedPresence); the device-
	// facing JSON decoder rejects one — so refusing it refused the platform's account of its
	// own connection, not the device's data.
	//
	// 🔴 SPARKPLUG IS THE ONE PRODUCER A DEVICE DRIVES. The host turns an edge node's
	// BIRTH/DEATH into CONNECTED/DISCONNECTED, and its children are identified by topic with
	// broker-level auth only, so an edge node can announce a disabled child and the child
	// reads CONNECTED. That moves the projection and the history and nothing else — no data
	// is resolved, the mark keeps it out of detection, and commands to a disabled device are
	// refused at the claim — and it is no more than the same node can already claim for any
	// enabled device of its tenant. Refused, the
	// DISCONNECTED that ends the connection never reached the projection, an asserted row
	// takes Active only from a StateChange, and the device read CONNECTED for as long as it
	// stayed disabled; the presence reconciler's repair was refused the same way, every pass.
	//
	// All three claims pass, CONNECTED included: a CONNECTED for a disabled device reports a
	// connection that really is open (one opened in the moment before the disable reached
	// the broker, or a Sparkplug device born behind an edge node that is not disabled), and
	// recording it is what lets a re-enabled device's held commands be judged against the
	// truth. The event is marked SourceDeviceDisabled, so detection, which acts on the
	// device's behalf, skips it: a disabled device still raises no alarm.
	if device.IsDisabled() {
		if unrez.EventType == eventtype.StateChange {
			results, reason, err := rez.HandleEvent(ctx, device, unrez)
			for _, r := range results {
				if r.Resolved != nil {
					r.Resolved.SourceDeviceDisabled = true
				}
			}
			return results, reason, err
		}
		if rez.deviceDisabled != nil {
			rez.deviceDisabled.Inc()
		}
		return nil, uint(resolvedevent.FailureReason_DeviceDisabled),
			fmt.Errorf("%w: event for device %q refused", model.ErrDeviceDisabled, device.Token)
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
			// Fields, not a formatted message: a disabled level makes this a no-op, and it
			// is reached for every event resolved.
			log.Debug().Str("correlation", correlation).Int("resolver", rez.WorkerId).Msg("Event resolution handled by resolver")

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
				// (terminal; redelivery cannot help). The source is acked there, once
				// its record is stored, not here: a record whose publish failed would
				// otherwise be lost with its source already gone.
				rez.Invalid(err, unresolved)
				done(core.ResultInvalid)
				continue
			}

			if log.Debug().Enabled() {
				// Dump the event WITHOUT the presented credential. This is the whole
				// unresolved struct rendered as JSON, and a debug line is routinely
				// shipped off-cluster and retained far longer than the log level was
				// meant to stay raised. The credential is redacted rather than the dump
				// dropped because the event is already decoded here, so the fields to
				// remove are known exactly; every other field is what the dump is for.
				// The copy is local to this line and resolution below still sees the
				// full event.
				redacted := event.WithoutPresentedCredential()
				jevent, err := json.MarshalIndent(&redacted, "", "  ")
				if err == nil {
					log.Debug().Str("correlation", correlation).Msg(fmt.Sprintf("Received %s event:\n%s", redacted.EventType.String(), jevent))
				}
			}

			// Attempt to resolve event using the per-message tenant context.
			started := time.Now()
			resolved, reason, err := rez.ResolveEvent(msgctx, event)
			rez.slow.observe(time.Since(started), correlation)
			if err != nil {
				// Resolution failed. Retry via redelivery (a transient lookup error
				// may clear, and a not-yet-registered device may appear) until the
				// delivery cap, then route to the failed-events dead-letter path, which
				// acks the source once the record is stored so a permanently-
				// unresolvable event stops looping (A4).
				//
				// A refusal for AGE is dead-lettered on its first delivery: it compares two
				// times the event carries immutably, so every redelivery would answer the
				// same, and leaving it unacked only holds it in flight for minutes. ONLY that
				// refusal: a not-yet-registered device must still get its redeliveries.
				//
				// A write refused because the tenant has been deleted is settled at once and
				// reported NOWHERE (messaging.Dispose): the fence refuses it on every delivery,
				// and a failed-events copy would be a new record of the data being erased. So is
				// ANY failure for a tenant that has been deleted (tenantDeleted): its device and
				// credentials are swept early, so its events mostly fail before reaching a write.
				//
				// An event from a DISABLED device is settled on its first delivery and
				// discarded: no redelivery can answer differently while the device stays
				// disabled, and a device is disabled precisely when its traffic is unwanted
				// — often a flood — so a failed-events copy of each event would move the
				// flood into the dead-letter store. It is counted
				// (resolve_device_disabled_total) and reported as dropped.
				disposition := messaging.Dispose(unresolved, err)
				if disposition == messaging.TenantErased || (rez.tenantDeleted != nil && rez.tenantDeleted(tenant)) {
					log.Info().Str("correlation", correlation).
						Msg("Settling an event for a deleted tenant; this area's data has been erased.")
					_ = unresolved.Ack()
					done(core.ResultErased)
				} else if errors.Is(err, model.ErrDeviceDisabled) {
					log.Debug().Str("correlation", correlation).Err(err).Msg("Discarding an event from a disabled device.")
					_ = unresolved.Ack()
					done(core.ResultDropped)
				} else if errors.Is(err, ErrNotGatewayChild) {
					// A gateway named a device that is not its own. Terminal on the FIRST
					// delivery (redelivery cannot change an assignment's answer, and a device
					// token a gateway names is never auto-registered), but never discarded: it
					// is recorded with its payload, counted, logged and announced to the
					// gateway, because the broker already told the gateway it was stored.
					rez.gateway.refused(tenant, event, err)
					rez.Failed(unresolved, tenant, reason, *event, err)
					done(core.ResultFailed)
				} else if disposition == messaging.Exhausted || errors.Is(err, eventtime.ErrTooOld) {
					rez.Failed(unresolved, tenant, reason, *event, err)
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
