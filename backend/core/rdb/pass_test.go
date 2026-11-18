// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"errors"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
)

func TestSingletonResult(t *testing.T) {
	failure := errors.New("failure")
	partial := fmt.Errorf("some work: %w", core.ErrPassPartial)
	for _, tc := range []struct {
		name      string
		ran       bool
		err, want error
		text      string
	}{
		{"skip", false, nil, core.ErrPassSkipped, "pass skipped"},
		{"acquire failure", false, failure, failure, "acquiring the example lock: failure"},
		{"success", true, nil, nil, ""},
		{"callback failure", true, failure, failure, "failure"},
		{"partial", true, partial, partial, partial.Error()},
		{"callback skip", true, core.ErrPassSkipped, core.ErrPassSkipped, "pass skipped"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := SingletonResult(tc.ran, tc.err, "example")
			if !errors.Is(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			if got != nil && got.Error() != tc.text {
				t.Fatalf("got %q, want %q", got, tc.text)
			}
			if tc.ran && got != tc.err {
				t.Fatalf("callback error identity lost: %v", got)
			}
		})
	}
}
