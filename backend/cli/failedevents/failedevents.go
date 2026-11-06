// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package failedevents is dcctl's read of a tenant's failed-event records: the events the
// platform refused or could not store, newest first, and a per-reason count of them.
//
// It reads event-management's TENANT plane as a member of the tenant, so what an operator
// sees here is exactly what that member's console shows, authorized by the same grant.
// It is a read of a report, not of a queue: nothing here re-sends anything, and the
// original event is not fetched.
package failedevents

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

// Client is the one method this package needs of a tenant session, narrowed so the walk
// can be driven without a server.
type Client interface {
	Query(ctx context.Context, baseURL, query string, variables map[string]any, out any) error
}

// Reasons is the FailedEventReason vocabulary the server serves. It is a copy, because
// dcctl does not compile event-management; TestReasonsMatchTheServedSchema holds it to
// the schema file so a reason added there cannot be rejected here as unknown.
var Reasons = []string{
	"INVALID", "API_CALL_FAILED", "DEVICE_NOT_FOUND", "UNAUTHENTICATED", "DEVICE_DISABLED",
	"NOT_GATEWAY_CHILD", "INTEGRATION_ALIAS_MISMATCH", "DECODE_FAILED", "UNKNOWN",
}

// maxServerPage is the server's page-size cap; it clamps larger requests.
const maxServerPage = 100

// NormalizeReason accepts a reason in any case, with '-' or '_' between words, and
// returns the enum name, or an error naming the valid ones.
func NormalizeReason(s string) (string, error) {
	n := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), "-", "_"))
	for _, r := range Reasons {
		if r == n {
			return n, nil
		}
	}
	return "", fmt.Errorf("unknown reason %q: one of %s", s, strings.Join(Reasons, ", "))
}

// Options is what to read.
type Options struct {
	// Endpoint is event-management's GraphQL URL.
	Endpoint string
	Reason   string
	Device   string
	Since    *time.Time
	Until    *time.Time
	// Limit is the most records to fetch; the walk stops there and says so if more exist.
	Limit int
	// Now is the clock the walk pins its window to; time.Now when nil.
	Now func() time.Time
}

// MaxLookback is how far back the server reads; an older since is treated as this old.
const MaxLookback = 31 * 24 * time.Hour

// ClampSince returns since, or now-MaxLookback when since is older, which is the instant
// the server will actually use.
func ClampSince(now, since time.Time) time.Time {
	if floor := now.Add(-MaxLookback); since.Before(floor) {
		return floor.UTC()
	}
	return since.UTC()
}

// Event is one record as rendered.
type Event struct {
	ID            string  `json:"id"`
	OccurredTime  string  `json:"occurredTime"`
	SourceArea    string  `json:"sourceArea"`
	Reason        string  `json:"reason"`
	ReasonCode    int32   `json:"reasonCode"`
	DeviceToken   *string `json:"deviceToken"`
	EventType     *int32  `json:"eventType"`
	Error         string  `json:"error"`
	Correlation   string  `json:"correlation"`
	Suppressed    int32   `json:"suppressed"`
	Overflow      bool    `json:"overflow"`
	PayloadStatus string  `json:"payloadStatus"`
}

type listResponse struct {
	FailedEvents struct {
		Results    []Event `json:"results"`
		Pagination struct {
			TotalRecords int32 `json:"totalRecords"`
		} `json:"pagination"`
	} `json:"failedEvents"`
}

const listQuery = `query FailedEvents($criteria: FailedEventSearchCriteria!) {
  failedEvents(criteria: $criteria) {
    results {
      id occurredTime sourceArea reason reasonCode deviceToken eventType error correlation
      suppressed overflow payloadStatus
    }
    pagination { totalRecords }
  }
}`

// Listing is what a list run found.
type Listing struct {
	Events []Event `json:"results"`
	Total  int32   `json:"total"`
	// Truncated says the limit was reached before the results ran out, so what was
	// shown is not everything that matches.
	Truncated bool `json:"truncated"`
}

// List pages through the failed events up to opts.Limit.
func List(ctx context.Context, client Client, opts Options) (Listing, error) {
	out := Listing{Events: []Event{}}
	if opts.Limit < 1 {
		return out, fmt.Errorf("limit must be at least 1")
	}
	size := opts.Limit
	if size > maxServerPage {
		size = maxServerPage
	}
	// 🔴 PIN THE WINDOW. The list is newest-first and paged by offset, so a failure that
	// arrives between two fetches shifts every row down and the last rows of one page
	// reappear on the next. Pinning until to the walk's first instant leaves newer
	// records outside it; since is pinned too, because the server's own default and cap
	// (now minus 31 days) would otherwise move under the walk.
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	start := now().UTC()
	until := start
	if opts.Until != nil {
		until = opts.Until.UTC()
	}
	since := ClampSince(start, start.Add(-MaxLookback))
	if opts.Since != nil {
		since = ClampSince(start, *opts.Since)
	}
	seen := map[string]bool{}
	for p := 1; ; p++ {
		criteria := map[string]any{"pageNumber": p, "pageSize": size,
			"since": since.Format(time.RFC3339), "until": until.Format(time.RFC3339)}
		if opts.Reason != "" {
			criteria["reason"] = opts.Reason
		}
		if opts.Device != "" {
			criteria["deviceToken"] = opts.Device
		}
		var resp listResponse
		if err := client.Query(ctx, opts.Endpoint, listQuery,
			map[string]any{"criteria": criteria}, &resp); err != nil {
			return out, fmt.Errorf("reading failed events: %w", err)
		}
		page := resp.FailedEvents.Results
		out.Total = resp.FailedEvents.Pagination.TotalRecords
		// Belt: a row already listed is never listed again.
		for _, e := range page {
			if !seen[e.ID] {
				seen[e.ID] = true
				out.Events = append(out.Events, e)
			}
		}
		// The server may clamp the page size, so a short page is not the end: the walk
		// ends when everything the server says exists has been collected, the limit is
		// reached, or the server returns nothing.
		if int32(len(out.Events)) >= out.Total || len(page) == 0 || len(out.Events) >= opts.Limit {
			break
		}
	}
	if len(out.Events) > opts.Limit {
		out.Events = out.Events[:opts.Limit]
	}
	out.Truncated = int32(len(out.Events)) < out.Total
	return out, nil
}

