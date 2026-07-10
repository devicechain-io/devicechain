// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
)

type Api struct {
	RDB *rdb.RdbManager

	// AlarmPublisher emits alarm state-change events (ADR-041). It is injected at
	// wiring time (the concrete publisher owns a NATS writer, so it cannot be built
	// until the messaging layer exists) and may be nil — in tests, or before wiring —
	// in which case emission is disabled. Both the evaluator and the operator API
	// mutate alarms through this shared *Api, so setting it here gives one uniform
	// event stream for every transition.
	AlarmPublisher AlarmEventPublisher

	// EntityDeletedPublisher emits entity-deletion events (ADR-044) so cross-service
	// reference holders can reconcile. Injected at wiring time like AlarmPublisher and
	// may be nil (tests / pre-wiring), disabling emission.
	EntityDeletedPublisher EntityEventPublisher

	// CacheEvictor drops cached entries a delete invalidates (ADR-044 F2). Without
	// it, after a delete the ingest hot path keeps resolving the removed device and
	// keeps a referencing device's tracked relationships cached for up to the TTL,
	// re-creating the very event_anchors the reconciler just removed. Injected at
	// wiring time (it holds the cache layer); nil in tests disables eviction.
	CacheEvictor CacheEvictor

	// DetectionRuleValidator compiles a profile's draft detection rules against
	// event-processing at publish (ADR-044 sync gate / ADR-051 slice 4b); a rule that
	// does not compile fails the publish closed. Injected at wiring time (it holds the
	// svcclient); nil in tests / before wiring / with the service secret unset, in which
	// case the publish gate is skipped.
	DetectionRuleValidator DetectionRuleValidator

	// DetectionRulesPublishedPublisher emits the enabled detection rules frozen into a
	// newly-published profile version (ADR-051 slice 4b-3) so event-processing's DETECT
	// engine can run them. Injected at wiring time (the concrete publisher owns a NATS
	// writer) and may be nil — in tests, or before wiring — disabling emission. Emission is
	// post-commit and best-effort (at-most-once): a DELIVERED fact is durably persisted by
	// event-processing, but a fact that never reaches the stream is recovered by a later
	// publish or the planned reconcile, not by replay.
	DetectionRulesPublishedPublisher DetectionRulesPublishedPublisher

	// DeviceRosterPublisher emits device-roster events (ADR-051 slice 4c-2) when a device
	// is created or re-typed, so event-processing's DETECT engine can arm absence for a
	// device that has never reported. Injected at wiring time (the concrete publisher owns
	// a NATS writer) and may be nil — in tests, or before wiring — disabling emission.
	// Emission is post-commit and best-effort (at-most-once), like the other fact publishers.
	DeviceRosterPublisher DeviceRosterPublisher
}

// CacheEvictor drops the hot-path caches (ADR-022 B2) that a mutation makes stale.
// Dependency-inverted so the model declares the need and the cache layer (CachedApi)
// satisfies it.
//
// EvictEntityDelete handles an entity delete: the deleted device's own by-token +
// relationship entries, plus the tracked relationships of every device that
// referenced it as a target. EvictRelationshipSources handles an edge *removal*
// (unassign): only the source devices' relationship entries, since the target
// entity survives — and unlike a delete, the reconciliation sweep will not repair a
// stale relationship set here (the target still resolves), so evicting is the only
// fix.
type CacheEvictor interface {
	EvictEntityDelete(ctx context.Context, etype entity.Type, id uint, token string, trackingSourceDeviceIds []uint)
	EvictRelationshipSources(ctx context.Context, sourceDeviceIds []uint)
}

// Create a new API instance.
func NewApi(rdb *rdb.RdbManager) *Api {
	api := &Api{}
	api.RDB = rdb
	return api
}

// emitAlarmEvent publishes an alarm state-change event when a publisher is wired,
// and is a no-op otherwise. Emission is best-effort (the publisher logs its own
// failures) so a transition never depends on the event reaching the stream.
//
// It assumes the caller has already committed the transition: the alarm write paths
// run on the autocommit connection (rdb.DB(ctx) carries no transaction), so the emit
// is post-commit and never fires for a rolled-back change. If a future caller wraps a
// transition in an explicit transaction, it must emit after the commit, not from
// inside the closure.
func (api *Api) emitAlarmEvent(ctx context.Context, event *AlarmStateChangeEvent) {
	if api.AlarmPublisher != nil {
		api.AlarmPublisher.PublishAlarmEvent(ctx, event)
	}
}

// emitEntityDeleted publishes an entity-deletion event when a publisher is wired,
// and is a no-op otherwise. Like emitAlarmEvent it is best-effort and must be called
// after the delete has committed: deleteEdgeEntity emits only once its transaction
// returns nil, so the event never fires for a rolled-back delete.
func (api *Api) emitEntityDeleted(ctx context.Context, event *EntityDeletedEvent) {
	if api.EntityDeletedPublisher != nil {
		api.EntityDeletedPublisher.PublishEntityDeleted(ctx, event)
	}
}

