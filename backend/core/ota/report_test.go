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

func TestGoldenReportFixtures(t *testing.T) {
	valid, err := filepath.Glob("testdata/reports/valid/*.json")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(valid), 10, "one fixture per stage plus inventory")
	stages := map[ReportStage]bool{}
	for _, f := range valid {
		b, err := os.ReadFile(f)
		require.NoError(t, err, f)
		r, err := DecodeReport(b)
		assert.NoError(t, err, f)
		stages[r.Stage] = true
	}
	for _, st := range allStages {
		assert.True(t, stages[st], "no valid fixture for stage %s", st)
	}
	assert.True(t, stages[""], "no valid inventory fixture")

	invalid, err := filepath.Glob("testdata/reports/invalid/*.json")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(invalid), 13)
	for _, f := range invalid {
		b, err := os.ReadFile(f)
		require.NoError(t, err, f)
		want, err := os.ReadFile(strings.TrimSuffix(f, ".json") + ".err")
		require.NoError(t, err, "every invalid fixture needs a sibling .err file: %s", f)
		_, err = DecodeReport(b)
		if assert.Error(t, err, f) {
			assert.Contains(t, err.Error(), strings.TrimSpace(string(want)), f)
		}
	}
	// The required invalid cases are present by name.
	for _, name := range []string{"unknown-field", "device-token-field", "unknown-kind", "version-2", "oversized",
		"bad-digest-uppercase", "detail-513", "failed-without-result", "running-without-running",
		"inventory-with-seq", "bytes-over-total"} {
		_, err := os.Stat("testdata/reports/invalid/" + name + ".json")
		assert.NoError(t, err, name)
	}
}

func TestDecodeReportEdges(t *testing.T) {
	_, err := DecodeReport(nil)
	assert.Error(t, err)
	_, err = DecodeReport([]byte(`[]`))
	assert.Error(t, err)

	good, err := os.ReadFile("testdata/reports/valid/running.json")
	require.NoError(t, err)

	// A report exactly at the cap decodes; one byte over does not.
	pad := strings.Repeat(" ", MaxReportBytes-len(good))
	_, err = DecodeReport(append(append([]byte{}, good...), pad...))
	assert.NoError(t, err)
	_, err = DecodeReport(append(append([]byte{}, good...), pad+" "...))
	assert.Error(t, err)

	r, err := DecodeReport(good)
	require.NoError(t, err)
	mut := func(f func(*Report)) error { c := r.cloneForTest(); f(&c); return c.Validate() }
	assert.NoError(t, r.Validate())
	assert.Error(t, mut(func(c *Report) { c.Seq = 0 }))
	assert.Error(t, mut(func(c *Report) { c.Stage = "" }))
	assert.Error(t, mut(func(c *Report) { c.Stage = "BOGUS" }))
	assert.Error(t, mut(func(c *Report) { c.BootID = "" }))
	assert.Error(t, mut(func(c *Report) { c.Component = "Firmware" }))
	assert.Error(t, mut(func(c *Report) { c.AttemptID = "a.b" }))
	assert.Error(t, mut(func(c *Report) { c.Result = &Result{Code: "X"} }), "result outside FAILED")
	assert.Error(t, mut(func(c *Report) { c.Running.Digest = "sha256:xyz" }))
	assert.Error(t, mut(func(c *Report) { c.Running.Version = "" }))
	assert.Error(t, mut(func(c *Report) { c.Stage = StageVerified }), "running outside RUNNING")
	assert.Error(t, mut(func(c *Report) { c.Progress = &Progress{Bytes: -1} }))
	assert.NoError(t, mut(func(c *Report) { c.Progress = &Progress{Bytes: 5, Total: 0} }), "unknown total")

	failed := Report{V: 1, Kind: KindProgress, AttemptID: "a", AssignmentID: "b", Component: "firmware",
		ArtifactDigest: digestA, Seq: 1, Stage: StageFailed, BootID: "x", Result: &Result{Code: "bad code", Detail: "d"}}
	assert.Error(t, failed.Validate(), "lowercase/space in code")
	failed.Result = &Result{Code: "OK_CODE", Detail: "\xff\xfe"}
	assert.Error(t, failed.Validate(), "detail must be UTF-8")
	failed.Result.Detail = strings.Repeat("é", 256) // 512 bytes exactly
	assert.NoError(t, failed.Validate())
}
