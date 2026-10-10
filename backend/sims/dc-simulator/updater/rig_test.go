// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package updater_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/devicechain-io/dc-simulator/updater"
	"github.com/devicechain-io/dc-simulator/updater/platformtest"
	"github.com/stretchr/testify/require"
)

const (
	chunk     = 1000
	imageSize = 10 * chunk
)

var testPolicy = ota.Policy{
	Acknowledge: time.Minute, Download: 10 * time.Minute, Verify: 2 * time.Minute,
	Install: 5 * time.Minute, Confirm: 3 * time.Minute,
}

// memSource serves an image from memory and can be made to fail, like a connection that drops.
type memSource struct {
	mu   sync.Mutex
	data []byte
	err  error
	offs []int64 // every offset asked for, since the last resetReads
}

func (s *memSource) ReadAt(p []byte, off int64) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.offs = append(s.offs, off)
	if s.err != nil {
		return 0, s.err
	}
	if off >= int64(len(s.data)) {
		return 0, errors.New("read past end")
	}
	return copy(p, s.data[off:]), nil
}

func (s *memSource) resetReads() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.offs = nil
}

// minOffset is the lowest offset read since resetReads (-1 when nothing was read).
func (s *memSource) minOffset() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	lowest := int64(-1)
	for _, o := range s.offs {
		if lowest < 0 || o < lowest {
			lowest = o
		}
	}
	return lowest
}

func (s *memSource) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func image() []byte {
	b := make([]byte, imageSize)
	for i := range b {
		b[i] = byte(i*7 + 3)
	}
	return b
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// rig is one device wired to one stand-in platform.
type rig struct {
	t        *testing.T
	clock    *platformtest.Clock
	platform *platformtest.Platform
	link     *platformtest.Link
	src      *memSource
	cfg      updater.Config
	asg      updater.Assignment
	up       *updater.Updater
}

func newRig(t *testing.T, requiresReboot bool) *rig {
	t.Helper()
	img := image()
	asg := updater.Assignment{
		AttemptID: "att-1", AssignmentID: "asg-1", Component: "firmware",
		ArtifactDigest: digestOf(img), Version: "2.0.0", Size: int64(len(img)), RequiresReboot: requiresReboot,
	}
	clock := platformtest.NewClock(time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC))
	att, err := ota.NewAttempt(asg.AttemptID, asg.AssignmentID, asg.Component,
		ota.Target{Version: asg.Version, Digest: asg.ArtifactDigest, RequiresReboot: requiresReboot}, clock.Now())
	require.NoError(t, err)
	p := platformtest.New(clock, testPolicy, att)
	link := platformtest.NewLink(p)
	src := &memSource{data: img}
	r := &rig{t: t, clock: clock, platform: p, link: link, src: src, asg: asg}
	r.cfg = updater.Config{
		StatePath: filepath.Join(t.TempDir(), "updater-state.json"), Component: "firmware",
		Transport: link, Source: src, ChunkSize: chunk, InitialVersion: "1.0.0",
	}
	r.up = r.open()
	return r
}

func (r *rig) open() *updater.Updater {
	r.t.Helper()
	u, err := updater.Open(r.cfg)
	require.NoError(r.t, err)
	return u
}

// restart is a process restart: a new Updater over the same state file, with no faults.
func (r *rig) restart() { r.up = r.open() }

func (r *rig) assign() {
	r.t.Helper()
	require.NoError(r.t, r.up.Assign(r.asg))
}

// runToEnd steps until the updater is done, failing the test on any error.
func (r *rig) runToEnd() {
	r.t.Helper()
	_, done, err := r.up.Run(context.Background(), 200)
	require.NoError(r.t, err)
	require.True(r.t, done, "updater did not finish within 200 steps")
}

func (r *rig) state() ota.State { return r.platform.Attempt().State }
