// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import "github.com/devicechain-io/dc-event-processing/model"

// Membership is worker-local even when the durable projections are process-owned.
// Include active versions without roster entries: a later never-reported device
// must resolve its profile immediately on its owner's loop.
func (rp *ResolvedEventsProcessor) ownedMembership(rosters []model.DeviceRoster, actives []model.ProfileActive) ([]model.DeviceRoster, []model.ProfileActive, error) {
	if rp.ruleRouting == nil {
		return rosters, actives, nil
	}
	ownedRosters := make([]model.DeviceRoster, 0, len(rosters))
	for _, row := range rosters {
		owner, err := rp.ruleWorker(row.Tenant)
		if err != nil {
			return nil, nil, err
		}
		if owner == rp {
			ownedRosters = append(ownedRosters, row)
		}
	}
	ownedActives := make([]model.ProfileActive, 0, len(actives))
	for _, row := range actives {
		owner, err := rp.ruleWorker(row.Tenant)
		if err != nil {
			return nil, nil, err
		}
		if owner == rp {
			ownedActives = append(ownedActives, row)
		}
	}
	return ownedRosters, ownedActives, nil
}
