// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import "github.com/devicechain-io/dc-microservice/ledger"

// fleetKind is a kind this area's engine posts.
type fleetKind string

func (k fleetKind) Name() string { return string(k) }

// Device-management is the only area that registers a kind: config.set is posted by the driver,
// settled by the reconciler and the deadline pass, and reversed by a reversal, all in this area.
// A registration refused here is a wiring defect, so the process does not start with it.
func init() {
	if err := ledger.Register(fleetKind(FleetKindConfigSet)); err != nil {
		panic(err)
	}
}
