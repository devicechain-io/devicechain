// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
)

// The request edge has one definition, config.RequestEdgeTimeout. Every default
// that is meant to BE the edge must equal it, and every budget meant to fit inside
// it must be strictly less, so an edit to one cannot silently diverge from the rest.
func TestRequestEdgeHasOneDefinition(t *testing.T) {
	if DefaultRequestExecTimeout != config.RequestEdgeTimeout {
		t.Errorf("DefaultRequestExecTimeout = %v, want the request edge %v", DefaultRequestExecTimeout, config.RequestEdgeTimeout)
	}
	if DefaultRequestBodyReadTimeout >= config.RequestEdgeTimeout {
		t.Errorf("body read timeout %v must be inside the request edge %v", DefaultRequestBodyReadTimeout, config.RequestEdgeTimeout)
	}
	if config.AiDraftBudget >= config.RequestEdgeTimeout {
		t.Errorf("AiDraftBudget %v must be strictly inside the request edge %v", config.AiDraftBudget, config.RequestEdgeTimeout)
	}
}
