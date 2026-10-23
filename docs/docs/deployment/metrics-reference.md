---
sidebar_position: 10
title: Metrics Reference
---

# Metrics Reference

Every service exports Prometheus metrics on its metrics port, and the monitoring stack scrapes them
without per-service setup (see [Observability & Metrics](./observability.md)). This page lists the
service-specific metrics that are not described with a feature elsewhere. The ones that belong to a
feature are on that feature's page: [ingest backpressure](./observability.md#ingest-backpressure),
[maintenance passes](./observability.md#maintenance-passes), [caches](./observability.md#kv-caches),
[the detection loop](./detection-engine.md#loop-phases) and the
[edge services](./edge-services.md#what-to-watch).

Names are `devicechain_`, then the service's name with its dashes removed, then the name in the first
column, so `batch_refusals_total` in command delivery is `devicechain_commanddelivery_batch_refusals_total`.
A name in braces after a metric lists its labels. No metric on this page is labelled by tenant or by
device, so none of them is a cardinality risk to scrape. Counters end in `_total` and only ever rise, so read them as a rate. Gauges are read as they
are.

Most of what follows is diagnostic: it lets you tell what a service did without reading its log.
Where a metric is one an operator should act on, its row says what to do, and the alert that watches
it, if there is one, is named.


## Command delivery {#command-delivery}

Prefix: `devicechain_commanddelivery_`.

The three [command delivery alerts](./observability.md#command-delivery-alerts) read
`command_delivery_claims_stranded_total`, `command_delivery_presence_read_errors_total` and
`batch_refusals_total{bound="reserve"}`. A Command Delivery dashboard charts the rest. Several of
these are expected to be busy and are not faults: a steady `not_sole` share of declined nudges, claims
lost to the sweep racing a nudge, and holds placed while devices are offline. `command_delivery_responses_refused_total` is expected to read zero. A rising
rate on `command_delivery_stranded_observed_total` or `command_response_lost_unsettled_total` means
commands are timing out against devices that did nothing wrong.
(The doubled `command_delivery_` in the older names is historical and stays, because renaming a series
breaks every dashboard that reads it.)

| Metric | Means |
| --- | --- |
| `batch_cancel_commands_total{disposition}` | Commands examined by a batch cancel, by what the cancel was able to do with each. A standing already_sent share is a brake that keeps arriving late. |
| `batch_devices_total{disposition}` | Devices a command batch resolved to, by whether the batch enqueued to them. |
| `batch_enqueues_total{target_kind, outcome}` | Command batches decided, by how the target was named and the outcome. |
| `batch_refusals_total{code, bound}` | Per-device batch refusals, by code and — for a ceiling refusal — which bound caused it. bound=reserve means the device would have fitted against the tenant's own ceiling and was refused only by the delivery reserve. |
| `command_dead_letters_not_ours_total` | Dead letters read from the shared stream that describe some other kind of work. Acked and ignored — every producer writes to one stream. |
| `command_dead_letters_unreadable_total` | Dead letters this consumer could not act on — no parseable tenant, a body that is not an envelope, or an envelope naming no command. Acked and counted rather than retried, because no redelivery makes a malformed message parse. |
| `command_delivery_claims_lost_total{path}` | Dispatches abandoned because another dispatcher claimed the command first, by the dispatch path that lost. "sweep" is the periodic pass, "nudge" is the dispatch issued when a command is enqueued; the two racing for one row is expected and safe (the claim is a compare-and-set), so read a rate on one path with none on the other rather than the total. |
| `command_delivery_claims_stranded_total` | Commands left reading SENT because their publish failed and the release failed too. On LwM2M the stranded reconciler re-arms these; on MQTT they still expire as TIMEOUT, wrongly blaming the device. |
| `command_delivery_dispatches_exhausted_total` | Commands failed because the platform could not publish them to their device as many times as the configured bound allows, so it stopped retrying. Each row records FAILED with the platform named as the cause, rather than retrying until its TTL and then recording TIMEOUT against a device it never reached. |
| `command_delivery_holds_placed_total` | Commands withheld from dispatch because the device is authoritatively absent. |
| `command_delivery_holds_released_total` | Withheld commands returned to the dispatch queue because their device came back. |
| `command_delivery_nudges_applied_total` | Dispatch nudges that found exactly one queued command and put it through the delivery gates. NOT a count of publishes: the presence gate may still hold or fail the command, exactly as it would on a sweep tick. |
| `command_delivery_nudges_declined_total{reason}` | Dispatch nudges the drain refused to act on, by reason. A high and steady reason="not_sole" rate is expected — the nudge stands down whenever a device has more than one queued command — and is not a fault. |
| `command_delivery_nudges_dropped_total` | Dispatch nudges discarded because the queue was full. A LATENCY signal, not an error rate: the command still goes out on the delivery sweep, which is the net under every nudge. |
| `command_delivery_nudges_requested_total` | Dispatch nudges accepted onto the enqueue-time dispatch queue, one per command created through createCommand (a fleet batch issues none). |
| `command_delivery_presence_read_errors_total` | Sweep passes that could not read the presence projection; the gate fails OPEN, so a standing rate here means commands are being dispatched ungated. |
| `command_delivery_disabled_gate_errors_total` | Times the record of disabled devices could not be read for a dispatch batch (that batch is not dispatched) or could not be synced from device-management. A standing rate means commands are delayed, not lost. |
| `command_delivery_disabled_gate_ready` | Gauge. `1` once this process has listed the tenants and read the disabled devices of every one, `0` until then. A tenant whose record was never filled anywhere is not dispatched to while it is `0`, so a value stuck at `0` with a rising `command_delivery_disabled_gate_errors_total` is a restart during an outage of user-management or device-management. |
| `command_delivery_disabled_gate_pending_tenants` | Gauge. Tenants whose disabled-device record this process has not yet read; their commands wait. A tenant that is deleted, or no longer listed, stops counting. |
| `command_delivery_window_commands_undefined_total` | Commands that were waiting for a maintenance window and were failed, never sent, because the profile version their device type is on now no longer defines them. Each ends `FAILED` with outcome `NOT_DELIVERED`. |
| `command_delivery_claims_refused_disabled_total` | Dispatches the claim refused because the command's device was disabled after the dispatch read the disabled-device record. Nothing is sent, the next sweep holds the command, and these are not counted in `command_delivery_claims_lost_total`. |
| `command_delivery_responses_dead_lettered_total` | Device command responses written to the dead-letter stream after every attempt to record them failed, so an answer the device did give can be seen rather than leaving its command looking unanswered. |
| `command_delivery_responses_refused_total` | Device responses rejected because the publishing device does not own the command they name. Expected to be zero: either a device is answering for another device, or dispatch addressed a command to the wrong one. |
| `command_delivery_stranded_observed_total` | Commands found sitting in SENT with no outcome for longer than the platform could still have been retrying them. |
| `command_response_lost_not_actionable_total` | Dead-lettered command responses this consumer left alone because their reason says the platform declined to write the answer rather than tried and failed. Nothing is settled: there is no lost outcome to record, and the command named may still be live. A rising rate on a reason nobody expected is worth looking at, because this gate is deliberately closed by default. |
| `command_response_lost_not_answerable_total` | Dead-lettered responses whose command was not in a state a response could settle: it had already reached a terminal outcome some other way, or it has gone back to being live (re-dispatched or held) since the answer was lost. Nothing is written in either case — a late dead letter must not overwrite an outcome that really happened, nor fail a command the platform still intends to deliver. |
| `command_response_lost_settled_total` | Commands driven to a terminal state because the device's answer to them was dead-lettered, so a command whose response the platform lost stops reading as though it were still in flight. |
| `command_response_lost_unsettled_total` | Dead-lettered responses that exhausted every delivery attempt without their command's disposition being written. Those commands read as in flight until their TTL and then lapse to TIMEOUT, blaming a device that did answer. |

## Device management {#device-management}

Prefix: `devicechain_devicemanagement_`.

`geofence_set_publish_failures_total` feeds the [`GeoFenceSetPublishFailing`](./detection-engine.md#what-to-watch)
alert. A rise in `credential_misconfigured_total` means a database was restored next to a root key
other than the one that sealed it, or a device credential was never stored; wrong passwords are not
counted here. The three dead-letter counters mean a state change reached the database but not the
bus: read them with `dcctl dead-letters`.

| Metric | Means |
| --- | --- |
| `alarm_event_dead_lettered_total` | Alarm state-change events that could not be published to the alarm-events stream and were written to the dead-letter stream instead. Each one is an alarm transition that reached the database but not the bus, so nobody was paged about it — it is visible to an operator rather than only logged. |
| `callout_in_flight` | Device auth-callout requests being authorized right now. |
| `callout_refused_busy_total` | Device auth-callout requests refused with the generic denial because the in-flight bound was reached. |
| `config_reports_rejected_total{reason}` | Messages on the [device-reports topic](../guides/connecting-a-device.md#reserved-device-topics) that device-management refused: `malformed` (did not decode), `throttled` (a sync inside the per-device cooldown), `unattributable` (no tenant or device in the topic), `unknown_device`, `unknown_revision`, `digest_mismatch`, `stale` (older than what is recorded) and `device_disabled` (the device is [disabled](../guides/device-credentials.md#disabling-a-device)). A steady rate from one fleet is a device-side bug. |
| `credential_misconfigured_total{path}` | Device credential checks refused because the stored secret can never match: none is stored, or its digest was made under a different key than this instance's root key derives (most likely a database restored next to the wrong root key). path="connect" is the MQTT auth callout, path="event" the per-event check. Wrong passwords are not counted here. |
| `device_reports_dead_lettered_total` | Device messages written to the dead-letter stream because they could not be understood or named a configuration revision the device does not have. Read them with `dcctl dead-letters`. |
| `geofence_set_publish_failures_total` | Geofence-set manifests that could not be published — a marshal error, a broker refusal, or a transport fault. Each one means event-processing was not told about a fence edit, so containment for that tenant holds its previous fence set until a reconcile sweep repairs it. A sustained non-zero rate means fence edits are not reaching the detection engine. |
| `raise_alarm_dead_lettered_total` | Raise-alarm edges written to the dead-letter stream after every attempt to apply them failed, so an alarm that should have been raised or cleared is visible rather than only logged. |
| `resolve_device_disabled_total` | Events refused because their device is [disabled](../guides/device-credentials.md#disabling-a-device). They are discarded, not dead-lettered. |
| `resolve_event_time_bounded_total` | Reported event times refused for leading the server clock by more than the configured tolerance, and replaced with the latest time the tolerance allows. |
| `resolve_not_gateway_child_total` | Events refused because a [gateway](./edge-services.md#gateway-identity) published for a device that is not assigned to it, that does not exist, or because the gateway is disabled. Each is recorded on the `failed-events` stream with its payload (reason `NotGatewayChild`), is not retried, and is reported to the gateway on its notices topic. It is not labelled by tenant or gateway; the log line, at most one a minute per gateway and device, names them. A rise usually means a device was never assigned to its gateway, or was assigned to another. |

## Event processing: detection {#event-processing-detect}

Prefix: `devicechain_eventprocessing_`.

The metrics that alerts read are `detect_is_leader`, `detect_live`, `detect_checkpoints_total` (the
`DetectLeaderIsNotConsuming` alert reads all three) and the `detect_fence_*` counters that back the
[geofence alerts](./detection-engine.md#what-to-watch). `detect_consumer_pending` is the lag signal
that `DetectConsumerBacklogHigh` watches, and `detect_watermark_lag_seconds` is the one behind
`DetectWatermarkLagHigh`. `detect_loop_heartbeat_timestamp_seconds` is the engine's own liveness: if
it is stale while `detect_is_leader` and `detect_live` both read 1, the loop is hung inside a call.

| Metric | Means |
| --- | --- |
| `detect_applied_stream_seq` | Highest JetStream stream sequence captured in the committed snapshot. |
| `detect_checkpoint_failures_total{stage}` | Scheduled DETECT checkpoints that did not commit, by stage (publish, serialize, save). A checkpoint call that outlives its deadline counts against the stage it was stuck in. A stale-writer refusal is not counted here: that ends the process. |
| `detect_checkpoint_save_seconds` | Time each committed snapshot spent being written to the database, as a distribution: the second part of a checkpoint's cost, after serialization. |
| `detect_checkpoint_serialize_seconds` | Time each committed snapshot spent serializing the engine's state, as a distribution: the first part of a checkpoint's cost. |
| `detect_checkpoints_total` | Committed DETECT snapshot checkpoints. |
| `detect_consumer_ack_pending` | Delivered-but-unacked messages on the resolved-events durable consumer (in-flight work). |
| `detect_consumer_pending` | Undelivered messages waiting on the resolved-events durable consumer (the primary DETECT lag signal). |
| `detect_derived_publish_ack_seconds` | Time each published derived event waited for the broker to acknowledge it, as a distribution. Successful publishes only. It is not detection latency: a detection waits for its checkpoint before it is published. |
| `detect_derived_publish_pass_seconds` | Time each checkpoint spent publishing its detections, from the first send to the last acknowledgement, as a distribution. It does not include the time detections waited for their checkpoint. |
| `detect_derived_publish_outstanding` | Derived events sent and waiting for the broker's acknowledgement right now (bounded by `derivedPublishing.maxInFlight`). Zero between checkpoints. |
| `detect_derived_publish_outstanding_bytes` | Payload bytes of those events (bounded by `derivedPublishing.maxBufferedBytes`, except that a single larger event is sent on its own). |
| `detect_derived_publish_window_full_seconds_total` | Seconds checkpoints spent waiting for an acknowledgement while another detection was ready to send but the in-flight limit or byte budget had no room. |
| `detect_derived_publish_lane_waits_total` | Detections that waited for an earlier detection of the same device (or correlation anchor) to be acknowledged before they could be sent, with `derivedPublishing.maxInFlight` above 1. |
| `detect_derived_publish_failures_total{outcome}` | Derived-event publishes without a positive acknowledgement, by `outcome`: `unknown` (a timeout: the event may have been stored) or `failed` (the broker refused it). Each defers its checkpoint and is retried with the same event and identity, so a stored one is not stored twice. |
| `detect_derived_publish_stale_outcomes_total` | Publish outcomes refused because they did not belong to a publish the current checkpoint was waiting for. Always zero unless something is wrong. |
| `detect_derived_events_published_total` | Derived signal events published. |
| `detect_derived_events_rejected_total{reason}` | Detections dropped before publish, by reason (bounded enum). |
| `detect_events_applied_total` | Resolved events fed into the DETECT engine. |
| `detect_fact_persist_retries_total` | Retries of a fact projection write (rules, roster, attributes, deletions) that failed with a non-terminal error. The fact stays unacked and this consumer is blocked behind it until a retry commits. |
| `detect_fanout_events_total` | Per-rule core events produced by the resolved-event fan-out. |
| `detect_fence_archive_skew_total` | Geofence archive reads that failed because device-management does not serve the manifest doors — it is running a build from before manifest delivery. Repairs itself when that service rolls forward. |
| `detect_fence_geometry_cache_evictions_total` | Compiled-geometry cache entries dropped to stay inside the cache's vertex bound. |
| `detect_fence_geometry_cache_hits_total` | Compiled-geometry cache lookups served from cache, avoiding both a cross-service read and a recompile. |
| `detect_fence_geometry_cache_misses_total` | Compiled-geometry cache lookups that had to fetch and compile the document. |
| `detect_fence_geometry_cache_vertices` | Total vertices held in the compiled-geometry cache — the quantity its bound is counted in. |
| `detect_fence_geometry_hash_mismatch_total` | Geofence geometry documents that did not hash to the content address they were requested under. Always a bug, never transient: the peer served the wrong row, something re-encoded the document in transit, or the archive is corrupt. |
| `detect_fence_geometry_unresolved_total` | Geofence manifest entries whose geometry could not be obtained from device-management's archive. Each one leaves that fence reporting unresolvable rather than answering, repaired by the next reconcile sweep if the cause was transient. |
| `detect_idle_advances_total` | Wall-clock idle advances that produced at least one detection. |
| `detect_idle_detections_total` | Detections produced by wall-clock idle advance (absence/duration/session firing on silence). |
| `detect_is_leader` | 1 while this replica holds the DETECT partition lease, from acquisition rather than from the end of the term build. |
| `detect_live` | 1 while this replica is consuming inside a held leadership term; 0 while standing by OR while building a term it has already acquired. |
| `detect_live_gap_fills_total{outcome}` | Times live consumption met a message whose stream sequence was more than one past the engine's and read the missing range from the stream, by outcome: filled (the range was read and held messages), absent_only (the stream held none of it: purged or evicted), failed (an attempt to read the range did not complete; live consumption parks and the attempt repeats every tick, so a sustained outage counts every attempt, not once). |
| `detect_live_gap_sequences_total{outcome}` | Stream sequences inside live gap fills, by outcome: applied (a message the broker had counted as delivered that never reached the loop, now applied), absent (not in the stream: purged or evicted), skipped (read back unprocessable, and recorded as handled without applying anything). A non-zero applied rate means deliveries are being lost between the broker and this loop. |
| `detect_loop_heartbeat_timestamp_seconds` | Unix time of the last pass of the DETECT single-writer loop, refreshed at least once per tick while a term is live. Stale while detect_is_leader and detect_live read 1 means the loop is hung inside a call. |
| `detect_restore_seconds` | Time to restore engine state from the snapshot store at startup. |
| `detect_rule_stat_write_failures_total` | Rule-health fire counter writes (one tenant's transaction) that failed or timed out and were dropped, not retried. Detection is unaffected (the counters are written in the background), but the console's rule-health counts fall short while this rises; the fires lost are in `detect_rule_stat_updates_discarded_total` with reason `write_failed`, and the cause is in the service log. |
| `detect_rule_stat_write_seconds` | Time each rule-health fire counter write (one tenant's transaction, including the tenant-deletion check it makes first) took on the background writer, as a distribution. It is not time the detection loop spent. Failed writes are included. |
| `detect_rule_stat_updates_admitted_total` | Published detections added to the rule-health fire counters, to be written in the background. |
| `detect_rule_stat_updates_coalesced_total` | Of those, the ones added to a rule already waiting to be written, so they cost no extra row. |
| `detect_rule_stat_updates_written_total` | Rule-health fire updates committed to the database. |
| `detect_rule_stat_updates_discarded_total` | Rule-health fire updates that will never be written, by `reason`: `full` (the waiting budget was exhausted), `overflow` (a rule's waiting count reached its ceiling), `closed` (arrived after the engine's leadership term ended), `write_failed` (its write failed or timed out), `purged` (its tenant was deleted first), `shutdown` (still waiting when the term ended). Detection is unaffected. |
| `detect_rule_stat_admission_contended_total` | Rule-health additions that waited for the background writer to finish a short bookkeeping step. Each wait is bounded and never includes a database call. |
| `detect_rule_stat_pending_keys` | Rules with fire updates not yet written (bounded by `ruleHealth.maxPendingKeys`). |
| `detect_rule_stat_pending_bytes` | Accounted memory of the fire updates not yet written (bounded by `ruleHealth.maxPendingBytes`). |
| `detect_rule_stat_oldest_pending_seconds` | Age of the oldest fire update not yet written: how stale the console's fire counts may be. |
| `detect_rule_stat_batch_rows` | Rules in each rule-health write, as a distribution. |
| `detect_rule_stat_batch_updates` | Fire updates in each rule-health write, as a distribution: how many single-row writes it stands in for. |
| `detect_rules_active` | Rules loaded into the DETECT engine. |
| `detect_stale_absence_dropped_total` | Absence **raises** dropped at publish because the device left the rule's scope: it was deleted, disabled, re-typed, or the rule version was superseded. It also counts a raise's paired clear when that raise was dropped in the same pass, and the clear a republish withholds because the new version keeps the rule. The clear for a device that has left scope is not dropped, so the alarm it raised is cleared. |
| `detect_superseded_frontier_dropped_total` | Detections of the duration, session and aggregate kinds that fire from the passage of time, dropped at publish because their profile version has been superseded. |
| `detect_tenants_over_live_key_budget` | Tenants currently exceeding the per-tenant live-key budget. |
| `detect_tenants_over_retained_sample_budget` | Tenants currently exceeding the per-tenant retained-sample budget. |
| `detect_tenants_over_rule_budget` | Tenants currently exceeding the per-tenant rule-count budget. |
| `detect_watermark_lag_seconds` | Wall-clock time minus the engine watermark at the last checkpoint. |

## Event processing: reactions {#event-processing-react}

Prefix: `devicechain_eventprocessing_`.

`react_events_poison_dropped_total` is the series the `ReactPoisonDropping` alert fires on.
`react_actions_dropped_total{reason="unknown_kind"}` should always be zero: a non-zero value is a
defect to investigate, not load. `react_actions_not_enabled_total` should never show `raiseAlarm` or
`clearAlarm`, because that sink is always wired.

| Metric | Means |
| --- | --- |
| `react_actions_dispatched_total{action}` | REACT actions handed to their sink, by action type (includes idempotent replays). |
| `react_actions_dropped_total{reason}` | REACT actions skipped without being attempted, by reason. unknown_kind: the rule definition carried an action type this build cannot dispatch (a forged or hand-edited definition; unreachable through the supported authoring path), so a non-zero value is a defect to investigate, not load. |
| `react_actions_not_enabled_total{action}` | REACT actions recognized but dropped because this deployment has no sink for them, by action type: sendCommand without command-delivery configured, or httpCall/publish without outbound connectors enabled. The alarm sink is always wired, so raiseAlarm/clearAlarm should never appear here. |
| `react_connector_egress_shed_total{action}` | Connector dispatch attempts (`httpCall`, `publish`) shed at the source for being over the tenant's outbound rate, by action type. Counted per attempt: a redelivery may shed and later admit the same action, so this is not a count of permanently dropped actions. |
| `react_connector_shed_dead_lettered_total{action}` | Connector actions (httpCall/publish) shed at the source and recorded as an individual dead letter with reason shed, by action type. |
| `react_events_dead_lettered_total` | Derived events written to the dead-letter stream after the redelivery cap, so their actions can be inspected rather than vanishing. |
| `react_events_orphaned_total` | Derived events whose rule was gone from the projection (nothing dispatched). |
| `react_events_poison_dropped_total` | Derived events dropped after the redelivery cap, because their dispatch kept failing. They are also written to the dead-letter stream, so this counts the same events as `react_events_dead_lettered_total`; it is kept because the `ReactPoisonDropping` alert fires on it. |

## Event sources {#event-sources}

Prefix: `devicechain_eventsources_`.

The presence metrics belong to the broker-asserted MQTT presence that this service reads from the
broker. `presence_tap_off` is the one to alert on: it is 1 when that presence is not running on a
replica, labelled by why, and a long-lived MQTT fleet emits no advisories to show it. The canary pair
tells you whether presence is being read at all: `presence_canary_missed_total` rising means it is not.
`presence_reconcile_regressed_sessions` standing above zero means the repairs are not converging. The
`total_msg_*` counters are per source.

| Metric | Means |
| --- | --- |
| `command_wake_dropped_total` | Command wakes discarded because the queue was full; their commands are released later by command-delivery's reconcile pass, so this is a latency signal rather than a loss. |
| `command_wake_failed_total` | Command wakes that could not reach command-delivery; the reconcile pass covers them. |
| `command_wake_released_total` | Withheld commands returned to the delivery queue because their device reconnected. |
| `command_wake_requested_total` | Returning devices queued for a command wake. |
| `presence_advisories_skipped_total{reason}` | Broker connection advisories that produced no presence event, by reason. |
| `presence_canary_missed_total` | Canary probes whose own broker connection was NOT observed — presence is not being read. |
| `presence_canary_observed_total` | Canary probes whose own broker connection was observed end to end. |
| `presence_events_failed_total` | Presence transitions that could not be written to the inbound stream. |
| `presence_events_refused_total` | Presence transitions refused by the ingest admission gate (deleted tenant or tenant ceiling). |
| `presence_reconcile_regressed_sessions` | Devices found LIVE on a session id lower than the one the projection holds, as of the last reconciliation pass. A standing non-zero value means the repairs are not converging. |
| `presence_reconcile_repaired_total{direction}` | Presence repairs that reconciliation emitted because an advisory was missed, and that the device's status was then seen to take, by direction. A repair is counted on the pass after it is emitted. |
| `presence_reconcile_unapplied_total{direction}` | Presence repairs that the device's status had not taken by the next reconciliation pass, by direction. They were refused or lost, and are emitted again. A standing rate means repairs are not landing; a device deleted while it still showed as connected is one cause, since its repair is refused on every pass. |
| `presence_reconcile_runs_total{outcome}` | Presence reconciliation passes, by outcome. |
| `presence_reconcile_withheld_disconnects_total` | Devices that would have been marked offline had the broker inventory been provably complete. |
| `presence_released_total` | Devices handed back from asserted to inferred presence because this source stopped reading the broker. |
| `presence_sessions_regressed_total` | Presence transitions observed with a session id lower than this replica's high-water mark (a broker node's clock may be trailing its peers). Diagnostic only: whether such a transition is applied is decided downstream by the projection. |
| `presence_still_asserted` | Devices this source still had asserted at the start of the last release pass. The work empties itself, so a healthy drain walks this to zero and stays there. |
| `total_http_connections_closed_before_request{source}` | Count of connections to an HTTP ingest listener that closed before delivering a request — a header-timeout close, but equally a port scan, a TCP health check or a client that hung up. |
| `total_msg_decode_successful{source}` | Count of total messages successfully decoded. |
| `total_msg_failed_decode{source}` | Count of total messages that failed to decode. |
| `total_msg_tenant_deleted{source}` | Count of inbound messages refused because their tenant has been deleted and its data is being reclaimed. |

## LwM2M ingest {#lwm2m-ingest}

Prefix: `devicechain_lwm2mingest_`.

The leader and serving gauges are described with the [edge services](./edge-services.md#lwm2m-ingestion-metrics),
as are the metrics that page. The ones below are the rest. The command counters tell the story of a
command through the adapter: attempted, then succeeded or failed, with the offline, parked and drained
counters showing commands that waited for a device to wake. `command_park_errors_total` and
`command_response_publish_failures_total` are the two that mean a command can end as a `TIMEOUT`
although the device was not at fault.

| Metric | Means |
| --- | --- |
| `bad_requests_total` | Malformed /rd requests (e.g. a registration-item request with no location). |
| `coap_requests_total{code}` | Transport-level CoAP requests handled by the L0 health probe, by response code. The /rd registration outcomes are metered by the registrations/updates/deregistrations/expiries counters. |
| `command_drain_claim_errors_total` | Backlogged commands NOT dispatched because ownership could not be established with command-delivery (fail-closed; retried shortly, in order). |
| `command_drain_claims_lost_total` | Backlogged commands another dispatcher or the delivery sweep claimed first, so this drain did not dispatch them — a duplicate actuation avoided, not a fault. |
| `command_drain_errors_total` | Drain fetches that failed; retried after a short delay while the device stays live. |
| `command_park_errors_total` | Undeliverable commands that could NOT be handed back to command-delivery; left unacked to retry on redelivery, and stuck in SENT until one succeeds. |
| `command_park_settled_total` | Hand-backs that moved no row because the command had already been answered, cancelled, expired or re-claimed — a settled outcome, not a fault. |
| `command_park_skipped_total` | Undeliverable commands NOT handed back because no parker is wired or the delivery envelope carried no dispatch nonce; they stay SENT and ride their TTL. |
| `command_response_publish_failures_total` | Command outcomes that could not be published to command-responses after local retries (the op already ran, so the command is not redelivered; it will TIMEOUT). |
| `commands_attempted_total{op}` | LwM2M commands dispatched to a live device (= succeeded + failed), by operation. |
| `commands_drained_total` | Backlogged (held or parked) commands dispatched to a live device by a drain, oldest first. |
| `commands_poison_total` | Commands dropped as unprocessable (no parseable tenant in the subject, or an undecodable envelope). |
| `commands_served_offline_total` | Commands for a device this adapter serves that had no live connection — parked in command-delivery and drained on the device's next wake, or EXPIRED at their TTL if it never wakes. |
| `commands_succeeded_total{op}` | LwM2M commands the device acknowledged with a 2.xx, by operation. |
| `commands_tenant_deleted_total` | Commands ack-dropped because their tenant has been deleted and its data is being reclaimed — the platform declining to actuate an offboarded customer's hardware. |
| `deregistrations_total` | LwM2M explicit deregistrations (`DELETE /rd/{id}`). |
| `device_info_attribute_write_errors_total` | Harvested device-information attributes that could not be written to device-management; written again at the device's next harvest. |
| `device_info_attribute_writes_shed_total` | Harvested attribute batches dropped because the attribute-write pool was busy; written at the device's next harvest. A steady rate means device-management is slow to answer. |
| `device_info_harvest_retries_total` | Device-information harvests retried once because the device did not answer. |
| `device_info_harvest_skipped_total` | Device-information harvest requests not run: the device was harvested within the last hour, its shard's queue was full, it was no longer reachable, or it is disabled (or whether it is could not be checked). |
| `device_info_harvests_total{outcome}` | Device-information reads run when a device became reachable, by outcome: `complete` (every resource read), `partial` (some missing), `empty` (answered, but gave none), `unanswered` (did not respond). |
| `devices_registered_total` | Devices auto-created on their first LwM2M registration. |
| `handshakes_total` | Completed DTLS handshakes (each is a new authenticated LwM2M session). |
| `measurements_emitted_total` | Individual measurement samples durably written from LwM2M Observe/Notify. |
| `notifies_received_total` | LwM2M Notify messages received on an observed object instance. |
| `observe_terminal_notifications_total` | Notifications that terminated an observation (RFC 7641, e.g. 4.04 after the observed instance was deleted). |
| `presence_dropped_total` | Presence transitions dropped: an unregistered device on a no-auto-register credential, or a durable-emit budget exhausted. |
| `registrations_disabled_refused_total` | Registrations refused (`4.03`), and registrations ended at the next update (`4.04`), because an operator has disabled the device. |
| `sessions_rejected_total` | New sessions refused because the live-session ceiling (maxSessions) was reached. |
| `telemetry_devices_registered_total` | Devices auto-created on a first telemetry sample (rare: LwM2M devices are created at /rd registration). |
| `telemetry_tenant_deleted_dropped_total` | Telemetry samples dropped because the tenant has been deleted and its data is being reclaimed. |
| `telemetry_unknown_dropped_total` | Telemetry samples dropped for an unregistered device (auto-registration off for the credential). |

## Sparkplug ingest {#sparkplug-ingest}

Prefix: `devicechain_sparkplugingest_`.

The rest of the Sparkplug metrics, including the leader gauge, are described with the
[edge services](./edge-services.md#sparkplug-ingestion-metrics). `samples_skipped_total` exists
because a node that publishes only values with no numeric sample otherwise looks identical to an idle one.

| Metric | Means |
| --- | --- |
| `devices_registered_total` | Devices auto-registered on first sight of their Sparkplug identity. |
| `samples_skipped_total{reason}` | Metrics of accepted Sparkplug messages that produced no sample, by reason: non_numeric (a boolean, string, bytes, dataset or template value), null (is_null), unnamed (no name and no resolvable alias) or oversize (the message carried more than 16,384 metrics, so none of its readings were stored; the service logs a warning naming the topic). A node publishing only such metrics otherwise looks identical to an idle one. |

## Notification management {#notification-management}

Prefix: `devicechain_notificationmanagement_`.

Notifications that fail every delivery attempt are written to the dead-letter stream, where
`dcctl dead-letters` lists them. This counter tells you that pages were never sent.

| Metric | Means |
| --- | --- |
| `notifications_dead_lettered_total` | Alarms written to the dead-letter stream after every delivery attempt failed, so an operator can see which pages were never sent. |
| `quiet_hours_total{outcome}` | What quiet hours did to a notification, by outcome: held (held instead of sent), released (sent when the quiet stretch ended), dropped_resolved (dropped because the alarm was acknowledged or cleared), dropped_policy (dropped because the policy was disabled or deleted), dropped_exhausted (dropped and dead-lettered after 5 releases reached no channel) or superseded (dropped because the policy paged a newer event for the same alarm). |

## User management {#user-management}

Prefix: `devicechain_usermanagement_`.

These count the dead-letter consumer, which stores a failure a service gave up on in a queryable store so it outlives the
stream's own seven-day window. `dead_letters_unstored_total` is the one that matters: a failure
| `tenant_purge_in_flight` | Tenant deletions currently open. |
| `tenant_purge_oldest_age_seconds` | Age of the oldest open tenant deletion, from its cut; 0 when none are open. `TenantPurgeStalled` watches it. |
| `tenant_purge_store_blocked{store,reason}` | Open deletions each storage system is holding back, by store (`rdb`, `tsdb`, `broker`, `kv`, `detect`, `blob`, `other`) and reason (`deferred`: data still present or arriving; `error`: the store could not be asked). `TenantPurgeStoreFailing` and `TenantPurgeStoreHolding` watch it. |
| `tenant_purge_store_rows_erased_total{store}` | Rows, objects, messages or keys each store has erased for tenant deletions: the progress signal for a store that is blocked. |
| `tenant_purge_store_erase_duration_seconds{store}` | Histogram of how long one store's erase call took. |
| `tenant_purge_completed_total` | Tenant deletions completed: every store clean, settled and held, fences lifted, token released. |
| `tenant_purge_failures_total` | Times the coordinator could not carry one tenant's deletion through a pass. It retries on the next pass, so this counts attempts. `TenantPurgeCoordinatorFailing` watches it. |
| `tenant_purge_duration_seconds` | Histogram of seconds from a tenant's deletion to its token being released. Includes the token hold. |
| `tenant_purge_clean_duration_seconds` | Histogram of seconds from a tenant's deletion to the last store reporting clean. Excludes the settle and token-hold waits that follow: the time the erasure itself took. |
the store could not take is recorded nowhere, and `DeadLetterStoreLosing` fires on it.

| Metric | Means |
| --- | --- |
| `dead_letters_stored_total` | Dead letters written to the queryable store, so a failure a consumer gave up on outlives the stream's own seven-day window. |
| `dead_letters_unstorable_total` | Dead-letter messages this consumer could not make sense of — no parseable tenant, or a body that is not an envelope. They are ACKED and counted rather than retried, because no redelivery makes a malformed message parse. |
| `dead_letters_unstored_total` | Dead letters that exhausted every delivery attempt without being stored — the store was unreachable for longer than the retries last. The failure they described is now recorded nowhere, which is the one thing this consumer exists to prevent. |

## Update management {#update-management}

Prefix: `devicechain_updatemanagement_`. The service is opt-in; see [Over-the-air updates](../concepts/over-the-air-updates.md).

`update_download_grants_total{result="not_configured"}` rising means `updates.downloadBaseUrl` is unset and no device can download. `update_attempts_expired_total` counts devices that went quiet during an update; an `UNKNOWN` one waits for the device's inventory or an operator's `abandonUnknownUpdate`.

| Metric | Means |
| --- | --- |
| `update_attempts_expired_total{state}` | Update attempts the deadline sweep moved because the device went quiet: `TIMED_OUT` before installation was reported started, `UNKNOWN` during installation or reboot. |
| `update_desired_publish_failures_total` | Update assignments, download grants and cancels that could not be put on a device's desired topic. The device picks the state up at its next sync. |
| `update_download_bytes_total` | Artifact bytes streamed to devices. |
| `update_download_grants_total{result}` | Download grants devices asked for, by outcome: `granted`, or why not (`unknown_assignment`, `superseded`, `finished`, `cancel_requested`, `artifact_unavailable`, `not_configured`, `error`). |
| `update_downloads_total{result}` | Artifact download requests, by outcome: `served`, `aborted` (the client went away or the object could not be read mid-stream), or why it was refused before any byte was served (`capability_malformed`, `capability_invalid`, `capability_expired`, `stale`, `gone`, `range_not_satisfiable`, `busy`, `device_disabled`, `error`). A rising `capability_invalid` rate is someone guessing. |
| `update_reports_rejected_total{reason}` | Device messages update-management refused before applying them: `malformed`, `unattributable`, `throttled` and `device_disabled` (the device is disabled). |
| `update_reports_total{verdict}` | Update progress and inventory reports, by the verdict the update state machine gave: `APPLIED`, or why not (`DUPLICATE`, `STALE_ATTEMPT`, `DIGEST_MISMATCH`, `WRONG_COMPONENT`, `REGRESSION`, `UNCONFIRMED`, `NOT_CANCELLABLE`, `LATE`, `CONFLICT`, `INVALID_ATTEMPT`, `WRONG_KIND`, `UNKNOWN_ASSIGNMENT`). A rising non-applied rate from one fleet is a device-side bug. |

## Every service {#every-service}

Prefix: `devicechain_<area>_`, where `<area>` is the service's name without dashes (`devicechain_devicemanagement_ready`). Every service exports these.

`safety_gate_enabled` is what the [`SafetyGateDisabled`](./observability.md#safety-gates) alert reads.
The JetStream gauges report each stream's size against its configured limits and its replica health;
the replication alerts under [Replication](./observability.md#replication) are built on the latter.

| Metric | Means |
| --- | --- |
| `auth_gate_attempts_total` | Background auth-gate JWKS fetch attempts. |
| `auth_gate_failures_total` | Background auth-gate JWKS fetch failures. |
| `ready` | 1 when the data plane is ready (auth live), else 0. |
| `safety_gate_enabled{gate}` | 1 when an optional safety gate is wired in this service, 0 when it is OFF because its configuration is absent. An OFF gate lets work through that the gate exists to refuse (tenant_lifecycle: work for a deleted tenant; presence: commands to absent devices; rule_validation: uncompilable detection rules at profile publish). |
| `jetstream_peers_current{stream}` | RAFT peers currently caught up and online for this stream or KV bucket, including the leader. Below jetstream_replicas_actual means the stream is labelled replicated but is not. |
| `jetstream_replicas_actual{stream}` | Replica count JetStream reports for this stream or KV bucket. |
| `jetstream_stream_limit_bytes{stream}` | Configured MaxBytes ceiling for a JetStream stream. |
| `jetstream_stream_limit_messages{stream}` | Configured MaxMsgs ceiling for a JetStream stream. |
| `jetstream_stream_used_bytes{stream}` | Current on-disk bytes stored in a JetStream stream. |
| `jetstream_stream_used_messages{stream}` | Current message count in a JetStream stream. |
