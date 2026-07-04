// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
// (type, id) anchors (ADR-013). An empty anchor set yields an anchorless event —
// the device is unassigned — which still persists and projects, keyed on the
// device, rather than being dropped (ADR-013 addendum 2026-07-01).
func (rez *EventResolver) MergeToResolveEvent(device *model.Device, anchors []model.ResolvedAnchor,
	event *esmodel.UnresolvedEvent, rezPayload interface{}) (*EventResolutionResults, error) {
	resolved := &model.ResolvedEvent{
		Source:         event.Source,
		AltId:          event.AltId,
		SourceDeviceId: device.ID,
		Anchors:        anchors,
		OccurredTime:   event.OccurredTime,
		ProcessedTime:  event.ProcessedTime,
		EventType:      event.EventType,
		Payload:        rezPayload,
	}
	return &EventResolutionResults{Device: device, Resolved: resolved}, nil
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
	// relationship is itself the event's single anchor.
	anchors := []model.ResolvedAnchor{
		{AnchorType: created.TargetType, AnchorId: created.TargetId, RelationshipId: created.ID},
	}
	resolved, err := rez.MergeToResolveEvent(device, anchors, event, payload)
	if err != nil {
		return nil, uint(dmproto.FailureReason_Unknown), errors.New("unable to merge info to resolve event")
	}

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

// Resolve a measurements event payload.
func (rez *EventResolver) ResolveMeasurementsEventPayload(ctx context.Context, device *model.Device,
	relation *model.EntityRelationship, event *esmodel.UnresolvedEvent) (interface{}, error) {
	if mpayload, ok := event.Payload.(*esmodel.UnresolvedMeasurementsPayload); ok {
		rmpayload := &model.ResolvedMeasurementsPayload{}
		rmsentries := make([]model.ResolvedMeasurementsEntry, 0)
		for _, umsentry := range mpayload.Entries {
			rmentries := make([]model.ResolvedMeasurementEntry, 0)
			for mxkey, mxvalue := range umsentry.Measurements {
				rmentry := model.ResolvedMeasurementEntry{
					Name:       mxkey,
					Value:      mxvalue,
					Classifier: nil,
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
	return nil, fmt.Errorf("can not resolve measurements payload. invalid unresolved payload type")
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
	// Validate measurements against the device type's declared metric definitions
	// (ADR-016): a non-conforming value routes the whole event to the dead-letter
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

	// Denormalize the full set of the device's tracked relationships as anchors.
	anchors, reason, err := rez.deviceAnchors(ctx, device)
	if err != nil {
		return nil, reason, err
	}
	if len(anchors) == 0 {
		log.Debug().Str("device", device.Token).
			Msg("Resolving event with no anchors (device has no tracked relationship)")
	}

	result, err := rez.MergeToResolveEvent(device, anchors, event, resolved)
	if err != nil {
		return nil, uint(dmproto.FailureReason_ApiCallFailed), err
	}
	return []EventResolutionResults{*result}, 0, nil
}

// deviceAnchors returns the device's tracked-relationship targets as anchors —
// one per tracked relationship — or an empty set when the device has no tracked
// relationship. Every anchor is denormalized onto the event (ADR-013 addendum
// 2026-07-01), so a device assigned to several targets is queryable by each.
func (rez *EventResolver) deviceAnchors(ctx context.Context, device *model.Device) ([]model.ResolvedAnchor, uint, error) {
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
		return nil, uint(dmproto.FailureReason_ApiCallFailed), err
	}
	anchors := make([]model.ResolvedAnchor, 0, len(drels.Results))
	for i := range drels.Results {
		r := &drels.Results[i]
		anchors = append(anchors, model.ResolvedAnchor{
			AnchorType:     r.TargetType,
			AnchorId:       r.TargetId,
			RelationshipId: r.ID,
		})
	}
	return anchors, 0, nil
}

// validateMeasurements enforces the device type's declared metric definitions
// against an inbound measurement event (ADR-016). It returns (0, nil) when the
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
	defs, err := rez.Api.MetricDefinitionsByDeviceType(ctx, device.DeviceTypeId)
	if err != nil {
		return uint(dmproto.FailureReason_ApiCallFailed), err
	}
	if len(defs) == 0 {
		return 0, nil
	}
	byKey := make(map[string]*model.MetricDefinition, len(defs))
	for _, d := range defs {
		byKey[d.MetricKey] = d
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
	if err != nil || len(matches) == 0 {
		return nil, uint(dmproto.FailureReason_DeviceNotFound), err
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
					_ = unresolved.Nak()
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
