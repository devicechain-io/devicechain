// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import "time"

// DeviceGate is command-delivery's record of whether one device is disabled.
//
// A disabled device is cut off from the platform on purpose, and a command already queued for
// it must not be actuated the moment it is shut out: such commands are HELD until the device is
// enabled again (or lapse by their TTL meanwhile). The authority on "disabled" is
// device-management; this table is the copy command-delivery dispatches against, kept by the
// device-roster fact (which names the state on every roster change) and corrected by a periodic
// walk of device-management's own list (see processor.GateSync).
//
// 🔴 IT IS A TABLE, NOT A PER-POD SET, AND THE REASON IS HOW THE FACT IS CONSUMED. The roster
// stream is read by one durable shared across replicas, so each fact reaches ONE pod. A set held
// in memory would be correct on the pod that heard the fact and silently wrong on every other,
// and the claim (MarkSent) that is the single choke point of every dispatch path would have
// nothing shared to consult.
//
// Rows are never deleted: Disabled=false is the tombstone that lets the corrective walk tell "a
// fact already enabled this device" from "this device was never recorded", so a walk that read
// device-management before an enable cannot re-disable the device afterwards.
type DeviceGate struct {
	ID uint `gorm:"primarykey"`

	TenantId    string `gorm:"not null;size:128;uniqueIndex:uix_device_gates_tenant_device,priority:1"`
	DeviceToken string `gorm:"not null;size:128;uniqueIndex:uix_device_gates_tenant_device,priority:2"`

	// Disabled is true while the device is disabled.
	Disabled bool `gorm:"not null"`

	// UpdatedAt is when this row last changed state. The corrective walk only touches a row older
	// than the instant it started reading, so a fact that landed during the walk always wins.
	UpdatedAt time.Time
}
