// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package updater_test

import (
	"crypto/sha256"
	"encoding"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-simulator/updater"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A state file that cannot be trusted is refused: starting over would forget a half-applied
// update.
func TestCorruptStateIsRefusedNotReset(t *testing.T) {
	for name, content := range map[string]string{
		"garbage":       `{not json`,
		"unknown field": `{"v":1,"component":"firmware","bootSeq":1,"bootId":"boot-1","runningVersion":"1.0.0","surprise":true}`,
		"wrong version": `{"v":2,"component":"firmware","bootSeq":1,"bootId":"boot-1","runningVersion":"1.0.0"}`,
		"trailing data": `{"v":1,"component":"firmware","bootSeq":1,"bootId":"boot-1","runningVersion":"1.0.0"} {}`,
		"no boot":       `{"v":1,"component":"firmware","runningVersion":"1.0.0"}`,
		"bytes beyond size": `{"v":1,"component":"firmware","bootSeq":1,"bootId":"boot-1","runningVersion":"1.0.0",` +
			`"attempt":{"attemptId":"a","assignmentId":"b","digest":"d","version":"v","size":10,"bytesHave":11,"hashState":"AA=="}}`,
		"progress without hash": `{"v":1,"component":"firmware","bootSeq":1,"bootId":"boot-1","runningVersion":"1.0.0",` +
			`"attempt":{"attemptId":"a","assignmentId":"b","digest":"d","version":"v","size":10,"bytesHave":5}}`,
		"other component": `{"v":1,"component":"bootloader","bootSeq":1,"bootId":"boot-1","runningVersion":"1.0.0"}`,
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, true)
			require.NoError(t, os.WriteFile(r.cfg.StatePath, []byte(content), 0o600))
			_, err := updater.Open(r.cfg)
			require.Error(t, err)
			assert.ErrorIs(t, err, updater.ErrCorruptState)
		})
	}
}

// A temp file left by a crash mid-write is not state: the committed file is.
func TestLeftoverTempFileIsIgnored(t *testing.T) {
	r := newRig(t, true)
	r.assign()
	for i := 0; i < 3; i++ {
		_, err := r.up.Step(ctx)
		require.NoError(t, err)
	}
	want := r.up.Snapshot()
	require.NoError(t, os.WriteFile(r.cfg.StatePath+".tmp", []byte(strings.Repeat("torn", 4096)), 0o600))

	r.restart()
	assert.Equal(t, want, r.up.Snapshot())

	// The next save truncates the leftover rather than overlaying it: the file must still parse,
	// which a longer leftover with its tail intact would not.
	// Stop after exactly one save, so a second save cannot paper over a bad first one.
	r.up.SetFaults(updater.Faults{Crash: func(p updater.CrashPoint, _ updater.Snapshot) bool {
		return p == updater.CrashBeforeSend
	}})
	_, err := r.up.Step(ctx)
	require.ErrorIs(t, err, updater.ErrCrashed)
	r.restart()
	assert.Greater(t, r.up.Snapshot().Seq, want.Seq)
}

// The size check must be what rejects an attempt claiming more bytes than its size, so the hash
// state in the file is a real one: a placeholder would fail for a different reason first.
func TestStateClaimingMoreBytesThanSizeIsRefused(t *testing.T) {
	h := sha256.New()
	h.Write(make([]byte, 11))
	hs, err := h.(encoding.BinaryMarshaler).MarshalBinary()
	require.NoError(t, err)
	content := fmt.Sprintf(`{"v":1,"component":"firmware","bootSeq":1,"bootId":"boot-1","runningVersion":"1.0.0",`+
		`"attempt":{"attemptId":"a","assignmentId":"b","digest":"d","version":"v","size":10,"bytesHave":11,"hashState":%q}}`,
		base64.StdEncoding.EncodeToString(hs))
	r := newRig(t, true)
	require.NoError(t, os.WriteFile(r.cfg.StatePath, []byte(content), 0o600))
	_, err = updater.Open(r.cfg)
	require.ErrorIs(t, err, updater.ErrCorruptState)
	assert.ErrorContains(t, err, "inconsistent attempt")
	assert.ErrorContains(t, err, "delete the state file")
}

// A save that fails kills the instance: the report it was about to send was never persisted, and a
// device that sends what it could not record would, after a restart, contradict itself.
func TestSaveFailureKillsTheInstanceAndSendsNothing(t *testing.T) {
	r := newRig(t, true)
	r.assign()
	_, err := r.up.Step(ctx) // RECEIVED
	require.NoError(t, err)
	sent := len(r.link.Frames())

	// A directory where the temp file goes makes the next save fail.
	require.NoError(t, os.Mkdir(r.cfg.StatePath+".tmp", 0o700))
	_, err = r.up.Step(ctx)
	require.Error(t, err)
	require.NotErrorIs(t, err, updater.ErrCrashed, "the first failure reports the cause")

	require.NoError(t, os.Remove(r.cfg.StatePath+".tmp"))
	_, err = r.up.Step(ctx)
	require.ErrorIs(t, err, updater.ErrCrashed)
	assert.Equal(t, sent, len(r.link.Frames()), "a report that was never persisted is never sent")

	r.restart()
	assert.Equal(t, uint64(1), r.up.Snapshot().Seq, "the restarted device knows only what it persisted")
}

func TestFreshDeviceNeedsAnInitialVersion(t *testing.T) {
	r := newRig(t, true)
	cfg := r.cfg
	cfg.StatePath = filepath.Join(t.TempDir(), "other.json")
	cfg.InitialVersion = ""
	_, err := updater.Open(cfg)
	assert.ErrorContains(t, err, "InitialVersion")
}

func TestOpenRejectsIncompleteConfig(t *testing.T) {
	r := newRig(t, true)
	for name, mut := range map[string]func(*updater.Config){
		"no path":      func(c *updater.Config) { c.StatePath = "" },
		"no component": func(c *updater.Config) { c.Component = "" },
		"no transport": func(c *updater.Config) { c.Transport = nil },
		"no source":    func(c *updater.Config) { c.Source = nil },
		"neg chunk":    func(c *updater.Config) { c.ChunkSize = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := r.cfg
			mut(&cfg)
			_, err := updater.Open(cfg)
			assert.Error(t, err)
		})
	}
}
