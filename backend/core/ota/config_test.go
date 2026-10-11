// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ota

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGoldenConfigFixtures(t *testing.T) {
	valid, err := filepath.Glob("testdata/config/valid/*.json")
	require.NoError(t, err)
	kinds := map[string]bool{}
	statuses := map[string]bool{}
	for _, f := range valid {
		b, err := os.ReadFile(f)
		require.NoError(t, err, f)
		r, err := DecodeDeviceReport(b)
		require.NoError(t, err, f)
		kinds[r.Kind] = true
		if r.ConfigStatus != nil {
			statuses[r.ConfigStatus.Status] = true
		}
	}
	assert.Equal(t, map[string]bool{KindSync: true, KindConfigStatus: true}, kinds)
	assert.Equal(t, map[string]bool{ConfigReceived: true, ConfigApplied: true, ConfigRejected: true}, statuses)

	invalid, err := filepath.Glob("testdata/config/invalid/*.json")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(invalid), 10)
	for _, f := range invalid {
		b, err := os.ReadFile(f)
		require.NoError(t, err, f)
		want, err := os.ReadFile(strings.TrimSuffix(f, ".json") + ".err")
		require.NoError(t, err, "every invalid fixture needs a sibling .err file: %s", f)
		_, err = DecodeDeviceReport(b)
		if assert.Error(t, err, f) {
			assert.Contains(t, err.Error(), strings.TrimSpace(string(want)), f)
		}
	}
}

// The desired message is the one thing the platform ENCODES, so its golden bytes are pinned:
// a device in any language parses exactly this, and hashes exactly the document string.
func TestDesiredFixtureIsWhatTheEncoderProduces(t *testing.T) {
	want, err := os.ReadFile("testdata/config/desired/config-revision.json")
	require.NoError(t, err)
	c, err := DecodeConfigRevision(want)
	require.NoError(t, err)
	assert.True(t, c.DigestMatches())
	got, err := EncodeConfigRevision(c.Revision, c.Digest, c.Document)
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got))
}

func TestEncodeConfigRevisionRefusesWhatADeviceCouldNotTrust(t *testing.T) {
	b, err := os.ReadFile("testdata/config/desired/config-revision.json")
	require.NoError(t, err)
	c, err := DecodeConfigRevision(b)
	require.NoError(t, err)

	_, err = EncodeConfigRevision(c.Revision, c.Digest, `{"sampleIntervalSeconds":31}`)
	assert.ErrorContains(t, err, "does not hash to its digest")
	_, err = EncodeConfigRevision(0, c.Digest, c.Document)
	assert.ErrorContains(t, err, "out of range")
	_, err = EncodeConfigRevision(c.Revision, "sha256:xyz", c.Document)
	assert.Error(t, err)
	_, err = EncodeConfigRevision(c.Revision, c.Digest, "")
	assert.ErrorContains(t, err, "document is required")
}

func TestDecodeConfigRevisionIsStrict(t *testing.T) {
	for name, body := range map[string]string{
		"unknown field": `{"v":1,"kind":"config.revision","revision":1,"digest":"sha256:` + strings.Repeat("a", 64) + `","document":"{}","extra":1}`,
		"embedded object instead of a string": `{"v":1,"kind":"config.revision","revision":1,"digest":"sha256:` +
			strings.Repeat("a", 64) + `","document":{}}`,
		"wrong kind": `{"v":1,"kind":"sync","revision":1,"digest":"sha256:` + strings.Repeat("a", 64) + `","document":"{}"}`,
		"trailing":   `{"v":1,"kind":"config.revision","revision":1,"digest":"sha256:` + strings.Repeat("a", 64) + `","document":"{}"} {}`,
	} {
		_, err := DecodeConfigRevision([]byte(body))
		assert.Error(t, err, name)
	}
	_, err := DecodeConfigRevision([]byte(strings.Repeat(" ", MaxDesiredBytes+1)))
	assert.ErrorContains(t, err, "exceeds")
}

func TestDecodeDeviceReportBounds(t *testing.T) {
	good, err := os.ReadFile("testdata/config/valid/status-applied.json")
	require.NoError(t, err)
	pad := strings.Repeat(" ", MaxReportBytes-len(good))
	_, err = DecodeDeviceReport(append(append([]byte{}, good...), pad...))
	assert.NoError(t, err, "exactly at the cap")
	_, err = DecodeDeviceReport(append(append([]byte{}, good...), pad+" "...))
	assert.ErrorContains(t, err, "exceeds")

	_, err = DecodeDeviceReport([]byte(`{"v":1,"kind":"nope"}`))
	assert.ErrorContains(t, err, "unknown report kind")

	// 17 errors is one too many; 16 of 256 bytes is the largest accepted list.
	errs := make([]string, 17)
	for i := range errs {
		errs[i] = "e"
	}
	c := ConfigStatusReport{V: 1, Kind: KindConfigStatus, Revision: 1, Digest: "sha256:" + strings.Repeat("a", 64),
		Status: ConfigRejected, Errors: errs}
	assert.ErrorContains(t, c.Validate(), "exceed")
	c.Errors = errs[:16]
	c.Errors[0] = strings.Repeat("x", 256)
	assert.NoError(t, c.Validate())
	c.Errors[0] = strings.Repeat("x", 257)
	assert.Error(t, c.Validate())
	c.Errors[0] = "bad\x00"
	assert.Error(t, c.Validate())
}

// An update report may not smuggle configuration fields, and a configuration report may not
// carry update ones: the kinds do not blend.
func TestKindsDoNotBlend(t *testing.T) {
	_, err := DecodeDeviceReport([]byte(`{"v":1,"kind":"update.inventory","component":"firmware","bootId":"b",` +
		`"running":{"version":"1"},"config":{"haveRevision":1}}`))
	assert.Error(t, err)
	_, err = DecodeReport([]byte(`{"v":1,"kind":"update.inventory","component":"firmware","bootId":"b",` +
		`"running":{"version":"1"},"revision":2}`))
	assert.Error(t, err)
}
