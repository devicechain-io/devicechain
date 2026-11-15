// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

import (
	"context"
	"errors"
	"testing"
)

func TestPartialPullAnswersStillDecideTheirDevices(t *testing.T) {
	r := WithPullChannels(Empty{}, fixedPull{pull: map[string]bool{"a": false, "c": true}, err: errors.New("partial")})
	states, err := r.StatesFor(context.Background(), []string{"a", "b", "c"})
	if err == nil {
		t.Fatal("partial read lost its error")
	}
	for token, want := range map[string]Verdict{"a": Dispatch, "b": Defer, "c": Hold} {
		if got := Decide(states[token], Capabilities{}); got != want {
			t.Errorf("%s: got %v, want %v", token, got, want)
		}
	}
}
