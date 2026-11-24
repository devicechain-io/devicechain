// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestAnythingButCheckIsNotImplementedAndSaysSo(t *testing.T) {
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	require.NoError(t, err)
	defer null.Close()
	require.Equal(t, exitNotImplemented, run([]string{"run"}, null, null), "a wrapper that is not built must not exit 0")
	require.Equal(t, exitNotImplemented, run([]string{"check", "extra"}, null, null))
	require.Equal(t, exitUsage, run(nil, null, null))
}

func TestCheckRefusesAnImageThatIsNotWhatItClaims(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// No tofu at the path: refused.
	require.Error(t, check(ctx, env(map[string]string{"FLEET_RUNNER_TOFU": filepath.Join(dir, "tofu")})))
	// A binary that is not named tofu: refused before it is run.
	other := filepath.Join(dir, "terraform")
	require.NoError(t, os.WriteFile(other, []byte("x"), 0o755))
	require.Error(t, check(ctx, env(map[string]string{"FLEET_RUNNER_TOFU": other})))
	// A relative path: refused.
	require.Error(t, check(ctx, env(map[string]string{"FLEET_RUNNER_TOFU": "tofu"})))
}
