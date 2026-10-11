// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ota

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGoldenDownloadRequestFixtures(t *testing.T) {
	valid, err := filepath.Glob("testdata/update/valid/*.json")
	require.NoError(t, err)
	require.NotEmpty(t, valid)
	for _, f := range valid {
		b, err := os.ReadFile(f)
		require.NoError(t, err, f)
		r, err := DecodeDeviceReport(b)
		require.NoError(t, err, f)
		require.NotNil(t, r.DownloadRequest, f)
		assert.Equal(t, KindDownloadRequest, r.Kind)
		assert.Nil(t, r.Update)
		assert.Nil(t, r.Sync)
		assert.Nil(t, r.ConfigStatus)
	}
	invalid, err := filepath.Glob("testdata/update/invalid/*.json")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(invalid), 5)
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

// The three down messages are the things the platform ENCODES, so their golden bytes are pinned: a
// device in any language parses exactly these.
func TestDesiredUpdateFixturesAreWhatTheEncodersProduce(t *testing.T) {
	read := func(name string) []byte {
		b, err := os.ReadFile("testdata/update/desired/" + name)
		require.NoError(t, err)
		return b
	}

	want := read("update-assignment.json")
	m, err := DecodeDesiredUpdate(want)
	require.NoError(t, err)
	require.NotNil(t, m.Assignment)
	got, err := EncodeUpdateAssignment(m.Assignment.AttemptID, m.Assignment.AssignmentID, m.Assignment.Component, m.Assignment.Artifact)
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got))

	want = read("download-grant.json")
	m, err = DecodeDesiredUpdate(want)
	require.NoError(t, err)
	require.NotNil(t, m.Grant)
	exp, err := m.Grant.Expiry()
	require.NoError(t, err)
	got, err = EncodeDownloadGrant(m.Grant.AssignmentID, m.Grant.URL, exp)
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got))

	want = read("update-cancel.json")
	m, err = DecodeDesiredUpdate(want)
	require.NoError(t, err)
	require.NotNil(t, m.Cancel)
	got, err = EncodeUpdateCancel(m.Cancel.AttemptID, m.Cancel.AssignmentID)
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got))
}

func TestEncodersRefuseWhatADeviceCouldNotTrust(t *testing.T) {
	art := AssignmentArtifact{Version: "1", Digest: "sha256:" + strings.Repeat("a", 64), Size: 10}
	_, err := EncodeUpdateAssignment("att-1", "asg-1", "firmware", art)
	require.NoError(t, err)

	bad := art
	bad.Digest = "sha256:xyz"
	_, err = EncodeUpdateAssignment("att-1", "asg-1", "firmware", bad)
	assert.Error(t, err)
	bad = art
	bad.Size = 0
	_, err = EncodeUpdateAssignment("att-1", "asg-1", "firmware", bad)
	assert.ErrorContains(t, err, "size")
	_, err = EncodeUpdateAssignment("att 1", "asg-1", "firmware", art)
	assert.Error(t, err)
	_, err = EncodeUpdateAssignment("att-1", "asg-1", "Firmware", art)
	assert.Error(t, err)

	exp := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	for name, u := range map[string]string{
		"not absolute":   "/updates/download/x?cap=1",
		"wrong scheme":   "ftp://x.test/a",
		"no host":        "https:///a",
		"empty":          "",
		"over the bound": "https://x.test/" + strings.Repeat("a", MaxGrantURLBytes),
	} {
		_, err = EncodeDownloadGrant("asg-1", u, exp)
		assert.Error(t, err, name)
	}
	_, err = EncodeUpdateCancel("", "asg-1")
	assert.NoError(t, err, "a cancel for an assignment the platform does not hold names no attempt")
	_, err = EncodeUpdateCancel("", "")
	assert.Error(t, err)
}

func TestDecodeDesiredUpdateIsStrict(t *testing.T) {
	d := "sha256:" + strings.Repeat("a", 64)
	for name, body := range map[string]string{
		"unknown field": `{"v":1,"kind":"update.cancel","assignmentId":"asg-1","extra":1}`,
		"trailing":      `{"v":1,"kind":"update.cancel","assignmentId":"asg-1"} {}`,
		"wrong trust": `{"v":1,"kind":"update.assignment","attemptId":"a","assignmentId":"b","component":"firmware",` +
			`"trust":"VERIFIED","artifact":{"version":"1","digest":"` + d + `","size":1,"requiresReboot":false}}`,
		"artifact extra field": `{"v":1,"kind":"update.assignment","attemptId":"a","assignmentId":"b","component":"firmware",` +
			`"trust":"UNVERIFIED","artifact":{"version":"1","digest":"` + d + `","size":1,"requiresReboot":false,"x":1}}`,
		"a config kind": `{"v":1,"kind":"config.revision","revision":1,"digest":"` + d + `","document":"{}"}`,
		"bad expiry":    `{"v":1,"kind":"download.grant","assignmentId":"a","url":"https://x.test/","expiresAt":"tomorrow"}`,
		"duplicate key": `{"v":1,"kind":"update.cancel","assignmentId":"a","assignmentId":"b"}`,
	} {
		_, err := DecodeDesiredUpdate([]byte(body))
		assert.Error(t, err, name)
	}
	_, err := DecodeDesiredUpdate([]byte(strings.Repeat(" ", MaxAssignmentBytes+1)))
	assert.ErrorContains(t, err, "exceeds")
}

// The kinds do not blend: an update report may not carry a grant's fields, and a download
// request may not carry an update's.
func TestDownloadKindsDoNotBlend(t *testing.T) {
	_, err := DecodeDeviceReport([]byte(`{"v":1,"kind":"update.inventory","component":"firmware","bootId":"b",` +
		`"running":{"version":"1"},"url":"https://x.test/"}`))
	assert.Error(t, err)
	_, err = DecodeDeviceReport([]byte(`{"v":1,"kind":"download.request","assignmentId":"a","attemptId":"b"}`))
	assert.Error(t, err)
	// A down kind is not an up kind.
	_, err = DecodeDeviceReport([]byte(`{"v":1,"kind":"download.grant","assignmentId":"a","url":"https://x.test/","expiresAt":"2026-10-11T12:00:00Z"}`))
	assert.ErrorContains(t, err, "unknown report kind")
}
