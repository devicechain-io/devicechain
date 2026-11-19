// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	epconfig "github.com/devicechain-io/dc-event-processing/config"
	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
)

func configuredWorkerGroups(ms *core.Microservice) int {
	if ms != nil {
		c := ms.InstanceConfiguration.Infrastructure.Nats.ShardGroups
		if c != nil {
			for _, suffix := range c.Suffixes {
				if suffix == streams.ResolvedEvents {
					return c.Count
				}
			}
		}
	}
	return 1
}

func (rp *ResolvedEventsProcessor) workerGroups() int {
	if rp.worker != nil && rp.worker.groups > 0 {
		return rp.worker.groups
	}
	return configuredWorkerGroups(rp.Microservice)
}

func (rp *ResolvedEventsProcessor) validateWorkerResources() error {
	return epconfig.ValidateDetectWorkers(rp.workerGroups(), rp.cfg.Shards)
}

func (rp *ResolvedEventsProcessor) shardQueueDepth() int {
	return detectcore.ShardQueueDepth / max(1, rp.workerGroups())
}
