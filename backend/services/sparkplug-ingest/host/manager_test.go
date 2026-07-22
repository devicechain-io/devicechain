// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// fakeSupervised records how many times Connect and Stop were called (Stop runs on
// its own goroutine, so the counters are atomic).
type fakeSupervised struct {
	connects atomic.Int32
	stops    atomic.Int32
}

func (f *fakeSupervised) Connect() { f.connects.Add(1) }
func (f *fakeSupervised) Stop()    { f.stops.Add(1) }

// TestManagerStartsAndStopsEverySource is the behavioral pin: Start must connect
// every source exactly once and Stop must stop every source exactly once. Without
// it, deleting the Connect/Stop call in the Manager's loop leaves the suite green
// while the service connects nothing (or never announces OFFLINE on shutdown).
func TestManagerStartsAndStopsEverySource(t *testing.T) {
	fakes := []*fakeSupervised{{}, {}, {}}
	sup := make([]supervised, len(fakes))
	for i, f := range fakes {
		sup[i] = f
	}
	m := &Manager{clients: sup}

	m.Start()
	m.Stop()

	for i, f := range fakes {
		assert.Equal(t, int32(1), f.connects.Load(), "source %d must be connected once", i)
		assert.Equal(t, int32(1), f.stops.Load(), "source %d must be stopped once", i)
	}
}

// TestManagerEmptyIsInertAndSafe pins the zero-source path: an empty configuration
// is valid (a profile may render sparkplug-ingest before any tenant enables it), so
// Start and Stop over no clients must be safe no-ops that return promptly rather
// than blocking or panicking.
func TestManagerEmptyIsInertAndSafe(t *testing.T) {
	m := NewManager(nil)
	m.Start() // no clients: nothing to connect

	done := make(chan struct{})
	go func() { m.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop over an empty Manager must return promptly")
	}
}
