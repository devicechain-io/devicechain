// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package failedevents

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// scriptedClient answers each Query from a queue of canned bodies and records the
// variables it was asked with, so the walk's paging and filter mapping are checked
// without a server.
type scriptedClient struct {
	bodies []string
	vars   []map[string]any
	err    error
}

func (c *scriptedClient) Query(_ context.Context, _ string, _ string, variables map[string]any, out any) error {
	if c.err != nil {
		return c.err
	}
	c.vars = append(c.vars, variables)
	body := `{}`
	if len(c.bodies) > 0 {
		body = c.bodies[0]
		c.bodies = c.bodies[1:]
	}
	return json.Unmarshal([]byte(body), out)
}

var pageSeq int

func page(n, total int) string {
	rs := make([]Event, 0, n)
	for i := 0; i < n; i++ {
		d := "sensor-1"
		pageSeq++
		rs = append(rs, Event{ID: fmt.Sprint("x", pageSeq), OccurredTime: "2026-11-02T10:00:00Z", SourceArea: "event-sources",
			Reason: "DEVICE_NOT_FOUND", DeviceToken: &d, Error: "no such\ndevice", PayloadStatus: "AVAILABLE"})
	}
	b, _ := json.Marshal(map[string]any{"failedEvents": map[string]any{
		"results": rs, "pagination": map[string]any{"totalRecords": total}}})
	return string(b)
}

func TestEveryFilterReachesTheServer(t *testing.T) {
	since := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	c := &scriptedClient{bodies: []string{page(0, 0)}}
	_, err := List(context.Background(), c, Options{Limit: 10, Reason: "INVALID", Device: "d1", Since: &since, Until: &until})
	if err != nil {
		t.Fatal(err)
	}
	got := c.vars[0]["criteria"].(map[string]any)
	for k, want := range map[string]any{
		"reason": "INVALID", "deviceToken": "d1", "since": "2026-11-01T00:00:00Z",
		"until": "2026-11-02T00:00:00Z", "pageSize": 10, "pageNumber": 1,
	} {
		if got[k] != want {
			t.Fatalf("criteria[%q] = %v, want %v", k, got[k], want)
		}
	}
}

// An unset filter is ABSENT: an empty deviceToken would be an exact match on "".
func TestAnUnsetFilterIsAbsent(t *testing.T) {
	c := &scriptedClient{bodies: []string{page(0, 0)}}
	if _, err := List(context.Background(), c, Options{Limit: 10}); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"reason", "deviceToken"} {
		if _, present := c.vars[0]["criteria"].(map[string]any)[k]; present {
			t.Fatalf("criteria carries %q with nothing set", k)
		}
	}
}

// A run that stops at its limit must say so; a bounded read is otherwise a wrong answer.
func TestTruncationIsReportedAndTheLimitHolds(t *testing.T) {
	c := &scriptedClient{bodies: []string{page(100, 500), page(100, 500)}}
	l, err := List(context.Background(), c, Options{Limit: 150})
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Events) != 150 || !l.Truncated || len(c.vars) != 2 {
		t.Fatalf("got %d events truncated=%v after %d requests", len(l.Events), l.Truncated, len(c.vars))
	}
	var b strings.Builder
	PrintList(&b, Options{}, l)
	if !strings.Contains(b.String(), "More remain") {
		t.Fatalf("a truncated list does not say so:\n%s", b.String())
	}
}

