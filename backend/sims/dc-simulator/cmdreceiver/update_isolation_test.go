// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmdreceiver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/devicechain-io/dc-simulator/internal/platformtest"
	"github.com/devicechain-io/dc-simulator/updater"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// publishRecorder is a client whose only behaviour is to record what is published, which is all
// the receiver's response path asks of it.
type publishRecorder struct {
	*fakeClient
	mu   sync.Mutex
	sent [][]byte
}

func (p *publishRecorder) Publish(_ string, _ byte, _ bool, payload interface{}) mqtt.Token {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, append([]byte(nil), payload.([]byte)...))
	return &fakeToken{completes: true}
}

type memImage []byte

func digestHex(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (m memImage) ReadAt(p []byte, off int64) (int, error) { return copy(p, m[off:]), nil }

// A command the device answers SUCCESSFUL, while an update is in flight, must never move the
// update. The real receiver answers a real command envelope; the answer is then handed to the
// platform's update intake as the worst case (a misrouted frame), and the update is shown to be
// exactly where it was. The update only reaches UPDATED through its own confirmation.
func TestCommandSuccessDoesNotMoveAttempt(t *testing.T) {
	const chunk = 1000
	img := make(memImage, 6*chunk)
	for i := range img {
		img[i] = byte(i)
	}
	clock := platformtest.NewClock(time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC))
	dig := digestHex(img)
	att, err := ota.NewAttempt("att-1", "asg-1", "firmware", ota.Target{Version: "2.0.0", Digest: dig, RequiresReboot: true}, clock.Now())
	require.NoError(t, err)
	pol := ota.Policy{Acknowledge: time.Minute, Download: time.Minute, Verify: time.Minute, Install: time.Minute, Confirm: time.Minute}
	plat := platformtest.New(clock, pol, att)
	link := platformtest.NewLink(plat)

	up, err := updater.Open(updater.Config{
		StatePath: filepath.Join(t.TempDir(), "state.json"), Component: "firmware",
		Transport: link, Source: img, ChunkSize: chunk, InitialVersion: "1.0.0",
	})
	require.NoError(t, err)
	require.NoError(t, up.Assign(updater.Assignment{
		AttemptID: "att-1", AssignmentID: "asg-1", Component: "firmware",
		ArtifactDigest: dig, Version: "2.0.0", Size: int64(len(img)), RequiresReboot: true,
	}))
	for i := 0; i < 4; i++ { // RECEIVED + 3 chunks: the update is mid-download
		_, err := up.Step(context.Background())
		require.NoError(t, err)
	}
	before := plat.Attempt()
	require.Equal(t, ota.StateDownloading, before.State)

	// The device's command receiver answers a command.
	r := New("inst-1", "acme", "tcp://x:1883", nil)
	ds := r.newTestDevice("dev-1")
	rec := &publishRecorder{fakeClient: newFakeClient()}
	ds.client = rec
	token, nonce, ok := r.recordFrame(ds, frame(t, "cmd-1", "dev-1", "reboot-sensor"))
	require.True(t, ok)
	r.respond(ds, token, nonce)
	require.Len(t, rec.sent, 1, "the receiver answered SUCCESSFUL")
	assert.Equal(t, 1, r.Report().Devices["dev-1"].Responded)

	// Normal routing: the answer goes to command-delivery, so the update has not been touched.
	assert.Equal(t, before, plat.Attempt())

	// Worst case: the answer lands in the update intake. It is not an update report.
	_, err = plat.Receive(rec.sent[0])
	require.Error(t, err)
	assert.Equal(t, 1, plat.Rejected())
	assert.Equal(t, before, plat.Attempt(), "a command acknowledgement cannot move an update")

	// And the update still needs its own evidence to finish.
	_, done, err := up.Run(context.Background(), 100)
	require.NoError(t, err)
	require.True(t, done)
	assert.Equal(t, ota.StateUpdated, plat.Attempt().State)
}
