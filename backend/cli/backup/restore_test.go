// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/backupset"
	"github.com/devicechain-io/dc-microservice/blob"
	"github.com/devicechain-io/dc-microservice/messaging"
	server "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

func restoreServer(t *testing.T) (*nats.Conn, *nats.Conn) {
	t.Helper()
	s, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: -1, NoLog: true, NoSigs: true, JetStream: true, StoreDir: t.TempDir(), Users: []*server.User{
		{Username: "seed", Password: "seed"},
		{Username: "plan", Password: "plan", Permissions: &server.Permissions{
			Publish:   &server.SubjectPermission{Allow: []string{"$JS.API.STREAM.INFO.>"}},
			Subscribe: &server.SubjectPermission{Allow: []string{"_INBOX.>"}},
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	go s.Start()
	if !s.ReadyForConnections(5 * time.Second) {
		t.Fatal("NATS not ready")
	}
	t.Cleanup(func() { s.Shutdown(); s.WaitForShutdown() })
	dial := func(user string) *nats.Conn {
		nc, err := nats.Connect(s.ClientURL(), nats.UserInfo(user, user), nats.NoReconnect(), nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) { t.Errorf("read-only permissions: %v", err) }))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(nc.Close)
		return nc
	}
	return dial("seed"), dial("plan")
}

func seedRestoreStreams(t *testing.T, nc *nats.Conn) string {
	t.Helper()
	js, _ := nc.JetStream()
	first := ""
	for _, item := range backupset.Items() {
		if !preserved(item) || item.Kind == backupset.KindObjectStore {
			continue
		}
		_, name := resource(item, "sample")
		if first == "" {
			first = name
		}
		if _, err := js.AddStream(&nats.StreamConfig{Name: name, Subjects: []string{"seed." + name}, Storage: nats.FileStorage}); err != nil {
			t.Fatal(err)
		}
	}
	return first
}

func emptyBlobs(t *testing.T) func(context.Context, io.Writer) error {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sample"), 0700); err != nil {
		t.Fatal(err)
	}
	return func(ctx context.Context, w io.Writer) error {
		return ExportBlobs(ctx, blob.Config{Directory: root}, "sample", w)
	}
}

