// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package backupset

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/durability"
	"github.com/devicechain-io/dc-microservice/kv"
	"github.com/devicechain-io/dc-microservice/streams"
)

func TestEveryDeclaredItemHasBackupPolicy(t *testing.T) {
	if err := ValidateBackupPolicy(); err != nil {
		t.Fatal(err)
	}
	// Check every entry, so failures name all declarations needing a policy.
	for _, it := range Items() {
		if !it.BackupClass.Valid() {
			t.Errorf("%s %q needs an explicit valid BackupClass on its declaration", it.Kind, it.Name)
		}
	}
}

func TestBackupPolicyRejectsMissingAndUnknownClasses(t *testing.T) {
	for _, original := range Items() {
		t.Run(string(original.Kind)+"/"+original.Name, func(t *testing.T) {
			for _, invalid := range []durability.BackupClass{"", "unknown"} {
				items := Items()
				for i := range items {
					if items[i].Kind == original.Kind && items[i].Name == original.Name {
						items[i].BackupClass = invalid
					}
				}
				err := validateBackupPolicy(items)
				if err == nil {
					t.Fatalf("accepted %s %q with invalid policy %q", original.Kind, original.Name, invalid)
				}
				if !strings.Contains(err.Error(), original.Name) || !strings.Contains(err.Error(), string(original.Kind)) {
					t.Errorf("error must identify the unclassified resource: %v", err)
				}
			}
		})
	}
}

func TestCheckpointPolicyPreservesAcceptedWork(t *testing.T) {
	// Section B's initial coordinated checkpoint includes every nonempty platform
	// stream and its consumers, including unsaved diagnostics and deletion fan-out.
	for _, s := range streams.All {
		want := durability.BackupCheckpoint
		if s.Suffix == streams.DeviceEventsCapture || s.Suffix == streams.ConnectorDispatchDead {
			want = durability.BackupRequired
		}
		if s.BackupClass != want {
			t.Errorf("stream %q has backup policy %q, want %q", s.Suffix, s.BackupClass, want)
		}
	}
	for _, b := range kv.All {
		want := durability.BackupEphemeral
		if b.Durability == durability.Rebuildable {
			want = durability.BackupRebuildable
		}
		if b.BackupClass != want {
			t.Errorf("bucket %q has backup policy %q, want %q; restore buckets empty", b.Name, b.BackupClass, want)
		}
	}
	wantGateway := map[string]durability.BackupClass{
		"$MQTT_msgs":   durability.BackupCheckpoint,
		"$MQTT_out":    durability.BackupEphemeral,
		"$MQTT_qos2in": durability.BackupEphemeral,
		"$MQTT_sess":   durability.BackupRebuildable,
		"$MQTT_rmsgs":  durability.BackupRebuildable,
	}
	for _, it := range gateway {
		if it.BackupClass != wantGateway[it.Name] {
			t.Errorf("gateway %q has backup policy %q, want %q", it.Name, it.BackupClass, wantGateway[it.Name])
		}
	}
	if objectStore.BackupClass != durability.BackupRequired {
		t.Error("object store must preserve OTA artifacts and branding content")
	}
}
