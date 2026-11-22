// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package blob

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"
)

// s3Store is the S3-compatible backend (ADR-058 §2): objects live in a bucket,
// keyed by the same {instanceId}/{tenant}/{purpose}/{id} layout as every backend.
// It fronts AWS S3 and any S3-compatible service (MinIO) — one API covers both.
// Credentials come from the standard AWS chain (env from the instance K8s Secret,
// IRSA, or an instance profile), never from config (ADR-058 §5). Reads may be served
// through the authorizing proxy (Open) OR a presigned, expiring URL (URL).
type s3Store struct {
	api        s3ObjectAPI
	presigner  s3Presigner
	bucket     string
	instanceID string
	// partSize overrides the default part size, for tests that need a part to be a few
	// bytes. Zero means the default. A real store never sets it.
	partSize int64
}

// s3ObjectAPI is the subset of the S3 client the store uses. Narrowing it to an
// interface lets the store be unit-tested with a fake — the concrete *s3.Client
// satisfies it.
type s3ObjectAPI interface {
	PutObject(ctx context.Context, in *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	// The multipart quartet serves Put for an object larger than one part, so an upload
	// streams a part at a time instead of being held in memory whole.
	CreateMultipartUpload(ctx context.Context, in *s3.CreateMultipartUploadInput, optFns ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error)
	UploadPart(ctx context.Context, in *s3.UploadPartInput, optFns ...func(*s3.Options)) (*s3.UploadPartOutput, error)
	CompleteMultipartUpload(ctx context.Context, in *s3.CompleteMultipartUploadInput, optFns ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error)
	AbortMultipartUpload(ctx context.Context, in *s3.AbortMultipartUploadInput, optFns ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error)
	GetObject(ctx context.Context, in *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	HeadObject(ctx context.Context, in *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	DeleteObject(ctx context.Context, in *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	// DeleteObjects serves DeleteTenant only: erasing a tenant is the one operation that
	// deletes in bulk, and a request per object makes it latency-bound.
	DeleteObjects(ctx context.Context, in *s3.DeleteObjectsInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error)
	// ListObjectsV2 serves DeleteTenant only: tenant erasure is the one operation that
	// must find objects no row names.
	ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

// s3Presigner is the presign subset (URL). *s3.PresignClient satisfies it, and
// presigning is a purely local computation (no network), so it is exercised
// directly in tests.
type s3Presigner interface {
	PresignGetObject(ctx context.Context, in *s3.GetObjectInput, optFns ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
}

const (
	// s3PartSize is the size of each part of a multipart upload, and so the most one Put
	// holds in memory whatever the object size. S3 refuses a part under 5 MiB (except the
	// last), and an upload has at most s3MaxParts parts, so a bounded Put whose MaxSize would
	// need more raises the part size to fit (partSizeFor).
	s3PartSize = 8 << 20 // 8 MiB
	s3MaxParts = 10000
	// s3MaxPartSize bounds how far a large MaxSize may raise the part size, and so the memory
	// one Put holds: 64 MiB parts reach about 640 GB. An object past that fails with the
	// part-count error rather than a Put growing its buffer without limit.
	s3MaxPartSize = 64 << 20
	// s3AbortTimeout bounds the AbortMultipartUpload that cleans up after a failed or
	// cancelled upload. It runs on a context detached from the caller, because the usual
	// reason an upload is abandoned is that the caller context is done.
	s3AbortTimeout = 30 * time.Second
	// defaultPresignExpiry / maxPresignExpiry bound a minted URL's lifetime; the S3
	// signature protocol caps presigned URLs at 7 days.
	defaultPresignExpiry = 15 * time.Minute
	maxPresignExpiry     = 7 * 24 * time.Hour
	// defaultS3Region is used for request signing when only a custom endpoint is
	// configured (S3-compatible servers like MinIO ignore the region but sigv4
	// still requires one).
	defaultS3Region = "us-east-1"
)

// NewS3Store builds an S3-compatible store for cfg.Bucket, prefixing every key with
// instanceID. It loads AWS config (credentials from the standard chain) and, when
// cfg.Endpoint is set, targets an S3-compatible service (MinIO) with path-style
// addressing per cfg.UsePathStyle. It is exported for direct construction; production
// wiring goes through New.
func NewS3Store(ctx context.Context, cfg Config, instanceID string) (Store, error) {
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, fmt.Errorf("blob: s3 backend requires a bucket")
	}
	// Mirror Config.Validate's requirement so a direct caller of NewS3Store (not via
	// New) cannot silently target real AWS us-east-1 with only a bucket configured.
	if strings.TrimSpace(cfg.Region) == "" && strings.TrimSpace(cfg.Endpoint) == "" {
		return nil, fmt.Errorf("blob: s3 backend requires a region (AWS) or an endpoint (S3-compatible)")
	}
	if err := validateSegment("instanceId", instanceID); err != nil {
		return nil, err
	}
	region := cfg.Region
	if region == "" {
		region = defaultS3Region
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("blob: loading AWS config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
		o.UsePathStyle = cfg.UsePathStyle
	})
	return &s3Store{
		api:        client,
		presigner:  s3.NewPresignClient(client),
		bucket:     cfg.Bucket,
		instanceID: instanceID,
	}, nil
}

// objectKey validates the Ref belongs to this backend and this instance, then
// returns its object key. The instance-prefix check is the ADR-048 isolation
// invariant made load-bearing on a shared bucket: a tampered Ref naming another
// instance's key is refused, not served.
func (s *s3Store) objectKey(ref Ref) (string, error) {
	if ref.Backend != BackendS3 {
		return "", fmt.Errorf("blob: ref backend %q is not %q", ref.Backend, BackendS3)
	}
	if !strings.HasPrefix(ref.Key, s.instanceID+"/") {
		return "", fmt.Errorf("blob: ref key is not within instance %q", s.instanceID)
	}
	return ref.Key, nil
}

func (s *s3Store) Put(ctx context.Context, key Key, r io.Reader, opts PutOptions) (Ref, error) {
	fullKey, err := buildKey(s.instanceID, key)
	if err != nil {
		return Ref{}, err
	}
	// Same up-front refusal as the filesystem backend; see checkContentTypeMatchesExt.
	if err := checkContentTypeMatchesExt(key.ID, opts.ContentType); err != nil {
		return Ref{}, err
	}
	bounded := opts.MaxSize > 0
	limit := opts.MaxSize
	if !bounded {
		limit = math.MaxInt64 - 1
	}
	// Guard limit+1 against int64 overflow (a MaxInt64 "unlimited" sentinel would
	// wrap negative, making LimitReader read zero bytes and silently commit an empty
	// object). Reading one byte past a limit this large never actually trips.
	if limit > math.MaxInt64-1 {
		limit = math.MaxInt64 - 1
	}
	partSize := s.partSize
	if partSize <= 0 {
		partSize = partSizeFor(opts.MaxSize)
	}
	// MaxSize is enforced as the bytes are read, so an oversized stream stops at the
	// ceiling plus one byte, never in full.
	src := bufio.NewReaderSize(io.LimitReader(r, limit+1), 16)
	first, err := io.ReadAll(io.LimitReader(src, partSize))
	if err != nil {
		return Ref{}, fmt.Errorf("blob: reading object: %w", err)
	}
	if int64(len(first)) > limit {
		return Ref{}, ErrTooLarge
	}
	more := false
	if int64(len(first)) == partSize {
		_, perr := src.Peek(1)
		switch {
		case perr == nil:
			more = true
		case errors.Is(perr, io.EOF):
		default:
			return Ref{}, fmt.Errorf("blob: reading object: %w", perr)
		}
	}
	if !more {
		return s.putSingle(ctx, fullKey, first, opts.ContentType)
	}
	return s.putMultipart(ctx, fullKey, first, src, partSize, limit, opts.ContentType)
}

// partSizeFor is the part size for an object of at most maxSize bytes (0 is unknown): the
// default, raised when the ceiling would need more than s3MaxParts of that size.
func partSizeFor(maxSize int64) int64 {
	if maxSize <= 0 || maxSize/s3PartSize < s3MaxParts {
		return s3PartSize
	}
	// Ceiling division written so a MaxInt64 sentinel cannot overflow, clamped to
	// s3MaxPartSize: an object needing more parts than that allows is refused at the limit.
	size := (maxSize-1)/s3MaxParts + 1
	if size > s3MaxPartSize {
		size = s3MaxPartSize
	}
	return size
}

// putSingle writes an object that fits in one part with a single PutObject: small objects,
// which are nearly all of them, cost one request, as before.
func (s *s3Store) putSingle(ctx context.Context, fullKey string, body []byte, contentType string) (Ref, error) {
	input := &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(fullKey),
		Body:          bytes.NewReader(body),
		ContentLength: aws.Int64(int64(len(body))),
	}
	if contentType != "" {
		input.ContentType = aws.String(contentType)
	}
	if _, err := s.api.PutObject(ctx, input); err != nil {
		return Ref{}, fmt.Errorf("blob: putting object: %w", err)
	}
	return Ref{Backend: BackendS3, Key: fullKey}, nil
}

// putMultipart streams an object larger than one part: first is the first part, already
// read, and rest holds the remainder. Only one part is in memory at a time.
//
// 🔴 EVERY EXIT BEFORE A SUCCESSFUL COMPLETE ABORTS THE UPLOAD. An unaborted multipart
// upload is not an error and not an object: it is invisible to a listing of the bucket keys
// and it is billed for its stored parts until something removes it. A read failure, a part
// that S3 refuses, a stream over MaxSize and a cancelled context all leave one behind, so
// the abort is deferred rather than written at each return, and it runs on a context
// detached from the caller: a cancelled caller is the commonest reason to be here.
func (s *s3Store) putMultipart(ctx context.Context, fullKey string, first []byte, rest io.Reader,
	partSize, limit int64, contentType string) (_ Ref, err error) {
	create := &s3.CreateMultipartUploadInput{Bucket: aws.String(s.bucket), Key: aws.String(fullKey)}
	if contentType != "" {
		create.ContentType = aws.String(contentType)
	}
	created, err := s.api.CreateMultipartUpload(ctx, create)
	if err != nil {
		return Ref{}, fmt.Errorf("blob: starting multipart upload: %w", err)
	}
	uploadID := created.UploadId
	completed := false
	defer func() {
		if completed {
			return
		}
		abortCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s3AbortTimeout)
		defer cancel()
		if _, aerr := s.api.AbortMultipartUpload(abortCtx, &s3.AbortMultipartUploadInput{
			Bucket: aws.String(s.bucket), Key: aws.String(fullKey), UploadId: uploadID,
		}); aerr != nil {
			err = fmt.Errorf("%w (aborting the multipart upload also failed, and it may be left behind: %v)", err, aerr)
		}
	}()

	var parts []s3types.CompletedPart
	var total int64
	// One buffer of exactly one part, reused for every part. (first came from ReadAll, whose
	// capacity is not the part size.)
	full := make([]byte, partSize)
	buf := full[:copy(full, first)]
	for n := int32(1); ; n++ {
		if n > s3MaxParts {
			return Ref{}, fmt.Errorf("blob: object needs more than %d parts of %d bytes; set PutOptions.MaxSize so the part size can grow to fit", s3MaxParts, partSize)
		}
		total += int64(len(buf))
		if total > limit {
			return Ref{}, ErrTooLarge
		}
		if cerr := ctx.Err(); cerr != nil {
			return Ref{}, fmt.Errorf("blob: uploading part %d: %w", n, cerr)
		}
		out, uerr := s.api.UploadPart(ctx, &s3.UploadPartInput{
			Bucket:        aws.String(s.bucket),
			Key:           aws.String(fullKey),
			UploadId:      uploadID,
			PartNumber:    aws.Int32(n),
			Body:          bytes.NewReader(buf),
			ContentLength: aws.Int64(int64(len(buf))),
		})
		if uerr != nil {
			return Ref{}, fmt.Errorf("blob: uploading part %d: %w", n, uerr)
		}
		parts = append(parts, s3types.CompletedPart{ETag: out.ETag, PartNumber: aws.Int32(n)})

		// The next part reuses this part's buffer, so memory stays at one part. A short
		// read is the last part, uploaded on the next pass; the pass after reads nothing.
		read, rerr := io.ReadFull(rest, full)
		if rerr != nil && !errors.Is(rerr, io.EOF) && !errors.Is(rerr, io.ErrUnexpectedEOF) {
			return Ref{}, fmt.Errorf("blob: reading object: %w", rerr)
		}
		if read == 0 {
			break
		}
		buf = full[:read]
	}
	if _, cerr := s.api.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:          aws.String(s.bucket),
		Key:             aws.String(fullKey),
		UploadId:        uploadID,
		MultipartUpload: &s3types.CompletedMultipartUpload{Parts: parts},
	}); cerr != nil {
		return Ref{}, fmt.Errorf("blob: completing multipart upload: %w", cerr)
	}
	completed = true
	return Ref{Backend: BackendS3, Key: fullKey}, nil
}

func (s *s3Store) Open(ctx context.Context, ref Ref) (io.ReadCloser, Info, error) {
	key, err := s.objectKey(ref)
	if err != nil {
		return nil, Info{}, err
	}
	out, err := s.api.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		if isS3NotFound(err) {
			return nil, Info{}, ErrNotFound
		}
		return nil, Info{}, fmt.Errorf("blob: getting object: %w", err)
	}
	info := Info{
		Key:         key,
		Size:        derefInt64(out.ContentLength),
		ContentType: contentTypeOr(out.ContentType),
		ModTime:     derefTime(out.LastModified),
	}
	return out.Body, info, nil
}

