// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package backup

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/devicechain-io/dc-microservice/blob"
)

type objectRecord struct {
	Ref         string `json:"ref"`
	Size        int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`
	ContentType string `json:"content_type,omitempty"`
	VersionID   string `json:"version_id,omitempty"`
}

// ExportBlobs copies a frozen instance prefix into a tar archive with index.json.
// Filesystem roots must be locally mounted, read-only PVC exports. S3 uses the
// standard AWS credential chain and records the version returned by each GET.
func ExportBlobs(ctx context.Context, cfg blob.Config, instance string, w io.Writer) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if instance == "" || strings.ContainsAny(instance, "/\\:") || instance == "." || instance == ".." {
		return fmt.Errorf("invalid instance")
	}
	backend := cfg.Backend
	if backend == "" {
		backend = blob.BackendFilesystem
	}
	tw := tar.NewWriter(w)
	index := []objectRecord{}
	copyObject := func(key string, size int64, contentType, version string, r io.Reader) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !fs.ValidPath(key) || !strings.HasPrefix(key, instance+"/") || strings.ContainsAny(key, "\\:") {
			return fmt.Errorf("invalid instance object key %q", key)
		}
		if err := tw.WriteHeader(&tar.Header{Name: "objects/" + key, Mode: 0o600, Size: size}); err != nil {
			return err
		}
		h := sha256.New()
		n, err := io.Copy(io.MultiWriter(tw, h), r)
		if err != nil {
			return err
		}
		if n != size {
			return fmt.Errorf("object %q changed size", key)
		}
		index = append(index, objectRecord{Ref: blob.Ref{Backend: backend, Key: key}.String(), Size: n, SHA256: hex.EncodeToString(h.Sum(nil)), ContentType: contentType, VersionID: version})
		return nil
	}
	switch backend {
	case blob.BackendFilesystem:
		root, err := os.OpenRoot(cfg.Directory)
		if err != nil {
			return err
		}
		defer root.Close()
		// A missing instance directory is not evidence of an empty object store.
		err = fs.WalkDir(root.FS(), instance, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				return nil
			}
			if !d.Type().IsRegular() {
				return fmt.Errorf("object %q is not a regular file", path)
			}
			if strings.HasPrefix(filepath.Base(path), ".put-") {
				return fmt.Errorf("incomplete upload %q: freeze object mutations before backup", path)
			}
			f, err := root.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			info, err := f.Stat()
			if err != nil {
				return err
			}
			return copyObject(path, info.Size(), "", "", f)
		})
		if err != nil {
			return err
		}
	case blob.BackendS3:
		region := cfg.Region
		if region == "" {
			region = "us-east-1"
		}
		ac, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
		if err != nil {
			return err
		}
		client := s3.NewFromConfig(ac, func(o *s3.Options) {
			o.UsePathStyle = cfg.UsePathStyle
			if cfg.Endpoint != "" {
				o.BaseEndpoint = aws.String(cfg.Endpoint)
			}
		})
		pages := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{Bucket: aws.String(cfg.Bucket), Prefix: aws.String(instance + "/")})
		for pages.HasMorePages() {
			page, err := pages.NextPage(ctx)
			if err != nil {
				return err
			}
			for _, obj := range page.Contents {
				out, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(cfg.Bucket), Key: obj.Key, IfMatch: obj.ETag})
				if err != nil {
					return err
				}
				err = copyObject(aws.ToString(obj.Key), aws.ToInt64(out.ContentLength), aws.ToString(out.ContentType), aws.ToString(out.VersionId), out.Body)
				closeErr := out.Body.Close()
				if err != nil {
					return err
				}
				if closeErr != nil {
					return closeErr
				}
			}
		}
	default:
		return fmt.Errorf("blob backup backend %q is unsupported", backend)
	}
	data, err := json.Marshal(index)
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: "index.json", Mode: 0o600, Size: int64(len(data))}); err != nil {
		return err
	}
	if _, err := tw.Write(data); err != nil {
		return err
	}
	return tw.Close()
}
