// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tenantpurge

import (
	"fmt"

	"github.com/devicechain-io/dc-microservice/streams"
)

// DetectSingletonPartition is the legacy group-zero checkpoint identity.
const DetectSingletonPartition = "singleton"

// DetectGroupPartition names the checkpoint owned by a resolved-events group.
func DetectGroupPartition(group int) string {
	if group == 0 {
		return DetectSingletonPartition
	}
	return fmt.Sprintf("%s-g%d", streams.ResolvedEvents, group)
}

// DetectTopologyTableQuery counts the catalog rows naming event-processing's stream
// topology table. pg_tables, not information_schema: the latter hides tables the current
// role holds no privilege on, which would read as absent.
const DetectTopologyTableQuery = `SELECT count(*) FROM pg_tables WHERE schemaname = 'event-processing' AND tablename = 'detect_stream_topology'`

// DetectTopologyGroupsQuery reads the group count event-processing recorded when it first
// ran with resolved-events sharded. The row exists only on an activated sharded instance.
const DetectTopologyGroupsQuery = `SELECT group_count FROM "event-processing"."detect_stream_topology" WHERE id = 'singleton'`
