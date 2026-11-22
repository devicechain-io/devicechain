// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package blob

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// The plain fake refuses multipart loudly: a test that did not set it up must not pass by
// silently taking a path it never examined.
func (f *fakeS3) CreateMultipartUpload(context.Context, *s3.CreateMultipartUploadInput, ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error) {
	return nil, errors.New("fakeS3: unexpected CreateMultipartUpload")
}
func (f *fakeS3) UploadPart(context.Context, *s3.UploadPartInput, ...func(*s3.Options)) (*s3.UploadPartOutput, error) {
	return nil, errors.New("fakeS3: unexpected UploadPart")
}
func (f *fakeS3) CompleteMultipartUpload(context.Context, *s3.CompleteMultipartUploadInput, ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error) {
	return nil, errors.New("fakeS3: unexpected CompleteMultipartUpload")
}
func (f *fakeS3) AbortMultipartUpload(context.Context, *s3.AbortMultipartUploadInput, ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error) {
	return nil, errors.New("fakeS3: unexpected AbortMultipartUpload")
}

// mpFake is an S3 that keeps multipart uploads: the parts each upload received, in the order
// they arrived, and whether it was completed or aborted.
type mpFake struct {
	fakeS3

	created   []*s3.CreateMultipartUploadInput
	partLens  []int
	partNums  []int32
	assembled []byte // the parts in arrival order
	completed *s3.CompleteMultipartUploadInput
	aborts    []*s3.AbortMultipartUploadInput
	abortCtxs []error // ctx.Err() of each abort call

	// onPart runs inside UploadPart, before it returns, with the part number.
	onPart  func(n int32)
	partErr func(n int32) error
	compErr error
	abortEr error
}

func (m *mpFake) CreateMultipartUpload(_ context.Context, in *s3.CreateMultipartUploadInput, _ ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error) {
	m.created = append(m.created, in)
	return &s3.CreateMultipartUploadOutput{UploadId: aws.String("up-1")}, nil
}

func (m *mpFake) UploadPart(_ context.Context, in *s3.UploadPartInput, _ ...func(*s3.Options)) (*s3.UploadPartOutput, error) {
	n := aws.ToInt32(in.PartNumber)
	body, _ := io.ReadAll(in.Body)
	if int64(len(body)) != aws.ToInt64(in.ContentLength) {
		return nil, fmt.Errorf("part %d: body is %d bytes but ContentLength says %d", n, len(body), aws.ToInt64(in.ContentLength))
	}
	if aws.ToString(in.UploadId) != "up-1" {
		return nil, fmt.Errorf("part %d: upload id %q", n, aws.ToString(in.UploadId))
	}
	if m.onPart != nil {
		m.onPart(n)
	}
	if m.partErr != nil {
		if err := m.partErr(n); err != nil {
			return nil, err
		}
	}
	m.partLens = append(m.partLens, len(body))
	m.partNums = append(m.partNums, n)
	m.assembled = append(m.assembled, body...)
	return &s3.UploadPartOutput{ETag: aws.String(fmt.Sprintf("etag-%d", n))}, nil
}

func (m *mpFake) CompleteMultipartUpload(_ context.Context, in *s3.CompleteMultipartUploadInput, _ ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error) {
	if m.compErr != nil {
		return nil, m.compErr
	}
	m.completed = in
	return &s3.CompleteMultipartUploadOutput{}, nil
}

func (m *mpFake) AbortMultipartUpload(ctx context.Context, in *s3.AbortMultipartUploadInput, _ ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error) {
	m.aborts = append(m.aborts, in)
	m.abortCtxs = append(m.abortCtxs, ctx.Err())
	return &s3.AbortMultipartUploadOutput{}, m.abortEr
}

func newMP(f *mpFake, partSize int64) *s3Store {
	return &s3Store{api: f, bucket: "test-bucket", instanceID: "inst1", partSize: partSize}
}

var mpKey = Key{Tenant: "acme", Purpose: "update-artifact", ID: "fw.bin"}

