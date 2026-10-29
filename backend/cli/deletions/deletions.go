// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package deletions is dcctl's read of a tenant deletion's outcome: is the tenant's data
// established as erased, and if not, what is holding it.
//
// It reads the server's own claim (erasedAt) and never derives one. A deletion is erased
// only when the platform says so, which it does once every storage system has reported clean
// and stayed clean for the settle window. "Nothing is blocking" and "no error" are not that
// claim, and neither is a list of stores that happen to look empty from here.
package deletions

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
)

// Client is the one method this package needs of an admin session.
type Client interface {
	Query(ctx context.Context, adminBaseURL, query string, variables map[string]any, out any) error
}

// Store is one storage system's line.
type Store struct {
	Store    string `json:"store"`
	Complete bool   `json:"complete"`
}

// Deletion is one record as read.
type Deletion struct {
	Token       string   `json:"token"`
	Epoch       string   `json:"epoch"`
	ErasedAt    *string  `json:"erasedAt"`
	Erased      bool     `json:"erased"`
	CompletedAt *string  `json:"completedAt"`
	RowsErased  int32    `json:"rowsErased"`
	Awaiting    string   `json:"awaiting"`
	ElapsesAt   *string  `json:"elapsesAt"`
	BlockedBy   []string `json:"blockedBy"`
	Stores      []Store  `json:"stores"`
}

const query = `query TenantDeletions($criteria: TenantDeletionSearchCriteria!) {
  tenantDeletions(criteria: $criteria) {
    results {
      token epoch erasedAt erased completedAt rowsErased awaiting elapsesAt blockedBy
      stores { store complete }
    }
    pagination { totalRecords }
  }
}`

type response struct {
	TenantDeletions struct {
		Results    []Deletion `json:"results"`
		Pagination struct {
			TotalRecords int32 `json:"totalRecords"`
		} `json:"pagination"`
	} `json:"tenantDeletions"`
}

const pageSize = 200

// Find returns the most recent deletion of a token, or nil when there is none. Records are
// newest first, so the first match is the latest deletion of that token. Pages are walked
// until the token is found or the records run out; an error is never read as "none".
func Find(ctx context.Context, c Client, adminURL, token string) (*Deletion, error) {
	for p := 1; ; p++ {
		var out response
		err := c.Query(ctx, adminURL, query, map[string]any{
			"criteria": map[string]any{"pageNumber": p, "pageSize": pageSize},
		}, &out)
		if err != nil {
			return nil, fmt.Errorf("reading tenant deletions: %w", err)
		}
		for i := range out.TenantDeletions.Results {
			if out.TenantDeletions.Results[i].Token == token {
				d := out.TenantDeletions.Results[i]
				return &d, nil
			}
		}
		if len(out.TenantDeletions.Results) < pageSize ||
			int32(p*pageSize) >= out.TenantDeletions.Pagination.TotalRecords {
			return nil, nil
		}
	}
}

// Target is how far a deletion must have got.
type Target string

const (
	// TargetErased: the data is established as erased (the token may still be held).
	TargetErased Target = "erased"
	// TargetComplete: the deletion finished and the token was released.
	TargetComplete Target = "complete"
)

// Reached reports whether d satisfies the target. A nil deletion satisfies nothing, and
// "complete" is checked against completedAt itself rather than against erased, so an
// erased deletion that is still holding its token does not pass for a finished one. A record
// that says erased without the timestamp that backs it is not trusted.
func Reached(d *Deletion, t Target) bool {
	if d == nil {
		return false
	}
	switch t {
	case TargetComplete:
		return d.CompletedAt != nil
	default:
		return d.Erased && (d.ErasedAt != nil || d.CompletedAt != nil)
	}
}

// Wait polls until the target is reached or ctx ends. An absent record is an error, not
// success: a tenant that was never deleted has no erasure to wait for.
func Wait(ctx context.Context, c Client, adminURL, token string, t Target, every time.Duration,
	progress func(*Deletion)) (*Deletion, error) {
	for {
		d, err := Find(ctx, c, adminURL, token)
		if err != nil {
			return nil, err
		}
		if d == nil {
			return nil, fmt.Errorf("no deletion record for tenant %q: it was never deleted, or the token is not spelled as stored", token)
		}
		if Reached(d, t) {
			return d, nil
		}
		if progress != nil {
			progress(d)
		}
		select {
		case <-ctx.Done():
			return d, fmt.Errorf("gave up waiting for tenant %q to be %s: %w", token, t, ctx.Err())
		case <-time.After(every):
		}
	}
}

// Print renders one deletion.
func Print(w io.Writer, d *Deletion) {
	state := "NOT ERASED"
	switch {
	case d.CompletedAt != nil:
		state = "COMPLETE (data erased, token released)"
	case Reached(d, TargetErased):
		state = "ERASED (token still held)"
	}
	fmt.Fprintf(w, "tenant %s (deleted %s): %s\n", d.Token, d.Epoch, state)
	if d.ErasedAt != nil {
		fmt.Fprintf(w, "  erased at:     %s\n", *d.ErasedAt)
	}
	if d.CompletedAt != nil {
		fmt.Fprintf(w, "  completed at:  %s\n", *d.CompletedAt)
	} else {
		wait := d.Awaiting
		if d.ElapsesAt != nil {
			wait += " until " + *d.ElapsesAt
		}
		fmt.Fprintf(w, "  waiting on:    %s\n", wait)
	}
	fmt.Fprintf(w, "  rows erased:   %d\n", d.RowsErased)
	tw := tabwriter.NewWriter(w, 2, 0, 2, ' ', 0)
	for _, s := range d.Stores {
		st := "clean"
		if !s.Complete {
			st = "NOT clean"
		}
		fmt.Fprintf(tw, "  store\t%s\t%s\n", s.Store, st)
	}
	_ = tw.Flush()
	if len(d.BlockedBy) > 0 {
		fmt.Fprintf(w, "  blocked by:\n    %s\n", strings.Join(d.BlockedBy, "\n    "))
	}
}
