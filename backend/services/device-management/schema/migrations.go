// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
)

var (
	Migrations = []*gormigrate.Migration{
		NewInitialSchema(),
		NewMetricDefinitionSchema(),
		NewEntityAttributeSchema(),
		NewProvisioningProfileSchema(),
		NewDeviceClaimSchema(),
		NewCommandDefinitionSchema(),
		NewAlarmDefinitionSchema(),
		NewAlarmSchema(),
		NewDeviceProfileSchema(),
		NewDefinitionsToProfileSchema(),
		NewProfileVersioningSchema(),
		NewFacetIndexSchema(),
		NewRelationshipTargetTokenSchema(),
	}
)
