// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import "testing"

// A late sweep complements the per-test checks (which attribute a leak to t.Name).
// The per-test checks do not depend on this test's order, -run, or -shuffle.
func TestZZFleetPostgresNoDispatchingOperationsLeftBehind(t *testing.T) {
	mgr := newPostgresRdbManager(t)
	checkFleetTestDispatching(t, mgr.Database)
}
