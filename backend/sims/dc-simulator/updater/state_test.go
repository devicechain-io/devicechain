// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package updater_test

import (
	"os"
	"path/filepath"
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
	require.NoError(t, os.WriteFile(r.cfg.StatePath+".tmp", []byte(`{torn`), 0o600))

	r.restart()
	assert.Equal(t, want, r.up.Snapshot())
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
