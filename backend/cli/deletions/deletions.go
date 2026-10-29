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
	"errors"
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
	Store    string  `json:"store"`
	Complete bool    `json:"complete"`
	Note     *string `json:"note"`
}

// Deletion is one record as read.
type Deletion struct {
	Token       string   `json:"token"`
	Epoch       string   `json:"epoch"`
	ErasedAt    *string  `json:"erasedAt"`
	VerifiedAt  *string  `json:"lastVerifiedAt"`
	Erased      bool     `json:"erased"`
	CompletedAt *string  `json:"completedAt"`
	RowsErased  int32    `json:"rowsErased"`
	Awaiting    string   `json:"awaiting"`
	ElapsesAt   *string  `json:"elapsesAt"`
	BlockedBy   []string `json:"blockedBy"`
	Stores      []Store  `json:"stores"`
}

const deletionFields = `token epoch erasedAt lastVerifiedAt erased completedAt rowsErased awaiting elapsesAt blockedBy
      stores { store complete note }`

const tenantsQuery = `query { tenants { token purgeState purgeEpoch } }`

const oneQuery = `query TenantDeletion($token: String!, $epoch: String) {
  tenantDeletion(token: $token, epoch: $epoch) {
      ` + deletionFields + `
  }
}`

const completedQuery = `query TenantDeletions($criteria: TenantDeletionSearchCriteria!) {
  tenantDeletions(criteria: $criteria) {
    results {
      ` + deletionFields + `
    }
    pagination { totalRecords }
  }
}`

type tenantRow struct {
	Token      string  `json:"token"`
	PurgeState string  `json:"purgeState"`
	PurgeEpoch *string `json:"purgeEpoch"`
}

const pageSize = 200

// ErrNotDeleting is returned for a token whose current tenant is live: it is not being deleted,
// so there is no erasure to report, and a predecessor's finished record is not that tenant's.
var ErrNotDeleting = errors.New("the tenant is active and is not being deleted")

// Lookup reads the deletion of a token, bound to ONE epoch.
//
// A TOKEN IS REUSABLE, SO A TOKEN ALONE DOES NOT NAME A DELETION. "The newest record for
// acme" can be the PREVIOUS acme's completed one, and reporting it would tell a caller that a
// tenant whose data has not been touched is erased. So this never picks a record by token:
//
//   - If the tenant exists, it must be purging, and the deletion is the one at ITS current
//     purgeEpoch. A purging tenant always has its own record from the moment it was deleted;
//     if the lookup at that epoch finds none, the answer is "no record", never an older one.
//   - If epoch is given (the one printed when the deletion was reported) the deletion is
//     exactly that one, whatever has happened to the token since.
//   - If the tenant is gone and no epoch is given, the newest COMPLETED deletion of the token
//     is reported, with its epoch printed so the caller can see which one it is.
//
// It returns nil, nil only when no record exists at the epoch it bound to.
func Lookup(ctx context.Context, c Client, adminURL, token, epoch string) (*Deletion, error) {
	if epoch == "" {
		var out struct {
			Tenants []tenantRow `json:"tenants"`
		}
		if err := c.Query(ctx, adminURL, tenantsQuery, nil, &out); err != nil {
			return nil, fmt.Errorf("reading tenants: %w", err)
		}
		for _, t := range out.Tenants {
			if t.Token != token {
				continue
			}
			if t.PurgeState != "purging" || t.PurgeEpoch == nil {
				return nil, fmt.Errorf("%w: tenant %q is %q", ErrNotDeleting, token, t.PurgeState)
			}
			epoch = *t.PurgeEpoch
		}
	}
	if epoch == "" {
		return newestCompleted(ctx, c, adminURL, token)
	}
	var out struct {
		TenantDeletion *Deletion `json:"tenantDeletion"`
	}
	if err := c.Query(ctx, adminURL, oneQuery, map[string]any{"token": token, "epoch": epoch}, &out); err != nil {
		return nil, fmt.Errorf("reading the deletion of %q at %s: %w", token, epoch, err)
	}
	d := out.TenantDeletion
	if d == nil {
		return nil, nil
	}
	// Belt to the server's exact match: never accept a record at another epoch.
	if !sameInstant(d.Epoch, epoch) || d.Token != token {
		return nil, fmt.Errorf("the server answered with the deletion of %q at %s, not %s", d.Token, d.Epoch, epoch)
	}
	return d, nil
}

func sameInstant(a, b string) bool {
	ta, err1 := time.Parse(time.RFC3339Nano, a)
	tb, err2 := time.Parse(time.RFC3339Nano, b)
	return err1 == nil && err2 == nil && ta.Equal(tb)
}

func newestCompleted(ctx context.Context, c Client, adminURL, token string) (*Deletion, error) {
	for p := 1; ; p++ {
		var out struct {
			TenantDeletions struct {
				Results    []Deletion `json:"results"`
				Pagination struct {
					TotalRecords int32 `json:"totalRecords"`
				} `json:"pagination"`
			} `json:"tenantDeletions"`
		}
		err := c.Query(ctx, adminURL, completedQuery, map[string]any{
			"criteria": map[string]any{"pageNumber": p, "pageSize": pageSize, "completed": true},
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
func Wait(ctx context.Context, c Client, adminURL, token, epoch string, t Target, every time.Duration,
	progress func(*Deletion)) (*Deletion, error) {
	for {
		d, err := Lookup(ctx, c, adminURL, token, epoch)
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
		state = "ERASED (token still held; withdrawn if data reappears, final at completion)"
	}
	fmt.Fprintf(w, "tenant %s (deleted %s): %s\n", d.Token, d.Epoch, state)
	if d.ErasedAt != nil {
		fmt.Fprintf(w, "  erased at:     %s\n", *d.ErasedAt)
		if d.CompletedAt == nil {
			ver := "never confirmed"
			if d.VerifiedAt != nil {
				ver = *d.VerifiedAt
			}
			fmt.Fprintf(w, "  last verified: %s\n", ver)
		}
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
		note := ""
		if s.Note != nil && *s.Note != "" {
			note = "note: " + *s.Note
		}
		fmt.Fprintf(tw, "  store\t%s\t%s\t%s\n", s.Store, st, note)
	}
	_ = tw.Flush()
	if len(d.BlockedBy) > 0 {
		fmt.Fprintf(w, "  blocked by:\n    %s\n", strings.Join(d.BlockedBy, "\n    "))
	}
}