func (s *s3Store) OpenRange(ctx context.Context, ref Ref, offset, length int64) (io.ReadCloser, Info, error) {
	key, err := s.objectKey(ref)
	if err != nil {
		return nil, Info{}, err
	}
	// Refuse a malformed range before the network round trip.
	if offset < 0 || length <= 0 {
		return nil, Info{}, ErrRangeNotSatisfiable
	}
	// Inclusive last byte; saturate rather than overflow on a huge length (S3 clamps
	// an end past the object).
	last := int64(math.MaxInt64)
	if length-1 <= math.MaxInt64-offset {
		last = offset + length - 1
	}
	out, err := s.api.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Range:  aws.String(fmt.Sprintf("bytes=%d-%d", offset, last)),
	})
	if err != nil {
		if isS3NotFound(err) {
			return nil, Info{}, ErrNotFound
		}
		if isS3InvalidRange(err) {
			return nil, Info{}, ErrRangeNotSatisfiable
		}
		return nil, Info{}, fmt.Errorf("blob: getting object range: %w", err)
	}
	// Trust only a response that says which window it carries. A server that ignores
	// Range answers with the whole object and no Content-Range; serving that as the
	// requested window would return the wrong bytes.
	start, end, total, ok := parseContentRange(aws.ToString(out.ContentRange))
	// The window must be EXACTLY what was asked for, clamped only by the object's end:
	// a short window would otherwise be served as a clean, complete answer.
	wantEnd := last
	if total-1 < wantEnd {
		wantEnd = total - 1
	}
	if !ok || total <= 0 || start != offset || end != wantEnd {
		out.Body.Close()
		return nil, Info{}, fmt.Errorf("blob: object store did not honour the range request (Content-Range %q)", aws.ToString(out.ContentRange))
	}
	info := Info{
		Key:         key,
		Size:        total,
		ContentType: contentTypeOr(out.ContentType),
		ModTime:     derefTime(out.LastModified),
	}
	// Bound the reader to the window even if the body is longer, and report a body
	// shorter than the window as an error rather than a clean EOF.
	return &sectionReadCloser{Reader: &exactReader{r: out.Body, remaining: end - start + 1}, Closer: out.Body}, info, nil
}

