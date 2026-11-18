// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package durability

// BackupClass is checkpoint policy metadata, independent of the published Class
// recovery-point contract. It does not change stream retention or runtime behavior.
// A new declaration must choose a policy; the zero value is invalid.
type BackupClass string

const (
	// BackupRequired preserves content that cannot be reconstructed, including blobs.
	BackupRequired BackupClass = "must-survive"
	// BackupCheckpoint snapshots a nonempty stream and its consumers at the frozen
	// checkpoint. Empty streams may be excluded explicitly in the backup manifest.
	// In-flight work can be the only accepted copy even when Class is Transient.
	BackupCheckpoint BackupClass = "checkpoint-required-if-nonempty"
	// BackupRebuildable recreates the resource empty and rebuilds its contents.
	// MQTT sessions require devices to resubscribe; retained messages need republishing.
	BackupRebuildable BackupClass = "rebuildable"
	// BackupEphemeral recreates empty: never resurrect bearer handshakes or leases.
	BackupEphemeral BackupClass = "ephemeral"
)

// Valid rejects missing and unknown policies rather than treating them as exclusions.
func (c BackupClass) Valid() bool {
	return c == BackupRequired || c == BackupCheckpoint || c == BackupRebuildable || c == BackupEphemeral
}
