// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/resolvedevent"
)

// The STRING bound is one number across the seam: device-management's resolver and device-state
// apply resolvedevent.MaxStringValueBytes, and this store refuses on its own copy. If the two ever
// differ, a value one side admits the other refuses, and a refusal here dead-letters the message.
func TestTheTextBoundEqualsTheSharedStringBound(t *testing.T) {
	if MaxTextValueBytes != resolvedevent.MaxStringValueBytes {
		t.Fatalf("MaxTextValueBytes = %d, resolvedevent.MaxStringValueBytes = %d", MaxTextValueBytes, resolvedevent.MaxStringValueBytes)
	}
	for name, v := range map[string]string{
		"at the bound":   strings.Repeat("a", MaxTextValueBytes),
		"over the bound": strings.Repeat("a", MaxTextValueBytes+1),
		"a NUL byte":     "a\x00b",
		"invalid UTF-8":  "a\xffb",
	} {
		if got, want := checkTextValue("m", v) == nil, resolvedevent.StorableStringValue(v); got != want {
			t.Errorf("%s: the store admits=%v, the shared predicate says %v", name, got, want)
		}
	}
}