// exactReader yields exactly `remaining` bytes from r: it stops at that many even if r
// has more, and returns io.ErrUnexpectedEOF if r ends first.
type exactReader struct {
	r         io.Reader
	remaining int64
}

func (e *exactReader) Read(p []byte) (int, error) {
	if e.remaining <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > e.remaining {
		p = p[:e.remaining]
	}
	n, err := e.r.Read(p)
	e.remaining -= int64(n)
	if err == io.EOF && e.remaining > 0 {
		err = io.ErrUnexpectedEOF
	}
	if err == nil && e.remaining <= 0 {
		err = io.EOF
	}
	return n, err
}

// parseContentRange strictly parses "bytes {start}-{end}/{total}": ASCII digits only
// in each number, no sign, no whitespace, no trailing text, and a numeric total (so
// "*" is refused).
func parseContentRange(h string) (start, end, total int64, ok bool) {
	rest, found := strings.CutPrefix(h, "bytes ")
	if !found {
		return 0, 0, 0, false
	}
	r, t, found := strings.Cut(rest, "/")
	if !found {
		return 0, 0, 0, false
	}
	a, b, found := strings.Cut(r, "-")
	if !found {
		return 0, 0, 0, false
	}
	var err error
	if start, err = parseDigits(a); err != nil {
		return 0, 0, 0, false
	}
	if end, err = parseDigits(b); err != nil {
		return 0, 0, 0, false
	}
	if total, err = parseDigits(t); err != nil {
		return 0, 0, 0, false
	}
	return start, end, total, true
}

