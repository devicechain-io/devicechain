// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graph

import "testing"

func TestConditionCount(t *testing.T) {
	if got := (CanvasDefinition{}).ConditionCount(); got != 0 {
		t.Fatalf("empty canvas: got %d conditions, want 0", got)
	}
	def := CanvasDefinition{Nodes: []Node{
		{Type: NodeSource},
		{Type: NodeThreshold},
		{Type: NodeDuration},
		{Type: NodeAbsence},
		{Type: NodeAggregate},
		{Type: NodeDeltaRate},
		{Type: NodeRepeating},
		{Type: NodeCorrelation},
		{Type: NodeConnectivity},
		{Type: NodeThreshold},
		{Type: NodeBranch},
		{Type: NodeAction},
		{Type: NodeCompute},
		{Type: NodeType("unknown")},
	}}
	if got := def.ConditionCount(); got != 9 {
		t.Fatalf("mixed canvas: got %d conditions, want 9", got)
	}
}