func putData(t *testing.T, s *s3Store, ctx context.Context, r io.Reader, opts PutOptions) (Ref, error) {
	t.Helper()
	return s.Put(ctx, mpKey, r, opts)
}

func TestS3PutSmallObjectIsOnePutObject(t *testing.T) {
	f := &mpFake{}
	data := bytes.Repeat([]byte("a"), 3)
	if _, err := putData(t, newMP(f, 4), context.Background(), bytes.NewReader(data), PutOptions{}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if len(f.created) != 0 || f.putIn == nil || !bytes.Equal(f.putBody, data) {
		t.Fatalf("a small object must be one PutObject; created=%d put=%v", len(f.created), f.putIn != nil)
	}
}

// An object of exactly one part has no second part to upload, so it is one PutObject too:
// the part boundary must not turn it into a one-part multipart upload.
func TestS3PutExactlyOnePartIsOnePutObject(t *testing.T) {
	f := &mpFake{}
	data := bytes.Repeat([]byte("b"), 4)
	if _, err := putData(t, newMP(f, 4), context.Background(), bytes.NewReader(data), PutOptions{}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if len(f.created) != 0 || !bytes.Equal(f.putBody, data) {
		t.Fatalf("exactly one part must be one PutObject; created=%d", len(f.created))
	}
}

func TestS3PutMultipartAssemblesInOrderAndCompletes(t *testing.T) {
	for _, size := range []int{5, 8, 9, 12, 13} { // short last part, exact multiple, one over
		f := &mpFake{}
		data := make([]byte, size)
		for i := range data {
			data[i] = byte('a' + i%26)
		}
		ref, err := putData(t, newMP(f, 4), context.Background(), bytes.NewReader(data),
			PutOptions{ContentType: "application/octet-stream"})
		if err != nil {
			t.Fatalf("size %d: Put: %v", size, err)
		}
		if ref.Key != "inst1/acme/update-artifact/fw.bin" {
			t.Fatalf("ref = %+v", ref)
		}
		if f.putIn != nil {
			t.Fatalf("size %d: a multipart object must not also be PutObject'd", size)
		}
		if !bytes.Equal(f.assembled, data) {
			t.Fatalf("size %d: parts assemble to %q, want %q", size, f.assembled, data)
		}
		wantParts := (size + 3) / 4
		if len(f.partLens) != wantParts {
			t.Fatalf("size %d: %d parts, want %d (%v)", size, len(f.partLens), wantParts, f.partLens)
		}
		for i, l := range f.partLens {
			if l > 4 || (l != 4 && i != len(f.partLens)-1) {
				t.Fatalf("size %d: part %d is %d bytes; every part but the last is exactly the part size (%v)", size, i+1, l, f.partLens)
			}
			if f.partNums[i] != int32(i+1) {
				t.Fatalf("size %d: part numbers %v", size, f.partNums)
			}
		}
		if f.completed == nil || len(f.completed.MultipartUpload.Parts) != wantParts {
			t.Fatalf("size %d: upload not completed with every part: %+v", size, f.completed)
		}
		for i, p := range f.completed.MultipartUpload.Parts {
			if aws.ToInt32(p.PartNumber) != int32(i+1) || aws.ToString(p.ETag) != fmt.Sprintf("etag-%d", i+1) {
				t.Fatalf("size %d: completed part %d = %+v", size, i, p)
			}
		}
		if len(f.aborts) != 0 {
			t.Fatalf("size %d: a successful upload must not abort", size)
		}
		if aws.ToString(f.created[0].ContentType) != "application/octet-stream" {
			t.Fatalf("content type not carried to the multipart upload: %q", aws.ToString(f.created[0].ContentType))
		}
	}
}

// Memory does not depend on the object's size: the store reads no further ahead than the part
// it is uploading (plus the single peeked byte and the reader's small buffer).
func TestS3PutMultipartHoldsOnePartInMemory(t *testing.T) {
	const part = 1 << 16
	const total = 40 * part
	var read atomic.Int64
	src := &countingReader{r: io.LimitReader(zeroReader{}, total), n: &read}
	f := &mpFake{}
	f.onPart = func(n int32) {
		if ahead := read.Load() - int64(n)*part; ahead > 64 {
			t.Errorf("at part %d the store had read %d bytes past it; it must hold one part, not the object", n, ahead)
		}
	}
	if _, err := putData(t, newMP(f, part), context.Background(), src, PutOptions{}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if len(f.partLens) != 40 {
		t.Fatalf("%d parts, want 40", len(f.partLens))
	}
	want := sha256.Sum256(make([]byte, total))
	if got := sha256.Sum256(f.assembled); got != want {
		t.Fatal("assembled object differs from the stream")
	}
}

// With no ceiling the old 64 MiB buffered cap is gone: a larger object streams.
func TestS3PutUnboundedObjectPastTheOldBufferedCap(t *testing.T) {
	f := &mpFake{}
	sink := &mpDiscard{mpFake: f}
	s := &s3Store{api: sink, bucket: "test-bucket", instanceID: "inst1"} // default part size
	total := int64(s3UnboundedCapForTest) + 1<<20
	if _, err := s.Put(context.Background(), mpKey, io.LimitReader(zeroReader{}, total), PutOptions{}); err != nil {
		t.Fatalf("Put of %d bytes: %v", total, err)
	}
	if sink.bytes != total {
		t.Fatalf("stored %d bytes, want %d", sink.bytes, total)
	}
	if sink.maxPart > s3PartSize {
		t.Fatalf("a part of %d bytes exceeds the part size %d", sink.maxPart, s3PartSize)
	}
	if f.completed == nil {
		t.Fatal("not completed")
	}
}

const s3UnboundedCapForTest = 64 << 20

// mpDiscard counts the parts' bytes without keeping them.
type mpDiscard struct {
	*mpFake
	bytes   int64
	maxPart int64
}

func (d *mpDiscard) UploadPart(ctx context.Context, in *s3.UploadPartInput, o ...func(*s3.Options)) (*s3.UploadPartOutput, error) {
	n, _ := io.Copy(io.Discard, in.Body)
	d.bytes += n
	if n > d.maxPart {
		d.maxPart = n
	}
	return &s3.UploadPartOutput{ETag: aws.String("e")}, nil
}

func TestS3PutMaxSizeEnforcedDuringTheStreamAndUploadAborted(t *testing.T) {
	var read atomic.Int64
	src := &countingReader{r: bytes.NewReader(bytes.Repeat([]byte("x"), 1000)), n: &read}
	f := &mpFake{}
	_, err := putData(t, newMP(f, 4), context.Background(), src, PutOptions{MaxSize: 10})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if read.Load() > 11 {
		t.Fatalf("read %d bytes of a 1000-byte stream with MaxSize 10; the cap must stop the read at MaxSize+1", read.Load())
	}
	if len(f.aborts) != 1 || f.completed != nil || f.putIn != nil {
		t.Fatalf("over-limit: aborts=%d completed=%v put=%v; want one abort, no object", len(f.aborts), f.completed != nil, f.putIn != nil)
	}
}

// Exactly MaxSize bytes is within the ceiling, and one byte more is not: the off-by-one pair
// that guards the cap in both directions.
func TestS3PutMaxSizeBoundary(t *testing.T) {
	f := &mpFake{}
	if _, err := putData(t, newMP(f, 4), context.Background(), bytes.NewReader(bytes.Repeat([]byte("y"), 10)), PutOptions{MaxSize: 10}); err != nil {
		t.Fatalf("exactly MaxSize: %v", err)
	}
	if len(f.assembled) != 10 || f.completed == nil {
		t.Fatalf("exactly MaxSize bytes must be stored in full: %d bytes, completed=%v", len(f.assembled), f.completed != nil)
	}
	g := &mpFake{}
	_, err := putData(t, newMP(g, 4), context.Background(), bytes.NewReader(bytes.Repeat([]byte("y"), 11)), PutOptions{MaxSize: 10})
	if !errors.Is(err, ErrTooLarge) || len(g.aborts) != 1 || g.completed != nil {
		t.Fatalf("MaxSize+1: err=%v aborts=%d completed=%v", err, len(g.aborts), g.completed != nil)
	}
	// A small over-limit object never reaches S3 at all.
	h := &mpFake{}
	_, err = putData(t, newMP(h, 64), context.Background(), bytes.NewReader(bytes.Repeat([]byte("y"), 11)), PutOptions{MaxSize: 10})
	if !errors.Is(err, ErrTooLarge) || h.putIn != nil || len(h.created) != 0 {
		t.Fatalf("small over-limit: err=%v put=%v created=%d", err, h.putIn != nil, len(h.created))
	}
}

func TestS3PutAbortsOnAnErrorAfterTheFirstPart(t *testing.T) {
	boom := errors.New("part refused")
	cases := map[string]func(*mpFake) io.Reader{
		"a part S3 refuses": func(f *mpFake) io.Reader {
			f.partErr = func(n int32) error {
				if n == 2 {
					return boom
				}
				return nil
			}
			return bytes.NewReader(make([]byte, 20))
		},
		"a failing complete": func(f *mpFake) io.Reader {
			f.compErr = boom
			return bytes.NewReader(make([]byte, 20))
		},
		"a read error mid-stream": func(f *mpFake) io.Reader {
			return io.MultiReader(bytes.NewReader(make([]byte, 9)), errReader{boom})
		},
	}
	for name, setup := range cases {
		f := &mpFake{}
		_, err := putData(t, newMP(f, 4), context.Background(), setup(f), PutOptions{})
		if !errors.Is(err, boom) {
			t.Fatalf("%s: err = %v, want it to wrap %v", name, err, boom)
		}
		if len(f.aborts) != 1 || aws.ToString(f.aborts[0].UploadId) != "up-1" ||
			aws.ToString(f.aborts[0].Key) != "inst1/acme/update-artifact/fw.bin" || aws.ToString(f.aborts[0].Bucket) != "test-bucket" {
			t.Fatalf("%s: aborts = %+v, want exactly one for this upload", name, f.aborts)
		}
		if f.completed != nil {
			t.Fatalf("%s: completed despite the failure", name)
		}
	}
}

// A cancelled caller is the commonest reason to abandon an upload, and the abort must still
// reach S3: it is sent on a context the cancellation does not touch.
func TestS3PutCancellationAbortsOnADetachedContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := &mpFake{}
	f.onPart = func(n int32) {
		if n == 1 {
			cancel()
		}
	}
	_, err := putData(t, newMP(f, 4), ctx, bytes.NewReader(make([]byte, 40)), PutOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(f.aborts) != 1 {
		t.Fatalf("aborts = %d, want 1", len(f.aborts))
	}
	if f.abortCtxs[0] != nil {
		t.Fatalf("the abort ran on a cancelled context (%v) and would not have been sent", f.abortCtxs[0])
	}
	if f.completed != nil || len(f.partLens) > 1 {
		t.Fatalf("kept uploading after cancellation: %d parts, completed=%v", len(f.partLens), f.completed != nil)
	}
}

// A failed abort does not hide the error that caused it, and says the upload may be left.
func TestS3PutAbortFailureIsReportedWithTheCause(t *testing.T) {
	boom := errors.New("part refused")
	f := &mpFake{abortEr: errors.New("abort refused")}
	f.partErr = func(n int32) error {
		if n == 2 {
			return boom
		}
		return nil
	}
	_, err := putData(t, newMP(f, 4), context.Background(), bytes.NewReader(make([]byte, 20)), PutOptions{})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap the cause", err)
	}
	if want := "abort refused"; !bytes.Contains([]byte(err.Error()), []byte(want)) {
		t.Fatalf("err = %q, want it to mention %q", err, want)
	}
}

func TestS3PutTooManyPartsIsRefusedAndAborted(t *testing.T) {
	f := &mpFake{}
	_, err := putData(t, newMP(f, 1), context.Background(), bytes.NewReader(make([]byte, s3MaxParts+1)), PutOptions{})
	if err == nil || errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want a part-count error", err)
	}
	if len(f.aborts) != 1 || f.completed != nil {
		t.Fatalf("aborts=%d completed=%v", len(f.aborts), f.completed != nil)
	}
	if len(f.partLens) != s3MaxParts {
		t.Fatalf("uploaded %d parts, want exactly the %d allowed", len(f.partLens), s3MaxParts)
	}
}

func TestS3PutCreateFailureHasNothingToAbort(t *testing.T) {
	f := &createFails{}
	s := &s3Store{api: f, bucket: "b", instanceID: "inst1", partSize: 4}
	if _, err := s.Put(context.Background(), mpKey, bytes.NewReader(make([]byte, 9)), PutOptions{}); err == nil {
		t.Fatal("want an error")
	}
	if f.aborts != 0 {
		t.Fatal("no upload was created, so there is nothing to abort")
	}
}

type createFails struct {
	mpFake
	aborts int
}

func (c *createFails) CreateMultipartUpload(context.Context, *s3.CreateMultipartUploadInput, ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error) {
	return nil, errors.New("no")
}
func (c *createFails) AbortMultipartUpload(context.Context, *s3.AbortMultipartUploadInput, ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error) {
	c.aborts++
	return &s3.AbortMultipartUploadOutput{}, nil
}

func TestPartSizeFor(t *testing.T) {
	if got := partSizeFor(0); got != s3PartSize {
		t.Fatalf("unbounded = %d", got)
	}
	if got := partSizeFor(100 << 20); got != s3PartSize {
		t.Fatalf("small ceiling = %d", got)
	}
	big := int64(1) << 37 // 128 GiB needs bigger parts than 8 MiB x 10000
	got := partSizeFor(big)
	if got <= s3PartSize || big/got > s3MaxParts || (big+got-1)/got > s3MaxParts {
		t.Fatalf("128 GiB ceiling gives part size %d, which still needs more than %d parts", got, s3MaxParts)
	}
	if got := partSizeFor(math.MaxInt64); got != s3MaxPartSize {
		t.Fatalf("MaxInt64 ceiling = %d, want the clamp %d", got, int64(s3MaxPartSize))
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

type countingReader struct {
	r io.Reader
	n *atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

var _ = s3types.CompletedPart{}

// A stream over MaxSize is ErrTooLarge even when it is also past the part count: the size
// refusal comes first, and the part-count refusal is for an object within its ceiling.
func TestS3PutSizeCheckPrecedesThePartCountCheck(t *testing.T) {
	f := &mpFake{}
	_, err := putData(t, newMP(f, 1), context.Background(), bytes.NewReader(make([]byte, s3MaxParts+1)),
		PutOptions{MaxSize: s3MaxParts})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if len(f.aborts) != 1 || f.completed != nil {
		t.Fatalf("aborts=%d completed=%v", len(f.aborts), f.completed != nil)
	}
}

// With a MaxSize the part-count refusal names the backend ceiling, not "set MaxSize".
func TestS3PutPartCountErrorForABoundedPutNamesTheCeiling(t *testing.T) {
	f := &mpFake{}
	// A MaxSize far past what 10,000 parts of this size hold, with a stream that gets there.
	s := newMP(f, 1)
	_, err := s.Put(context.Background(), mpKey, bytes.NewReader(make([]byte, s3MaxParts+1)), PutOptions{MaxSize: math.MaxInt64})
	if err == nil || errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want a part-count error", err)
	}
	if bytes.Contains([]byte(err.Error()), []byte("set PutOptions.MaxSize")) {
		t.Fatalf("a bounded put must not be told to set MaxSize: %v", err)
	}
	if !bytes.Contains([]byte(err.Error()), []byte("at most")) {
		t.Fatalf("err = %q, want the backend ceiling named", err)
	}
}

// A stream already at the ceiling with a byte to spare is refused before any upload is started.
func TestS3PutRefusesEarlyWhenTheFirstPartReachesTheCeiling(t *testing.T) {
	f := &mpFake{}
	_, err := putData(t, newMP(f, 4), context.Background(), bytes.NewReader(make([]byte, 40)), PutOptions{MaxSize: 4})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if len(f.created) != 0 {
		t.Fatal("no multipart upload should have been started for an object already over the ceiling")
	}
}

// A complete that reports failure but whose abort finds the upload gone may have succeeded on the
// server: the object is looked for, deleted if there, and the error says what became of it.
func TestS3PutAmbiguousCompleteDeletesAnObjectThatWasStored(t *testing.T) {
	boom := errors.New("connection reset")
	f := &mpFake{compErr: boom, abortEr: &s3types.NoSuchUpload{}}
	f.headOut = &s3.HeadObjectOutput{}
	_, err := putData(t, newMP(f, 4), context.Background(), bytes.NewReader(make([]byte, 20)), PutOptions{})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap the cause", err)
	}
	if len(f.delKeys) != 1 || f.delKeys[0] != "inst1/acme/update-artifact/fw.bin" {
		t.Fatalf("deleted %v, want the stored object removed", f.delKeys)
	}
	if !bytes.Contains([]byte(err.Error()), []byte("not kept")) {
		t.Fatalf("err = %q, want it to say the upload was not kept", err)
	}
}

func TestS3PutAmbiguousCompleteWithNoObjectSaysSo(t *testing.T) {
	f := &mpFake{compErr: errors.New("connection reset"), abortEr: &s3types.NoSuchUpload{}}
	f.headErr = &s3types.NotFound{}
	_, err := putData(t, newMP(f, 4), context.Background(), bytes.NewReader(make([]byte, 20)), PutOptions{})
	if err == nil || len(f.delKeys) != 0 {
		t.Fatalf("err=%v deleted=%v; nothing exists, so nothing is deleted", err, f.delKeys)
	}
	if !bytes.Contains([]byte(err.Error()), []byte("no object was stored")) {
		t.Fatalf("err = %q", err)
	}
}

func TestS3PutAmbiguousCompleteThatCannotBeSettledSaysTheObjectMayExist(t *testing.T) {
	f := &mpFake{compErr: errors.New("connection reset"), abortEr: &s3types.NoSuchUpload{}}
	f.headErr = errors.New("head refused")
	_, err := putData(t, newMP(f, 4), context.Background(), bytes.NewReader(make([]byte, 20)), PutOptions{})
	if err == nil || len(f.delKeys) != 0 {
		t.Fatalf("err=%v deleted=%v", err, f.delKeys)
	}
	if !bytes.Contains([]byte(err.Error()), []byte("may have been stored")) {
		t.Fatalf("err = %q, want it to say the object may have been stored", err)
	}

	// and an object that exists but cannot be deleted says it may be left behind
	g := &mpFake{compErr: errors.New("connection reset"), abortEr: &s3types.NoSuchUpload{}}
	g.headOut = &s3.HeadObjectOutput{}
	g.delErr = errors.New("delete refused")
	_, err = putData(t, newMP(g, 4), context.Background(), bytes.NewReader(make([]byte, 20)), PutOptions{})
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("left behind")) {
		t.Fatalf("err = %v, want it to say the object may be left behind", err)
	}
}

// The ordinary failed complete (the upload still exists, so the abort works) never touches the
// object: there is nothing to look for.
func TestS3PutFailedCompleteWithASuccessfulAbortLooksForNothing(t *testing.T) {
	f := &mpFake{compErr: errors.New("refused")}
	f.headOut = &s3.HeadObjectOutput{}
	_, err := putData(t, newMP(f, 4), context.Background(), bytes.NewReader(make([]byte, 20)), PutOptions{})
	if err == nil || len(f.delKeys) != 0 || len(f.aborts) != 1 {
		t.Fatalf("err=%v deleted=%v aborts=%d", err, f.delKeys, len(f.aborts))
	}
}
