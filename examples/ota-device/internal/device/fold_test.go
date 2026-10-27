// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package device_test

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/stretchr/testify/require"
)

// foldPlatform feeds the reports this device emitted, in order, through the platform's own attempt
// reducer, as the platform would on receipt, and returns where the attempt ends up and the verdict
// of each report. The attempt ids are the harness's first (att-1 / asg-1).
func foldPlatform(t *testing.T, rec *recorder, img image) (ota.Attempt, []ota.Verdict) {
	t.Helper()
	t0 := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	a, err := ota.NewAttempt("att-1", "asg-1", "firmware", ota.Target{Version: img.version, Digest: img.digest, RequiresReboot: true}, t0)
	require.NoError(t, err)
	var verdicts []ota.Verdict
	for i, r := range rec.progress(t) {
		var v ota.Verdict
		a, v = ota.Apply(a, r, t0.Add(time.Duration(i+1)*time.Second))
		verdicts = append(verdicts, v)
	}
	require.NotEmpty(t, verdicts)
	return a, verdicts
}

// platformDecodesEverything runs every frame through the platform's single decoder for the up
// channel, which knows the sync and the download.request as well as the reports.
func platformDecodesEverything(t *testing.T, rec *recorder) {
	t.Helper()
	kinds := map[string]bool{}
	for _, f := range rec.all() {
		d, err := ota.DecodeDeviceReport(f)
		require.NoError(t, err, "%s", f)
		kinds[d.Kind] = true
	}
	require.True(t, kinds[ota.KindInventory] && kinds[ota.KindProgress] && kinds["sync"] && kinds[ota.KindDownloadRequest], "saw %v", kinds)
}
