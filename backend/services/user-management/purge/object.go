// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package purge

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/devicechain-io/dc-microservice/blob"
	"github.com/devicechain-io/dc-user-management/iam"
)

// Object erases a tenant's uploaded assets from the object store.
//
// # It is driven from Postgres, because the object store cannot be asked
//
// blob.Store has Put, Open, URL, Delete and Stat — and deliberately no List. There is no
// way to ask it "what does this tenant have", so the work list has to come from the rows
// that NAME the objects, which is the one place a reference is recorded.
//
// 🔑 THE ROW OUTLIVES THE SWEEP, WHICH IS WHY THIS WORKS AT ALL. iam_tenants is exempt from
// the relational sweep — it holds the token reservation that stops a successor being
// created mid-purge, so it is removed by the COMPLETION step after every store has
// reported clean. That ordering is what lets this store read the reference on every pass
// including the last one. It is not a coincidence to rely on quietly: were iam_tenants
// ever swept like an ordinary table, this store would go looking for its work list after
// something else had deleted it, find nothing, and report clean over a live object.
//
// # 🔴 What a row-driven work list cannot see: an ORPHAN
//
// An object whose reference was already lost is invisible here, and one way to produce one
// is ordinary: replacing a logo deletes the old object best-effort, warning rather than
// failing, so a delete that fails leaves an object no row names. Nothing will ever find it
// again — not this store, and not a later one.
//
// It is stated rather than deferred because deferring it would block every purge on every
// instance forever over a case that usually does not exist, and a deferral that is always
// present stops being read. The real fix is a prefix sweep, which the key layout already
// supports — every object is at `{instance}/{tenant}/{purpose}/{id}`, so a tenant's objects
// share a prefix — but blob.Store has no List, and giving it one means widening the S3
// client interface that deliberately exposes only four operations. That is a change worth
// making on its own, not smuggled in here.
//
// # 🔴 The reference column is polymorphic
//
// branding_logo holds EITHER a blob:// ref or a Tier-0 value the platform never stored: an
// https URL to someone else's server, or an inline data: URI. Deleting is only meaningful
// for the first. blob.ParseRef is the discriminator, and a parse failure here is not an
// error — it is the ordinary case for a tenant that pasted a URL.
type Object struct {
	store  blob.Store
	tenant tenantReader
}

// tenantReader is the row access this store needs, narrowed to what it uses.
//
// It WRITES as well as reads, and that is the fix for a livelock rather than a convenience
// — see Erase. The exemption bars the SWEEP from deleting iam_tenants; it does not bar the
// store that owns a reference column from clearing it once the object behind it is gone.
type tenantReader interface {
	TenantByToken(ctx context.Context, token string) (*iam.Tenant, error)
	UpdateTenantFields(ctx context.Context, t *iam.Tenant, fields map[string]any) error
}

// objectRef is one object this tenant still has, and the column that names it.
//
// The column travels with the ref because clearing it is part of erasing: it is what makes
// the next pass find nothing.
type objectRef struct {
	column string
	ref    blob.Ref
}

// NewObject builds the object store. store may be nil, which is what an instance that has
// not configured object storage has — see Erase for why that is not automatically clean.
func NewObject(store blob.Store, tenant tenantReader) *Object {
	return &Object{store: store, tenant: tenant}
}

func (o *Object) Name() string { return StoreObject }

