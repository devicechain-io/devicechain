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
