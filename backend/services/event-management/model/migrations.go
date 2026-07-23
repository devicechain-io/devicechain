// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
)

var (
	Migrations = []*gormigrate.Migration{
		NewInitialSchema(),
		NewAltIdIdempotencyIndex(),
		NewMeasurementAggregationIndex(),
		NewEventAnchorsTable(),
		NewMeasurementRollupAggregate(),
		NewMeasurementBindingColumns(),
		NewStateChangeEventsTable(),
	}
)
