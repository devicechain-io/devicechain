// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"path/filepath"
	"testing"
)

// Empty after the service cutover; keep the two-way ratchet.
var knownRawCapChecks = map[deliveryCapSite]string{}

var sanctionedFinalDeliveryCallers = map[deliveryCapSite]string{
	{"backend/services/event-processing/processor/react_dispatcher.go", "ReactDispatcher.dispatch"}:      "Classifies a react.Result rather than an error.",
	{"backend/services/outbound-connectors/processor/consumer.go", "DispatchConsumer.retryOrDeadLetter"}: "The deadLetter helper owns the tenant decision.",
	{"backend/services/outbound-connectors/processor/consumer.go", "DispatchConsumer.deadLetter"}:        "Bounds write attempts on the final delivery.",
	{"backend/services/event-sources/processor/gateway_jetstream.go", "GatewayJetStreamSource.settler"}:  "The gateway's give-up protocol may leave poison unacked.",
	{"backend/services/outbound-connectors/lpwan/adapter.go", "Adapter.handle"}:                          "No handling error: a cancelled capacity wait refuses the command as unsent on the final delivery.",
}

var sanctionedDisposeCallers = map[deliveryCapSite]string{
	{"backend/core/deadletter/settle.go", "Sink.Settle"}:                                                                   "Owns the shared dead-letter and acknowledgement tail.",
	{"backend/services/command-delivery/processor/CommandDeliveryProcessor.go", "CommandDeliveryProcessor.handleResponse"}: "Owns the handling error and consumer-specific acknowledgement effects.",
	{"backend/services/command-delivery/processor/DeadLetterWriteback.go", "DeadLetterWriteback.Handle"}:                   "Owns the handling error and consumer-specific acknowledgement effects.",
	{"backend/services/dashboard-management/processor/share_reconciler.go", "ShareReconciler.handle"}:                      "Owns the handling error and consumer-specific acknowledgement effects.",
	{"backend/services/device-management/processor/event_resolver.go", "EventResolver.Process"}:                            "Owns the handling error and consumer-specific acknowledgement effects.",
	{"backend/services/device-state/processor/ownership_processor.go", "OwnershipProcessor.handle"}:                        "Owns the handling error and consumer-specific acknowledgement effects.",
	{"backend/services/device-state/processor/StateProcessor.go", "StateProcessor.dispose"}:                                "Owns the handling error and consumer-specific acknowledgement effects.",
	{"backend/services/event-management/processor/EntityAnchorReconciler.go", "EntityAnchorReconciler.handle"}:             "Owns the handling error and consumer-specific acknowledgement effects.",
	{"backend/services/event-management/processor/failed_events_indexer.go", "FailedEventsIndexer.handle"}:                 "Owns the handling error and consumer-specific acknowledgement effects.",
	{"backend/services/event-management/processor/persist_batch.go", "EventPersistenceWorker.dispose"}:                     "Owns the handling error and consumer-specific acknowledgement effects.",
	{"backend/services/notification-management/processor/NotificationProcessor.go", "NotificationProcessor.dispatchOne"}:   "Owns the handling error and consumer-specific acknowledgement effects.",
	{"backend/services/user-management/deadletters/consumer.go", "Consumer.handle"}:                                        "Owns the handling error and consumer-specific acknowledgement effects.",
	{"backend/services/command-delivery/processor/DeletionConsumer.go", "DeletionConsumer.retryOrDrop"}:                    "Owns the handling error and consumer-specific acknowledgement effects.",
	{"backend/services/device-state/processor/deletion_processor.go", "DeletionProcessor.handle"}:                          "Owns the handling error and consumer-specific acknowledgement effects.",
	{"backend/services/update-management/processor/deletion_consumer.go", "DeletionConsumer.retryOrDrop"}:                  "Owns the handling error and consumer-specific acknowledgement effects.",
}

func checkDeliveryCapRepo(t *testing.T, kind string, ledger map[deliveryCapSite]string) {
	t.Helper()
	root := workspaceRoot(t)
	found, visited, err := deliveryCapChecksUnder(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range workspaceModuleDirs(t, root) {
		abs, err := filepath.Abs(dir)
		if err != nil {
			t.Fatal(err)
		}
		if !visited[abs] {
			t.Errorf("delivery cap scan never visited module %s", abs)
		}
	}
	for _, err := range deliveryCapLedgerErrors(found, kind, ledger) {
		t.Error(err)
	}
}

func TestNoRawDeliveryCapChecks(t *testing.T) { checkDeliveryCapRepo(t, "raw", knownRawCapChecks) }
func TestFinalDeliveryCallersAreSanctioned(t *testing.T) {
	checkDeliveryCapRepo(t, "FinalDelivery", sanctionedFinalDeliveryCallers)
}
func TestDisposeCallersAreSanctioned(t *testing.T) {
	checkDeliveryCapRepo(t, "Dispose", sanctionedDisposeCallers)
}
