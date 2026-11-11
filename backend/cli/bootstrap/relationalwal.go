// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import "fmt"

// ValidateRelationalStoreWAL checks the optional install choice before cluster writes.
// The cluster root additionally limits the target to a quarter of the data volume.
func ValidateRelationalStoreWAL(value *int) error {
	if value != nil && *value != 0 && *value < 64 {
		return fmt.Errorf("--relational-store-max-wal-mib must be 0 (stock default) or at least 64 MiB (got %d)", *value)
	}
	return nil
}

// restoreRelationalStoreWAL prefers this run's explicit choice, then the choice
// recorded before the previous apply (even if it failed), then the completed install.
// Records without this additive field keep the old root behavior. Do not infer a
// value from a performance preset: its recommendation is for the operator to choose.
func restoreRelationalStoreWAL(st *State, prev *InstallRecord) error {
	if st.RelationalStoreMaxWALMiB == nil && prev != nil && prev.Schema == installRecordSchema {
		st.RelationalStoreMaxWALMiB = prev.Settings.RelationalStoreMaxWALMiB
		if st.RelationalStoreMaxWALMiB == nil {
			if last := prev.lastCompleted(); last != nil {
				st.RelationalStoreMaxWALMiB = last.Settings.RelationalStoreMaxWALMiB
			}
		}
	}
	return ValidateRelationalStoreWAL(st.RelationalStoreMaxWALMiB)
}
