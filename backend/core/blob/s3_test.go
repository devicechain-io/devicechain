// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package blob

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"
)

// fakeS3 records the inputs it is called with and returns canned outputs/errors, so
// the store's key mapping, error mapping, and metadata mapping are testable without
// a live endpoint.
type fakeS3 struct {
	putIn   *s3.PutObjectInput
	putBody []byte
	putErr  error

	getOut *s3.GetObjectOutput
	getErr error

	headOut *s3.HeadObjectOutput
	headErr error

	delIn  *s3.DeleteObjectInput
	delErr error
}

func (f *fakeS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.putIn = in
	if in.Body != nil {
		f.putBody, _ = io.ReadAll(in.Body)
	}
	return &s3.PutObjectOutput{}, f.putErr
}

func (f *fakeS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.getOut, nil
}

func (f *fakeS3) HeadObject(_ context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	if f.headErr != nil {
		return nil, f.headErr
	}
	return f.headOut, nil
}

func (f *fakeS3) DeleteObject(_ context.Context, in *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	f.delIn = in
	return &s3.DeleteObjectOutput{}, f.delErr
}

func newS3(f *fakeS3) *s3Store {
	return &s3Store{api: f, bucket: "test-bucket", instanceID: "inst1"}
}

func TestS3PutBuildsKeyAndBody(t *testing.T) {
	f := &fakeS3{}
	s := newS3(f)
	data := []byte("logo-bytes")
	ref, err := s.Put(context.Background(),
		Key{Tenant: "acme", Purpose: "branding-logo", ID: "logo.png"},
		bytes.NewReader(data), PutOptions{ContentType: "image/png", MaxSize: 1024})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if ref.Backend != BackendS3 || ref.Key != "inst1/acme/branding-logo/logo.png" {
		t.Fatalf("ref = %+v", ref)
	}
	if aws.ToString(f.putIn.Bucket) != "test-bucket" || aws.ToString(f.putIn.Key) != "inst1/acme/branding-logo/logo.png" {
		t.Fatalf("put target = %s/%s", aws.ToString(f.putIn.Bucket), aws.ToString(f.putIn.Key))
	}
	if aws.ToString(f.putIn.ContentType) != "image/png" {
		t.Fatalf("content-type = %q", aws.ToString(f.putIn.ContentType))
	}
	if aws.ToInt64(f.putIn.ContentLength) != int64(len(data)) {
		t.Fatalf("content-length = %d, want %d", aws.ToInt64(f.putIn.ContentLength), len(data))
	}
	if !bytes.Equal(f.putBody, data) {
		t.Fatalf("body = %q, want %q", f.putBody, data)
	}
}

func TestS3PutMaxSize(t *testing.T) {
	f := &fakeS3{}
	s := newS3(f)
	_, err := s.Put(context.Background(),
		Key{Tenant: "t", Purpose: "branding-logo", ID: "big.png"},
		bytes.NewReader(bytes.Repeat([]byte("a"), 100)), PutOptions{MaxSize: 10})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Put over limit = %v, want ErrTooLarge", err)
	}
	if f.putIn != nil {
		t.Fatal("PutObject must not be called for an over-limit body")
	}
}

