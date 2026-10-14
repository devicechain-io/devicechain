// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

// forcedStatus is the column map a test uses to place a row in a status no public transition
// reaches. A terminal status carries an outcome like any real write does (the model's write guard
// refuses one without), so the placed row is classed UNKNOWN or, for SUCCESSFUL, SUCCEEDED.
func forcedStatus(status string) map[string]any {
	updates := map[string]any{"status": status}
	switch status {
	case "SUCCESSFUL":
		updates["outcome"] = "SUCCEEDED"
	case "EXPIRED", "CANCELLED":
		updates["outcome"] = "NOT_DELIVERED"
	case "FAILED", "TIMEOUT":
		updates["outcome"] = "UNKNOWN"
	}
	return updates
}
