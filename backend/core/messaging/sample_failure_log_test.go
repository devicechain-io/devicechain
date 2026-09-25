// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

// levelOf returns the level of the one captured line carrying msg, failing the test
// unless there is exactly one. It asserts PRESENCE first, so a muted or detached logger
// fails here instead of reading as "logged quietly".
func levelOf(t *testing.T, captured, msg string) string {
	t.Helper()
	var levels []string
	for _, line := range strings.Split(captured, "\n") {
		var entry struct {
			Level   string `json:"level"`
			Message string `json:"message"`
		}
		if json.Unmarshal([]byte(line), &entry) == nil && entry.Message == msg {
			levels = append(levels, entry.Level)
		}
	}
	if len(levels) != 1 {
		t.Fatalf("want exactly one %q line, got levels %v\ncaptured:\n%s", msg, levels, captured)
	}
	return levels[0]
}

// A failed broker sample is broker trouble an operator should see, so it is a Warn —
// but a sampler told to stop fails every request in flight, so a cancelled pass stays
// at Debug rather than printing a warning per stream on every rollout. Both halves are
// pinned: the promotion and the quiet-on-cancel.
func TestSampleFailureLogWarnsOnALiveContextAndIsQuietOnACancelledOne(t *testing.T) {
	prev := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.TraceLevel)
	t.Cleanup(func() { zerolog.SetGlobalLevel(prev) })
	logs := captureLogs(t)

	sampleFailureLog(context.Background()).Msg("live sample failure")
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	sampleFailureLog(cancelled).Msg("cancelled sample failure")

	if got := levelOf(t, logs.String(), "live sample failure"); got != "warn" {
		t.Errorf("a failed sample on a live context must warn; got %q", got)
	}
	if got := levelOf(t, logs.String(), "cancelled sample failure"); got != "debug" {
		t.Errorf("a failed sample on a cancelled context must stay at debug; got %q", got)
	}
}
