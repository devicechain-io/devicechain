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
		deletionPurge, "processor/deletion_processor.go -> ApplyDeviceDeleted deletes the device's drop rows with its live view; a higher-incarnation update resets them (projection_merge foldIncarnations); the orphan sweep removes rows of devices that no longer exist."},
	"backend/services/device-state/model/model.go:DeviceState": {
		deletionPurge, "ApplyDeviceDeleted (entity-deleted consumer) hard-deletes the row when its incarnation or creation time belongs to the deleted device; the merge drops lower-incarnation updates and resets on a higher one; the orphan sweep deletes rows of missing or different devices."},
	"backend/services/device-state/model/model.go:LatestLocation": {
		deletionPurge, "Deleted with the device's live view by ApplyDeviceDeleted, on a higher-incarnation reset in the merge, and by the orphan sweep; a lower-incarnation update writes no position."},
	"backend/services/device-state/model/model.go:LatestMeasurement": {
		deletionPurge, "Deleted with the device's live view by ApplyDeviceDeleted, on a higher-incarnation reset in the merge, and by the orphan sweep; a lower-incarnation update writes no reading."},
	"backend/services/event-management/model/device_incarnation.go:deletedDeviceBound": {
		deletionHistory, "Read shape of entity_deletion_watermarks: the token's deletion time that bounds a read by device token to its current incarnation. Never written through; the tenant purge erases the rows."},
	"backend/services/event-management/model/device_incarnation.go:heldDeviceToken": {
		deletionHistory, "Read shape of device_token_holders: when a live device last held a token, read only to decide whether a deleted token was created again. Never written through; the tenant purge erases the rows."},
	"backend/services/event-management/model/device_token_holders.go:DeviceTokenHolder": {
		deletionHistory, "A monotone HeldSince per (tenant, device token), recorded from device-roster facts and the anchor sweep. Deleting the holder moves the token's deletion watermark past it, which is what turns the read bound off; the row itself stays so a late or replayed fact cannot move it back. The tenant purge erases the rows."},
	"backend/services/event-management/model/device_incarnation.go:eventReceiptKey": {
		deletionHistory, "Read shape of the events table, the inside of the incarnation bound's key subquery; the rows are Event's and follow its retention."},
	"backend/services/event-management/model/entity_deletion_watermarks.go:EntityDeletionWatermark": {
		deletionHistory, "The deletion fact itself: a monotone DeletedTime per (tenant, entity type, token), retained so an anchor of an event received before it and stored late is dropped on insert and swept by the resweep. A re-created same-token entity's anchors (received after DeletedTime) pass; the tenant purge erases the rows."},
	"backend/services/event-management/model/events.go:AlertEvent": {
		deletionHistory, "Write-time alert payload records OccurredTime and event/payload identity; retained as historical telemetry under DataLifecyclePolicy retention, not a live device projection. Token queries can include old history."},
	"backend/services/event-management/model/events.go:Event": {
		deletionHistory, "Immutable event identity and OccurredTime record what happened; DataLifecyclePolicy retention bounds history when enabled. A read by a reused token returns only events received at or after its latest recorded deletion (deviceIncarnationBound)."},
	"backend/services/event-management/model/events.go:EventAnchor": {
		deletionPurge, "EntityAnchorReconciler calls DeleteAnchorsForEntity with DeletedTime, removing source-device or target-entity anchors strictly before that occurrence-time bound; the orphan sweep also reconciles missing entities."},
	"backend/services/event-management/model/events.go:LocationEvent": {
		deletionHistory, "Write-time location payload records OccurredTime and event/payload identity; DataLifecyclePolicy location retention bounds historical queries when enabled. A read by a reused token returns only rows whose stored parent was received at or after its latest recorded deletion."},
	"backend/services/event-management/model/events.go:MeasurementEvent": {
		deletionHistory, "Write-time measurement payload records OccurredTime and event/payload identity; DataLifecyclePolicy retention bounds historical queries when enabled. A read by a reused token returns only rows whose stored parent was received at or after its latest recorded deletion."},
	"backend/services/event-management/model/events.go:MeasurementRollup": {
		deletionHistory, "Continuous aggregate records measurement history in time buckets, read over historical ranges rather than as live device state. There is no entity-deletion hook and its buckets can span incarnations, so a read by a token with a recorded deletion is served from the raw rows instead."},
	"backend/services/event-management/model/events.go:StateChangeEvent": {
		deletionHistory, "Write-time state-change payload records OccurredTime and event/payload identity; DataLifecyclePolicy retention bounds history when enabled. A read by a reused token returns only rows whose stored parent was received at or after its latest recorded deletion."},
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
		deletionTerminate, "The deletion outbox publishes a CLEARED alarm event (clear_reason ORIGINATOR_DELETED) for each active alarm, which ends the lifecycle; the escalation backstop and the orphan sweep end states whose alarm device-management no longer has. A failed lookup pages anyway (deliberate fail-open), so this row may page once more before it ends."},
	"backend/services/outbound-connectors/lpwan/pending.go:LpwanPendingAck": {
		deletionBounded, "No entity-deletion consumer; LPWAN report settlement or the deadline sweep claims and deletes pending acknowledgements at DeadlineAt. Responses carry command token and dispatch nonce."},
	"backend/services/update-management/model/api_installed_components.go:DeviceInstalledComponent": {
		deletionPurge, "DeviceDeleted (entity-deleted consumer) calls forgetInstalled, hard-deleting the device's rows for every incarnation up to the deleted one; a row recorded late for a deleted incarnation is never read (queries use the current incarnation) and the tenant purge erases it."},
	"backend/services/update-management/model/model_rollouts.go:UpdateRolloutTarget": {
		deletionTerminate, "DeviceDeleted (entity-deleted consumer) marks the dispatched targets of the deleted device's ended assignments CANCELLED."},
	"backend/services/update-management/model/model_updates.go:UpdateAssignment": {
		deletionTerminate, "DeviceDeleted ends the deleted incarnation's non-terminal assignments through the cancel path or superseded_at; every device-facing path serves an assignment only to the device incarnation it was made for (assignmentIsForDevice)."},
	"backend/services/update-management/model/model_updates.go:UpdateAttempt": {
		deletionTerminate, "The deleted device's attempts are cancelled through the existing cancel reducer when it allows, otherwise their assignment is superseded; the device-facing gate refuses reports and downloads against them."},
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
