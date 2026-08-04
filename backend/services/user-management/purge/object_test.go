// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package purge

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-microservice/blob"
	"github.com/devicechain-io/dc-user-management/iam"
)

// stubTenants is the tenant row, and it is STATEFUL on purpose.
//
// 🔴 A STATELESS FAKE HERE CANNOT EXPRESS THE ONE THING THAT MATTERS. This store's work
// list does not shrink on its own — the column is exempt from the sweep and survives to
// completion — so the only way the second pass finds nothing is if the first pass cleared
// it. A fake that returned the same row forever, or ignored the write, would make a store
// that never clears look identical to one that does, which is how the livelock this models
// got committed in the first place.
type stubTenants struct {
	logo    *string
	err     error
	updates int
}

func (s *stubTenants) TenantByToken(context.Context, string) (*iam.Tenant, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &iam.Tenant{BrandingLogo: s.logo}, nil
}

func (s *stubTenants) UpdateTenantFields(_ context.Context, _ *iam.Tenant, fields map[string]any) error {
	s.updates++
	if v, ok := fields[brandingLogoColumn]; ok && v == nil {
		s.logo = nil
	}
	return nil
}

// stubBlobs records what was deleted. Every other method panics: this store's whole job is
// Delete, and a change that made it read or write an object should say so loudly rather
// than be absorbed by a fake that quietly answers.
//
// 🔴 IT HONOURS blob.Store's IDEMPOTENT-SILENT DELETE, which is the contract dimension that
// matters most here: deleting an object that is not there SUCCEEDS, on both real backends.
// A fake that errored on the second delete would hide the livelock by making the store look
// like it noticed, when in production nothing notices at all.
type stubBlobs struct {
	deleted []blob.Ref
	present map[blob.Ref]bool
	err     error
}

func (s *stubBlobs) Delete(_ context.Context, ref blob.Ref) error {
	if s.err != nil {
		return s.err
	}
	if s.present != nil {
		delete(s.present, ref)
	}
	s.deleted = append(s.deleted, ref)
	return nil
}

func (s *stubBlobs) Put(context.Context, blob.Key, io.Reader, blob.PutOptions) (blob.Ref, error) {
	panic("the purge wrote to the object store")
}

func (s *stubBlobs) Open(context.Context, blob.Ref) (io.ReadCloser, blob.Info, error) {
	panic("the purge read an object it was erasing")
}

func (s *stubBlobs) URL(context.Context, blob.Ref, blob.URLOptions) (string, time.Time, error) {
	panic("the purge minted a URL for an object it was erasing")
}

func (s *stubBlobs) Stat(context.Context, blob.Ref) (blob.Info, error) {
	panic("the purge stat'd an object instead of deleting it")
}

func ptr(s string) *string { return &s }

// TestAStoredLogoIsDeleted is the ordinary case, and it goes through blob.ParseRef rather
// than a hand-made Ref so the test cannot pass on a ref shape the platform never writes.
func TestAStoredLogoIsDeleted(t *testing.T) {
	ref := blob.Ref{Backend: "filesystem", Key: "inst/acme/branding-logo/abc.png"}
	blobs := &stubBlobs{}
	store := NewObject(blobs, &stubTenants{logo: ptr(ref.String())})

	out, err := store.Erase(context.Background(), "acme", time.Now())

	require.NoError(t, err)
	assert.True(t, out.Clean())
	assert.Equal(t, int64(1), out.Rows)
	require.Len(t, blobs.deleted, 1)
	assert.Equal(t, ref, blobs.deleted[0], "the ref must survive the round trip through the column")
}

// TestATierZeroLogoIsNotAnObjectAndNotAFailure pins the polymorphic column.
//
// branding_logo holds EITHER a blob:// ref or a Tier-0 value the platform never stored —
// an https URL to someone else's server, or an inline data: URI. Treating a parse failure
// as an error would make every tenant that pasted a URL block its own purge forever;
// treating it as an object would ask the store to delete something that is not there.
func TestATierZeroLogoIsNotAnObjectAndNotAFailure(t *testing.T) {
	for name, value := range map[string]string{
		"an external https URL": "https://cdn.example.com/acme.png",
		"an inline data URI":    "data:image/png;base64,iVBORw0KGgo=",
		"empty":                 "",
	} {
		t.Run(name, func(t *testing.T) {
			blobs := &stubBlobs{}
			out, err := NewObject(blobs, &stubTenants{logo: ptr(value)}).
				Erase(context.Background(), "acme", time.Now())

			require.NoError(t, err)
			assert.True(t, out.Clean())
			assert.Zero(t, out.Rows)
			assert.Empty(t, blobs.deleted, "nothing of ours was ever stored, so nothing is deleted")
		})
	}
}

