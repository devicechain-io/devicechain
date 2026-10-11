// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package updater_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/devicechain-io/dc-simulator/updater"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every kind of report the contract's golden fixtures list as valid is one the updater actually
// emits when driven, and every one of those the contract accepts. The fixture directory is read
// at run time, so a fixture added to the contract without a way for the updater to produce it
// fails here.
func TestDrivenRunsEmitEveryFixtureKind(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("..", "..", "..", "core", "ota", "testdata", "reports", "valid"))
	require.NoError(t, err)
	var want []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			want = append(want, strings.TrimSuffix(e.Name(), ".json"))
		}
	}
	require.GreaterOrEqual(t, len(want), 10)

	seen := map[string]bool{}
	note := func(l interface{ Frames() [][]byte }) {
		for _, f := range l.Frames() {
			rep, err := ota.DecodeReport(f)
			require.NoError(t, err, "%s", f)
			if rep.Kind == ota.KindInventory {
				seen["inventory"] = true
			} else {
				seen[strings.ToLower(string(rep.Stage))] = true
			}
		}
	}

	// success with a reboot, then an inventory
	r := newRig(t, true)
	r.assign()
	r.runToEnd()
	require.NoError(t, r.up.ReportInventory(ctx))
	note(r.link)

	// verification failure
	f := newRig(t, true)
	f.assign()
	f.up.SetFaults(updater.Faults{FailVerification: true})
	f.runToEnd()
	note(f.link)

	// abandon
	a := newRig(t, true)
	a.assign()
	_, err = a.up.Step(ctx)
	require.NoError(t, err)
	require.NoError(t, a.platform.RequestCancel()) // QUEUED -> CANCELLED outright; the device still abandons
	require.NoError(t, a.up.Abandon(ctx))
	note(a.link)

	var got []string
	for k := range seen {
		got = append(got, k)
	}
	sort.Strings(got)
	sort.Strings(want)
	assert.Equal(t, want, got)
}
