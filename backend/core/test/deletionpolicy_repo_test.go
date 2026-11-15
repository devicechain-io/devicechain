// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import "testing"

// A decision is mandatory even when deletion handling has not landed: bounded
// records that gap honestly. Run with -count=1: Go's cache cannot track models
// read from other service modules. These decisions must describe THIS branch.
var repositoryDeletionPolicies = map[string]deletionPolicy{
	"backend/services/command-delivery/model/model.go:Command": {
		deletionTerminate, "DeletionConsumer calls CancelDeletedDeviceCommands: unsent commands created through DeletedTime become CANCELLED/NOT_DELIVERED. Late inserts remain an incarnation gap; sent/terminal rows retain their recorded outcome."},
	"backend/services/command-delivery/model/model_campaign.go:CampaignElement": {
		deletionBounded, "No device-deletion hook for planned elements. PruneCampaignHistory removes finished elements of cycles older than 30 days; pending/claimed work is excluded, so that retention is not a bound on live work."},
	"backend/services/command-delivery/model/model_device_gate.go:DeviceGate": {
		deletionHistory, "CancelDeletedDeviceCommands retains a monotone DeletedThrough timestamp. Claims compare command creation against that floor; later device facts can update Disabled without erasing the floor."},
	"backend/services/dashboard-management/model/model_shares.go:DashboardShare": {
		deletionPurge, "share_reconciler calls DeleteSharesOfEntity on entity-deleted. This branch has no share-incarnation fence for a share created concurrently with deletion."},
	"backend/services/device-management/model/model_detection_rule_scope_ref.go:DetectionRuleScopeRef": {
		deletionPurge, "DeleteEntityGroup refuses a group pinned by live rules; syncProfileScopeRefsAndEnroll hard-deletes refs on profile publish/rollback/delete before group deletion can proceed."},
	"backend/services/device-management/model/model_group_scoping.go:EntityGroupFacetRef": {
		deletionPurge, "DeleteEntityGroup calls purgeGroupScopingRows in its transaction, removing facet references with the deleted group's ID."},
	"backend/services/device-management/model/model_group_scoping.go:EntityGroupMembership": {
		deletionPurge, "DeleteEntityGroup calls purgeGroupScopingRows in its transaction, removing membership rows with the deleted group's ID."},
	"backend/services/device-management/model/model_profile_decoder.go:ProfileDecoderSample": {
		deletionHistory, "Failing uplink input is frozen with CreatedAt and profile/version for decoder replay, not used as current device state. StoreDecoderSample keeps only 20 samples per profile; profile deletion cascades them."},
	"backend/services/device-state/model/dropped_measurements.go:DroppedMeasurement": {
		deletionBounded, "Diagnostic projection has no deletion handling until its deletion consumer lands. enforceDroppedBound keeps newest keys within MaxDroppedMeasurementsPerDevice, with no time expiry guarantee."},
	"backend/services/device-state/model/model.go:DeviceState": {
		deletionBounded, "Live last-known projection has no deletion handling until its deletion consumer lands; old values survive until newer facts overwrite them, with no TTL guarantee."},
	"backend/services/device-state/model/model.go:LatestLocation": {
		deletionBounded, "Live location projection has no deletion handling until its deletion consumer lands; a newer location can overwrite it, but there is no TTL guarantee."},
	"backend/services/device-state/model/model.go:LatestMeasurement": {
		deletionBounded, "Live per-metric projection has no deletion handling until its deletion consumer lands; newer samples overwrite keys, with no TTL guarantee."},
	"backend/services/event-management/model/events.go:AlertEvent": {
		deletionHistory, "Write-time alert payload records OccurredTime and event/payload identity; retained as historical telemetry under DataLifecyclePolicy retention, not a live device projection. Token queries can include old history."},
	"backend/services/event-management/model/events.go:Event": {
		deletionHistory, "Immutable event identity and OccurredTime record what happened; DataLifecyclePolicy retention bounds history when enabled. Token reuse can expose older historical events, deliberately not current device state."},
	"backend/services/event-management/model/events.go:EventAnchor": {
		deletionPurge, "EntityAnchorReconciler calls DeleteAnchorsForEntity with DeletedTime, removing source-device or target-entity anchors strictly before that occurrence-time bound; the orphan sweep also reconciles missing entities."},
	"backend/services/event-management/model/events.go:LocationEvent": {
		deletionHistory, "Write-time location payload records OccurredTime and event/payload identity; DataLifecyclePolicy location retention bounds historical queries when enabled. Reused tokens can include older history."},
	"backend/services/event-management/model/events.go:MeasurementEvent": {
		deletionHistory, "Write-time measurement payload records OccurredTime and event/payload identity; DataLifecyclePolicy retention bounds historical queries when enabled. Reused tokens can include older history."},
	"backend/services/event-management/model/events.go:MeasurementRollup": {
		deletionHistory, "Continuous aggregate records measurement history in time buckets, read over historical ranges rather than as live device state. There is no entity-deletion hook; old-token history can span incarnations."},
	"backend/services/event-management/model/events.go:StateChangeEvent": {
		deletionHistory, "Write-time state-change payload records OccurredTime and event/payload identity; DataLifecyclePolicy retention bounds history when enabled. Historical token queries may span incarnations."},
	"backend/services/event-management/model/failed_events.go:FailedEventRecord": {
		deletionHistory, "Failure index records occurrence-time diagnostics rather than current device state; DataLifecyclePolicy.FailedEventRetentionDays always ages these out, including history for a reused token."},
	"backend/services/event-management/model/scope_rules.go:eventAnchorKey": {
		deletionPurge, "Read shape of the event_anchors table; EntityAnchorReconciler and DeleteAnchorsForEntity remove the underlying EventAnchor rows strictly before DeletedTime, with an orphan sweep for missing entities."},
	"backend/services/event-management/model/usage.go:tenantUsageDeviceRow": {
		deletionHistory, "Day-stamped tenant_usage_devices entries deduplicate active-device billing counts within a UTC day. Keeping a deleted device's usage is intentional; this is tenant accounting, not device state."},
	"backend/services/event-processing/model/model.go:DeviceAttribute": {
		deletionPurge, "handleEntityDeletedFact calls DeviceAttributeStore.PurgeDevice, deleting attributes through DeletedTime and retaining a separate fence against stale facts."},
	"backend/services/event-processing/model/model.go:DeviceAttributeDeletion": {
		deletionHistory, "PurgeDevice retains the monotone DeletedAt fence; deletionFence rejects attributes stamped at or before it while genuinely newer incarnation facts may pass."},
	"backend/services/event-processing/model/model.go:DeviceRoster": {
		deletionTerminate, "handleEntityDeletedFact calls DeviceRosterStore.Delete: Deleted=true tombstone is excluded from LoadAll and disarmed. LastEventAt fences stale facts; newer lifecycle facts can replace it."},
	"backend/services/notification-management/model/model_state.go:NotificationState": {
		deletionBounded, "No entity-deletion consumer: escalation continues until acknowledgement/clear or effectiveMaxEscalations. The cap bounds re-pages, not row lifetime; remembered device tokens have no deletion TTL."},
	"backend/services/outbound-connectors/lpwan/pending.go:LpwanPendingAck": {
		deletionBounded, "No entity-deletion consumer; LPWAN report settlement or the deadline sweep claims and deletes pending acknowledgements at DeadlineAt. Responses carry command token and dispatch nonce."},
	"backend/services/update-management/model/model_rollouts.go:UpdateRolloutTarget": {
		deletionBounded, "Frozen rollout targets have no entity-deletion hook until its deletion consumer lands; rollout cancellation/dispatch checks are not an incarnation fence or a deletion TTL."},
	"backend/services/update-management/model/model_updates.go:UpdateAssignment": {
		deletionBounded, "No deletion handling until its deletion consumer lands: append-only assignments remain current until superseded, with no deletion TTL or incarnation fence."},
	"backend/services/update-management/model/model_updates.go:UpdateAttempt": {
		deletionBounded, "No deletion handling until its deletion consumer lands; the OTA reducer's timeout/cancellation bounds attempts, but retained rows have no deletion TTL or incarnation fence."},
}

func TestDeletionPolicyRepository(t *testing.T) {
	root := workspaceRoot(t)
	holders, err := deletionHoldersUnder(root)
	if err != nil {
		t.Fatalf("scanning deletion holders: %v", err)
	}
	if len(holders) == 0 {
		t.Fatal("no deletion holders recognized; the scanner asserts nothing")
	}
	t.Logf("scanned %d deletion holders", len(holders))
	for _, problem := range deletionPolicyProblems(holders, repositoryDeletionPolicies) {
		t.Error(problem)
	}
}