// TestAnUnconfiguredStoreWithALiveReferenceDefersRatherThanAcking is the assertion that
// keeps this store honest.
//
// 🔴 blob.Store IS NIL ON AN INSTANCE THAT NEVER MOUNTED OBJECT STORAGE, and the tempting
// reading is "no store, nothing to erase". That is right only when the rows agree. A
// tenant holding a live blob:// reference DOES have an object, and this process cannot
// reach the store that holds it — so reporting clean would release the token over an
// asset that is still sitting on a disk somewhere, which is the reassuring silence the
// whole store-shaped design exists to prevent.
func TestAnUnconfiguredStoreWithALiveReferenceDefersRatherThanAcking(t *testing.T) {
	ref := blob.Ref{Backend: "s3", Key: "inst/acme/branding-logo/abc.png"}

	out, err := NewObject(nil, &stubTenants{logo: ptr(ref.String())}).
		Erase(context.Background(), "acme", time.Now())

	require.NoError(t, err, "an unreachable store is a stated gap, not a transient failure")
	assert.False(t, out.Clean(),
		"the reference is live and the object cannot be reached from here, so this purge must "+
			"not be allowed to complete")
	assert.Contains(t, out.Reason(), "object storage")

	// The other half: with no reference there is genuinely nothing, and a nil store must
	// not block a purge that has nothing to do. Without this the guard above would be
	// satisfied by a store that defers unconditionally, on every instance, forever.
	clean, err := NewObject(nil, &stubTenants{}).Erase(context.Background(), "acme", time.Now())
	require.NoError(t, err)
	assert.True(t, clean.Clean(), "nothing was ever uploaded; a nil store is the correct state")
}

// TestAMissingTenantRowIsNotAnError covers the race the coordinator leaves open on purpose:
// it re-reads the lifecycle before each pass, but a peer replica can complete the purge in
// between. A tenant with no row has no references left to follow.
func TestAMissingTenantRowIsNotAnError(t *testing.T) {
	out, err := NewObject(&stubBlobs{}, &stubTenants{err: gorm.ErrRecordNotFound}).
		Erase(context.Background(), "acme", time.Now())

	require.NoError(t, err)
	assert.True(t, out.Clean())
}

// TestADeleteFailureIsRetryableRatherThanClean pins the direction the error goes.
//
// A backend that refuses a delete has NOT erased the object. Swallowing that would let the
// purge settle and release the token over an asset that is still there.
func TestADeleteFailureIsRetryableRatherThanClean(t *testing.T) {
	ref := blob.Ref{Backend: "s3", Key: "inst/acme/branding-logo/abc.png"}
	blobs := &stubBlobs{err: errors.New("access denied")}

	out, err := NewObject(blobs, &stubTenants{logo: ptr(ref.String())}).
		Erase(context.Background(), "acme", time.Now())

	require.Error(t, err, "the object is still there; the ledger records this as retryable")
	assert.Zero(t, out.Rows, "nothing was erased, and the record must not imply otherwise")
}

// TestASecondPassFindsNothingAndReportsZero is the test whose absence let a GA-blocking
// livelock ship, and it is worth being explicit about the shape.
//
// 🔴 THIS STORE'S WORK LIST DOES NOT SHRINK BY ITSELF. Every other store's does: the sweep
// deletes rows, the broker purges messages, so the next pass genuinely finds nothing. Here
// the column is exempt from the sweep and survives until completion — that is what makes it
// readable at all — and blob.Delete succeeds on an object that is already gone. So without
// clearing the reference, every pass reports one row erased.
//
// That is not untidiness. The coordinator restarts the settle window whenever a store
// reports rows, deliberately, because rows arriving is what "not settled" means. A store
// that reports one row forever therefore never settles: the tenant reports CLEAN on every
// pass, logs the most reassuring line the coordinator has, and never completes. The token
// is never released.
//
// Two passes, and the second must be silent.
func TestASecondPassFindsNothingAndReportsZero(t *testing.T) {
	ref := blob.Ref{Backend: "filesystem", Key: "inst/acme/branding-logo/abc.png"}
	blobs := &stubBlobs{present: map[blob.Ref]bool{ref: true}}
	tenants := &stubTenants{logo: ptr(ref.String())}
	store := NewObject(blobs, tenants)

	first, err := store.Erase(context.Background(), "acme", time.Now())
	require.NoError(t, err)
	require.Equal(t, int64(1), first.Rows, "the first pass erases the object")
	require.False(t, blobs.present[ref], "and the object is actually gone")

	second, err := store.Erase(context.Background(), "acme", time.Now())

	require.NoError(t, err)
	assert.True(t, second.Clean())
	assert.Zerof(t, second.Rows,
		"the second pass reported %d row(s) erased over an object that was already gone. The "+
			"coordinator reads that as the store still receiving work and restarts the settle "+
			"window, so this tenant's purge can never complete and its token is never released",
		second.Rows)
	assert.Len(t, blobs.deleted, 1, "and nothing was asked of the object store a second time")
}

// TestTheReferenceIsClearedOnlyAfterTheObjectIsGone pins the ordering.
//
// Clearing first would lose the only record of an object that is still there — the work
// list is the column, so a cleared column with a live object is an orphan this store can
// never find again. The delete has to succeed first.
func TestTheReferenceIsClearedOnlyAfterTheObjectIsGone(t *testing.T) {
	ref := blob.Ref{Backend: "s3", Key: "inst/acme/branding-logo/abc.png"}
	tenants := &stubTenants{logo: ptr(ref.String())}
	blobs := &stubBlobs{err: errors.New("access denied")}

	_, err := NewObject(blobs, tenants).Erase(context.Background(), "acme", time.Now())

	require.Error(t, err)
	assert.Zero(t, tenants.updates,
		"the delete failed, so the reference must survive — clearing it would strand the object "+
			"with nothing left to find it by")
	assert.NotNil(t, tenants.logo)
}