func parseDigits(s string) (int64, error) {
	if s == "" {
		return 0, errors.New("empty")
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, errors.New("not a digit")
		}
	}
	return strconv.ParseInt(s, 10, 64)
}

// isS3InvalidRange reports whether err is S3's 416 "range not satisfiable" error.
func isS3InvalidRange(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "InvalidRange", "416":
			return true
		}
	}
	return false
}

func (s *s3Store) Stat(ctx context.Context, ref Ref) (Info, error) {
	key, err := s.objectKey(ref)
	if err != nil {
		return Info{}, err
	}
	out, err := s.api.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		if isS3NotFound(err) {
			return Info{}, ErrNotFound
		}
		return Info{}, fmt.Errorf("blob: stating object: %w", err)
	}
	return Info{
		Key:         key,
		Size:        derefInt64(out.ContentLength),
		ContentType: contentTypeOr(out.ContentType),
		ModTime:     derefTime(out.LastModified),
	}, nil
}

// URL mints a presigned, expiring GET URL — the cloud-backend read path. The expiry
// is clamped to the S3 protocol maximum (7 days) and defaults to 15 minutes.
func (s *s3Store) URL(ctx context.Context, ref Ref, opts URLOptions) (string, time.Time, error) {
	key, err := s.objectKey(ref)
	if err != nil {
		return "", time.Time{}, err
	}
	expiry := opts.Expiry
	if expiry <= 0 {
		expiry = defaultPresignExpiry
	}
	if expiry > maxPresignExpiry {
		expiry = maxPresignExpiry
	}
	req, err := s.presigner.PresignGetObject(ctx,
		&s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)},
		s3.WithPresignExpires(expiry))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("blob: presigning object URL: %w", err)
	}
	return req.URL, time.Now().Add(expiry), nil
}

