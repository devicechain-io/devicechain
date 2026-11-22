// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package manageropts

import (
	"reflect"
	"testing"
)

func TestExecutorControllersExcludeObserver(t *testing.T) {
	if got := ControllersFor(true); !reflect.DeepEqual(got, []string{"executor"}) {
		t.Fatalf("executor controllers=%v", got)
	}
	if got := ControllersFor(false); !reflect.DeepEqual(got, []string{"instance", "snapshot-retention"}) {
		t.Fatalf("observer controllers=%v", got)
	}
}
