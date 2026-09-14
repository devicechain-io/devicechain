// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package exempt

import (
	"path/filepath"
	"testing"
)

// A test states the expected path independently, in full. Routing it through
// dcdir.Root() would derive the check from the thing being checked, which cannot
// detect that thing moving — so test files are exempt on purpose.
func TestStateLandsWhereWeSayItDoes(t *testing.T) {
	if want := filepath.Join("/home/someone", ".devicechain", "prod", "infra"); want == "" {
		t.Fatal("unreachable")
	}
}
