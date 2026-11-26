// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// An artifact larger than the old 64 MiB buffered-put cap, uploaded through the real upload
// endpoint into a REAL S3-compatible server.
//
// 🔴 WHY THIS FILE EXISTS: the S3 store used to buffer an upload in memory, so update-management
// refused to declare an artifact above 64 MiB on that backend. The store now streams a multipart
// upload (proved against MinIO in core/blob), and the clamp is gone. A fake store cannot show that
// the two halves meet: that the handler's MaxSize reaches the multipart path, that a 100 MiB body
// is verified against its declared digest while it streams, and that the object read back is the
// object that was declared. This starts the object store the platform ships (the image the
// object-store module pins) and does exactly that.
//
// It starts its own container through the docker CLI, so it needs Docker and nothing else. Run it with:
//
//	go test -tags integration ./upload/ -run MinIO -v
package upload

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/devicechain-io/dc-microservice/blob"
	"github.com/devicechain-io/dc-update-management/model"
	"github.com/stretchr/testify/require"
)

// minioImage is deploy/opentofu/modules/object-store's `image` default (and core/blob's integration
// test uses the same one). Keep them equal.
const minioImage = "cgr.dev/chainguard/minio:latest@sha256:73297936408b731668ca0e835ac9add1a35cb6026a125c2f0cd476d9ef48222c"

const (
	minioUser     = "dcittest"
	minioPassword = "dcittest-secret"
	minioBucket   = "dc-upload-it"
)

// startMinio runs a throwaway server, waits until it answers, creates the bucket and removes the
// container when the test ends. It returns the endpoint and a client for inspecting the bucket.
func startMinio(t *testing.T) (string, *s3.Client) {
	t.Helper()
	name := fmt.Sprintf("dc-it-um-minio-%d", time.Now().UnixNano())
	out, err := exec.Command("docker", "run", "-d", "--name", name, "-p", "127.0.0.1::9000",
		"-e", "MINIO_ROOT_USER="+minioUser, "-e", "MINIO_ROOT_PASSWORD="+minioPassword,
		minioImage, "server", "/data").CombinedOutput()
	require.NoError(t, err, "starting MinIO (this test needs Docker): %s", out)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })

	portOut, err := exec.Command("docker", "port", name, "9000/tcp").Output()
	require.NoError(t, err)
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
	require.NoError(t, createBucketWhenReady(context.Background(), admin, minioBucket, time.Now().Add(60*time.Second)), "creating the bucket")
	return endpoint, admin
}

