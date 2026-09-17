// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"github.com/devicechain-io/dc-k8s/functionalarea"
)

// relationalAreas are the functional areas whose services open a pool on the shared
// relational store. Every other area uses the instance's event store or no database.
//
// Held against the services' own source by TestTheRelationalAreasAreTheServicesThatOpenTheRelationalStore,
// so an area that starts or stops using the store cannot leave its instance's budget
// wrong without failing the build.
var relationalAreas = map[functionalarea.FunctionalArea]bool{
	functionalarea.UserManagement:   true,
	functionalarea.DeviceManagement: true,
	functionalarea.DeviceState:      true,
	functionalarea.DashboardMgmt:    true,
	functionalarea.CommandDelivery:  true,
	functionalarea.NotificationMgmt: true,
	functionalarea.EventProcessing:  true,
	functionalarea.OutboundConn:     true,
	functionalarea.AiInference:      true,
}

const (
	// servicePoolSize mirrors defaultMaxOpenConnections in backend/core/rdb, which no
	// service overrides; held to it by the same test.
	servicePoolSize = 20
	// rolloutSurge sizes the limit for a rollout rather than the steady state: with
	// maxSurge 1 a new pod holds a full pool before the old one lets go of its own.
	rolloutSurge = 2
)

// instanceConnectionLimit is how many connections an instance's login may hold on the
// shared relational store: every relational area it runs, at a full pool, mid-rollout.
func instanceConnectionLimit(st *State) (int, error) {
	areas := make([]functionalarea.FunctionalArea, 0, len(st.EnabledAreas))
	for _, a := range st.EnabledAreas {
		areas = append(areas, functionalarea.FunctionalArea(a))
	}
	if len(areas) == 0 {
		var err error
		if areas, err = functionalarea.ResolveEnabled(st.Profile, nil); err != nil {
			return 0, err
		}
	}
	n := 0
	for _, a := range areas {
		if relationalAreas[a] {
			n++
		}
	}
	return n * servicePoolSize * rolloutSurge, nil
}
