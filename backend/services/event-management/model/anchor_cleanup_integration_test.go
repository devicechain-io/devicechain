// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import "testing"

// The real migration rig supplies schema-qualified TimescaleDB hypertables.
// Exercise both correlated probes against them, including the missing-parent path.
func TestIntegrationDeleteAnchorsForEntity_ProcessedTime(t *testing.T) {
	api := newPostgresApi(t, freshInstance(t, "anchorreceipt"))
	assertAnchorCleanupByReceiptTime(t, api)
}

func TestIntegrationAnchorsPersistedAfterDeletionCleanupAreNotReinserted(t *testing.T) {
	api := newPostgresApi(t, freshInstance(t, "anchorlate"))
	assertAnchorsPersistedAfterCleanupAreDropped(t, api)
}
