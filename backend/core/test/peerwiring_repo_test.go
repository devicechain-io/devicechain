// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"strings"
	"testing"
)

var peerWiringLedger = map[string]peerWiringCount{
	"backend/core/config/endpoint.go":                   {1, 0},
	"backend/core/governance/lifecycle_resolver.go":     {0, 1},
	"backend/services/event-sources/main.go":            {6, 5},
	"backend/services/event-sources/presence_wiring.go": {5, 3},
	"backend/services/lwm2m-ingest/main.go":             {4, 1},
	"backend/services/mcp/server/graphql.go":            {1, 0},
	"backend/services/sparkplug-ingest/main.go":         {4, 3},
}

func TestPeerWiringLedger(t *testing.T) {
	found, err := peerWiringUnder(workspaceRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	urls, mints := 0, 0
	for _, count := range found {
		urls += count.urls
		mints += count.mints
	}
	t.Logf("peer wiring: %d urls, %d mints", urls, mints)
	if urls < 21 || mints < 13 {
		t.Fatal("peer wiring scan is empty")
	}
	if problems := checkPeerWiring(found, peerWiringLedger); len(problems) != 0 {
		t.Fatal(strings.Join(problems, "\n"))
	}
}
