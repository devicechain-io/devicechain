// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
	"slices"

	"github.com/devicechain-io/dc-k8s/functionalarea"
)

// persistenceTopology is how many event-management pods an instance runs, decided
// once and rendered into both tools that need it (haTopology is the same shape, for
// the same reason: neither tool can see the other's half):
//
//   - Helm: functionalAreas.event-management.replicas, which the chart's Deployment,
//     PodDisruptionBudget and the area's own pod spread all read;
//   - OpenTofu: event_management_replicas, by which the instance root multiplies the
//     event store's per-pod connection reserve (instance/main.tf).
//
// Raising only the first lets analytics readers take connections a rollout of the
// extra pods needs, and that failure is silent: pools open lazily, and the database
// keeps reporting healthy.
//
// Two under --ha, measured on GKE (three 4-vCPU service nodes, 3-minute runs): the
// single pod shared a node with the NATS server leading the incoming-event stream and
// with device-state, ran CPU-starved, and storing was the first stage to fall behind
// from 6,800 events/s offered; with two pods every stage held 6,800. One otherwise:
// without --ha there is one broker and no such node, and --compact targets a node that
// cannot spare a second 900m request. One, too, when the instance runs no
// event-management at all (ingest-only): there is nothing to run twice.
//
// The reserve itself is per pod: one pool, doubled for a rollout (servicePoolSize x
// rolloutSurge, the shipped timescale_analytics_reserved_connections), times this
// count. Doubled per pod rather than "plus one pod" because a two-pod rollout can hold
// four pools at once: the Deployment creates the next new pod while an old one is
// still terminating and has not yet closed its pool.
type persistenceTopology struct {
	Replicas int
}

// persistenceFor resolves the topology for an instance.
//
// An area set that cannot be resolved (an unknown profile, refused by the chart and by
// install's own validation long before this matters) counts event-management as
// deployed. That is the side that reserves more connections, not fewer.
func persistenceFor(st *State) persistenceTopology {
	if !st.HA || st.Compact {
		return persistenceTopology{Replicas: 1}
	}
	if areas, err := instanceAreas(st); err == nil && !slices.Contains(areas, functionalarea.EventManagement) {
		return persistenceTopology{Replicas: 1}
	}
	return persistenceTopology{Replicas: 2}
}

// mergeInto renders the Helm half: functionalAreas.event-management.replicas, merged
// rather than assigned (mergeFunctionalArea) so another feature's block for another
// area survives. Only replicas: Helm coalesces it over the chart's own block for the
// area, so its resources, measured requests and placement switches survive too.
func (p persistenceTopology) mergeInto(vals map[string]interface{}) {
	mergeFunctionalArea(vals, string(functionalarea.EventManagement),
		map[string]interface{}{"replicas": p.Replicas})
}

// infraVars renders the OpenTofu half. Emitted at 1 too, as haTopology.infraVars is,
// so both halves come from this value on every path, including the one that turns
// --ha back off.
func (p persistenceTopology) infraVars() []string {
	return []string{fmt.Sprintf("event_management_replicas=%d", p.Replicas)}
}
