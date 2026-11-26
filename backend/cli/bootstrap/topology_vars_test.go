// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The broker topology is dcctl's on every apply: both variables are always passed, whatever
// the operator states. This pins the documented stance (sizing: "Which value wins").
func TestTheBrokerTopologyVariablesAreAlwaysPassed(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "terraform.tfvars"), []byte("nats_cluster_replicas = 3\nha = true\n"), 0o600))
	for _, ha := range []bool{false, true} {
		st := &State{Instance: "prod", Profile: "default", Values: map[string]string{}, HA: ha}
		vars := infraVarsIn(st, dir)
		want := haFor(ha)
		require.True(t, slices.Contains(vars, fmt.Sprintf("nats_cluster_replicas=%d", want.ServerReplicas)), "ha=%v: %v", ha, vars)
	}
}

// Stating either variable is reported, and only a stated one. The negative control is the
// empty root, which names nothing.
func TestAStatedTopologyVariableIsReportedAsOverridden(t *testing.T) {
	t.Setenv("TF_VAR_ha", "")
	t.Setenv("TF_VAR_nats_cluster_replicas", "")
	dir := t.TempDir()
	require.Empty(t, overriddenTopologyVars(dir))

	require.NoError(t, os.WriteFile(filepath.Join(dir, "perf.auto.tfvars"), []byte("nats_cluster_replicas = 3\n"), 0o600))
	require.Equal(t, []string{"nats_cluster_replicas"}, overriddenTopologyVars(dir))

	t.Setenv("TF_VAR_ha", "true")
	require.Equal(t, []string{"ha", "nats_cluster_replicas"}, overriddenTopologyVars(dir))
}

// Both applies compose their variables through instanceRootVarsIn with the root they run in,
// so the note is said on bootstrap and on upgrade alike.
func TestTheOverrideNoteIsSaidWhereTheVariablesAreComposed(t *testing.T) {
	raw, err := os.ReadFile("tofu.go")
	require.NoError(t, err)
	src := string(raw)
	start := strings.Index(src, "func instanceRootVarsIn(")
	require.GreaterOrEqual(t, start, 0)
	end := strings.Index(src[start:], "\n}\n")
	require.Contains(t, src[start:start+end], "sayOverriddenTopologyVars(stdConsole, st, rootdir)")
}
