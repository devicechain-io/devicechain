// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package deadletters

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// scriptedClient answers each Query from a queue of canned pages and records the criteria
// it was asked with, so the walk's paging and its filter mapping can both be checked
// without a server.
type scriptedClient struct {
	pages    []page
	criteria []map[string]any
	err      error
}

func (c *scriptedClient) Query(_ context.Context, _ string, _ string,
	variables map[string]any, out any) error {
	if c.err != nil {
		return c.err
	}
	c.criteria = append(c.criteria, variables["criteria"].(map[string]any))
	var p page
	if len(c.pages) > 0 {
		p = c.pages[0]
		c.pages = c.pages[1:]
	}
	b, _ := json.Marshal(response{DeadLetters: p})
	return json.Unmarshal(b, out)
}

func letters(n int) []Letter {
	out := make([]Letter, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, Letter{ID: "1", Tenant: "acme", Kind: "notification",
			Source: "notification-management", Summary: "nobody was paged", Attempts: 5,
			OccurredTime: "2026-09-04T12:00:00Z"})
	}
	return out
}

func full(n, total int) page {
	p := page{Results: letters(n)}
	p.Pagination.TotalRecords = int32(total)
	return p
}

// A short page ends the walk: there is nothing after it, so asking again would be a
// round-trip that can only return nothing.
func TestTheWalkStopsOnAShortPage(t *testing.T) {
	c := &scriptedClient{pages: []page{full(2, 2)}}
	sum, err := Run(context.Background(), c, Options{PageSize: 10, Pages: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.criteria) != 1 {
		t.Fatalf("made %d requests for a single short page, want 1", len(c.criteria))
	}
	if len(sum.Letters) != 2 || sum.Truncated {
		t.Fatalf("got %d letters truncated=%v", len(sum.Letters), sum.Truncated)
	}
}

// 🔴 A TRUNCATED LIST MUST SAY SO. An operator handed 40 of 200 records with no marker
// reads 40 as the answer — which is how a bounded read becomes a wrong one.
func TestARunThatHitsItsPageBudgetSaysSo(t *testing.T) {
	c := &scriptedClient{pages: []page{full(2, 200), full(2, 200)}}
	sum, err := Run(context.Background(), c, Options{PageSize: 2, Pages: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !sum.Truncated {
		t.Fatal("a run that stopped at its page budget reported a complete list")
	}
	var b strings.Builder
	Print(&b, Options{}, sum)
	if !strings.Contains(b.String(), "More remain") {
		t.Fatalf("the output does not say the list is truncated:\n%s", b.String())
	}
}

// Every filter reaches the server. A flag the walk silently dropped would return the whole
// table, and the extra rows would read as findings.
func TestEveryFilterReachesTheServer(t *testing.T) {
	since := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	c := &scriptedClient{pages: []page{full(0, 0)}}
	_, err := Run(context.Background(), c, Options{
		PageSize: 10, Pages: 1, Tenant: "acme", Kind: "notification",
		Source: "notification-management", Since: &since,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := c.criteria[0]
	for k, want := range map[string]any{
		"tenant": "acme", "kind": "notification", "source": "notification-management",
		"since": "2026-09-04T00:00:00Z", "pageSize": 10, "pageNumber": 1,
	} {
		if got[k] != want {
			t.Fatalf("criteria[%q] = %v, want %v", k, got[k], want)
		}
	}
}

// An unset filter is ABSENT, not empty — an empty string would be an exact match on "" and
// return nothing at all.
func TestAnUnsetFilterIsAbsentRatherThanEmpty(t *testing.T) {
	c := &scriptedClient{pages: []page{full(0, 0)}}
	if _, err := Run(context.Background(), c, Options{PageSize: 10, Pages: 1}); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"tenant", "kind", "source", "since"} {
		if _, present := c.criteria[0][k]; present {
			t.Fatalf("criteria carries %q with nothing set; an empty exact match returns nothing", k)
		}
	}
}

// 🔑 AN EMPTY RESULT GETS A SENTENCE, NOT AN EMPTY TABLE. A header with no rows reads as
// "still loading" or "I typed the filter wrong".
func TestAnEmptyResultIsASentence(t *testing.T) {
	var b strings.Builder
	Print(&b, Options{Tenant: "acme"}, Summary{})
	out := b.String()
	if strings.Contains(out, "WHEN\t") || strings.Contains(out, "TENANT") {
		t.Fatalf("an empty result rendered a table header:\n%s", out)
	}
	if !strings.Contains(out, "No dead letters") || !strings.Contains(out, "tenant acme") {
		t.Fatalf("the sentence does not say what was asked:\n%s", out)
	}
}

// 🔴 THE OUTPUT SAYS NOTHING REPLAYS THESE. An operator looking at a list of failures
// reasonably assumes something is working through it; nothing is, and the one place they
// will certainly read is the thing they just ran.
func TestTheOutputSaysNothingReplaysThem(t *testing.T) {
	var b strings.Builder
	Print(&b, Options{}, Summary{Letters: letters(1), Total: 1})
	if !strings.Contains(b.String(), "Nothing replays these") {
		t.Fatalf("the output lets a list of failures read as a queue:\n%s", b.String())
	}
}
