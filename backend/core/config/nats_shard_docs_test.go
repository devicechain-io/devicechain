// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/stretchr/testify/require"
)

// Keep the published placementTags guidance in every locale consistent with validation.
// The shared range notation lets this check avoid depending on translated prose.
//
// The guidance is the v1.6 upgrade note that introduced shard groups. From v1.9 the living
// releases page keeps v1.6 only as a stub under Older releases and links to the 1.6 docs
// snapshot, so the snapshot is where an operator reads the range, and where it is held here.
func TestNatsShardGroupsDocumentedPlacementRange(t *testing.T) {
	docs := filepath.Join("..", "..", "..", "docs")
	for _, page := range []string{
		"versioned_docs/version-1.6/deployment/releases-and-upgrades.md",
		"i18n/es/docusaurus-plugin-content-docs/version-1.6/deployment/releases-and-upgrades.md",
		"i18n/zh-CN/docusaurus-plugin-content-docs/version-1.6/deployment/releases-and-upgrades.md",
	} {
		t.Run(page, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(docs, filepath.FromSlash(page)))
			require.NoError(t, err)
			ranges := regexp.MustCompile("`placementTags`[^\n]*`([0-9]+)\\.\\.count-1`").FindAllSubmatch(data, -1)
			require.NotEmpty(t, ranges, "document placementTags keys using the shared range notation")
			for _, bounds := range ranges {
				first, err := strconv.Atoi(string(bounds[1]))
				require.NoError(t, err)
				for _, count := range []int{2, 16} {
					for group := -1; group <= count; group++ {
						cfg := config.NatsShardGroupsConfiguration{Count: count, PlacementTags: map[int][]string{group: {"rack"}}}
						require.Equal(t, group >= first && group < count, cfg.Validate() == nil, "documented range differs at count=%d, group=%d", count, group)
					}
				}
			}
		})
	}
}
