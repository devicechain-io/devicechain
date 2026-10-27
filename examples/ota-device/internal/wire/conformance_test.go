// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package wire_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"devicechain.io/examples/ota-device/internal/wire"
)

// The platform's golden fixtures are read by path, outside this module, so run with -count=1: the
// test cache does not track them and a cached PASS would survive a fixture edit.
var fixtures = filepath.Join("..", "..", "..", "..", "backend", "core", "ota", "testdata")

func files(t *testing.T, pattern string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(fixtures, pattern))
	require.NoError(t, err)
	require.NotEmpty(t, m, "no fixtures match %s: has the contract's testdata moved?", pattern)
	return m
}

func read(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	return b
}

// Every report the contract calls valid is one this device accepts for sending, and the platform
// accepts too: the two agree about the grammar.
func TestValidReportFixturesAreAccepted(t *testing.T) {
	for _, f := range files(t, "reports/valid/*.json") {
		b := read(t, f)
		_, err := wire.CheckOutbound(b)
		assert.NoError(t, err, filepath.Base(f))
		_, err = ota.DecodeReport(b)
		assert.NoError(t, err, "the platform's own decoder must accept %s", filepath.Base(f))
	}
}

// Every frame the contract calls invalid is refused by this device's outbound check, so a bug here
// can never put one of them on the wire.
func TestInvalidFixturesAreRefused(t *testing.T) {
	for _, pat := range []string{"reports/invalid/*.json", "update/invalid/*.json"} {
		for _, f := range files(t, pat) {
			_, err := wire.CheckOutbound(read(t, f))
			assert.Error(t, err, "%s must be refused", filepath.Base(f))
		}
	}
}

func TestOtherValidDeviceFramesAreAccepted(t *testing.T) {
	for _, f := range append(files(t, "update/valid/*.json"), files(t, "config/valid/sync*.json")...) {
		kind, err := wire.CheckOutbound(read(t, f))
		assert.NoError(t, err, filepath.Base(f))
		assert.True(t, kind == wire.KindDownloadRequest || kind == wire.KindSync, "%s: %s", filepath.Base(f), kind)
	}
}

// Every message the platform sends under the contract decodes here, to the same content the
// platform's decoder yields.
func TestDesiredFixturesDecode(t *testing.T) {
	for _, f := range files(t, "update/desired/*.json") {
		b := read(t, f)
		got, err := wire.DecodeDesired(b)
		require.NoError(t, err, filepath.Base(f))
		want, err := ota.DecodeDesiredUpdate(b)
		require.NoError(t, err, filepath.Base(f))
		assert.Equal(t, want.Kind, got.Kind)
		switch got.Kind {
		case wire.KindAssignment:
			assert.Equal(t, want.Assignment.Artifact.Version, got.Assignment.Artifact.Version)
			assert.Equal(t, want.Assignment.Signature, got.Assignment.Signature)
			assert.Equal(t, want.Assignment.KeyID, got.Assignment.KeyID)
		case wire.KindGrant:
			assert.Equal(t, want.Grant.URL, got.Grant.URL)
		case wire.KindCancel:
			assert.Equal(t, want.Cancel.AssignmentID, got.Cancel.AssignmentID)
		}
	}
	got, err := wire.DecodeDesired(read(t, files(t, "config/desired/config-revision.json")[0]))
	require.NoError(t, err)
	assert.Equal(t, wire.KindConfigRevision, got.Kind)
}

func TestDesiredRefusals(t *testing.T) {
	good := string(read(t, filepath.Join(fixtures, "update", "desired", "update-assignment.json")))
	for name, b := range map[string]string{
		"unknown field":     strings.Replace(good, `"trust"`, `"extra":1,"trust"`, 1),
		"case variant":      strings.Replace(good, `"trust"`, `"Trust"`, 1),
		"duplicate key":     strings.Replace(good, `"component":"firmware"`, `"component":"firmware","component":"x"`, 1),
		"trailing data":     good + "{}",
		"unsigned+sig":      strings.Replace(good, `"UNVERIFIED"`, `"UNVERIFIED","signature":"AAAA"`, 1),
		"zero size":         strings.Replace(good, `"size":8388608`, `"size":0`, 1),
		"not an object":     `[]`,
		"unknown kind":      `{"v":1,"kind":"update.nope"}`,
		"oversized":         good + strings.Repeat(" ", wire.MaxBytes),
		"device identity":   strings.Replace(good, `"trust"`, `"deviceToken":"d","trust"`, 1),
		"bad version":       strings.Replace(good, `"v":1`, `"v":2`, 1),
		"upper-case digest": strings.Replace(good, `"sha256:a`, `"sha256:A`, 1),
	} {
		_, err := wire.DecodeDesired([]byte(b))
		assert.Error(t, err, name)
	}
}
