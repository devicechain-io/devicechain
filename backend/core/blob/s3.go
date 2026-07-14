// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package blob

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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
}

// s3ObjectAPI is the subset of the S3 client the store uses. Narrowing it to an
// interface lets the store be unit-tested with a fake — the concrete *s3.Client
// satisfies it.
type s3ObjectAPI interface {
	PutObject(ctx context.Context, in *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(ctx context.Context, in *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	HeadObject(ctx context.Context, in *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	DeleteObject(ctx context.Context, in *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

// s3Presigner is the presign subset (URL). *s3.PresignClient satisfies it, and
// presigning is a purely local computation (no network), so it is exercised
// directly in tests.
type s3Presigner interface {
	PresignGetObject(ctx context.Context, in *s3.GetObjectInput, optFns ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
}

const (
	// s3UnboundedPutCap bounds an object written with PutOptions.MaxSize == 0 so a
	// caller that sets no ceiling cannot make the store buffer without limit (the S3
	// backend buffers to hand PutObject a known length). The current consumer
	// (branding logos) always sets MaxSize; large streaming/multipart uploads
	// (firmware) are a later slice, together with the range-read.
	s3UnboundedPutCap = 64 << 20 // 64 MiB
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
	// Buffer bounded by the ceiling so we can enforce MaxSize and hand PutObject a
	// known-length, seekable body (required for signing without a chunked upload).
	limit := opts.MaxSize
	if limit <= 0 {
		limit = s3UnboundedPutCap
	}
	buf, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return Ref{}, fmt.Errorf("blob: reading object: %w", err)
	}
	if int64(len(buf)) > limit {
		return Ref{}, ErrTooLarge
	}
	input := &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(fullKey),
		Body:          bytes.NewReader(buf),
		ContentLength: aws.Int64(int64(len(buf))),
	}
	if opts.ContentType != "" {
		input.ContentType = aws.String(opts.ContentType)
	}
	if _, err := s.api.PutObject(ctx, input); err != nil {
		return Ref{}, fmt.Errorf("blob: putting object: %w", err)
	}
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