// emitDetectionRulesPublished publishes a detection-rules-published event when a
// publisher is wired, and is a no-op otherwise (ADR-051 slice 4b-3). Like the other
// emit helpers it is best-effort and must be called AFTER the publish transaction has
// committed: PublishDeviceProfile emits only once its version-insert transaction
// returns nil, so the fact never fires for a rolled-back publish.
func (api *Api) emitDetectionRulesPublished(ctx context.Context, event *DetectionRulesPublishedEvent) {
	if api.DetectionRulesPublishedPublisher != nil {
		api.DetectionRulesPublishedPublisher.PublishDetectionRulesPublished(ctx, event)
	}
}

// emitDeviceRoster publishes a device-roster event when a publisher is wired, and is a
// no-op otherwise (ADR-051 slice 4c-2). Like the other emit helpers it is best-effort
// and must be called AFTER the device write has committed: the create/update paths emit
// only once their write returns nil, so the fact never fires for a rolled-back write.
func (api *Api) emitDeviceRoster(ctx context.Context, event *DeviceRosterEvent) {
	if api.DeviceRosterPublisher != nil {
		api.DeviceRosterPublisher.PublishDeviceRoster(ctx, event)
	}
}

// evictEntityDelete drops the caches a delete invalidated when an evictor is wired,
// and is a no-op otherwise. Called post-commit alongside emitEntityDeleted.
func (api *Api) evictEntityDelete(ctx context.Context, etype entity.Type, id uint, token string, sources []uint) {
	if api.CacheEvictor != nil {
		api.CacheEvictor.EvictEntityDelete(ctx, etype, id, token, sources)
	}
}

// evictRelationshipSources drops the cached tracked-relationship sets of the given
// source devices when an evictor is wired (ADR-044 F2). No-op otherwise.
func (api *Api) evictRelationshipSources(ctx context.Context, sourceDeviceIds []uint) {
	if api.CacheEvictor != nil && len(sourceDeviceIds) > 0 {
		api.CacheEvictor.EvictRelationshipSources(ctx, sourceDeviceIds)
	}
}

