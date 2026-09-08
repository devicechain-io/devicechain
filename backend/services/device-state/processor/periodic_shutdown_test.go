// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-state/model"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// 🔴 WHAT THIS FILE IS FOR. ExecuteStop ended at `close(sp.quit)`. Closing quit stops the
// NEXT inactivity sweep from starting and says nothing about the one already running, so
// ExecuteStop returned while a sweep was still in flight. Its caller
// (beforeMicroserviceStopped) then went on to stop the RdbManager, whose Terminate closes
// the pool — leaving the abandoned sweep issuing queries against a pool that had gone,
// from a service that had already reported a clean stop.
//
// 🔑 A TEST THAT ONLY ASSERTS ExecuteStop RETURNS nil CANNOT SEE THAT. It passes just as
// happily against the version with no join, which is the version that had the defect. So
// this test parks a sweep and asserts ExecuteStop has NOT returned; releasing it is the
// counterweight, since a join that never completes is a hung shutdown rather than a fixed
// one.

// blockingSweepApi parks the inactivity sweep on a channel the test owns. The embedded
// interface is nil deliberately: a parked sweep reaches no other method.
type blockingSweepApi struct {
	model.DeviceStateApi

	entered chan struct{}
	release chan struct{}
}

func (a *blockingSweepApi) SweepInactive(context.Context, time.Time) (int64, error) {
	a.entered <- struct{}{}
	<-a.release
	return 0, nil
}

// eofStateReader ends the read loop immediately, so a test can drive the real lifecycle
// without a broker.
type eofStateReader struct{}

func (eofStateReader) ReadMessage(context.Context) (messaging.Message, error) {
	return messaging.Message{}, io.EOF
}
func (eofStateReader) HandleResponse(error) {}

func TestStopWaitsForTheInactivitySweep(t *testing.T) {
	api := &blockingSweepApi{entered: make(chan struct{}, 1), release: make(chan struct{})}
	sp := &StateProcessor{
		ResolvedEventsReader: eofStateReader{},
		Api:                  api,
		inactivityInterval:   5 * time.Millisecond,
	}
	if err := sp.ExecuteInitialize(context.Background()); err != nil {
		t.Fatalf("ExecuteInitialize: %v", err)
	}
	if err := sp.ExecuteStart(context.Background()); err != nil {
		t.Fatalf("ExecuteStart: %v", err)
	}

	select {
	case <-api.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the inactivity sweep never started; this test cannot measure a join it never set up")
	}

	stopped := make(chan error, 1)
	go func() { stopped <- sp.ExecuteStop(context.Background()) }()

	// The window is enormous relative to the work — an unjoined ExecuteStop closes a
	// channel and returns in microseconds — so this is a widened race, not a tight one.
	select {
	case err := <-stopped:
		t.Fatalf("ExecuteStop returned (err=%v) while an inactivity sweep was still running: "+
			"the service reports a clean stop and then closes its database pool underneath a "+
			"goroutine still using it", err)
	case <-time.After(250 * time.Millisecond):
	}

	close(api.release)

	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("ExecuteStop: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ExecuteStop did not return after the sweep was released; the join has turned " +
			"a shutdown into a hang")
	}
}

// 🔑 THE COUNTERWEIGHT. Waiting is only correct while a shutdown with nothing in flight is
// still prompt: a join placed where the monitor can never reach its Done would satisfy the
// test above by hanging, and this is what tells the two apart.
func TestStopIsPromptWhenNoSweepIsRunning(t *testing.T) {
	sp := &StateProcessor{ResolvedEventsReader: eofStateReader{}, Api: &blockingSweepApi{}}
	if err := sp.ExecuteInitialize(context.Background()); err != nil {
		t.Fatalf("ExecuteInitialize: %v", err)
	}
	if err := sp.ExecuteStart(context.Background()); err != nil {
		t.Fatalf("ExecuteStart: %v", err)
	}

	stopped := make(chan error, 1)
	go func() { stopped <- sp.ExecuteStop(context.Background()) }()

	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("ExecuteStop: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ExecuteStop hung with no sweep in flight; the join is waiting on a loop that " +
			"cannot exit")
	}
}

// 🔑 THE OTHER HALF OF "the loop stops": the monitor used to select on quit ALONE, so it
// went on sweeping after the root context was cancelled and only stopped at close(quit).
// Microservice shutdown cancels the root context BEFORE it calls Stop, and that root
// context is the one ExecuteStart hands the monitor — so a monitor that ignores it keeps
// sweeping through a window in which the rest of the process is already being torn down.
// It drives the loop directly, since ExecuteStop closes quit and would therefore stop even
// a monitor that reads nothing else.
func TestTheInactivityMonitorStopsOnContextCancellation(t *testing.T) {
	// quit is left nil deliberately: a nil channel never becomes ready, so the only way
	// out of the loop is the cancellation this test is about.
	sp := &StateProcessor{Api: &blockingSweepApi{}, inactivityInterval: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		sp.runInactivityMonitor(ctx)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the inactivity monitor ignored its cancelled context and kept ticking through " +
			"a shutdown the rest of the process had already begun")
	}
}
