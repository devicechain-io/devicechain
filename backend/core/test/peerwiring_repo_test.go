// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"strings"
	"testing"
)

var peerWiringAllowedURLs = map[string]bool{
	"backend/core/config/endpoint.go":        true,
	"backend/services/mcp/server/graphql.go": true,
}

func TestPeerWiringAllowlist(t *testing.T) {
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
	if urls < 2 {
		t.Fatal("peer wiring scan is empty")
	}
	if problems := checkPeerWiring(found, peerWiringAllowedURLs); len(problems) != 0 {
		t.Fatal(strings.Join(problems, "\n"))
	}
}