// Interface for device management API (used for mocking)
type DeviceManagementApi interface {
	// Device types.
	DeviceTypesById(ctx context.Context, ids []uint) ([]*DeviceType, error)
	DeviceTypesByToken(ctx context.Context, tokens []string) ([]*DeviceType, error)
	DeviceTypes(ctx context.Context, criteria DeviceTypeSearchCriteria) (*DeviceTypeSearchResults, error)

	// Devices.
	DevicesById(ctx context.Context, ids []uint) ([]*Device, error)
	DevicesByToken(ctx context.Context, tokens []string) ([]*Device, error)
	Devices(ctx context.Context, criteria DeviceSearchCriteria) (*DeviceSearchResults, error)

	// Device credentials (ADR-014).
	CreateDeviceCredential(ctx context.Context, request *DeviceCredentialCreateRequest) (*DeviceCredential, error)
	UpdateDeviceCredential(ctx context.Context, token string, request *DeviceCredentialCreateRequest) (*DeviceCredential, error)
	DeviceCredentialsById(ctx context.Context, ids []uint) ([]*DeviceCredential, error)
	DeviceCredentialsByToken(ctx context.Context, tokens []string) ([]*DeviceCredential, error)
	DeviceCredentials(ctx context.Context, criteria DeviceCredentialSearchCriteria) (*DeviceCredentialSearchResults, error)
	DeviceCredentialByCredentialId(ctx context.Context, credentialType string, credentialId string) (*DeviceCredential, error)

	// Device authentication (transport security, ADR-014).
	AuthenticateDevice(ctx context.Context, presented *PresentedCredential, now time.Time) (*Device, error)

	// Entity relationships (uniform edge model, ADR-013).
	EntityRelationshipsById(ctx context.Context, ids []uint) ([]*EntityRelationship, error)
	EntityRelationshipsByToken(ctx context.Context, tokens []string) ([]*EntityRelationship, error)
	EntityRelationships(ctx context.Context, criteria EntityRelationshipSearchCriteria) (*EntityRelationshipSearchResults, error)
	CreateEntityRelationship(ctx context.Context, request *EntityRelationshipCreateRequest) (*EntityRelationship, error)
	EntityRelationshipTypesByToken(ctx context.Context, tokens []string) ([]*EntityRelationshipType, error)

	// Metric definitions (ADR-016).
	CreateMetricDefinition(ctx context.Context, request *MetricDefinitionCreateRequest) (*MetricDefinition, error)
	UpdateMetricDefinition(ctx context.Context, token string, request *MetricDefinitionCreateRequest) (*MetricDefinition, error)
	MetricDefinitionsById(ctx context.Context, ids []uint) ([]*MetricDefinition, error)
	MetricDefinitionsByToken(ctx context.Context, tokens []string) ([]*MetricDefinition, error)
	MetricDefinitions(ctx context.Context, criteria MetricDefinitionSearchCriteria) (*MetricDefinitionSearchResults, error)
	MetricDefinitionsByDeviceType(ctx context.Context, deviceTypeId uint) ([]*MetricDefinition, error)

	// ProfileScopeByDeviceType resolves a device type's denormalized rule-scoping
	// identity (device-type + active-published-profile-version tokens) for stamping
	// onto resolved events (ADR-051).
	ProfileScopeByDeviceType(ctx context.Context, deviceTypeId uint) (*ProfileScope, error)

	// Command definitions (ADR-043).
	CreateCommandDefinition(ctx context.Context, request *CommandDefinitionCreateRequest) (*CommandDefinition, error)
	UpdateCommandDefinition(ctx context.Context, token string, request *CommandDefinitionCreateRequest) (*CommandDefinition, error)
	CommandDefinitionsById(ctx context.Context, ids []uint) ([]*CommandDefinition, error)
	CommandDefinitionsByToken(ctx context.Context, tokens []string) ([]*CommandDefinition, error)
	CommandDefinitions(ctx context.Context, criteria CommandDefinitionSearchCriteria) (*CommandDefinitionSearchResults, error)
	CommandDefinitionsByDeviceType(ctx context.Context, deviceTypeId uint) ([]*CommandDefinition, error)

	// Alarm definitions (ADR-041).
	CreateAlarmDefinition(ctx context.Context, request *AlarmDefinitionCreateRequest) (*AlarmDefinition, error)
	UpdateAlarmDefinition(ctx context.Context, token string, request *AlarmDefinitionCreateRequest) (*AlarmDefinition, error)
	AlarmDefinitionsById(ctx context.Context, ids []uint) ([]*AlarmDefinition, error)
	AlarmDefinitionsByToken(ctx context.Context, tokens []string) ([]*AlarmDefinition, error)
	AlarmDefinitions(ctx context.Context, criteria AlarmDefinitionSearchCriteria) (*AlarmDefinitionSearchResults, error)
	AlarmDefinitionsByDeviceType(ctx context.Context, deviceTypeId uint) ([]*AlarmDefinition, error)

	// Alarms (raised, ADR-041). Raised by the evaluator (a later slice); the API here
	// is read + the operator transitions.
	AlarmsById(ctx context.Context, ids []uint) ([]*Alarm, error)
	AlarmsByToken(ctx context.Context, tokens []string) ([]*Alarm, error)
	Alarms(ctx context.Context, criteria AlarmSearchCriteria) (*AlarmSearchResults, error)
	AcknowledgeAlarm(ctx context.Context, token string, by *string) (*Alarm, error)
	ClearAlarm(ctx context.Context, token string) (*Alarm, error)
	// EvaluateMeasurementAlarms is the SIMPLE alarm evaluator (ADR-041): it upserts
	// alarm state from a resolved measurements payload (raise/escalate/auto-clear).
	// The source device is named by its token (ADR-044); the evaluator resolves it to
	// the local row id for its id-keyed alarm state.
	EvaluateMeasurementAlarms(ctx context.Context, deviceToken string, payload *ResolvedMeasurementsPayload, occurredTime time.Time) error

	// Entity attributes (ADR-012).
	SetEntityAttribute(ctx context.Context, request *EntityAttributeSetRequest) (*EntityAttribute, error)
	EntityAttributes(ctx context.Context, criteria EntityAttributeSearchCriteria) (*EntityAttributeSearchResults, error)
	DeleteEntityAttribute(ctx context.Context, entityType string, entity string, scope string, attrKey string) (bool, error)
	EntityAttributesByEntity(ctx context.Context, entityType string, entityId uint, scope *string) ([]*EntityAttribute, error)

	// Device provisioning + self-registration (ADR-012).
	CreateProvisioningProfile(ctx context.Context, request *ProvisioningProfileCreateRequest) (*ProvisioningProfile, error)
	UpdateProvisioningProfile(ctx context.Context, token string, request *ProvisioningProfileCreateRequest) (*ProvisioningProfile, error)
	ProvisioningProfilesById(ctx context.Context, ids []uint) ([]*ProvisioningProfile, error)
	ProvisioningProfilesByToken(ctx context.Context, tokens []string) ([]*ProvisioningProfile, error)
	ProvisioningProfiles(ctx context.Context, criteria ProvisioningProfileSearchCriteria) (*ProvisioningProfileSearchResults, error)
	ProvisioningProfileByProvisionKey(ctx context.Context, provisionKey string) (*ProvisioningProfile, error)
	ProvisionDevice(ctx context.Context, request *ProvisionDeviceRequest, now time.Time) (*ProvisionDeviceResult, error)
	ProvisionDeviceBootstrap(ctx context.Context, request *ProvisionDeviceRequest, now time.Time) (*ProvisionDeviceResult, error)

	// Device→customer claiming (ADR-012).
	InitiateDeviceClaim(ctx context.Context, request *DeviceClaimInitiateRequest) (*DeviceClaim, error)
	ClaimDevice(ctx context.Context, request *DeviceClaimRequest, now time.Time) (*EntityRelationship, error)
	CancelDeviceClaim(ctx context.Context, deviceToken string) (bool, error)
	DeviceClaimByDeviceToken(ctx context.Context, deviceToken string) (*DeviceClaim, error)
}
