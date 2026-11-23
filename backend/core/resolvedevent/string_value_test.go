// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package resolvedevent

import (
	"strings"
	"testing"
)

func TestStorableStringValue(t *testing.T) {
	for _, ok := range []string{"", "heating", strings.Repeat("a", MaxStringValueBytes)} {
		if !StorableStringValue(ok) {
			t.Errorf("%d-byte value should be storable", len(ok))
		}
	}
	for name, bad := range map[string]string{
		"over the bound": strings.Repeat("a", MaxStringValueBytes+1),
		"a NUL byte":     "a\x00b",
		"invalid UTF-8":  "a\xffb",
	} {
		if StorableStringValue(bad) {
			t.Errorf("a value with %s should be refused", name)
		}
	}
}
