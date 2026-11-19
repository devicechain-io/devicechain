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

// The DETECT stream topology table: event-processing records in it the group count it
// activated resolved-events sharding with. The row exists only on an activated sharded
// instance, and it holds no tenant data.
const (
	DetectTopologySchema = "event-processing"
	DetectTopologyTable  = "detect_stream_topology"
	// DetectTopologyQualifiedTable is the schema-qualified name; a query builder quotes each part.
	DetectTopologyQualifiedTable = DetectTopologySchema + "." + DetectTopologyTable
)