func (s *s3Store) Delete(ctx context.Context, ref Ref) error {
	key, err := s.objectKey(ref)
	if err != nil {
		return err
	}
	// S3 DeleteObject is idempotent: deleting an absent key is not an error.
	if _, err := s.api.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)}); err != nil {
		return fmt.Errorf("blob: deleting object: %w", err)
	}
	return nil
}

// s3DeleteBatchMax is the most keys one DeleteObjects request may carry (the S3 API limit).
const s3DeleteBatchMax = 1000

// DeleteTenant lists the tenant's prefix and deletes what it finds, a page at a time, in
// batches of up to s3DeleteBatchMax keys per request. Deleting the keys a page returned does
// not disturb the continuation token.
//
// 🔴 A BATCH DELETE CAN SUCCEED AS A REQUEST AND FAIL PER KEY. DeleteObjects answers 200 with
// an Errors list naming each key it could not delete, so a caller that only checks the call's
// error reports a clean erasure over objects that are still there. Every per-key error fails
// the pass: the count returned is only the keys confirmed deleted, and the error names the
// first failing key. The caller repeats the pass until it finds nothing, so the objects that
// did go are not reported twice.
func (s *s3Store) DeleteTenant(ctx context.Context, tenant string) (int64, error) {
	prefix, err := tenantPrefix(s.instanceID, tenant)
	if err != nil {
		return 0, err
	}
	var n int64
	var next *string
	for {
		out, err := s.api.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket: aws.String(s.bucket), Prefix: aws.String(prefix), ContinuationToken: next,
		})
		if err != nil {
			return n, fmt.Errorf("blob: listing tenant %q: %w", tenant, err)
		}
		// Check the whole page before deleting any of it: the listing asked for this prefix
		// only, so anything else is a server that did not honour it, and deleting it would
		// reach past the tenant.
		ids := make([]s3types.ObjectIdentifier, 0, len(out.Contents))
		for _, o := range out.Contents {
			if o.Key == nil || !strings.HasPrefix(*o.Key, prefix) {
				return n, fmt.Errorf("blob: listing tenant %q returned a key outside its prefix", tenant)
			}
			ids = append(ids, s3types.ObjectIdentifier{Key: o.Key})
		}
		for len(ids) > 0 {
			batch := ids
			if len(batch) > s3DeleteBatchMax {
				batch = batch[:s3DeleteBatchMax]
			}
			ids = ids[len(batch):]
			deleted, err := s.deleteBatch(ctx, batch)
			n += deleted
			if err != nil {
				return n, err
			}
		}
		if out.IsTruncated == nil || !*out.IsTruncated || out.NextContinuationToken == nil {
			return n, nil
		}
		next = out.NextContinuationToken
	}
}

