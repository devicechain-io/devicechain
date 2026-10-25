// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package blob

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/require"
)

func putFS(t *testing.T, s Store, tenant, purpose, id string) Ref {
	t.Helper()
	ref, err := s.Put(context.Background(), Key{Tenant: tenant, Purpose: purpose, ID: id},
		bytes.NewReader([]byte(id)), PutOptions{})
	require.NoError(t, err)
	return ref
}

// Every object of the tenant goes, whatever purpose stored it and whether or not a row
// names it; another tenant's objects and the instance slot are untouched; a second call
// finds nothing.
func TestFilesystemDeleteTenantErasesEveryPurposeAndOnlyThatTenant(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := NewFilesystemStore(Config{Backend: BackendFilesystem, Directory: dir}, "inst")
	require.NoError(t, err)

	putFS(t, s, "acme", "artifacts", "a1")
	putFS(t, s, "acme", "artifacts", "a2")
	putFS(t, s, "acme", "branding-logo", "logo.png")
	keep := putFS(t, s, "globex", "artifacts", "g1")
	inst := putFS(t, s, "", "branding-logo", "default.png")
	// A tenant whose token is a prefix of another's must not take the other with it.
	longer := putFS(t, s, "acme2", "artifacts", "x")

	n, err := s.DeleteTenant(ctx, "acme")
	require.NoError(t, err)
	require.EqualValues(t, 3, n)
	_, err = os.Stat(filepath.Join(dir, "inst", "acme"))
	require.ErrorIs(t, err, os.ErrNotExist, "the tenant's directory is gone")

	for _, ref := range []Ref{keep, inst, longer} {
		_, err := s.Stat(ctx, ref)
		require.NoError(t, err, "%s must survive", ref)
	}

	n, err = s.DeleteTenant(ctx, "acme")
	require.NoError(t, err)
	require.Zero(t, n, "a second pass has nothing to erase")
}

func TestDeleteTenantRefusesTheInstanceSlotAndBadSegments(t *testing.T) {
	s, err := NewFilesystemStore(Config{Backend: BackendFilesystem, Directory: t.TempDir()}, "inst")
	require.NoError(t, err)
	for _, tenant := range []string{"", instanceScopeSegment, "..", "a/b", ".hidden"} {
		_, err := s.DeleteTenant(context.Background(), tenant)
		require.Error(t, err, "tenant %q", tenant)
	}
}

func TestS3DeleteTenantPagesThroughThePrefix(t *testing.T) {
	f := &fakeS3{listPages: []*s3.ListObjectsV2Output{
		{
			Contents:              []s3types.Object{{Key: aws.String("inst1/acme/artifacts/a1")}, {Key: aws.String("inst1/acme/branding-logo/l.png")}},
			IsTruncated:           aws.Bool(true),
			NextContinuationToken: aws.String("page-2"),
		},
		{Contents: []s3types.Object{{Key: aws.String("inst1/acme/artifacts/a2")}}, IsTruncated: aws.Bool(false)},
	}}
	n, err := newS3(f).DeleteTenant(context.Background(), "acme")
	require.NoError(t, err)
	require.EqualValues(t, 3, n)
	require.Equal(t, []string{"inst1/acme/artifacts/a1", "inst1/acme/branding-logo/l.png", "inst1/acme/artifacts/a2"}, f.delKeys)
	require.Len(t, f.listIns, 2)
	require.Equal(t, "inst1/acme/", aws.ToString(f.listIns[0].Prefix), "the trailing slash keeps acme2 out")
	require.Equal(t, "page-2", aws.ToString(f.listIns[1].ContinuationToken))
}

