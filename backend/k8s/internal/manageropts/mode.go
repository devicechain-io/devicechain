// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package manageropts

// ControllersFor is consumed by startup so executor mode cannot acquire observer writers.
func ControllersFor(executor bool) []string {
	if executor {
		return []string{"executor"}
	}
	return []string{"instance", "snapshot-retention"}
}