// deleteBatch deletes up to s3DeleteBatchMax keys in one request and returns how many were
// confirmed deleted. Quiet mode reports only failures, so the successes are the batch minus
// the errors; a response naming more failures than the batch held cannot make the count
// negative.
func (s *s3Store) deleteBatch(ctx context.Context, batch []s3types.ObjectIdentifier) (int64, error) {
	out, err := s.api.DeleteObjects(ctx, &s3.DeleteObjectsInput{
		Bucket: aws.String(s.bucket),
		Delete: &s3types.Delete{Objects: batch, Quiet: aws.Bool(true)},
	})
	if err != nil {
		return 0, fmt.Errorf("blob: deleting %d objects: %w", len(batch), err)
	}
	if out == nil || len(out.Errors) == 0 {
		return int64(len(batch)), nil
	}
	failed := min(len(out.Errors), len(batch))
	first := out.Errors[0]
	return int64(len(batch) - failed), fmt.Errorf("blob: deleting objects: %d of %d failed, first %q: %s: %s",
		len(out.Errors), len(batch), aws.ToString(first.Key), aws.ToString(first.Code), aws.ToString(first.Message))
}

// isS3NotFound reports whether err is an S3 "object does not exist" error, across
// the typed (GetObject NoSuchKey / HeadObject NotFound) and generic API-error forms.
func isS3NotFound(err error) bool {
	var nsk *s3types.NoSuchKey
	var nf *s3types.NotFound
	if errors.As(err, &nsk) || errors.As(err, &nf) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound", "404":
			return true
		}
	}
	return false
}

func contentTypeOr(ct *string) string {
	if ct == nil || *ct == "" {
		return defaultContentType
	}
	return *ct
}

func derefInt64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func derefTime(v *time.Time) time.Time {
	if v == nil {
		return time.Time{}
	}
	return *v
}