// DeviceCount is one device's share of a reason.
type DeviceCount struct {
	DeviceToken string `json:"deviceToken"`
	Records     int32  `json:"records"`
}

// ReasonCount is one reason's line of the summary.
type ReasonCount struct {
	Reason     string        `json:"reason"`
	ReasonCode int32         `json:"reasonCode"`
	Records    int32         `json:"records"`
	Suppressed int32         `json:"suppressed"`
	Devices    []DeviceCount `json:"devices"`
}

const summaryQuery = `query FailedEventSummary($since: String!) {
  failedEventSummary(since: $since) {
    reason reasonCode records suppressed
    devices { deviceToken records }
  }
}`

// Summarize reads the per-reason counts since an instant, largest first.
func Summarize(ctx context.Context, client Client, endpoint string, since time.Time) ([]ReasonCount, error) {
	var resp struct {
		Summary []ReasonCount `json:"failedEventSummary"`
	}
	if err := client.Query(ctx, endpoint, summaryQuery,
		map[string]any{"since": since.UTC().Format(time.RFC3339)}, &resp); err != nil {
		return nil, fmt.Errorf("reading the failed-event summary: %w", err)
	}
	if resp.Summary == nil {
		resp.Summary = []ReasonCount{}
	}
	sort.SliceStable(resp.Summary, func(i, j int) bool { return resp.Summary[i].Records > resp.Summary[j].Records })
	return resp.Summary, nil
}

// PrintList renders a listing as a table.
//
// An empty result gets a sentence, not an empty table: a bare header reads as "still
// loading" or "I typed the filter wrong", and "nothing was refused" is a real answer.
func PrintList(w io.Writer, opts Options, l Listing) {
	if len(l.Events) == 0 {
		fmt.Fprintf(w, "No failed events%s. The platform refused nothing in the window you asked about.\n",
			describe(opts))
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "WHEN\tREASON\tDEVICE\tSOURCE\tFOLDED\tPAYLOAD\tERROR")
	for _, e := range l.Events {
		device := "-"
		if e.DeviceToken != nil {
			device = *e.DeviceToken
		}
		if e.Overflow {
			device = "(further records)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
			e.OccurredTime, e.Reason, device, e.SourceArea, e.Suppressed, e.PayloadStatus, oneLine(e.Error))
	}
	tw.Flush()
	fmt.Fprintf(w, "\n%d of %d record(s)%s.\n", len(l.Events), l.Total, describe(opts))
	if l.Truncated {
		fmt.Fprintf(w, "More remain: raise --limit or narrow the filters.\n")
	}
	fmt.Fprintf(w, "\nThese are a record of what was refused, not a queue: nothing is re-sent.\n")
}

// PrintSummary renders the per-reason counts as a table, with the top devices of each.
func PrintSummary(w io.Writer, since time.Time, rows []ReasonCount) {
	if len(rows) == 0 {
		fmt.Fprintf(w, "No failed events since %s. The platform refused nothing in that window.\n",
			since.UTC().Format(time.RFC3339))
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "REASON\tRECORDS\tFOLDED\tTOP DEVICES")
	for _, r := range rows {
		devs := make([]string, 0, len(r.Devices))
		for _, d := range r.Devices {
			devs = append(devs, fmt.Sprintf("%s (%d)", d.DeviceToken, d.Records))
		}
		name := r.Reason
		if r.Reason == "UNKNOWN" {
			name = fmt.Sprintf("UNKNOWN (code %d)", r.ReasonCode)
		}
		fmt.Fprintf(tw, "%s\t%d\t%d\t%s\n", name, r.Records, r.Suppressed, strings.Join(devs, ", "))
	}
	tw.Flush()
	fmt.Fprintf(w, "\nSince %s. Records include those folded into a count.\n", since.UTC().Format(time.RFC3339))
}

// PrintJSON writes v as indented JSON.
func PrintJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func describe(opts Options) string {
	parts := []string{}
	if opts.Reason != "" {
		parts = append(parts, "reason "+opts.Reason)
	}
	if opts.Device != "" {
		parts = append(parts, "device "+opts.Device)
	}
	if opts.Since != nil {
		parts = append(parts, "since "+opts.Since.UTC().Format(time.RFC3339))
	}
	if opts.Until != nil {
		parts = append(parts, "until "+opts.Until.UTC().Format(time.RFC3339))
	}
	if len(parts) == 0 {
		return ""
	}
	return " for " + strings.Join(parts, ", ")
}

// oneLine keeps a multi-line error from breaking the table's rows.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
