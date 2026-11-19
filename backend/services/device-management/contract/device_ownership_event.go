// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package contract

import "time"

// DeviceOwnershipChangedEvent is the device ownership-changed fact: a device's COMPLETE
// current anchor set — its tracked relationships and the access scopes it is in — read after
// the write that may have changed it. A consumer
// overwrites what it holds with it; an empty Anchors means the device is owned by nobody.
// The tenant travels on the per-tenant subject.
type DeviceOwnershipChangedEvent struct {
	DeviceIncarnation uint64
	DeviceToken       string
	Anchors           []string
	ChangedTime       time.Time
}
