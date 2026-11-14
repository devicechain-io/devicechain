// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/blob"
)

func TestS3BlobArchive(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	listed, fetched := false, false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("list-type") == "2" {
			listed = true
			if r.URL.Query().Get("prefix") != "sample/" {
				t.Errorf("unscoped list: %s", r.URL)
			}
			w.Header().Set("Content-Type", "application/xml")
			io.WriteString(w, `<ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>sample/t/branding/logo.png</Key><Size>4</Size><ETag>"etag"</ETag></Contents></ListBucketResult>`)
			return
		}
		fetched = true
		if r.URL.Path != "/bucket/sample/t/branding/logo.png" || r.Header.Get("If-Match") != `"etag"` {
			t.Errorf("unexpected GET %s, If-Match %s", r.URL, r.Header.Get("If-Match"))
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("x-amz-version-id", "version-7")
		io.WriteString(w, "logo")
	}))
	defer srv.Close()
	var out bytes.Buffer
	if err := ExportBlobs(context.Background(), blob.Config{Backend: "s3", Bucket: "bucket", Endpoint: srv.URL, UsePathStyle: true}, "sample", &out); err != nil {
		t.Fatal(err)
	}
	if !listed || !fetched {
		t.Fatal("S3 listing or GET omitted")
	}
	tr := tar.NewReader(&out)
	if _, err := tr.Next(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(tr)
	if err != nil || string(data) != "logo" {
		t.Fatalf("wrong S3 bytes %q: %v", data, err)
	}
	if _, err := tr.Next(); err != nil {
		t.Fatal(err)
	}
	var index []objectRecord
	if err := json.NewDecoder(tr).Decode(&index); err != nil {
		t.Fatal(err)
	}
	if len(index) != 1 || index[0].VersionID != "version-7" || index[0].ContentType != "image/png" || index[0].Ref != "blob://s3/sample/t/branding/logo.png" {
		t.Fatalf("wrong S3 metadata: %+v", index)
	}
}

func TestBlobExportFailsClosed(t *testing.T) {
	for _, tc := range []string{"missing prefix", "incomplete upload", "unsupported backend", "cancelled"} {
		t.Run(tc, func(t *testing.T) {
			root := t.TempDir()
			cfg := blob.Config{Directory: root}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch tc {
			case "unsupported backend":
				cfg.Backend = "gcs"
			case "incomplete upload", "cancelled":
				if err := os.Mkdir(filepath.Join(root, "sample"), 0o700); err != nil {
					t.Fatal(err)
				}
				name := "object"
				if tc == "incomplete upload" {
					name = ".put-inflight"
				} else {
					cancel()
				}
				if err := os.WriteFile(filepath.Join(root, "sample", name), []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			if err := ExportBlobs(ctx, cfg, "sample", &out); err == nil {
				t.Fatal("unsafe export accepted")
			}
		})
	}
	for _, instance := range []string{"../other", "other/x", "..", ""} {
		if err := ExportBlobs(context.Background(), blob.Config{Directory: t.TempDir()}, instance, io.Discard); err == nil {
			t.Fatalf("unsafe prefix %q accepted", instance)
		}
	}
}

func TestBlobExportExcludesOtherInstances(t *testing.T) {
	root := t.TempDir()
	for _, instance := range []string{"sample", "other"} {
		if err := os.Mkdir(filepath.Join(root, instance), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, instance, "data"), []byte(instance), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := ExportBlobs(context.Background(), blob.Config{Directory: root}, "sample", &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "other") {
		t.Fatal("export crossed instance boundary")
	}
}
