// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDeployedAreasParsing(t *testing.T) {
	d, err := parseDeployedAreas("")
	require.NoError(t, err)
	require.Nil(t, d, "unset means every area")
	require.True(t, d.has("update-management"))
	require.Empty(t, d.undeployed())

	d, err = parseDeployedAreas("device-management, outbound-connectors")
	require.NoError(t, err)
	require.True(t, d.has("device-management"))
	require.False(t, d.has("update-management"))
	require.Contains(t, d.undeployed(), "update-management")

	// A typo must not quietly exclude a real area.
	_, err = parseDeployedAreas("device-managment")
	require.Error(t, err)
	_, err = parseDeployedAreas(" , ")
	require.Error(t, err, "a list that names nothing is a mistake, not an empty instance")
}

// The seed skips an undeployed area's entities by name, and the artifact entity is one of them.
func TestTheArtifactEntityBelongsToAnAreaTheListCanExclude(t *testing.T) {
	found := false
	for _, e := range allEntities() {
		if e.Name == "artifact" {
			found = true
			require.Equal(t, "update-management", e.Area)
			require.Nil(t, e.Record, "nothing references it, so skipping it strands no later entity")
		}
	}
	require.True(t, found)
}

// Declaring an area undeployed while the database holds its tables is a contradiction the
// table sweep must surface, otherwise the flag is a way to measure less and call it the same.
func TestAnUndeployedAreaWithTablesIsACoverageFinding(t *testing.T) {
	d, err := parseDeployedAreas("device-management")
	require.NoError(t, err)
	tables := []string{"device-management.devices", "update-management.artifacts", "user-management.tenants"}
	require.Equal(t, []string{"update-management.artifacts"}, tablesOfUndeployedAreas(tables, d))

	// Nothing declared undeployed: nothing to contradict.
	require.Empty(t, tablesOfUndeployedAreas(tables, nil))
	// The same table is fine when its area is deployed.
	d, err = parseDeployedAreas("device-management,update-management")
	require.NoError(t, err)
	require.Empty(t, tablesOfUndeployedAreas(tables, d))
}

func TestTheSeedSkipsAnUndeployedAreaByNameAndNeverStrandsADependent(t *testing.T) {
	d, err := parseDeployedAreas("device-management")
	require.NoError(t, err)

	artifact := entity{Name: "artifact", Area: "update-management"}
	skip, why, err := skipForUndeployedArea(artifact, d)
	require.NoError(t, err)
	require.True(t, skip)
	require.Contains(t, why, "update-management", "the skip names the area")

	// A deployed area is never skipped.
	skip, _, err = skipForUndeployedArea(entity{Name: "device", Area: "device-management"}, d)
	require.NoError(t, err)
	require.False(t, skip)
	// No list, no skipping.
	skip, _, err = skipForUndeployedArea(artifact, nil)
	require.NoError(t, err)
	require.False(t, skip)

	// Skipping an entity that later ones reference would strand them: a setup error.
	referenced := entity{Name: "x", Area: "update-management", Record: func(*state, map[string]any) {}}
	_, _, err = skipForUndeployedArea(referenced, d)
	require.Error(t, err)
	require.Equal(t, exitSetup, codeOf(err))
}

func TestTheTableSweepFailsAsCoverageWhenAnUndeployedAreaHasTables(t *testing.T) {
	d, err := parseDeployedAreas("device-management")
	require.NoError(t, err)
	err = checkUndeployedAreasHaveNoTables([]string{"device-management.devices", "update-management.artifacts"}, d)
	require.Error(t, err)
	require.Equal(t, exitCoverage, codeOf(err))
	require.Contains(t, err.Error(), "update-management.artifacts")

	require.NoError(t, checkUndeployedAreasHaveNoTables([]string{"device-management.devices"}, d))
	require.NoError(t, checkUndeployedAreasHaveNoTables([]string{"update-management.artifacts"}, nil))
}
