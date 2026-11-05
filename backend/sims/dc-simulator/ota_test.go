// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func parseOTA(t *testing.T, args ...string) (otaFlags, *flag.FlagSet) {
	t.Helper()
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	o := defineOTAFlags(fs)
	require.NoError(t, fs.Parse(args))
	return o, fs
}

func TestOTAOffByDefault(t *testing.T) {
	o, fs := parseOTA(t)
	opts, err := o.options(fs)
	require.NoError(t, err)
	assert.Nil(t, opts)
}

// A fault flag without --ota would be accepted and do nothing: refused instead.
func TestOTAFaultFlagWithoutOTAIsRefused(t *testing.T) {
	o, fs := parseOTA(t, "--ota-bad-hash=2")
	_, err := o.options(fs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--ota-bad-hash")
}

// With --ota the devices must have a way to decide: a trusted key, or an explicit allowance for
// unsigned images. Neither would refuse every update.
func TestOTAWithoutTrustIsRefused(t *testing.T) {
	o, fs := parseOTA(t, "--ota")
	_, err := o.options(fs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--ota-trusted-key")
}

func TestOTAOptions(t *testing.T) {
	key := filepath.Join(t.TempDir(), "k.pem")
	require.NoError(t, os.WriteFile(key, []byte("PEM"), 0o600))
	o, fs := parseOTA(t, "--ota", "--ota-trusted-key", key, "--ota-version", "2.1.0", "--ota-bad-hash", "1",
		"--ota-interrupted", "2", "--ota-offline", "1", "--ota-offline-for", "5s", "--ota-step-delay", "200ms", "--ota-chunk", "1024")
	opts, err := o.options(fs)
	require.NoError(t, err)
	require.NotNil(t, opts)
	assert.Equal(t, "2.1.0", opts.InitialVersion)
	assert.Equal(t, []string{"PEM"}, opts.TrustedKeys)
	assert.False(t, opts.AllowUnsigned)
	assert.Equal(t, int64(1024), opts.ChunkSize)
	assert.Equal(t, 200*time.Millisecond, opts.StepDelay)
	assert.Equal(t, 1, opts.Faults.BadHash)
	assert.Equal(t, 2, opts.Faults.Interrupted)
	assert.Equal(t, 5*time.Second, opts.Faults.OfflineFor)
}

func TestOTAOfflineForNeedsOfflineDevices(t *testing.T) {
	o, fs := parseOTA(t, "--ota", "--ota-allow-unsigned", "--ota-offline-for", "5s")
	_, err := o.options(fs)
	require.Error(t, err)
}

func TestOTAMissingKeyFileIsRefused(t *testing.T) {
	o, fs := parseOTA(t, "--ota", "--ota-trusted-key", filepath.Join(t.TempDir(), "nope.pem"))
	_, err := o.options(fs)
	require.Error(t, err)
}
