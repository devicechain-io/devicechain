// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"errors"
	"sync"
	"testing"
)

// A Microservice starts live, and the FIRST reason recorded is the one Live reports.
//
// First-wins is what makes the latch safe to trip from more than one place: the
// condition that actually ended the process is the earliest one, and a later caller
// reporting a consequence of it must not overwrite it with the consequence.
func TestMarkNotLiveKeepsTheFirstReason(t *testing.T) {
	ms := &Microservice{FunctionalArea: "liveness"}
	if err := ms.Live(); err != nil {
		t.Fatalf("a fresh Microservice is not live: %v", err)
	}

	first := errors.New("first cause")
	ms.MarkNotLive(first)
	ms.MarkNotLive(errors.New("second cause"))

	if err := ms.Live(); !errors.Is(err, first) {
		t.Errorf("Live() = %v, want the first reason recorded", err)
	}
}

// A nil reason must not read as live. Without the replacement, Live() would return the
// nil it was handed — the latch would be set and the probe would still answer 200.
func TestMarkNotLiveReplacesANilReason(t *testing.T) {
	ms := &Microservice{FunctionalArea: "liveness-nil"}
	ms.MarkNotLive(nil)

	err := ms.Live()
	if err == nil {
		t.Fatal("MarkNotLive(nil) left the process live")
	}
	if err.Error() == "" {
		t.Error("the replacement reason is empty; it should say none was given")
	}
}

// Concurrent callers agree on one reason. The latch is tripped from a client callback
// goroutine while the probe handler reads it from a server goroutine; run under -race
// this is also the check that the two do not race.
func TestMarkNotLiveIsSafeFromManyGoroutines(t *testing.T) {
	ms := &Microservice{FunctionalArea: "liveness-race"}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ms.MarkNotLive(errors.New("racer"))
			_ = ms.Live()
		}()
	}
	wg.Wait()
	if err := ms.Live(); err == nil || err.Error() != "racer" {
		t.Errorf("Live() = %v after concurrent marks, want the racer reason", err)
	}
}
