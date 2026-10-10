// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixtureDir is the contract package's golden reports: the cross-language contract. The updater
// does not import that package's types to PRODUCE reports, so this is what ties the two together.
var fixtureDir = filepath.Join("..", "..", "..", "core", "ota", "testdata", "reports", "valid")

func validFixtures(t *testing.T) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(fixtureDir)
	require.NoError(t, err, "the contract's golden fixtures must be reachable")
	out := map[string][]byte{}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			raw, err := os.ReadFile(filepath.Join(fixtureDir, e.Name()))
			require.NoError(t, err)
			out[e.Name()] = bytes.TrimSpace(raw)
		}
	}
	// A directory that quietly emptied would make the loops below pass over nothing.
	require.GreaterOrEqual(t, len(out), 10, "expected the contract's ten valid fixtures")
	return out
}

// Every golden fixture decodes strictly into the updater's own struct and re-encodes to the same
// bytes: the updater can produce each message the contract lists as valid, and its struct has no
// field the fixtures lack nor lacks one they carry.
func TestOwnStructsReproduceEveryValidFixtureByteForByte(t *testing.T) {
	for name, raw := range validFixtures(t) {
		t.Run(name, func(t *testing.T) {
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			var w wireReport
			require.NoError(t, dec.Decode(&w), "the updater's struct must know every field the fixture carries")
			out, err := w.encode()
			require.NoError(t, err)
			assert.Equal(t, string(raw), string(out))

			_, err = ota.DecodeReport(out)
			assert.NoError(t, err, "and the contract accepts what the updater encodes")
		})
	}
}

// The wire vocabulary the updater uses is exactly the contract's. A stage the contract renamed
// would otherwise only show up as a rejected report at run time.
func TestStageLiteralsMatchTheContract(t *testing.T) {
	for _, s := range []string{stageReceived, stageDownloading, stageDownloaded, stageVerified,
		stageInstalling, stageRebooting, stageRunning, stageFailed, stageAbandoned} {
		assert.True(t, ota.ReportStage(s).Valid(), "%q is not a contract stage", s)
	}
	assert.Equal(t, ota.KindProgress, kindProgress)
	assert.Equal(t, ota.KindInventory, kindInventory)
	assert.Equal(t, ota.ReportVersion, wireVersion)
}

func TestTruncateDetailKeepsTheContractBound(t *testing.T) {
	long := strings.Repeat("é", 400) // 800 bytes, two bytes a character
	got := truncateDetail(long)
	assert.LessOrEqual(t, len(got), maxDetailBytes)
	assert.True(t, strings.HasPrefix(long, got), "cut on a character boundary")
	assert.Equal(t, "short", truncateDetail("short"))

	w := wireReport{V: 1, Kind: kindProgress, AttemptID: "att-1", AssignmentID: "asg-1", Component: "firmware",
		ArtifactDigest: "sha256:" + strings.Repeat("a", 64), Seq: 1, Stage: stageFailed,
		Result: &wireResult{Code: "X", Detail: got}, BootID: "boot-1"}
	raw, err := w.encode()
	require.NoError(t, err)
	_, err = ota.DecodeReport(raw)
	assert.NoError(t, err)
}
