// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package backup

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/backupset"
	"github.com/devicechain-io/dc-microservice/blob"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/durability"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/natsauth"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	"github.com/klauspost/compress/s2"
	server "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats-server/v2/server/archive"
	"github.com/nats-io/nats.go"
)

func fixture(t *testing.T) (*nats.Conn, *nats.Conn) {
	t.Helper()
	p, err := natsauth.AdminPermissions("sample")
	if err != nil {
		t.Fatal(err)
	}
	srvs, _ := dctest.StartJetStreamClusterConfigured(t, 3, func(_ int, o *server.Options) {
		o.NoAuthUser = "seed" // The fixture's readiness probe connects without credentials.
		o.Users = []*server.User{
			{Username: "seed", Password: "seed"},
			{Username: "backup", Password: "backup", Permissions: &server.Permissions{Publish: &server.SubjectPermission{Allow: p.Publish.Allow}, Subscribe: &server.SubjectPermission{Allow: p.Subscribe.Allow}}},
			{Username: "no-snapshot", Password: "no-snapshot", Permissions: &server.Permissions{Publish: &server.SubjectPermission{Allow: []string{"$JS.API.INFO", "$JS.API.STREAM.INFO.>"}}, Subscribe: &server.SubjectPermission{Allow: []string{"_INBOX.>"}}}},
		}
	})
	dial := func(user string, options ...nats.Option) *nats.Conn {
		nc, err := nats.Connect(srvs[0].ClientURL(), append(options, nats.UserInfo(user, user), nats.NoReconnect())...)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(nc.Close)
		return nc
	}
	seed := dial("seed")
	js, _ := seed.JetStream()
	for _, item := range backupset.Items() {
		if item.Kind != backupset.KindStream && item.Kind != backupset.KindGatewayStream {
			continue
		}
		if item.BackupClass != durability.BackupRequired && item.BackupClass != durability.BackupCheckpoint {
			continue
		}
		name := item.Name
		if item.Kind == backupset.KindStream {
			name = messaging.StreamName("sample", name)
		}
		if _, err := js.AddStream(&nats.StreamConfig{Name: name, Subjects: []string{"seed." + name}, Storage: nats.FileStorage}); err != nil {
			t.Fatal(err)
		}
	}
	return seed, dial("backup", nats.CustomInboxPrefix(messaging.InboxPrefix(config.NatsAdminPrincipal)))
}

func blobFixture(t *testing.T) func(context.Context, io.Writer) error {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sample", "tenant", "ota"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sample", "tenant", "ota", "firmware"), []byte("firmware bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	return func(ctx context.Context, w io.Writer) error {
		return ExportBlobs(ctx, blob.Config{Directory: root}, "sample", w)
	}
}

