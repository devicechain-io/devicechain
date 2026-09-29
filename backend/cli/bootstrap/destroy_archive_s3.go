// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// s3ArchiveStore is archiveStore over the S3 API of the in-cluster object store.
type s3ArchiveStore struct {
	api *s3.Client
}

// deleteParallelism bounds the per-object deletes in flight. Enough that an archive of
// thousands of segments goes in seconds over a port-forward, few enough not to crowd a
// single-replica store that is still serving every other instance's archiver.
const deleteParallelism = 8

// newS3ArchiveStore builds a client for one endpoint with a literal credential.
//
// 🔴 NOT awsconfig.LoadDefaultConfig. That reads the operator's environment and ~/.aws —
// a profile, a region, a role to assume — into a command that is about to delete things,
// and none of it is about the in-cluster store. Everything this client knows is passed
// here.
func newS3ArchiveStore(endpoint, accessKeyID, secretAccessKey string) *s3ArchiveStore {
	return &s3ArchiveStore{api: s3.New(s3.Options{
		Region:       "us-east-1",
		BaseEndpoint: aws.String(endpoint),
		// The in-cluster store is addressed by path: there is no DNS for
		// <bucket>.127.0.0.1.
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, ""),
		// Checksums only where the API requires one, which no call here does.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})}
}

func (s *s3ArchiveStore) List(ctx context.Context, bucket, prefix string) ([]archiveObject, error) {
	var out []archiveObject
	pages := s3.NewListObjectsV2Paginator(s.api, &s3.ListObjectsV2Input{Bucket: aws.String(bucket), Prefix: aws.String(prefix)})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, o := range page.Contents {
			out = append(out, archiveObject{Key: aws.ToString(o.Key), Size: aws.ToInt64(o.Size)})
		}
	}
	return out, nil
}

func (s *s3ArchiveStore) ListPrefixes(ctx context.Context, bucket, prefix string) ([]string, error) {
	var out []string
	pages := s3.NewListObjectsV2Paginator(s.api, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket), Prefix: aws.String(prefix), Delimiter: aws.String("/"),
	})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, p := range page.CommonPrefixes {
			out = append(out, aws.ToString(p.Prefix))
		}
	}
	return out, nil
}

// Delete removes each key with its own DeleteObject.
//
// 🔴 NOT DeleteObjects. The multi-object delete is the one S3 call that REQUIRES a body
// checksum, and which checksum header an S3-compatible server insists on has changed
// under this SDK before (Content-MD5 against the CRC32 the SDK now sends). The in-cluster
// store is a pinned third-party build that moves weekly, and a delete it rejected would
// leave the archive in place on every destroy. A single-object delete needs no checksum
// on any server; and the listing afterwards, not these calls' results, is what decides
// whether the path is empty.
func (s *s3ArchiveStore) Delete(ctx context.Context, bucket string, keys []string) error {
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		first error
	)
	sem := make(chan struct{}, deleteParallelism)
	for _, key := range keys {
		mu.Lock()
		stop := first != nil
		mu.Unlock()
		if stop {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(key string) {
			defer func() { <-sem; wg.Done() }()
			_, err := s.api.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
			if err != nil {
				mu.Lock()
				if first == nil {
					first = fmt.Errorf("deleting %s: %w", key, err)
				}
				mu.Unlock()
			}
		}(key)
	}
	wg.Wait()
	return first
}
