// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"devicechain.io/examples/ota-device/internal/testutil"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testutil.Cleanup()
	os.Exit(code)
}

// The version a device reports is the one the linker compiled into the binary, and `describe`
// answers it with an empty environment and no arguments beyond the word itself.
func TestDescribeReportsWhatWasCompiledIn(t *testing.T) {
	bin := testutil.Build(t, "./cmd/ota-device",
		"-X devicechain.io/examples/ota-device/internal/build.Version=7.7.7 -X devicechain.io/examples/ota-device/internal/build.Product=widget")
	cmd := exec.Command(bin, "describe")
	cmd.Env = []string{}
	out, err := cmd.Output()
	require.NoError(t, err)
	var id struct{ Product, Version string }
	require.NoError(t, json.Unmarshal(out, &id))
	assert.Equal(t, "7.7.7", id.Version)
	assert.Equal(t, "widget", id.Product)
}

func TestRefusesToRunWithoutItsConfiguration(t *testing.T) {
	bin := testutil.Build(t, "./cmd/ota-device", "")
	cmd := exec.Command(bin)
	cmd.Env = []string{}
	err := cmd.Run()
	var ee *exec.ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, 2, ee.ExitCode())
}
