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

// tenantReader is the row lookup this store needs, narrowed to one method.
type tenantReader interface {
	TenantByToken(ctx context.Context, token string) (*iam.Tenant, error)
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
// # A missing object is success, not failure
//
// Delete is idempotent by contract, and this runs on every pass until the purge completes,
// so the second pass necessarily deletes nothing. A backend that reports "not found" is
// telling us the erasure already holds.
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

	var erased int64
	for _, ref := range refs {
		if err := o.store.Delete(ctx, ref); err != nil {
			return Outcome{Rows: erased}, fmt.Errorf("deleting %s for %q: %w", ref, tenant, err)
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
func (o *Object) refsFor(ctx context.Context, tenant string) ([]blob.Ref, error) {
	t, err := o.tenant.TenantByToken(ctx, tenant)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %q to find its objects: %w", tenant, err)
	}

	var refs []blob.Ref
	// One column today. It is a loop rather than a line because the second consumer of the
	// object store — the package doc names firmware and exports — adds a reference
	// somewhere else, and a purge that erased only the one someone remembered is the shape
	// this whole subsystem is built to avoid.
	for _, candidate := range []*string{t.BrandingLogo} {
		if candidate == nil || *candidate == "" {
			continue
		}
		ref, err := blob.ParseRef(*candidate)
		if err != nil {
			// A Tier-0 https:/data: value: the platform never stored it, so there is
			// nothing of ours to delete. Not an error, and not a retention.
			continue
		}
		refs = append(refs, ref)
	}
	return refs, nil
}
