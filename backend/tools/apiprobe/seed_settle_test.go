// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// artifactPlatform stands in for update-management for ONE artifact: a create answers
// with the row as declared (UPLOADING), and every later read answers with whatever state
// the row holds by then. reads counts the reads, so a test can say when it settles.
type artifactPlatform struct {
	state     string
	settleAt  int // the read (1-based) from which the row reads AVAILABLE; 0 = on upload
	reads     int
	uploaded  bool
	createDoc string
}

func (p *artifactPlatform) row(state string) map[string]any {
	return map[string]any{
		"token": "apiprobe-artifact-available", "kind": "FIRMWARE", "name": "apiprobe artifact (uploaded)",
		"version": "2.0.0", "component": "firmware", "sizeBytes": "12", "digest": "sha256:x",
		"mediaType": "application/octet-stream", "compatibleProfiles": []any{"p"},
		"trustStatus": "UNVERIFIED", "state": state,
	}
}

func (p *artifactPlatform) Query(_ context.Context, _ string, doc string, _ map[string]any, out any) error {
	var answer map[string]any
	switch {
	case strings.HasPrefix(doc, "mutation"):
		p.createDoc = doc
		answer = map[string]any{"createArtifact": p.row("UPLOADING")}
	case strings.HasPrefix(doc, "query"):
		p.reads++
		state := p.state
		if p.uploaded && p.settleAt > 0 && p.reads >= p.settleAt {
			state = "AVAILABLE"
		}
		answer = map[string]any{"artifact": p.row(state)}
	default:
		return fmt.Errorf("unexpected document %q", doc)
	}
	b, _ := json.Marshal(answer)
	return json.Unmarshal(b, out)
}

func artifactAvailable(t *testing.T) entity {
	t.Helper()
	for _, e := range allEntities() {
		if e.Name == "artifact-available" {
			return e
		}
	}
	t.Fatal("no artifact-available entity in the table")
	return entity{}
}

func fastSettle(t *testing.T, timeout time.Duration) {
	t.Helper()
	oldT, oldI := settleTimeout, settleInterval
	settleTimeout, settleInterval = timeout, time.Millisecond
	t.Cleanup(func() { settleTimeout, settleInterval = oldT, oldI })
}

func seedArtifact(t *testing.T, p *artifactPlatform) ([]Recorded, error) {
	t.Helper()
	e := artifactAvailable(t)
	upload := func(context.Context) error {
		p.uploaded = true
		if p.settleAt == 0 {
			p.state = "AVAILABLE"
		}
		return nil
	}
	p.state = "UPLOADING"
	return seedOne(context.Background(), p, "http://probe.invalid", e, e.Vars(newState("t")), newState("t"), upload, io.Discard)
}

// The receipt must hold what verify will read on a HEALTHY instance. The create response
// says UPLOADING; the upload the seed itself performs makes the row AVAILABLE. Recording
// the response made verify report that transition as a change across the upgrade.
func TestARowItsAfterStepChangesIsRecordedAsVerifyReadsIt(t *testing.T) {
	fastSettle(t, time.Second)
	p := &artifactPlatform{}
	rows, err := seedArtifact(t, p)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("recorded %d rows, want 1", len(rows))
	}

	// What verify does next, on an instance the upgrade did not touch.
	e := artifactAvailable(t)
	read, err := readRow(context.Background(), p, "http://probe.invalid", e, rows[0].Token, rows[0].Object)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	want, _ := canonical(rows[0].Object)
	got, _ := canonical(read)
	if want != got {
		t.Fatalf("verify would report a healthy instance as CHANGED:\n written: %s\n read:    %s", want, got)
	}
	if !strings.Contains(want, `"state":"AVAILABLE"`) {
		t.Fatalf("recorded %s, want the settled AVAILABLE row", want)
	}
}

// The state is still COMPARED: a row recorded AVAILABLE that reads back otherwise after
// an upgrade must still differ, or the fix would have bought its green by blinding verify.
func TestTheSettledStateIsStillCompared(t *testing.T) {
	fastSettle(t, time.Second)
	p := &artifactPlatform{}
	rows, err := seedArtifact(t, p)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	p.state, p.settleAt = "RETIRED", 0
	p.uploaded = false
	read, err := readRow(context.Background(), p, "http://probe.invalid", artifactAvailable(t), rows[0].Token, rows[0].Object)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	want, _ := canonical(rows[0].Object)
	got, _ := canonical(read)
	if want == got {
		t.Fatalf("a state changed by the upgrade compared EQUAL: %s", got)
	}
}

// A row that settles a little after its After step returns is waited for, boundedly.
func TestARowThatSettlesLateIsWaitedFor(t *testing.T) {
	fastSettle(t, 5*time.Second)
	p := &artifactPlatform{settleAt: 3}
	rows, err := seedArtifact(t, p)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if s, _ := canonical(rows[0].Object); !strings.Contains(s, `"state":"AVAILABLE"`) {
		t.Fatalf("recorded %s, want AVAILABLE", s)
	}
	if p.reads != 3 {
		t.Fatalf("read %d times, want 3 (two unsettled, one settled)", p.reads)
	}
}

// One that never settles fails the seed loudly, as a refusal, rather than being recorded
// unsettled (a false CHANGED later) or skipped (coverage that silently stops).
func TestARowThatNeverSettlesFailsTheSeed(t *testing.T) {
	fastSettle(t, 20*time.Millisecond)
	p := &artifactPlatform{settleAt: 1 << 30}
	_, err := seedArtifact(t, p)
	if err == nil {
		t.Fatal("seed recorded a row that never settled")
	}
	if codeOf(err) != exitRefused || !strings.Contains(err.Error(), "never settled") {
		t.Fatalf("got exit %d %v, want exitRefused naming the row that never settled", codeOf(err), err)
	}
}

// Every After step needs a Settled check, or the row it changes is recorded as the create
// returned it. Pinned over the real table, and seedOne refuses one at run time as well.
func TestEveryAfterStepSaysHowItsRowSettles(t *testing.T) {
	n := 0
	for _, e := range allEntities() {
		if e.After == nil {
			continue
		}
		n++
		if e.Settled == nil {
			t.Errorf("%s has an After step but no Settled check", e.Name)
		}
	}
	if n == 0 {
		t.Fatal("no entity has an After step, so this checked nothing")
	}
	e := artifactAvailable(t)
	e.Settled = nil
	if _, err := seedOne(context.Background(), &artifactPlatform{}, "u", e, e.Vars(newState("t")), newState("t"),
		func(context.Context) error { return nil }, io.Discard); err == nil || codeOf(err) != exitSetup {
		t.Fatalf("seedOne ran an After step with no Settled check (err %v)", err)
	}
}