func TestS3DeleteTenantBatchesAPageIntoFewRequests(t *testing.T) {
	var contents []s3types.Object
	for i := 0; i < 2500; i++ {
		contents = append(contents, s3types.Object{Key: aws.String(fmt.Sprintf("inst1/acme/artifacts/a%04d", i))})
	}
	f := &fakeS3{listPages: []*s3.ListObjectsV2Output{{Contents: contents}}}
	n, err := newS3(f).DeleteTenant(context.Background(), "acme")
	require.NoError(t, err)
	require.EqualValues(t, 2500, n)
	require.Len(t, f.delBatches, 3, "a page larger than the API limit is split, never sent whole")
	require.Len(t, f.delBatches[0], 1000)
	require.Len(t, f.delBatches[2], 500)
	require.Len(t, f.delKeys, 2500)
	require.Equal(t, []bool{true, true, true}, f.batchQuiet)
}

// The request succeeds and names the keys it could not delete. Reporting that as a clean
// erasure is the failure this guards: the count must exclude the failed keys and the error
// must name one.
func TestS3DeleteTenantPartialBatchFailureIsAnError(t *testing.T) {
	f := &fakeS3{
		listPages: []*s3.ListObjectsV2Output{{Contents: []s3types.Object{
			{Key: aws.String("inst1/acme/artifacts/a1")},
			{Key: aws.String("inst1/acme/artifacts/a2")},
			{Key: aws.String("inst1/acme/artifacts/a3")},
		}}},
		batchFail: map[string]string{"inst1/acme/artifacts/a2": "AccessDenied"},
	}
	n, err := newS3(f).DeleteTenant(context.Background(), "acme")
	require.Error(t, err)
	require.EqualValues(t, 2, n, "only the confirmed deletions are counted")
	require.Contains(t, err.Error(), "inst1/acme/artifacts/a2")
	require.Contains(t, err.Error(), "AccessDenied")
	require.Contains(t, err.Error(), "1 of 3")
}

func TestS3DeleteTenantStopsAtAFailedPage(t *testing.T) {
	f := &fakeS3{
		listPages: []*s3.ListObjectsV2Output{
			{
				Contents:              []s3types.Object{{Key: aws.String("inst1/acme/artifacts/a1")}},
				IsTruncated:           aws.Bool(true),
				NextContinuationToken: aws.String("page-2"),
			},
			{Contents: []s3types.Object{{Key: aws.String("inst1/acme/artifacts/a2")}}},
		},
		batchFail: map[string]string{"inst1/acme/artifacts/a1": "InternalError"},
	}
	_, err := newS3(f).DeleteTenant(context.Background(), "acme")
	require.Error(t, err)
	require.Len(t, f.listIns, 1, "a failed page is not walked past: the pass reports it and is retried")
}

func TestS3DeleteTenantWholeRequestFailureIsAnError(t *testing.T) {
	f := &fakeS3{
		listPages: []*s3.ListObjectsV2Output{{Contents: []s3types.Object{{Key: aws.String("inst1/acme/artifacts/a1")}}}},
		batchErr:  errors.New("connection reset"),
	}
	n, err := newS3(f).DeleteTenant(context.Background(), "acme")
	require.Error(t, err)
	require.Zero(t, n)
}

func TestS3DeleteTenantValidatesTheWholePageBeforeDeletingAny(t *testing.T) {
	f := &fakeS3{listPages: []*s3.ListObjectsV2Output{{Contents: []s3types.Object{
		{Key: aws.String("inst1/acme/artifacts/a1")},
		{Key: aws.String("inst1/globex/artifacts/g1")},
	}}}}
	_, err := newS3(f).DeleteTenant(context.Background(), "acme")
	require.Error(t, err)
	require.Empty(t, f.delKeys, "an out-of-prefix key anywhere in the page deletes nothing of the page")
}

func TestS3DeleteTenantRefusesAKeyOutsideThePrefix(t *testing.T) {
	f := &fakeS3{listPages: []*s3.ListObjectsV2Output{
		{Contents: []s3types.Object{{Key: aws.String("inst1/globex/artifacts/g1")}}},
	}}
	_, err := newS3(f).DeleteTenant(context.Background(), "acme")
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "outside its prefix"))
	require.Empty(t, f.delKeys, "nothing outside the tenant is deleted")
}
