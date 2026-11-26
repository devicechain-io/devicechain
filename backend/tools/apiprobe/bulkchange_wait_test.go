// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestReachedNamesEachPointAndRefusesAPause(t *testing.T) {
	c := bcChange{Token: "t", Dispatch: "RUNNING", Evidence: "OPEN", TargetCount: 10}
	c.Progress.Posted = 4
	if ok, _ := bcReached(c, "half-posted"); ok {
		t.Fatal("4 of 10 read as half posted")
	}
	c.Progress.Posted = 5
	if ok, _ := bcReached(c, "half-posted"); !ok {
		t.Fatal("5 of 10 did not read as half posted")
	}
	if ok, _ := bcReached(c, "dispatched"); ok {
		t.Fatal("RUNNING read as dispatched")
	}
	c.Dispatch = "DISPATCHED"
	if ok, _ := bcReached(c, "closed"); ok {
		t.Fatal("DISPATCHED+OPEN read as closed")
	}
	c.Evidence = "CLOSED"
	if ok, _ := bcReached(c, "closed"); !ok {
		t.Fatal("DISPATCHED+CLOSED did not read as closed")
	}
	if _, err := bcReached(bcChange{Dispatch: "PAUSED"}, "dispatched"); err == nil {
		t.Fatal("a paused change was waited on")
	}
	if _, err := bcReached(c, "nope"); err == nil {
		t.Fatal("an unknown point was accepted")
	}
}

// The repair control passes only when a whole bound goes by with the line unredacted, and fails the
// moment it is redacted.
func TestRedactionControlNeedsTheWholeBoundUnredacted(t *testing.T) {
	var polls, redactAfter atomic.Int32
	redactAfter.Store(1 << 30)
	url, receipt, _ := bcFixture(t, func(w http.ResponseWriter, r *http.Request) {
		l := okLine(3, "REPORT_LOG")
		if polls.Add(1) >= redactAfter.Load() {
			l = erasedLine(3, "POSTED")
		}
		body, _ := json.Marshal(map[string]any{"data": map[string]any{"lines": map[string]any{
			"results": []bcLine{l}, "pagination": map[string]any{"totalRecords": 1}}}})
		_, _ = w.Write(body)
	})
	out := filepath.Join(t.TempDir(), "lines.json")
	// A short bound where the line never changes; a long one where it does, so the redaction is always
	// seen inside the bound however slowly the test runs (it returns the moment it is).
	args := func(expect, within string) []string {
		return bcArgs(url, receipt, "--token", "t", "--ordinals", "3", "--expect", expect, "--out", out,
			"--within", within, "--every", "20ms")
	}
	if err := bcRedactionCmd(context.Background(), args("unredacted", "200ms"), io.Discard); err != nil {
		t.Fatalf("an unredacted line through the whole bound failed the control: %v", err)
	}
	assertCode(t, bcRedactionCmd(context.Background(), args("redacted", "200ms"), io.Discard), exitMismatch)
	polls.Store(0)
	redactAfter.Store(2)
	assertCode(t, bcRedactionCmd(context.Background(), args("unredacted", "1m"), io.Discard), exitMismatch)
	polls.Store(0)
	if err := bcRedactionCmd(context.Background(), args("redacted", "1m"), io.Discard); err != nil {
		t.Fatalf("a line redacted within the bound failed: %v", err)
	}
}
