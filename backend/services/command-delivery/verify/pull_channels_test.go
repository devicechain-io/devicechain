// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
)

type fakeQuery struct {
	calls   [][]string
	answer  *[]string
	err     error
	rawNull bool
}

func (f *fakeQuery) Query(_ context.Context, _, tenant, query string, vars map[string]any, out any) error {
	tokens, _ := vars["deviceTokens"].([]string)
	f.calls = append(f.calls, tokens)
	if f.err != nil {
		return f.err
	}
	var body []byte
	switch {
	case f.rawNull:
		body = []byte(`{}`)
	default:
		list := []string{}
		if f.answer != nil {
			list = *f.answer
		}
		body, _ = json.Marshal(map[string]any{"commandPullDevices": list})
	}
	return json.Unmarshal(body, out)
}

func tenantCtx() context.Context { return core.WithTenant(context.Background(), "A") }

func TestPullChannelsAsksAndAnswers(t *testing.T) {
	q := &fakeQuery{answer: &[]string{"d1"}}
	p := NewPullChannels(q, "http://dm")
	got, err := p.PullDevices(tenantCtx(), []string{"d1", "d2"})
	if err != nil {
		t.Fatal(err)
	}
	if !got["d1"] || got["d2"] || len(got) != 1 {
		t.Fatalf("answer = %v, want only d1", got)
	}
}

// TestPullChannelsRemembersBothAnswersForTheTTL: a device asked about again inside the TTL costs no
// question, whichever way it was answered; after the TTL it is asked again.
func TestPullChannelsRemembersBothAnswersForTheTTL(t *testing.T) {
	q := &fakeQuery{answer: &[]string{"d1"}}
	p := NewPullChannels(q, "http://dm")
	now := time.Now()
	p.now = func() time.Time { return now }

	p.PullDevices(tenantCtx(), []string{"d1", "d2"})
	p.PullDevices(tenantCtx(), []string{"d1", "d2"})
	if len(q.calls) != 1 {
		t.Fatalf("%d questions inside the TTL, want 1", len(q.calls))
	}
	got, _ := p.PullDevices(tenantCtx(), []string{"d1", "d2"})
	if !got["d1"] || got["d2"] {
		t.Fatalf("a remembered answer changed: %v", got)
	}

	now = now.Add(pullTTL + time.Second)
	p.PullDevices(tenantCtx(), []string{"d1"})
	if len(q.calls) != 2 {
		t.Fatalf("%d questions after the TTL, want 2", len(q.calls))
	}
}

// TestPullChannelsKeysByTenant: the same device token in another tenant is another device.
func TestPullChannelsKeysByTenant(t *testing.T) {
	q := &fakeQuery{answer: &[]string{"d1"}}
	p := NewPullChannels(q, "http://dm")
	p.PullDevices(tenantCtx(), []string{"d1"})
	q.answer = &[]string{}
	got, _ := p.PullDevices(core.WithTenant(context.Background(), "B"), []string{"d1"})
	if got["d1"] {
		t.Fatal("tenant B inherited tenant A's answer for a device with the same token")
	}
}

func TestPullChannelsFailsClosedAndCachesNothingOnError(t *testing.T) {
	q := &fakeQuery{err: errors.New("device-management down")}
	p := NewPullChannels(q, "http://dm")
	if _, err := p.PullDevices(tenantCtx(), []string{"d1"}); err == nil {
		t.Fatal("a failed question must be an error")
	}
	q.err, q.answer = nil, &[]string{"d1"}
	got, err := p.PullDevices(tenantCtx(), []string{"d1"})
	if err != nil || !got["d1"] {
		t.Fatalf("after recovery: %v, %v; a failure must not be remembered as 'not pull'", got, err)
	}
}

// TestPullChannelsAbsentListIsAnError: a response that lost the field reads as the healthy empty
// answer unless it is told apart.
func TestPullChannelsAbsentListIsAnError(t *testing.T) {
	q := &fakeQuery{rawNull: true}
	p := NewPullChannels(q, "http://dm")
	if _, err := p.PullDevices(tenantCtx(), []string{"d1"}); err == nil {
		t.Fatal("an absent list must be an error, not 'no pull devices'")
	}
}

func TestPullChannelsNeedsATenant(t *testing.T) {
	p := NewPullChannels(&fakeQuery{}, "http://dm")
	if _, err := p.PullDevices(context.Background(), []string{"d1"}); err == nil {
		t.Fatal("no tenant in context must be an error")
	}
}

func TestPullChannelsChunksLargeQuestions(t *testing.T) {
	q := &fakeQuery{answer: &[]string{}}
	p := NewPullChannels(q, "http://dm")
	tokens := make([]string, 0, pullChunk*2+1)
	for i := 0; i < pullChunk*2+1; i++ {
		tokens = append(tokens, "d"+string(rune('a'+i%26))+time.Duration(i).String())
	}
	if _, err := p.PullDevices(tenantCtx(), tokens); err != nil {
		t.Fatal(err)
	}
	if len(q.calls) != 3 {
		t.Fatalf("%d questions for %d devices, want 3 chunks", len(q.calls), len(tokens))
	}
	for _, c := range q.calls {
		if len(c) > pullChunk {
			t.Fatalf("a chunk of %d exceeds %d", len(c), pullChunk)
		}
	}
}