// Erase deletes whatever objects the tenant's rows still name.
//
// # An unconfigured store is not the same as an empty one
//
// blob.Store is nil on an instance that never mounted object storage, and the honest
// answer then depends on what the rows say. No reference means nothing was ever uploaded
// and there is nothing to erase — clean. A live blob:// reference means the tenant DOES
// have an object and this process cannot reach the store that holds it, which is a
// retention, not a success. Reporting the second as clean would be the reassuring-silence
// failure the Pending type exists to prevent, one layer down.
//
// # 🔴 THE REFERENCE IS CLEARED, AND WITHOUT THAT THIS STORE LIVELOCKS
//
// Every other store's work list shrinks as it works: the sweep deletes rows, the broker
// purges messages, and the next pass genuinely finds nothing. This one's does not. The
// column is exempt from the sweep and survives to completion — which is what makes the work
// list readable at all — and blob.Delete is idempotent-SILENT by contract, so deleting an
// object that is already gone succeeds. Leave it there and every pass reports one row
// erased, forever.
//
// That is not merely untidy, it is a purge that can never complete. The coordinator treats
// rows erased as evidence the store has NOT gone quiet and restarts the settle window — a
// deliberate rule, and the right one, because rows arriving is exactly what "not settled"
// means. The two combine into a tenant that reports clean on every pass and never finishes,
// logging the most reassuring line the coordinator has while doing it.
//
// So a successful delete clears the column that named the object, per reference, and the
// next pass has nothing to find. That also makes a partial failure exact: with two
// references and the second failing, the first is already cleared, so the retry deletes
// only what is left rather than counting the first again.
//
// The Store contract states the requirement this satisfies in one line — "the second call
// has nothing to delete and must succeed, reporting zero rows".
func (o *Object) Erase(ctx context.Context, tenant string, _ time.Time) (Outcome, error) {
	refs, err := o.refsFor(ctx, tenant)
	if err != nil {
		return Outcome{}, err
	}
	if len(refs) == 0 {
		return Outcome{}, nil
	}
	if o.store == nil {
		return Outcome{Deferred: []string{fmt.Sprintf(
			"the object store still holds %d of this tenant's uploaded asset(s), and this instance "+
				"has no object storage configured — so the reference is known and the object cannot "+
				"be reached from here. Configure the backend that stored it, or delete the object "+
				"out of band, before treating this tenant's data as erased", len(refs))}}, nil
	}

	row, err := o.tenant.TenantByToken(ctx, tenant)
	if err != nil {
		return Outcome{}, fmt.Errorf("re-reading %q to clear its object references: %w", tenant, err)
	}

	var erased int64
	for _, r := range refs {
		if err := o.store.Delete(ctx, r.ref); err != nil {
			return Outcome{Rows: erased}, fmt.Errorf("deleting %s for %q: %w", r.ref, tenant, err)
		}
		// Clear before counting: a count that outlived the reference would be the
		// double-count this ordering exists to prevent.
		if err := o.tenant.UpdateTenantFields(ctx, row, map[string]any{r.column: nil}); err != nil {
			return Outcome{Rows: erased}, fmt.Errorf("clearing %s for %q after deleting %s: %w",
				r.column, tenant, r.ref, err)
		}
		erased++
	}
	return Outcome{Rows: erased}, nil
}

// refsFor returns every object this tenant's rows name.
//
// A tenant row that has already gone is not an error: the coordinator re-reads the
// lifecycle before each pass, but a peer replica can complete a purge in between, and a
// tenant with no row has no references left to follow.
func (o *Object) refsFor(ctx context.Context, tenant string) ([]objectRef, error) {
	t, err := o.tenant.TenantByToken(ctx, tenant)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %q to find its objects: %w", tenant, err)
	}

	var refs []objectRef
	// One column today. It is a loop rather than a line because the second consumer of the
	// object store — the package doc names firmware and exports — adds a reference
	// somewhere else, and a purge that erased only the one someone remembered is the shape
	// this whole subsystem is built to avoid.
	for _, c := range []struct {
		column string
		value  *string
	}{
		{brandingLogoColumn, t.BrandingLogo},
	} {
		if c.value == nil || *c.value == "" {
			continue
		}
		ref, err := blob.ParseRef(*c.value)
		if err != nil {
			// A Tier-0 https:/data: value: the platform never stored it, so there is
			// nothing of ours to delete. Not an error, and not a retention.
			continue
		}
		refs = append(refs, objectRef{column: c.column, ref: ref})
	}
	return refs, nil
}

// brandingLogoColumn is the database column behind iam.Tenant.BrandingLogo. It is spelled
// out because it is written through UpdateTenantFields, which takes column names rather
// than struct fields.
const brandingLogoColumn = "branding_logo"
