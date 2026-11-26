// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The S3 backend against a REAL S3-compatible server.
//
// 🔴 WHY THIS FILE EXISTS: every other test of the S3 store runs against a fake that returns what
// the test tells it to, so it can prove the store calls the API as designed and cannot prove the
// API accepts the calls. A multipart upload is exactly where the two come apart: the part size
// floor, the part ordering and ETags, the checksum trailer the SDK adds by default, and what an
// aborted upload leaves behind are all behaviours of the server. This starts the object store the
// platform itself ships (the image the object-store module pins) and runs the store against it.
//
// It starts its own container through the docker CLI, so it needs Docker and nothing else. The
// image reference is the object-store module's default, digest and all, and must be moved with it.
// Tagged `integration` so it stays out of the default `go test ./...`. Run it with:
//
//	go test -tags integration ./blob/ -run MinIO -v
package blob

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// minioImage is deploy/opentofu/modules/object-store's `image` default. Keep them equal.
const minioImage = "cgr.dev/chainguard/minio:latest@sha256:73297936408b731668ca0e835ac9add1a35cb6026a125c2f0cd476d9ef48222c"

const (
	minioUser     = "dcittest"
	minioPassword = "dcittest-secret"
	minioBucket   = "dc-blob-it"
)

type minioServer struct {
	endpoint string
	admin    *s3.Client
}

// startMinio runs a throwaway server, waits until it answers, creates the bucket and removes the
// container when the test ends.
func startMinio(t *testing.T) *minioServer {
	t.Helper()
	name := fmt.Sprintf("dc-it-minio-%d", time.Now().UnixNano())
	out, err := exec.Command("docker", "run", "-d", "--name", name, "-p", "127.0.0.1::9000",
		"-e", "MINIO_ROOT_USER="+minioUser, "-e", "MINIO_ROOT_PASSWORD="+minioPassword,
		minioImage, "server", "/data").CombinedOutput()
	if err != nil {
		t.Fatalf("starting MinIO (this test needs Docker): %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })

	portOut, err := exec.Command("docker", "port", name, "9000/tcp").Output()
	if err != nil {
		t.Fatalf("docker port: %v", err)
	}
	line := strings.SplitN(strings.TrimSpace(string(portOut)), "\n", 2)[0]
	endpoint := "http://127.0.0.1:" + line[strings.LastIndex(line, ":")+1:]

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
			logs, _ := exec.Command("docker", "logs", name).CombinedOutput()
			t.Fatalf("MinIO did not become live: %v\n%s", err, logs)
		}
		time.Sleep(250 * time.Millisecond)
	}

	admin := s3.New(s3.Options{
		Region: "us-east-1", BaseEndpoint: aws.String(endpoint), UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider(minioUser, minioPassword, ""),
	})
	if err := createBucketWhenReady(context.Background(), admin, minioBucket, time.Now().Add(60*time.Second)); err != nil {
		t.Fatalf("creating the bucket: %v", err)
	}
	return &minioServer{endpoint: endpoint, admin: admin}
}

