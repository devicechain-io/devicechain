// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"testing"
	"time"
)

func countEdges(ds []Detection) (raised, resolved int) {
	for _, d := range ds {
		if d.Edge == EdgeResolved {
			resolved++
		} else {
			raised++
		}
	}
	return
}

// TestThresholdPulseInOneMessageKeepsBothEdges pins that a high, low, high run delivered together
// (one resolved message, as a post-outage catch-up delivers a backlog) is NOT collapsed by batching:
// every sample is applied in order, so the pulse yields its own Raised and Resolved and the next high
// a second Raised. Each Raised is a distinct rising edge, stamped at its own sample, so the REACT
// idempotency tokens differ and each sendCommand fires.
func TestThresholdPulseInOneMessageKeepsBothEdges(t *testing.T) {
	e := NewEngine([]Rule{thresholdRule}, 5*time.Second)
	e.ProcessResolved(1, at(3), []Event{
		thrEv("d", 1, true, 45), thrEv("d", 2, false, 5), thrEv("d", 3, true, 45),
	})
	ds := e.Drain()
	if r, s := countEdges(ds); r != 2 || s != 1 {
		t.Fatalf("H,L,H in one message must give 2 raised + 1 resolved, got %d raised %d resolved: %+v", r, s, ds)
	}
	if !ds[0].At.Equal(at(1)) || ds[1].Edge != EdgeResolved || !ds[2].At.Equal(at(3)) {
		t.Fatalf("edges must be stamped at their own samples, in order: %+v", ds)
	}
}

// TestThresholdPulseInOrderAcrossMessagesKeepsBothEdges is the same pulse delivered as a catch-up
// burst of one message per sample, all in event order.
func TestThresholdPulseInOrderAcrossMessagesKeepsBothEdges(t *testing.T) {
	e := NewEngine([]Rule{thresholdRule}, 5*time.Second)
	evs := []Event{thrEv("d", 1, true, 45), thrEv("d", 2, false, 5), thrEv("d", 3, true, 45)}
	ds := feedArrivals(e, evs, []int{0, 1, 2}, 0)
	if r, s := countEdges(ds); r != 2 || s != 1 {
		t.Fatalf("got %d raised %d resolved: %+v", r, s, ds)
	}
}

// TestThresholdReorderedLowBetweenHighsCollapsesPulse pins the one way a single-sample pulse yields
// one rising edge instead of two, and that it is the documented newest-reading semantics rather than
// a silent loss: a Threshold alarm tracks the level of the series' NEWEST reading, so when the low@2
// reaches the engine after the high@3 it is stale (older than the cursor), is ignored and counted in
// lateSamples, and the alarm simply stays raised. The same samples in event order give two edges
// (tests above); only a reordered delivery collapses the pulse, and the collapse is observable in
// the late-sample count.
func TestThresholdReorderedLowBetweenHighsCollapsesPulse(t *testing.T) {
	e := NewEngine([]Rule{thresholdRule}, 5*time.Second)
	evs := []Event{thrEv("d", 1, true, 45), thrEv("d", 2, false, 5), thrEv("d", 3, true, 45)}
	ds := feedArrivals(e, evs, []int{0, 2, 1}, 0) // H@1, H@3, then the stale L@2
	if r, s := countEdges(ds); r != 1 || s != 0 {
		t.Fatalf("a low older than the newest high is stale: want 1 raised 0 resolved, got %d/%d: %+v", r, s, ds)
	}
	if n := e.DrainLateSamples(); n != 1 {
		t.Fatalf("the collapsed low must be counted in lateSamples, got %d", n)
	}
	// The next real low resolves, and a following high is a fresh edge: the series is not wedged.
	ds = feedArrivals(e, []Event{thrEv("d", 4, false, 5), thrEv("d", 5, true, 45)}, []int{0, 1}, 10)
	if r, s := countEdges(ds); r != 1 || s != 1 {
		t.Fatalf("series must recover: want 1 raised 1 resolved, got %d/%d: %+v", r, s, ds)
	}
}

// TestThresholdDuplicateHighIsOneEdge pins that two stored highs with no low between them are ONE
// rising edge, so counting stored high readings overstates edges whenever a high is duplicated
// (an ingest retry whose first attempt was in fact accepted).
func TestThresholdDuplicateHighIsOneEdge(t *testing.T) {
	e := NewEngine([]Rule{thresholdRule}, 5*time.Second)
	evs := []Event{thrEv("d", 1, true, 45), thrEv("d", 1, true, 45), thrEv("d", 2, false, 5)}
	ds := feedArrivals(e, evs, []int{0, 1, 2}, 0)
	if r, s := countEdges(ds); r != 1 || s != 1 {
		t.Fatalf("duplicate high must be one edge: got %d/%d: %+v", r, s, ds)
	}
}
