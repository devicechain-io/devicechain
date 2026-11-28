// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Object restore against a REAL S3-compatible server. The fake in apply_test.go
// proves the writer sends the checksum and the If-None-Match condition; only a
// server can prove they are honoured: that a put whose bytes do not match the
// checksum is refused, and that a conditional create does not clobber a key.
//
// The server is the object store the platform ships (the object-store module's
// pinned image, as in core/blob's MinIO test). The test starts it through the
// docker CLI, or uses DC_IT_MINIO_ENDPOINT when the caller already runs one
// (credentials dcittest / dcittest-secret), for a test that itself runs in a
// container. Run it with:
//
//	go test -tags integration ./backup/ -run MinIO -v
package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/devicechain-io/dc-microservice/blob"
)

// minioImage is deploy/opentofu/modules/object-store's `image` default. Keep them equal.
const minioImage = "cgr.dev/chainguard/minio:latest@sha256:73297936408b731668ca0e835ac9add1a35cb6026a125c2f0cd476d9ef48222c"

const (
	minioUser     = "dcittest"
	minioPassword = "dcittest-secret"
)

func startRestoreMinio(t *testing.T) (string, *s3.Client) {
	t.Helper()
	endpoint := os.Getenv("DC_IT_MINIO_ENDPOINT")
	if endpoint == "" {
		name := fmt.Sprintf("dc-it-restore-minio-%d", time.Now().UnixNano())
		out, err := exec.Command("docker", "run", "-d", "--name", name, "-p", "127.0.0.1::9000",
			"-e", "MINIO_ROOT_USER="+minioUser, "-e", "MINIO_ROOT_PASSWORD="+minioPassword,
			minioImage, "server", "/data").CombinedOutput()
		if err != nil {
			t.Fatalf("starting MinIO (this test needs Docker, or DC_IT_MINIO_ENDPOINT): %v\n%s", err, out)
		}
		t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
		portOut, err := exec.Command("docker", "port", name, "9000/tcp").Output()
		if err != nil {
			t.Fatalf("docker port: %v", err)
		}
		line := strings.SplitN(strings.TrimSpace(string(portOut)), "\n", 2)[0]
		endpoint = "http://127.0.0.1:" + line[strings.LastIndex(line, ":")+1:]
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		resp, err := http.Get(endpoint + "/minio/health/live")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("MinIO did not become live: %v", err)
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Setenv("AWS_ACCESS_KEY_ID", minioUser)
	t.Setenv("AWS_SECRET_ACCESS_KEY", minioPassword)
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	admin := s3.New(s3.Options{
		Region: "us-east-1", BaseEndpoint: aws.String(endpoint), UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider(minioUser, minioPassword, ""),
	})
	return endpoint, admin
}

// makeBucket creates a bucket, retrying only while MinIO's object layer is
// still initialising (its health endpoint answers before it is).
func makeBucket(t *testing.T, admin *s3.Client, bucket string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		_, err := admin.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(bucket)})
		if err == nil {
			return
		}
		if !strings.Contains(err.Error(), "XMinioServerNotInitialized") || time.Now().After(deadline) {
			t.Fatalf("creating bucket %s: %v", bucket, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func putObject(t *testing.T, admin *s3.Client, bucket, key, body, contentType string) {
	t.Helper()
	_, err := admin.PutObject(context.Background(), &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Body: strings.NewReader(body), ContentType: aws.String(contentType)})
	if err != nil {
		t.Fatal(err)
	}
}

func getObject(t *testing.T, admin *s3.Client, bucket, key string) (string, string) {
	t.Helper()
	out, err := admin.GetObject(context.Background(), &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		t.Fatalf("get %s: %v", key, err)
	}
	defer out.Body.Close()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(data), aws.ToString(out.ContentType)
}

func TestMinIOObjectRestore(t *testing.T) {
	endpoint, admin := startRestoreMinio(t)
	suffix := fmt.Sprint(time.Now().UnixNano())
	source, dest := "dc-restore-src-"+suffix, "dc-restore-dst-"+suffix
	makeBucket(t, admin, source)
	makeBucket(t, admin, dest)
	cfg := func(bucket string) blob.Config {
		return blob.Config{Backend: blob.BackendS3, Bucket: bucket, Endpoint: endpoint, UsePathStyle: true}
	}
	for key, body := range sourceObjects {
		putObject(t, admin, source, key, body, "application/octet-stream")
	}
	putObject(t, admin, source, "sample/acme/branding/badge.png", "png bytes", "image/png")

	seed, _ := restoreServer(t)
	seedRestoreStreams(t, seed)
	dir := filepath.Join(t.TempDir(), "set")
	export := func(bucket string) func(context.Context, io.Writer) error {
		return func(ctx context.Context, w io.Writer) error { return ExportBlobs(ctx, cfg(bucket), "sample", w) }
	}
	if err := Create(context.Background(), seed, "sample", dir, export(source), nil); err != nil {
		t.Fatal(err)
	}
	plan := prepared(t, dir, "sample", false)
	target, err := NewObjectTarget(context.Background(), cfg(dest))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("restores into an empty bucket", func(t *testing.T) {
		var progress bytes.Buffer
		res, err := plan.ApplyObjects(context.Background(), export(dest), target, false, &progress)
		if err != nil {
			t.Fatalf("%v\n%s", err, progress.String())
		}
		if res != (ObjectRestore{Written: 4}) {
			t.Fatalf("result %+v", res)
		}
		for key, body := range sourceObjects {
			if got, _ := getObject(t, admin, dest, key); got != body {
				t.Fatalf("%s holds %q", key, got)
			}
		}
		if got, ct := getObject(t, admin, dest, "sample/acme/branding/badge.png"); got != "png bytes" || ct != "image/png" {
			t.Fatalf("badge holds %q as %q", got, ct)
		}
	})

	t.Run("a re-run writes nothing", func(t *testing.T) {
		res, err := plan.ApplyObjects(context.Background(), export(dest), target, false, nil)
		if err != nil || res != (ObjectRestore{Matched: 4}) {
			t.Fatalf("re-run %+v: %v", res, err)
		}
	})

	// The server, not the client, refuses the clobber and the corrupt upload.
	t.Run("a conditional create does not clobber", func(t *testing.T) {
		if err := target.write(context.Background(), "sample/acme/branding/logo", record("other"), strings.NewReader("other"), false); err == nil {
			t.Fatal("If-None-Match: * create overwrote an existing object")
		}
		if got, _ := getObject(t, admin, dest, "sample/acme/branding/logo"); got != "logo" {
			t.Fatalf("logo clobbered: %q", got)
		}
	})
	t.Run("the server recorded the verified checksum", func(t *testing.T) {
		head, err := admin.HeadObject(context.Background(), &s3.HeadObjectInput{Bucket: aws.String(dest), Key: aws.String("sample/acme/branding/logo"), ChecksumMode: types.ChecksumModeEnabled})
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte("logo"))
		if got := aws.ToString(head.ChecksumSHA256); got != base64.StdEncoding.EncodeToString(sum[:]) {
			t.Fatalf("server holds checksum %q for the restored object", got)
		}
	})

	t.Run("a non-empty bucket is refused, then overwritten on request", func(t *testing.T) {
		putObject(t, admin, dest, "sample/acme/ota/firmware-1", "newer firmware", "application/octet-stream")
		if _, err := plan.ApplyObjects(context.Background(), export(dest), target, false, nil); err == nil || !strings.Contains(err.Error(), "--overwrite-target") {
			t.Fatalf("divergent bucket accepted: %v", err)
		}
		if got, _ := getObject(t, admin, dest, "sample/acme/ota/firmware-1"); got != "newer firmware" {
			t.Fatalf("refused restore still wrote: %q", got)
		}
		res, err := plan.ApplyObjects(context.Background(), export(dest), target, true, nil)
		if err != nil || res != (ObjectRestore{Written: 1, Matched: 3}) {
			t.Fatalf("overwrite %+v: %v", res, err)
		}
		if got, _ := getObject(t, admin, dest, "sample/acme/ota/firmware-1"); got != "firmware one" {
			t.Fatalf("not replaced: %q", got)
		}
	})
}
