// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package blob

import (
	"bytes"
	"context"
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

func TestS3DeleteTenantRefusesAKeyOutsideThePrefix(t *testing.T) {
	f := &fakeS3{listPages: []*s3.ListObjectsV2Output{
		{Contents: []s3types.Object{{Key: aws.String("inst1/globex/artifacts/g1")}}},
	}}
	_, err := newS3(f).DeleteTenant(context.Background(), "acme")
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "outside its prefix"))
	require.Empty(t, f.delKeys, "nothing outside the tenant is deleted")
}
