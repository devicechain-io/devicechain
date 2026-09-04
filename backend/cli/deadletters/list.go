// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package deadletters is dcctl's read of the ADR-024 dead-letter store: what the platform
// accepted and then gave up on.
//
// It is a READ, and it is the first one dcctl has of the admin plane — every other admin
// command is a mutation from `dcctl sim`. That is worth knowing when adding the next one:
// the session, the URL derivation and the superuser check are all shared with those, and
// only the query and the rendering are new here.
package deadletters

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
)

// Client is the one method this package needs of an admin session, narrowed so the walk
// below can be driven without a server.
type Client interface {
	Query(ctx context.Context, adminBaseURL, query string, variables map[string]any, out any) error
}

// Options is what to list.
type Options struct {
	AdminURL string
	Tenant   string
	Kind     string
	Source   string
	Since    *time.Time
	PageSize int
	Pages    int
}

// Letter is one record as rendered.
type Letter struct {
	ID           string  `json:"id"`
	Tenant       string  `json:"tenant"`
	OccurredTime string  `json:"occurredTime"`
	Kind         string  `json:"kind"`
	Reason       string  `json:"reason"`
	Source       string  `json:"source"`
	Summary      string  `json:"summary"`
	Detail       *string `json:"detail"`
	Attempts     int32   `json:"attempts"`
	Reference    *string `json:"reference"`
	Correlation  *string `json:"correlation"`
}

type page struct {
	Results    []Letter `json:"results"`
	Pagination struct {
		PageStart    int32 `json:"pageStart"`
		PageEnd      int32 `json:"pageEnd"`
		TotalRecords int32 `json:"totalRecords"`
	} `json:"pagination"`
}

type response struct {
	DeadLetters page `json:"deadLetters"`
}

const listQuery = `query DeadLetters($criteria: DeadLetterSearchCriteria!) {
  deadLetters(criteria: $criteria) {
    results {
      id tenant occurredTime kind reason source summary detail attempts reference correlation
    }
    pagination { pageStart pageEnd totalRecords }
  }
}`

// Summary is what a run found.
type Summary struct {
	Letters []Letter
	Total   int32
	// Truncated says the operator's page budget ran out before the results did, so the
	// count they were shown is not the count that exists. Saying so is the difference
	// between a bounded read and a wrong answer.
	Truncated bool
}

// Run pages through the store and collects what it finds.
func Run(ctx context.Context, client Client, opts Options) (Summary, error) {
	sum := Summary{}
	for p := 1; p <= opts.Pages; p++ {
		criteria := map[string]any{"pageNumber": p, "pageSize": opts.PageSize}
		if opts.Tenant != "" {
			criteria["tenant"] = opts.Tenant
		}
		if opts.Kind != "" {
			criteria["kind"] = opts.Kind
		}
		if opts.Source != "" {
			criteria["source"] = opts.Source
		}
		if opts.Since != nil {
			criteria["since"] = opts.Since.UTC().Format(time.RFC3339)
		}
		var resp response
		if err := client.Query(ctx, opts.AdminURL, listQuery,
			map[string]any{"criteria": criteria}, &resp); err != nil {
			return sum, fmt.Errorf("reading dead letters: %w", err)
		}
		sum.Total = resp.DeadLetters.Pagination.TotalRecords
		sum.Letters = append(sum.Letters, resp.DeadLetters.Results...)
		if len(resp.DeadLetters.Results) < opts.PageSize {
			return sum, nil
		}
	}
	sum.Truncated = int32(len(sum.Letters)) < sum.Total
	return sum, nil
}

// Print renders a summary.
//
// 🔑 AN EMPTY RESULT GETS A SENTENCE, NOT AN EMPTY TABLE. A header with no rows under it
// reads as "the query is still loading" or "I typed the filter wrong"; a sentence says
// what was asked and what the answer was.
func Print(w io.Writer, opts Options, sum Summary) {
	if len(sum.Letters) == 0 {
		fmt.Fprintf(w, "No dead letters%s. Nothing has been given up on in the window you asked about.\n",
			describeFilters(opts))
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "WHEN\tTENANT\tKIND\tSOURCE\tATTEMPTS\tREFERENCE\tSUMMARY")
	for _, l := range sum.Letters {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
			l.OccurredTime, l.Tenant, l.Kind, l.Source, l.Attempts,
			deref(l.Reference), l.Summary)
	}
	tw.Flush()
	fmt.Fprintf(w, "\n%d of %d record(s)%s.\n", len(sum.Letters), sum.Total, describeFilters(opts))
	if sum.Truncated {
		fmt.Fprintf(w, "More remain: raise --pages or narrow the filters.\n")
	}
	// 🔴 SAY WHAT THIS IS NOT. An operator looking at a list of failures reasonably
	// assumes something is working through it. Nothing is.
	fmt.Fprintf(w, "\nNothing replays these. They are a record that the work did not happen.\n")
}

func describeFilters(opts Options) string {
	parts := []string{}
	if opts.Tenant != "" {
		parts = append(parts, "tenant "+opts.Tenant)
	}
	if opts.Kind != "" {
		parts = append(parts, "kind "+opts.Kind)
	}
	if opts.Source != "" {
		parts = append(parts, "source "+opts.Source)
	}
	if opts.Since != nil {
		parts = append(parts, "since "+opts.Since.UTC().Format(time.RFC3339))
	}
	if len(parts) == 0 {
		return ""
	}
	return " for " + strings.Join(parts, ", ")
}

func deref(s *string) string {
	if s == nil {
		return "-"
	}
	return *s
}
