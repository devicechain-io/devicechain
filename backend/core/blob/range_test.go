// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package blob

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"
)

// rangeS3 is an S3 double that serves a real object and HONOURS the Range header the
// way S3 does (Content-Range, 416 InvalidRange), so the store's range handling is
// checked against behaviour rather than against a canned reply. ignoreRange makes it
// answer with the whole object and no Content-Range, the way a non-conforming
// S3-compatible server can.
type rangeS3 struct {
	fakeS3
	data        []byte
	ignoreRange bool
	gotRange    *string
}

func (f *rangeS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	f.gotRange = in.Range
	mod := time.Unix(1_700_000_000, 0).UTC()
	size := int64(len(f.data))
	if in.Range == nil || f.ignoreRange {
		return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(f.data)), ContentLength: aws.Int64(size), LastModified: aws.Time(mod)}, nil
	}
	var a, b int64
	if _, err := fmt.Sscanf(*in.Range, "bytes=%d-%d", &a, &b); err != nil {
		return nil, &smithy.GenericAPIError{Code: "InvalidArgument"}
	}
	if a >= size || a > b {
		return nil, &smithy.GenericAPIError{Code: "InvalidRange", Message: "The requested range is not satisfiable"}
	}
	if b >= size {
		b = size - 1
	}
	return &s3.GetObjectOutput{
		Body:          io.NopCloser(bytes.NewReader(f.data[a : b+1])),
		ContentLength: aws.Int64(b - a + 1),
		ContentRange:  aws.String("bytes " + strconv.FormatInt(a, 10) + "-" + strconv.FormatInt(b, 10) + "/" + strconv.FormatInt(size, 10)),
		LastModified:  aws.Time(mod),
	}, nil
}

// longBodyS3 answers a range with a body LONGER than the window it claims.
type longBodyS3 struct{ fakeS3 }

func (l *longBodyS3) GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	return &s3.GetObjectOutput{
		Body:         io.NopCloser(strings.NewReader(rangeObject)),
		ContentRange: aws.String("bytes 0-2/20"),
	}, nil
}

type rangeCase struct {
	name           string
	offset, length int64
	want           string // expected bytes when wantErr is nil
	wantErr        error
}

// 20-byte object: indexes 0..19.
const rangeObject = "0123456789abcdefghij"

func rangeCases() []rangeCase {
	return []rangeCase{
		{name: "middle", offset: 5, length: 4, want: "5678"},
		{name: "from start", offset: 0, length: 3, want: "012"},
		{name: "exact tail", offset: 15, length: 5, want: "fghij"},
		{name: "past EOF is clamped", offset: 15, length: 100, want: "fghij"},
		{name: "whole object", offset: 0, length: 20, want: rangeObject},
		{name: "single last byte", offset: 19, length: 1, want: "j"},
		{name: "offset at EOF", offset: 20, length: 1, wantErr: ErrRangeNotSatisfiable},
		{name: "offset past EOF", offset: 99, length: 5, wantErr: ErrRangeNotSatisfiable},
		{name: "zero length", offset: 3, length: 0, wantErr: ErrRangeNotSatisfiable},
		{name: "negative length", offset: 3, length: -1, wantErr: ErrRangeNotSatisfiable},
		{name: "negative offset", offset: -1, length: 3, wantErr: ErrRangeNotSatisfiable},
		{name: "overflowing sum is clamped", offset: 5, length: math.MaxInt64, want: rangeObject[5:]},
		{name: "max offset", offset: math.MaxInt64, length: 1, wantErr: ErrRangeNotSatisfiable},
	}
}

func checkRange(t *testing.T, s Store, ref Ref) {
	t.Helper()
	for _, tc := range rangeCases() {
		t.Run(tc.name, func(t *testing.T) {
			rc, info, err := s.OpenRange(context.Background(), ref, tc.offset, tc.length)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("OpenRange(%d,%d) err = %v, want %v", tc.offset, tc.length, err, tc.wantErr)
				}
				if rc != nil {
					t.Fatal("a refused range must not return a reader")
				}
				return
			}
			if err != nil {
				t.Fatalf("OpenRange(%d,%d): %v", tc.offset, tc.length, err)
			}
			got, rerr := io.ReadAll(rc)
			rc.Close()
			if rerr != nil {
				t.Fatalf("read: %v", rerr)
			}
			if string(got) != tc.want {
				t.Fatalf("bytes = %q, want %q", got, tc.want)
			}
			if info.Size != int64(len(rangeObject)) {
				t.Fatalf("Info.Size = %d, want the whole object's %d", info.Size, len(rangeObject))
			}
		})
	}
}

