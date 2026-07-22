// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/proto"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/google/uuid"
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
}

// Results of event resolution process.
type EventResolutionResults struct {
	Device   *model.Device
	Resolved *model.ResolvedEvent
}

// Create a new event resolver.
func NewEventResolver(workerId int, api model.DeviceManagementApi, authMode string,
	unrez <-chan messaging.Message,
	invalid func(error, messaging.Message),
	resolved func(messaging.Message, string, []EventResolutionResults),
	failed func(string, uint, esmodel.UnresolvedEvent, error, string),
	metrics *core.ProcessorMetrics) *EventResolver {
	return &EventResolver{
		WorkerId:   workerId,
		Api:        api,
		AuthMode:   authMode,
		Unresolved: unrez,
		Invalid:    invalid,
		Resolved:   resolved,
		Failed:     failed,
		metrics:    metrics,
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
	resolved := &model.ResolvedEvent{
		Source:              event.Source,
		AltId:               event.AltId,
		SourceDeviceToken:   device.Token,
		DeviceTypeToken:     scope.DeviceTypeToken,
		ProfileVersionToken: scope.ProfileVersionToken,
		Anchors:             anchors,
		ScopeMemberships:    memberships,
		OccurredTime:        event.OccurredTime,
		ProcessedTime:       event.ProcessedTime,
		EventType:           event.EventType,
		Payload:             rezPayload,
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
				OccurredTime: ulentry.OccurredTime,
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
		rmsentry := model.ResolvedMeasurementsEntry{
			Entries:      rmentries,
			OccurredTime: umsentry.OccurredTime,
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
				OccurredTime: uaentry.OccurredTime,
			}
			raentries = append(raentries, raentry)
		}
		rapayload.Entries = raentries
		return rapayload, nil
	}
	return nil, fmt.Errorf("can not resolve alerts payload. invalid unresolved payload type")
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

	result := rez.MergeToResolveEvent(device, anchors, memberships, event, resolved, scope)
	return []EventResolutionResults{*result}, 0, nil
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
	case esmodel.Location, esmodel.Measurement, esmodel.Alert:
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

// resolveDevice determines the originating device for an event, enforcing the
// configured device authentication policy (transport security, ADR-014):
//   - disabled: the self-asserted device token is trusted (legacy path).
//   - optional: a presented credential is authenticated and authoritative; with
//     no credential the device token is trusted.
//   - required: a valid credential must be presented or the event is rejected.
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
		if rez.AuthMode == config.AuthModeRequired {
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