// createBucketWhenReady creates the bucket, retrying while the server says it is not up yet.
//
// 🔴 WHY /minio/health/live IS NOT ENOUGH: it answers 200 as soon as the HTTP listener is up, while the
// object layer behind it is still initialising, and every S3 call in that window is refused with
// 503 XMinioServerNotInitialized. The SDK's own three quick retries finish inside that window on a
// loaded runner (CI failed TestMinIOCancelledPutLeavesNoUpload this way at the bucket creation, before
// any upload ran). Only that answer is retried: any other refusal fails at once. Same fix as the
// update-management MinIO tests (#3178).
func createBucketWhenReady(ctx context.Context, admin *s3.Client, bucket string, deadline time.Time) error {
	for {
		_, err := admin.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
		if err == nil || !strings.Contains(err.Error(), "XMinioServerNotInitialized") {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("MinIO still not initialised at the deadline: %w", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// store builds the store the way production does: NewS3Store, credentials from the environment.
func (m *minioServer) store(t *testing.T) Store {
	t.Helper()
	t.Setenv("AWS_ACCESS_KEY_ID", minioUser)
	t.Setenv("AWS_SECRET_ACCESS_KEY", minioPassword)
	s, err := NewS3Store(context.Background(), Config{
		Backend: BackendS3, Bucket: minioBucket, Endpoint: m.endpoint, UsePathStyle: true,
	}, "inst1")
	if err != nil {
		t.Fatalf("NewS3Store: %v", err)
	}
	return s
}

func (m *minioServer) incompleteUploads(t *testing.T) int {
	t.Helper()
	out, err := m.admin.ListMultipartUploads(context.Background(), &s3.ListMultipartUploadsInput{Bucket: aws.String(minioBucket)})
	if err != nil {
		t.Fatalf("ListMultipartUploads: %v", err)
	}
	return len(out.Uploads)
}

func (m *minioServer) hasObject(t *testing.T, key string) bool {
	t.Helper()
	_, err := m.admin.HeadObject(context.Background(), &s3.HeadObjectInput{Bucket: aws.String(minioBucket), Key: aws.String(key)})
	return err == nil
}

// patterned is a deterministic stream of n bytes that does not repeat on a part boundary, so a
// part uploaded twice, dropped or reordered changes the digest.
func patterned(n int64) io.Reader {
	return io.LimitReader(rand.New(rand.NewSource(7)), n)
}

func digestOf(r io.Reader) ([32]byte, int64) {
	h := sha256.New()
	n, _ := io.Copy(h, r)
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum, n
}

// A multipart Put round-trips byte for byte, at sizes either side of the part boundary (8 MiB)
// and a size that ends in a short last part. The Get reads back the same bytes and size.
func TestMinIOMultipartPutGetRoundTrips(t *testing.T) {
	m := startMinio(t)
	store := m.store(t)
	const mib = 1 << 20
	for _, size := range []int64{8 * mib, 8*mib + 1, 16 * mib, 20*mib + 12345} {
		id := fmt.Sprintf("blob-%d.bin", size)
		ref, err := store.Put(context.Background(), Key{Tenant: "acme", Purpose: "update-artifact", ID: id},
			patterned(size), PutOptions{ContentType: "application/octet-stream"})
		if err != nil {
			t.Fatalf("size %d: Put: %v", size, err)
		}
		rc, info, err := store.Open(context.Background(), ref)
		if err != nil {
			t.Fatalf("size %d: Open: %v", size, err)
		}
		got, n := digestOf(rc)
		_ = rc.Close()
		want, _ := digestOf(patterned(size))
		if n != size || info.Size != size {
			t.Fatalf("size %d: read %d bytes, Info.Size %d", size, n, info.Size)
		}
		if got != want {
			t.Fatalf("size %d: the stored object differs from the stream that was put", size)
		}
		if info.ContentType != "application/octet-stream" {
			t.Fatalf("size %d: content type %q", size, info.ContentType)
		}
	}
	if n := m.incompleteUploads(t); n != 0 {
		t.Fatalf("%d incomplete multipart uploads left after successful puts", n)
	}
}

// A ranged read of a multipart object lands on the right bytes: the part boundary is invisible
// to a reader.
func TestMinIOMultipartObjectServesRanges(t *testing.T) {
	m := startMinio(t)
	store := m.store(t)
	const mib = 1 << 20
	size := int64(20 * mib)
	ref, err := store.Put(context.Background(), Key{Tenant: "acme", Purpose: "update-artifact", ID: "range.bin"},
		patterned(size), PutOptions{})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	var whole bytes.Buffer
	_, _ = io.Copy(&whole, patterned(size))
	rc, _, err := store.OpenRange(context.Background(), ref, 8*mib-10, 20)
	if err != nil {
		t.Fatalf("OpenRange: %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if !bytes.Equal(got, whole.Bytes()[8*mib-10:8*mib+10]) {
		t.Fatal("a range across the first part boundary returned the wrong bytes")
	}
}

// An over-limit stream is refused, the upload it had started is aborted, and nothing is left:
// no incomplete multipart upload and no object. The control is the same Put under the limit.
func TestMinIOOverLimitPutLeavesNoUpload(t *testing.T) {
	m := startMinio(t)
	store := m.store(t)
	const mib = 1 << 20
	key := Key{Tenant: "acme", Purpose: "update-artifact", ID: "over.bin"}

	_, err := store.Put(context.Background(), key, patterned(30*mib), PutOptions{MaxSize: 10 * mib})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if n := m.incompleteUploads(t); n != 0 {
		t.Fatalf("%d incomplete multipart uploads left behind by an over-limit put", n)
	}
	if m.hasObject(t, "inst1/acme/update-artifact/over.bin") {
		t.Fatal("an object exists after an over-limit put")
	}

	// the control: within the limit the same call stores the object, so the checks above can fail
	if _, err := store.Put(context.Background(), key, patterned(10*mib), PutOptions{MaxSize: 10 * mib}); err != nil {
		t.Fatalf("control put within the limit: %v", err)
	}
	if !m.hasObject(t, "inst1/acme/update-artifact/over.bin") {
		t.Fatal("control: the object is missing")
	}
}

// A cancelled caller mid-stream: the Put fails and the abort still reaches the server, because it
// is sent on a context the cancellation does not touch.
func TestMinIOCancelledPutLeavesNoUpload(t *testing.T) {
	m := startMinio(t)
	store := m.store(t)
	const mib = 1 << 20
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src := &cancelAfter{r: patterned(40 * mib), after: 12 * mib, cancel: cancel}
	_, err := store.Put(ctx, Key{Tenant: "acme", Purpose: "update-artifact", ID: "cancel.bin"}, src, PutOptions{})
	if err == nil {
		t.Fatal("a cancelled put returned no error")
	}
	if n := m.incompleteUploads(t); n != 0 {
		t.Fatalf("%d incomplete multipart uploads left behind by a cancelled put", n)
	}
	if m.hasObject(t, "inst1/acme/update-artifact/cancel.bin") {
		t.Fatal("an object exists after a cancelled put")
	}
}

type cancelAfter struct {
	r      io.Reader
	after  int64
	read   int64
	cancel context.CancelFunc
}

func (c *cancelAfter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read += int64(n)
	if c.read >= c.after {
		c.cancel()
	}
	return n, err
}