func TestFilesystemOpenRange(t *testing.T) {
	s := newFS(t)
	ref, err := s.Put(context.Background(), Key{Tenant: "acme", Purpose: "firmware", ID: "fw.bin"}, strings.NewReader(rangeObject), PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	checkRange(t, s, ref)
}

func TestFilesystemOpenRangeEmptyObjectAndMissing(t *testing.T) {
	ctx := context.Background()
	s := newFS(t)
	ref, err := s.Put(ctx, Key{Tenant: "acme", Purpose: "firmware", ID: "empty.bin"}, strings.NewReader(""), PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.OpenRange(ctx, ref, 0, 1); !errors.Is(err, ErrRangeNotSatisfiable) {
		t.Fatalf("range over an empty object = %v, want ErrRangeNotSatisfiable", err)
	}
	missing := Ref{Backend: BackendFilesystem, Key: "inst1/acme/firmware/none.bin"}
	if _, _, err := s.OpenRange(ctx, missing, 0, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing = %v, want ErrNotFound", err)
	}
	// A key naming a directory is not an object.
	dir := Ref{Backend: BackendFilesystem, Key: "inst1/acme"}
	if _, _, err := s.OpenRange(ctx, dir, 0, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("directory = %v, want ErrNotFound", err)
	}
}

func TestFilesystemOpenRangeKeepsInstanceScope(t *testing.T) {
	s := newFS(t)
	for _, ref := range []Ref{
		{Backend: BackendS3, Key: "inst1/t/p/i"},
		{Backend: BackendFilesystem, Key: "otherinst/t/p/i"},
		{Backend: BackendFilesystem, Key: "inst1/../../etc/passwd"},
		{Backend: BackendFilesystem, Key: ""},
	} {
		if rc, _, err := s.OpenRange(context.Background(), ref, 0, 1); err == nil || rc != nil {
			t.Errorf("OpenRange(%+v) must be refused", ref)
		}
	}
}

func TestFilesystemOpenRangeReaderIsBounded(t *testing.T) {
	// The reader must stop at length even though the file continues.
	ctx := context.Background()
	s := newFS(t)
	ref, _ := s.Put(ctx, Key{Tenant: "acme", Purpose: "firmware", ID: "fw.bin"}, strings.NewReader(rangeObject), PutOptions{})
	rc, _, err := s.OpenRange(ctx, ref, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	buf := make([]byte, 64)
	n, _ := io.ReadFull(rc, buf)
	if n != 3 || string(buf[:n]) != "234" {
		t.Fatalf("read %d bytes %q, want exactly 3 (%q)", n, buf[:n], "234")
	}
}

func newRangeS3(f *rangeS3) *s3Store {
	s := newS3(&f.fakeS3)
	s.api = f
	return s
}

func TestS3OpenRange(t *testing.T) {
	s := newRangeS3(&rangeS3{data: []byte(rangeObject)})
	checkRange(t, s, Ref{Backend: BackendS3, Key: "inst1/acme/firmware/fw.bin"})
}

func TestS3OpenRangeSendsExactRangeHeader(t *testing.T) {
	f := &rangeS3{data: []byte(rangeObject)}
	rc, _, err := newRangeS3(f).OpenRange(context.Background(), Ref{Backend: BackendS3, Key: "inst1/acme/firmware/fw.bin"}, 5, 4)
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()
	if aws.ToString(f.gotRange) != "bytes=5-8" {
		t.Fatalf("Range header = %q, want bytes=5-8", aws.ToString(f.gotRange))
	}
}

func TestS3OpenRangeRefusesBeforeTheNetwork(t *testing.T) {
	f := &rangeS3{data: []byte(rangeObject)}
	s := newRangeS3(f)
	ref := Ref{Backend: BackendS3, Key: "inst1/acme/firmware/fw.bin"}
	for _, r := range [][2]int64{{-1, 1}, {0, 0}, {0, -5}} {
		if _, _, err := s.OpenRange(context.Background(), ref, r[0], r[1]); !errors.Is(err, ErrRangeNotSatisfiable) {
			t.Errorf("OpenRange(%d,%d) = %v", r[0], r[1], err)
		}
	}
	if f.gotRange != nil {
		t.Fatal("an invalid range must not reach the backend")
	}
}

func TestS3OpenRangeRefusesAServerThatIgnoresRange(t *testing.T) {
	// A server that answers with the whole object and no Content-Range: serving that
	// as the requested range would hand back the wrong bytes.
	f := &rangeS3{data: []byte(rangeObject), ignoreRange: true}
	rc, _, err := newRangeS3(f).OpenRange(context.Background(), Ref{Backend: BackendS3, Key: "inst1/acme/firmware/fw.bin"}, 5, 4)
	if err == nil {
		got, _ := io.ReadAll(rc)
		rc.Close()
		t.Fatalf("a response without Content-Range must be refused, got %q", got)
	}
}

func TestS3OpenRangeReaderIsBounded(t *testing.T) {
	// Even if the body is longer than the window (misbehaving server), the reader
	// yields at most length bytes.
	s := newS3(&fakeS3{})
	s.api = &longBodyS3{}
	rc, _, err := s.OpenRange(context.Background(), Ref{Backend: BackendS3, Key: "inst1/acme/firmware/fw.bin"}, 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != "012" {
		t.Fatalf("bytes = %q, want 012", got)
	}
}

func TestS3OpenRangeNotFoundAndScope(t *testing.T) {
	ctx := context.Background()
	s := newS3(&fakeS3{getErr: &s3types.NoSuchKey{}})
	if _, _, err := s.OpenRange(ctx, Ref{Backend: BackendS3, Key: "inst1/t/p/i"}, 0, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("NoSuchKey = %v, want ErrNotFound", err)
	}
	f := &rangeS3{data: []byte(rangeObject)}
	s = newRangeS3(f)
	for _, ref := range []Ref{
		{Backend: BackendFilesystem, Key: "inst1/t/p/i"},
		{Backend: BackendS3, Key: "otherinst/t/p/i"},
		{Backend: BackendS3, Key: ""},
	} {
		if _, _, err := s.OpenRange(ctx, ref, 0, 1); err == nil {
			t.Errorf("OpenRange(%+v) must be refused", ref)
		}
	}
	if f.gotRange != nil {
		t.Fatal("a rejected ref must not reach the backend")
	}
}