func TestS3MaxSizeSentinelNoTruncate(t *testing.T) {
	// A MaxInt64 "effectively unlimited" MaxSize must not overflow limit+1 and
	// commit a zero-length object.
	f := &fakeS3{}
	s := newS3(f)
	data := []byte("hello-not-truncated")
	if _, err := s.Put(context.Background(),
		Key{Tenant: "t", Purpose: "branding-logo", ID: "x.png"},
		bytes.NewReader(data), PutOptions{MaxSize: math.MaxInt64}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !bytes.Equal(f.putBody, data) {
		t.Fatalf("MaxInt64 MaxSize stored %q, want %q", f.putBody, data)
	}
}

func TestS3OpenInfoAndBody(t *testing.T) {
	mod := time.Unix(1_700_000_000, 0).UTC()
	f := &fakeS3{getOut: &s3.GetObjectOutput{
		Body:          io.NopCloser(strings.NewReader("xyz")),
		ContentType:   aws.String("image/webp"),
		ContentLength: aws.Int64(3),
		LastModified:  aws.Time(mod),
	}}
	s := newS3(f)
	rc, info, err := s.Open(context.Background(), Ref{Backend: BackendS3, Key: "inst1/t/branding-logo/x.webp"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != "xyz" {
		t.Fatalf("body = %q", got)
	}
	if info.ContentType != "image/webp" || info.Size != 3 || !info.ModTime.Equal(mod) {
		t.Fatalf("info = %+v", info)
	}
}

func TestS3NotFoundMapping(t *testing.T) {
	ctx := context.Background()
	ref := Ref{Backend: BackendS3, Key: "inst1/t/branding-logo/x.png"}

	// GetObject NoSuchKey → ErrNotFound.
	s := newS3(&fakeS3{getErr: &s3types.NoSuchKey{}})
	if _, _, err := s.Open(ctx, ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Open NoSuchKey = %v, want ErrNotFound", err)
	}
	// HeadObject NotFound → ErrNotFound.
	s = newS3(&fakeS3{headErr: &s3types.NotFound{}})
	if _, err := s.Stat(ctx, ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Stat NotFound = %v, want ErrNotFound", err)
	}
	// A generic API 404 also maps to ErrNotFound.
	s = newS3(&fakeS3{getErr: &smithy.GenericAPIError{Code: "NotFound", Message: "nope"}})
	if _, _, err := s.Open(ctx, ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Open generic 404 = %v, want ErrNotFound", err)
	}
	// A non-404 error is surfaced, not swallowed as not-found.
	s = newS3(&fakeS3{getErr: &smithy.GenericAPIError{Code: "AccessDenied"}})
	if _, _, err := s.Open(ctx, ref); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("Open AccessDenied = %v, want a non-NotFound error", err)
	}
}

func TestS3RejectsForeignAndCrossInstanceRef(t *testing.T) {
	ctx := context.Background()
	f := &fakeS3{}
	s := newS3(f)
	for _, ref := range []Ref{
		{Backend: BackendFilesystem, Key: "inst1/t/p/i"}, // wrong backend
		{Backend: BackendS3, Key: "otherinst/t/p/i"},     // cross-instance
		{Backend: BackendS3, Key: ""},                    // empty
	} {
		if _, _, err := s.Open(ctx, ref); err == nil {
			t.Errorf("Open(%+v) must error", ref)
		}
		if err := s.Delete(ctx, ref); err == nil {
			t.Errorf("Delete(%+v) must error", ref)
		}
	}
	if f.delIn != nil {
		t.Fatal("DeleteObject must not be called for a rejected ref")
	}
}

func TestS3DeleteIdempotent(t *testing.T) {
	f := &fakeS3{}
	s := newS3(f)
	if err := s.Delete(context.Background(), Ref{Backend: BackendS3, Key: "inst1/t/branding-logo/x.png"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if aws.ToString(f.delIn.Key) != "inst1/t/branding-logo/x.png" {
		t.Fatalf("delete key = %q", aws.ToString(f.delIn.Key))
	}
}

// TestS3URLPresigns exercises URL with a REAL presign client (presigning is a local
// computation — no network) to prove it mints a signed, expiring URL for the key.
func TestS3URLPresigns(t *testing.T) {
	client := s3.New(s3.Options{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("AKIDTEST", "SECRETTEST", ""),
	})
	s := &s3Store{api: &fakeS3{}, presigner: s3.NewPresignClient(client), bucket: "test-bucket", instanceID: "inst1"}
	url, expiry, err := s.URL(context.Background(), Ref{Backend: BackendS3, Key: "inst1/acme/branding-logo/logo.png"}, URLOptions{Expiry: time.Hour})
	if err != nil {
		t.Fatalf("URL: %v", err)
	}
	if !strings.Contains(url, "test-bucket") || !strings.Contains(url, "inst1/acme/branding-logo/logo.png") {
		t.Fatalf("presigned URL missing bucket/key: %s", url)
	}
	if !strings.Contains(url, "X-Amz-Signature") {
		t.Fatalf("presigned URL not signed: %s", url)
	}
	if !expiry.After(time.Now()) {
		t.Fatalf("expiry %v is not in the future", expiry)
	}
	// A foreign/cross-instance ref is refused before presigning.
	if _, _, err := s.URL(context.Background(), Ref{Backend: BackendS3, Key: "otherinst/x/y/z"}, URLOptions{}); err == nil {
		t.Fatal("URL for a cross-instance ref must error")
	}
}
