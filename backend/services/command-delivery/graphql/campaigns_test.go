// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"

	"gorm.io/datatypes"

	"github.com/devicechain-io/dc-command-delivery/model"
)

// An unreadable stored step list is an error, not an empty list: a campaign with no steps is a
// plausible answer, and it would hide the fault from whoever reads the campaign.
func TestCampaignStepsFailOnAnUnreadableList(t *testing.T) {
	r := &CampaignResolver{M: model.Campaign{Steps: datatypes.JSON(`{"not":"a list"}`)}}
	if steps, err := r.Steps(); err == nil {
		t.Fatalf("an unreadable step list answered %d steps and no error", len(steps))
	}
	ok := &CampaignResolver{M: model.Campaign{Steps: datatypes.JSON(`[{"type":"COMMAND","name":"dim"}]`)}}
	steps, err := ok.Steps()
	if err != nil || len(steps) != 1 {
		t.Fatalf("a readable step list: %d steps, err %v", len(steps), err)
	}
}