// createBucketWhenReady creates the bucket, retrying while the server says it is not up yet.
//
// 🔴 WHY /minio/health/live IS NOT ENOUGH: it answers 200 as soon as the HTTP listener is up, while the
// object layer behind it is still initialising, and every S3 call in that window is refused with
// 503 XMinioServerNotInitialized ("Server not initialized yet, please try again"). The SDK's own
// retries (three attempts, tens of milliseconds apart) finish inside the window on a loaded runner,
// which failed the test in about 0.7 s at the bucket creation, before any upload ran. Only that
// answer is retried: any other refusal (bad credentials, a bucket that cannot be created) fails at once.
func createBucketWhenReady(ctx context.Context, admin *s3.Client, bucket string, deadline time.Time) error {
	for {
		_, err := admin.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
		// The SDK wraps the code into its error text; the module does not import smithy-go directly.
		if err == nil || !strings.Contains(err.Error(), "XMinioServerNotInitialized") {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("MinIO still not initialised at the deadline: %w", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// s3Env is the upload test env with its store replaced by the S3 backend against a fresh MinIO.
func s3Env(t *testing.T) (*env, *s3.Client) {
	t.Helper()
	endpoint, admin := startMinio(t)
	t.Setenv("AWS_ACCESS_KEY_ID", minioUser)
	t.Setenv("AWS_SECRET_ACCESS_KEY", minioPassword)
	store, err := blob.New(context.Background(), blob.Config{
		Backend: blob.BackendS3, Bucket: minioBucket, Endpoint: endpoint, UsePathStyle: true,
	}, "inst1")
	require.NoError(t, err)
	e := newEnv(t)
	e.store = store
	e.api.Blobs = store
	e.api.Limits.MaxArtifactBytes = 512 << 20
	return e, admin
}

// patterned is a deterministic stream of n bytes that does not repeat on a part boundary, so a part
// uploaded twice, dropped or reordered changes the digest.
func patterned(n int64) io.Reader { return io.LimitReader(rand.New(rand.NewSource(11)), n) }

func sha256Of(r io.Reader) (string, int64) {
	h := sha256.New()
	n, _ := io.Copy(h, r)
	return model.DigestPrefix + hex.EncodeToString(h.Sum(nil)), n
}

func (e *env) putStream(token string, size int64) *http.Response {
	e.t.Helper()
	req, err := http.NewRequest(http.MethodPut, e.url(token), patterned(size))
	require.NoError(e.t, err)
	req.ContentLength = size
	req.Header.Set("Authorization", "Bearer "+e.writer("acme"))
	resp, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	e.t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// An artifact past the old 64 MiB cap is declared (no clamp), uploaded through the endpoint, stored
// as a multipart object, and read back byte for byte; the upload does not hold the artifact in memory.
func TestMinIOUploadOfAnArtifactPastTheOldBufferedCapStreamsAndVerifies(t *testing.T) {
	e, admin := s3Env(t)
	const size = 100 << 20 // above the old 64 MiB cap
	digest, n := sha256Of(patterned(size))
	require.EqualValues(t, size, n)

	_, err := e.api.CreateArtifact(ctxFor("acme"), &model.ArtifactCreateRequest{
		Token: "big", Kind: model.KindFirmware, Name: "big", Version: "1", Component: "firmware",
		SizeBytes: size, Digest: digest, CompatibleProfiles: []string{"gateway"},
	})
	require.NoError(t, err, "an artifact above 64 MiB is declarable on the S3 backend")

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	status(t, e.putStream("big", size), http.StatusOK)
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("uploaded %d MiB, allocated %d MiB", size>>20, allocated>>20)
	// A multipart put holds one part at a time while it streams; a store that buffered the object
	// would allocate at least its whole size.
	require.Less(t, allocated, uint64(size), "the upload buffered the whole artifact")

	a := e.state("acme", "big")
	require.Equal(t, model.StateAvailable, a.State)
	ref, err := blob.ParseRef(a.BlobRef.String)
	require.NoError(t, err)
	rc, info, err := e.store.Open(ctxFor("acme"), ref)
	require.NoError(t, err)
	defer rc.Close()
	got, read := sha256Of(rc)
	require.EqualValues(t, size, info.Size)
	require.EqualValues(t, size, read)
	require.Equal(t, digest, got, "the stored object is the declared artifact")

	// A ranged read across a part boundary lands on the right bytes, as a device resuming a download does.
	var whole bytes.Buffer
	_, _ = io.Copy(&whole, patterned(size))
	rr, _, err := e.store.OpenRange(ctxFor("acme"), ref, 16<<20-10, 20)
	require.NoError(t, err)
	part, _ := io.ReadAll(rr)
	_ = rr.Close()
	require.Equal(t, whole.Bytes()[16<<20-10:16<<20+10], part)

	ups, err := admin.ListMultipartUploads(context.Background(), &s3.ListMultipartUploadsInput{Bucket: aws.String(minioBucket)})
	require.NoError(t, err)
	require.Empty(t, ups.Uploads, "no multipart upload is left open")
}

// A body that does not match the declared digest is refused after the stream, and the multipart
// upload is aborted rather than left holding parts.
func TestMinIOUploadWithAWrongDigestLeavesNothingBehind(t *testing.T) {
	e, admin := s3Env(t)
	const size = 70 << 20
	wrong, _ := sha256Of(strings.NewReader("not the artifact"))
	_, err := e.api.CreateArtifact(ctxFor("acme"), &model.ArtifactCreateRequest{
		Token: "bad", Kind: model.KindFirmware, Name: "bad", Version: "1", Component: "firmware",
		SizeBytes: size, Digest: wrong, CompatibleProfiles: []string{"gateway"},
	})
	require.NoError(t, err)

	resp := e.putStream("bad", size)
	require.GreaterOrEqual(t, resp.StatusCode, 400, "a digest mismatch is refused")
	require.Equal(t, model.StateUploading, e.state("acme", "bad").State)

	ups, err := admin.ListMultipartUploads(context.Background(), &s3.ListMultipartUploadsInput{Bucket: aws.String(minioBucket)})
	require.NoError(t, err)
	require.Empty(t, ups.Uploads, "a refused upload leaves no multipart upload open")
	objs, err := admin.ListObjectsV2(context.Background(), &s3.ListObjectsV2Input{Bucket: aws.String(minioBucket)})
	require.NoError(t, err)
	require.Empty(t, objs.Contents, "a refused upload leaves no object behind")
}
