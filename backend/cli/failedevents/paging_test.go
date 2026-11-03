// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package failedevents

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// pagingServer is a fake event-management: rows newest first, filtered by the criteria's
// since/until and cut into pages, with onFetch run after each request so a test can let
// new failures arrive between page fetches.
type pagingServer struct {
	rows    []Event // newest first
	onFetch func(s *pagingServer)
}

func (s *pagingServer) Query(_ context.Context, _ string, _ string, vars map[string]any, out any) error {
	c := vars["criteria"].(map[string]any)
	var match []Event
	for _, r := range s.rows {
		ts, _ := time.Parse(time.RFC3339Nano, r.OccurredTime)
		if v, ok := c["until"].(string); ok {
			if u, _ := time.Parse(time.RFC3339, v); ts.After(u) {
				continue
			}
		}
		if v, ok := c["since"].(string); ok {
			if u, _ := time.Parse(time.RFC3339, v); ts.Before(u) {
				continue
			}
		}
		match = append(match, r)
	}
	n, size := c["pageNumber"].(int), c["pageSize"].(int)
	lo, hi := (n-1)*size, n*size
	if lo > len(match) {
		lo = len(match)
	}
	if hi > len(match) {
		hi = len(match)
	}
	body, _ := json.Marshal(map[string]any{"failedEvents": map[string]any{
		"results": match[lo:hi], "pagination": map[string]any{"totalRecords": len(match)}}})
	if s.onFetch != nil {
		s.onFetch(s)
	}
	return json.Unmarshal(body, out)
}

// New failures arriving while the walk runs shift an offset-paged, newest-first list: the
// last rows of page 1 reappear on page 2. The walk pins its upper bound at its first
// request so what arrives later is outside it, and drops a repeated id as a belt.
func TestNewFailuresDuringTheWalkDoNotRepeatRows(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	mk := func(i int, at time.Time) Event {
		return Event{ID: fmt.Sprintf("id-%d", i), OccurredTime: at.Format(time.RFC3339Nano), Reason: "INVALID"}
	}
	srv := &pagingServer{}
	for i := 0; i < 250; i++ { // one per second, newest first, all before "now"
		srv.rows = append(srv.rows, mk(i, now.Add(-time.Duration(i+1)*time.Second)))
	}
	next := 1000
	srv.onFetch = func(s *pagingServer) {
		// Seven new failures after every fetch, newer than the walk's start.
		for k := 0; k < 7; k++ {
			next++
			s.rows = append([]Event{mk(next, now.Add(time.Duration(next-1000)*2*time.Second))}, s.rows...)
		}
	}
	l, err := List(context.Background(), srv, Options{Limit: 1000, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range l.Events {
		if seen[e.ID] {
			t.Fatalf("row %s listed twice", e.ID)
		}
		seen[e.ID] = true
	}
	if len(l.Events) != 250 || l.Truncated {
		t.Fatalf("got %d events truncated=%v, want the 250 that existed when the walk began", len(l.Events), l.Truncated)
	}
}

// A repeated id never reaches the output, whatever the server does.
func TestARepeatedIDIsDropped(t *testing.T) {
	same := `{"failedEvents":{"results":[{"id":"a"},{"id":"b"}],"pagination":{"totalRecords":3}}}`
	c := &scriptedClient{bodies: []string{same, same, `{"failedEvents":{"results":[],"pagination":{"totalRecords":3}}}`}}
	l, err := List(context.Background(), c, Options{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Events) != 2 {
		t.Fatalf("got %d events; the server repeated a and b", len(l.Events))
	}
}