func restoreSet(t *testing.T) (string, string) {
	t.Helper()
	seed, _ := restoreServer(t)
	name := seedRestoreStreams(t, seed)
	js, _ := seed.JetStream()
	for range 2 {
		if _, err := js.Publish("seed."+name, []byte("checkpoint")); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(t.TempDir(), "set")
	if err := Create(context.Background(), seed, "sample", dir, blobFixture(t)); err != nil {
		t.Fatal(err)
	}
	return dir, name
}

func editRestoreManifest(t *testing.T, dir string, edit func(*Manifest)) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	edit(&m)
	raw, err = json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreEmptyTarget(t *testing.T) {
	dir, _ := restoreSet(t)
	seed, reader := restoreServer(t)
	plan, err := PrepareRestore(dir, "sample", false)
	if err != nil {
		t.Fatal(err)
	}
	actions, err := plan.Plan(context.Background(), reader, emptyBlobs(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != len(backupset.Items()) {
		t.Fatalf("incomplete plan: %d", len(actions))
	}
	for i, a := range actions {
		if preserved(backupset.Items()[i]) && a.Action != "create" {
			t.Fatalf("want create: %+v", a)
		}
		if a.Action == "skip" && a.Reason == "" {
			t.Fatal("unexplained skip")
		}
	}
	var out bytes.Buffer
	if err := WritePlan(&out, actions); err != nil || !strings.Contains(out.String(), `object-store "object-store": create`) {
		t.Fatalf("output %s: %v", out.String(), err)
	}
	js, _ := seed.JetStream()
	if _, err := js.StreamInfo(messaging.StreamName("sample", backupset.Items()[0].Name)); err != nats.ErrStreamNotFound {
		t.Fatalf("plan mutated target: %v", err)
	}
}

func TestRestorePopulatedTarget(t *testing.T) {
	seed, reader := restoreServer(t)
	name := seedRestoreStreams(t, seed)
	js, _ := seed.JetStream()
	if _, err := js.Publish("seed."+name, []byte("older")); err != nil {
		t.Fatal(err)
	}
	dir, _ := restoreSet(t) // checkpoint is later than the target message
	before, err := js.StreamInfo(name)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PrepareRestore(dir, "sample", false)
	if err != nil {
		t.Fatal(err)
	}
	actions, err := plan.Plan(context.Background(), reader, blobFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if actions[0].Name != name || actions[0].Action != "replace" || actions[0].CurrentMessages != 1 {
		t.Fatalf("wrong replacement: %+v", actions[0])
	}
	if a := actions[len(actions)-1]; a.Action != "skip" || a.Reason == "" {
		t.Fatalf("identical blobs: %+v", a)
	}
	var out bytes.Buffer
	if err := WritePlan(&out, actions); err != nil || !strings.Contains(out.String(), "replace (current messages: 1)") {
		t.Fatalf("output %q: %v", out.String(), err)
	}
	after, err := js.StreamInfo(name)
	if err != nil || after.State.Msgs != before.State.Msgs || after.State.LastSeq != before.State.LastSeq || after.State.Consumers != before.State.Consumers {
		t.Fatalf("plan changed target: %v", err)
	}
}

func TestRestoreRefusesIdentity(t *testing.T) {
	for _, field := range []string{"instance", "tenant", "missing", "missing tenant"} {
		t.Run(field, func(t *testing.T) {
			dir, _ := restoreSet(t)
			_, _ = restoreServer(t)
			editRestoreManifest(t, dir, func(m *Manifest) {
				switch field {
				case "instance":
					m.Instance = "foreign"
				case "tenant":
					m.Tenant = "foreign"
				case "missing":
					m.Instance = ""
				case "missing tenant":
					m.Tenant = ""
				}
			})
			if _, err := PrepareRestore(dir, "sample", false); err == nil || !strings.Contains(err.Error(), "identity") {
				t.Fatalf("identity accepted: %v", err)
			}
			if field == "tenant" {
				if _, err := PrepareRestore(dir, "sample", true); err != nil {
					t.Fatalf("explicit foreign override refused: %v", err)
				}
			}
			if strings.HasPrefix(field, "missing") {
				if _, err := PrepareRestore(dir, "sample", true); err == nil || !strings.Contains(err.Error(), "identity are required") {
					t.Fatalf("force bypassed missing identity evidence: %v", err)
				}
			}
		})
	}
}

func TestRestoreRefusesMissingEntry(t *testing.T) {
	for _, kind := range []string{"stream", "object-store"} {
		t.Run(kind, func(t *testing.T) {
			dir, name := restoreSet(t)
			_, _ = restoreServer(t)
			editRestoreManifest(t, dir, func(m *Manifest) {
				for i, e := range m.Entries {
					if e.Kind == kind {
						m.Entries = append(m.Entries[:i], m.Entries[i+1:]...)
						break
					}
				}
			})
			_, err := PrepareRestore(dir, "sample", false)
			if err == nil || !strings.Contains(err.Error(), "missing must-survive") {
				t.Fatalf("missing %s accepted (%s): %v", kind, name, err)
			}
			if _, err := PrepareRestore(dir, "sample", true); err == nil {
				t.Fatal("force bypassed coverage")
			}
		})
	}
}

func TestRestoreRefusesNewerStream(t *testing.T) {
	dir, name := restoreSet(t)
	seed, reader := restoreServer(t)
	seedRestoreStreams(t, seed)
	js, _ := seed.JetStream()
	// Even one message, fewer than the set, must be refused when it is newer.
	if _, err := js.Publish("seed."+name, []byte("post-checkpoint sentinel")); err != nil {
		t.Fatal(err)
	}
	plan, err := PrepareRestore(dir, "sample", true)
	if err != nil {
		t.Fatal(err)
	}
	actions, err := plan.Plan(context.Background(), reader, emptyBlobs(t))
	if err == nil || !strings.Contains(err.Error(), "newer data") || actions != nil {
		t.Fatalf("newer target accepted: %+v %v", actions, err)
	}
}

func TestRestoreRefusesDivergentObject(t *testing.T) {
	dir, _ := restoreSet(t)
	_, reader := restoreServer(t)
	root := t.TempDir()
	path := filepath.Join(root, "sample", "tenant", "ota")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "firmware"), []byte("new firmware"), 0600); err != nil {
		t.Fatal(err)
	}
	plan, err := PrepareRestore(dir, "sample", true)
	if err != nil {
		t.Fatal(err)
	}
	actions, err := plan.Plan(context.Background(), reader, func(ctx context.Context, w io.Writer) error {
		return ExportBlobs(ctx, blob.Config{Directory: root}, "sample", w)
	})
	if err == nil || !strings.Contains(err.Error(), "newer data") || actions != nil {
		t.Fatalf("newer object accepted: %+v %v", actions, err)
	}
}

func TestRestoreRefusesMissingGateway(t *testing.T) {
	dir, _ := restoreSet(t)
	seed, reader := restoreServer(t)
	editRestoreManifest(t, dir, func(m *Manifest) {
		for i, e := range m.Entries {
			if e.Name == "$MQTT_msgs" {
				m.Entries = append(m.Entries[:i], m.Entries[i+1:]...)
				break
			}
		}
	})
	if _, err := PrepareRestore(dir, "sample", false); err == nil || !strings.Contains(err.Error(), "missing must-survive") {
		t.Fatalf("unexplained omitted gateway accepted: %v", err)
	}
	editRestoreManifest(t, dir, func(m *Manifest) { m.AbsentGatewayStreams = []string{"$MQTT_msgs"} })
	plan, err := PrepareRestore(dir, "sample", false)
	if err != nil {
		t.Fatal(err)
	}
	actions, err := plan.Plan(context.Background(), reader, emptyBlobs(t))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range actions {
		if a.Name == "$MQTT_msgs" && a.Action == "skip" && a.Reason != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing gateway skip reason")
	}
	seedRestoreStreams(t, seed)
	actions, err = plan.Plan(context.Background(), reader, emptyBlobs(t))
	if err == nil || !strings.Contains(err.Error(), "missing checkpoint") || actions != nil {
		t.Fatalf("missing gateway accepted: %+v %v", actions, err)
	}
}

func TestRestoreForeignOverride(t *testing.T) {
	dir, _ := restoreSet(t)
	_, reader := restoreServer(t)
	plan, err := PrepareRestore(dir, "other", true)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "other"), 0700); err != nil {
		t.Fatal(err)
	}
	actions, err := plan.Plan(context.Background(), reader, func(ctx context.Context, w io.Writer) error {
		return ExportBlobs(ctx, blob.Config{Directory: root}, "other", w)
	})
	if err != nil || len(actions) == 0 || actions[0].Name != messaging.StreamName("other", backupset.Items()[0].Name) {
		t.Fatalf("foreign override: %+v %v", actions, err)
	}
}

func TestRestoreRefusesPolicyAndUnknownEntry(t *testing.T) {
	for _, tc := range []string{"policy", "unknown"} {
		t.Run(tc, func(t *testing.T) {
			dir, _ := restoreSet(t)
			editRestoreManifest(t, dir, func(m *Manifest) {
				if tc == "policy" {
					m.Entries[0].Class = "must-survive"
					if backupset.Items()[0].BackupClass == "must-survive" {
						m.Entries[0].Class = "checkpoint-required-if-nonempty"
					}
				} else {
					e := m.Entries[0]
					e.Name = "unknown"
					e.Snapshot = "unknown.snapshot"
					raw, err := os.ReadFile(filepath.Join(dir, m.Entries[0].Snapshot))
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, e.Snapshot), raw, 0600); err != nil {
						t.Fatal(err)
					}
					m.Entries = append(m.Entries, e)
				}
			})
			_, err := PrepareRestore(dir, "sample", false)
			want := "incorrect backup policy"
			if tc == "unknown" {
				want = "unclassified restore entry"
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("%s accepted: %v", tc, err)
			}
		})
	}
}
