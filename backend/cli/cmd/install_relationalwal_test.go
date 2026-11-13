// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"strconv"
	"testing"

	"github.com/devicechain-io/dcctl/bootstrap"
)

func TestRelationalStoreWALFlagAndOptions(t *testing.T) {
	flags := installCmd.Flags()
	originalValue := installRelationalMaxWALMiB
	originalChanged := flags.Changed("relational-store-max-wal-mib")
	t.Cleanup(func() {
		installRelationalMaxWALMiB = originalValue
		flags.Lookup("relational-store-max-wal-mib").Changed = originalChanged
	})
	for _, tc := range []struct {
		name           string
		value          int
		given, invalid bool
	}{
		{"omitted", 4096, false, false},
		{"reset", 0, true, false},
		{"too small", 63, true, true},
		{"minimum", 64, true, false},
		{"tuned", 4096, true, false},
		{"negative", -1, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Set on the real install flag set is red on main by value: unknown flag.
			if err := flags.Set("relational-store-max-wal-mib", strconv.Itoa(tc.value)); err != nil {
				t.Fatal(err)
			}
			flags.Lookup("relational-store-max-wal-mib").Changed = tc.given
			opts, err := installOptions(nil, bootstrap.RestorePlan{}, bootstrap.ImageSource{})
			if (err != nil) != tc.invalid {
				t.Fatalf("options error = %v, invalid = %v", err, tc.invalid)
			}
			if tc.invalid {
				return
			}
			got := opts.RelationalStoreMaxWALMiB
			if !tc.given {
				if got != nil {
					t.Fatalf("omitted flag became %d", *got)
				}
			} else if got == nil || *got != tc.value {
				t.Fatalf("install options WAL = %v, want %d", got, tc.value)
			}
		})
	}
}