func TestCreateRoundTrip(t *testing.T) {
	seed, nc := fixture(t)
	js, _ := seed.JetStream()
	name := messaging.StreamName("sample", backupset.Items()[0].Name)
	// Exceed the server's 8 MiB flow-control window with incompressible data.
	data := make([]byte, 512<<10)
	wantMessages := map[uint64][32]byte{}
	for range 18 {
		if _, err := rand.Read(data); err != nil {
			t.Fatal(err)
		}
		ack, err := js.Publish("seed."+name, data)
		if err != nil {
			t.Fatal(err)
		}
		wantMessages[ack.Sequence] = sha256.Sum256(data)
	}
	if _, err := js.AddConsumer(name, &nats.ConsumerConfig{Durable: "pending", AckPolicy: nats.AckExplicitPolicy}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "backup")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := Create(ctx, nc, "sample", dir, blobFixture(t)); err != nil {
		t.Fatal(err)
	}
	m, err := Inspect(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := 1
	for _, item := range backupset.Items() {
		if (item.Kind == backupset.KindStream || item.Kind == backupset.KindGatewayStream) && (item.BackupClass == durability.BackupRequired || item.BackupClass == durability.BackupCheckpoint) {
			want++
		}
	}
	if len(m.Entries) != want {
		t.Fatalf("got %d entries, want %d", len(m.Entries), want)
	}
	if err := js.DeleteStream("$MQTT_msgs"); err != nil {
		t.Fatal(err)
	}
	withoutGateway := filepath.Join(t.TempDir(), "backup")
	if err := Create(ctx, nc, "sample", withoutGateway, blobFixture(t)); err != nil {
		t.Fatal(err)
	}
	second, err := Inspect(withoutGateway)
	if err != nil || len(second.Entries) != want-1 {
		t.Fatalf("backup without MQTT: %v", err)
	}
	for _, e := range second.Entries {
		if e.Kind == "object-store" {
			path := filepath.Join(withoutGateway, e.Snapshot)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			data[512] ^= 1
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Inspect(withoutGateway); err == nil {
				t.Fatal("corrupt blob archive accepted")
			}
		}
	}
	var snapshotPath string
	for _, e := range m.Entries {
		if e.Name == name {
			snapshotPath = filepath.Join(dir, e.Snapshot)
		}
	}
	raw, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	meta, err := bundle.Open("metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	var metadata struct {
		Config nats.StreamConfig `json:"config"`
		State  nats.StreamState  `json:"state"`
	}
	if err := json.NewDecoder(meta).Decode(&metadata); err != nil {
		t.Fatal(err)
	}
	meta.Close()
	if metadata.Config.Name != name || metadata.State.Msgs != 18 {
		t.Fatalf("stream configuration/state not preserved: %+v", metadata)
	}
	native, err := bundle.Open("snapshot.s2")
	if err != nil {
		t.Fatal(err)
	}
	defer native.Close()
	tr := archive.NewReader(s2.NewReader(native))
	consumer := false
	messageCount := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("native archive %d bytes, prefix %x: %v", len(raw), raw[:min(len(raw), 32)], err)
		}
		if strings.Contains(h.Name, "pending") {
			consumer = true
		}
		if h.Sequence > 0 {
			payload, err := io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}
			if h.Name != "seed."+name || sha256.Sum256(payload) != wantMessages[h.Sequence] {
				t.Fatalf("native snapshot changed message %d", h.Sequence)
			}
			messageCount++
		}
	}
	if !consumer {
		t.Fatal("snapshot omitted durable consumer")
	}
	if messageCount != len(wantMessages) {
		t.Fatalf("snapshot has %d messages, want %d", messageCount, len(wantMessages))
	}
	// Same-size corruption must be caught by the digest, not just a size check.
	raw[len(raw)/2] ^= 1
	if err := os.WriteFile(snapshotPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(dir); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("tampered snapshot accepted: %v", err)
	}
}

func TestCreateFailsClosed(t *testing.T) {
	seed, nc := fixture(t)
	js, _ := seed.JetStream()
	for _, tc := range []string{"blob error", "snapshot error", "disk corruption", "missing required"} {
		t.Run(tc, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "backup")
			conn := nc
			export := blobFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			switch tc {
			case "disk corruption":
				original := export
				export = func(ctx context.Context, w io.Writer) error {
					path := filepath.Join(dir, "000000.snapshot")
					data, err := os.ReadFile(path)
					if err != nil {
						return err
					}
					data[0] ^= 1
					if err := os.WriteFile(path, data, 0o600); err != nil {
						return err
					}
					return original(ctx, w)
				}
			case "blob error":
				export = func(context.Context, io.Writer) error { return errors.New("blob read failed") }
			case "snapshot error":
				var err error
				conn, err = nats.Connect(seed.ConnectedUrl(), nats.UserInfo("no-snapshot", "no-snapshot"), nats.NoReconnect(), nats.ErrorHandler(func(*nats.Conn, *nats.Subscription, error) {}))
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
			case "missing required":
				if err := js.DeleteStream(messaging.StreamName("sample", streams.DeviceEventsCapture)); err != nil {
					t.Fatal(err)
				}
			}
			if err := Create(ctx, conn, "sample", dir, export); err == nil {
				t.Fatal("export failure accepted")
			}
			if _, err := os.Stat(filepath.Join(dir, "manifest.json")); !os.IsNotExist(err) {
				t.Fatalf("failure published a manifest: %v", err)
			}
		})
	}
}

func TestBlobArchive(t *testing.T) {
	var out bytes.Buffer
	if err := blobFixture(t)(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(&out)
	h, err := tr.Next()
	if err != nil {
		t.Fatal(err)
	}
	if h.Name != "objects/sample/tenant/ota/firmware" {
		t.Fatalf("wrong object key %q", h.Name)
	}
	data, _ := io.ReadAll(tr)
	if string(data) != "firmware bytes" {
		t.Fatalf("wrong blob bytes %q", data)
	}
	h, err = tr.Next()
	if err != nil || h.Name != "index.json" {
		t.Fatalf("missing index: %v", err)
	}
	var index []objectRecord
	if err := json.NewDecoder(tr).Decode(&index); err != nil {
		t.Fatal(err)
	}
	if len(index) != 1 || index[0].Ref != "blob://filesystem/sample/tenant/ota/firmware" || index[0].Size != 14 || index[0].SHA256 != "dbf1bce8c5e41be6f0c17880b1ed3033714b384b2c2232b1c7b4f3fda822b6ce" {
		t.Fatalf("wrong object metadata: %+v", index)
	}
}
