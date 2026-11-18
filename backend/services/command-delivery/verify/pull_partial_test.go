// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

type partialPullQuery struct {
	calls  int
	failAt int
	err    error
}

func (q *partialPullQuery) Query(_ context.Context, _, _, _ string, vars map[string]any, out any) error {
	q.calls++
	if q.calls == q.failAt {
		return q.err
	}
	tokens := vars["deviceTokens"].([]string)
	body, _ := json.Marshal(map[string]any{"commandPullDevices": []string{tokens[0]}})
	return json.Unmarshal(body, out)
}

func TestPullPartialAnswersSurviveAFailedChunk(t *testing.T) {
	for _, failure := range []error{errors.New("unreachable"),
		errors.New("Cannot query field commandPullDevices on type Query")} {
		q := &partialPullQuery{}
		p := NewPullChannels(q, "http://dm")
		if _, err := p.PullDevices(tenantCtx(), []string{"cached-pull", "cached-push"}); err != nil {
			t.Fatal(err)
		}
		q.failAt, q.err = 3, failure
		tokens := []string{"cached-pull", "cached-push"}
		for i := 0; i < pullChunk*2+1; i++ {
			tokens = append(tokens, fmt.Sprintf("d-%04d", i))
		}
		got, err := p.PullDevices(tenantCtx(), tokens)
		if err == nil || q.calls != 3 || len(got) != pullChunk+2 {
			t.Fatalf("partial read: %d answers, %d calls, %v", len(got), q.calls, err)
		}
		for token, want := range map[string]bool{"cached-pull": true, "cached-push": false, "d-0000": true, "d-0001": false} {
			if v, ok := got[token]; !ok || v != want {
				t.Errorf("%s: %v, present %v, want %v", token, v, ok, want)
			}
		}
		if _, ok := got["d-0500"]; ok {
			t.Fatal("failed chunk was guessed")
		}
		if errors.Is(err, ErrPullChannelsUnsupported) != (failure.Error() != "unreachable") {
			t.Fatalf("incorrect classification: %v", err)
		}
		got, err = p.PullDevices(tenantCtx(), []string{"d-0500"})
		if err != nil || !got["d-0500"] || q.calls != 4 {
			t.Fatalf("failed chunk was cached: %v, %v, calls %d", got, err, q.calls)
		}
	}
}