// The server clamps the page size, so a short page is not the end of the walk.
func TestAClampedPageDoesNotEndTheWalk(t *testing.T) {
	c := &scriptedClient{bodies: []string{page(50, 120), page(50, 120), page(20, 120)}}
	l, err := List(context.Background(), c, Options{Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Events) != 120 || l.Truncated || len(c.vars) != 3 {
		t.Fatalf("got %d events truncated=%v after %d requests", len(l.Events), l.Truncated, len(c.vars))
	}
}

// A server that returns nothing while claiming more exist must not loop forever.
func TestAnEmptyPageEndsTheWalk(t *testing.T) {
	c := &scriptedClient{bodies: []string{page(0, 9), page(0, 9)}}
	l, err := List(context.Background(), c, Options{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.vars) != 1 || !l.Truncated {
		t.Fatalf("requests=%d truncated=%v", len(c.vars), l.Truncated)
	}
}

func TestAnEmptyResultIsASentence(t *testing.T) {
	var b strings.Builder
	PrintList(&b, Options{Device: "d9"}, Listing{Events: []Event{}})
	out := b.String()
	if strings.Contains(out, "WHEN") || !strings.Contains(out, "No failed events") || !strings.Contains(out, "device d9") {
		t.Fatalf("unexpected empty output:\n%s", out)
	}
}

func TestTableKeepsAMultilineErrorOnOneRowAndMarksOverflow(t *testing.T) {
	var b strings.Builder
	PrintList(&b, Options{}, Listing{Total: 2, Events: []Event{
		func() Event { e := page1(); return e }(),
		{OccurredTime: "t", Reason: "INVALID", Overflow: true, Error: "more", PayloadStatus: "NOT_RETAINED"},
	}})
	out := b.String()
	if !strings.Contains(out, "no such device") || !strings.Contains(out, "(further records)") {
		t.Fatalf("unexpected table:\n%s", out)
	}
}

func page1() Event {
	d := "sensor-1"
	return Event{OccurredTime: "t", Reason: "DEVICE_NOT_FOUND", DeviceToken: &d, Error: "no such\ndevice", PayloadStatus: "AVAILABLE"}
}

func TestSummaryIsSortedAndSendsSince(t *testing.T) {
	c := &scriptedClient{bodies: []string{`{"failedEventSummary":[
	  {"reason":"INVALID","reasonCode":1,"records":3,"suppressed":0,"devices":[]},
	  {"reason":"UNKNOWN","reasonCode":99,"records":40,"suppressed":5,"devices":[{"deviceToken":"d1","records":30}]}]}`}}
	since := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	rows, err := Summarize(context.Background(), c, "u", since)
	if err != nil {
		t.Fatal(err)
	}
	if c.vars[0]["since"] != "2026-11-02T00:00:00Z" || rows[0].Reason != "UNKNOWN" {
		t.Fatalf("since=%v rows=%+v", c.vars[0]["since"], rows)
	}
	var b strings.Builder
	PrintSummary(&b, since, rows)
	if !strings.Contains(b.String(), "UNKNOWN (code 99)") || !strings.Contains(b.String(), "d1 (30)") {
		t.Fatalf("unexpected summary:\n%s", b.String())
	}
}

func TestEmptySummaryIsASentenceAndJSONIsAnArray(t *testing.T) {
	c := &scriptedClient{bodies: []string{`{"failedEventSummary":[]}`}}
	rows, err := Summarize(context.Background(), c, "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	PrintSummary(&b, time.Now(), rows)
	if !strings.Contains(b.String(), "No failed events since") {
		t.Fatalf("got %q", b.String())
	}
	b.Reset()
	if err := PrintJSON(&b, rows); err != nil || strings.TrimSpace(b.String()) != "[]" {
		t.Fatalf("json=%q err=%v", b.String(), err)
	}
}

func TestNormalizeReason(t *testing.T) {
	for in, want := range map[string]string{"device-not-found": "DEVICE_NOT_FOUND", " invalid ": "INVALID", "UNKNOWN": "UNKNOWN"} {
		if got, err := NormalizeReason(in); err != nil || got != want {
			t.Fatalf("%q -> %q, %v", in, got, err)
		}
	}
	if _, err := NormalizeReason("nope"); err == nil || !strings.Contains(err.Error(), "DEVICE_NOT_FOUND") {
		t.Fatalf("err=%v", err)
	}
}

// Reasons is a copy of the served enum; hold it to the schema file so a reason added
// there is not rejected here as unknown. (Reads outside the module: run with -count=1.)
func TestReasonsMatchTheServedSchema(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "services", "event-management", "graphql", "schema.graphql"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)enum FailedEventReason \{(.*?)\n\}`).FindSubmatch(src)
	if m == nil {
		t.Fatal("FailedEventReason enum not found in the schema")
	}
	body := regexp.MustCompile(`(?s)""".*?"""`).ReplaceAll(m[1], nil)
	var served []string
	for _, f := range strings.Fields(string(body)) {
		served = append(served, f)
	}
	if strings.Join(served, ",") != strings.Join(Reasons, ",") {
		t.Fatalf("served %v, dcctl knows %v", served, Reasons)
	}
}
