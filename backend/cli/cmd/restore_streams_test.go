// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/backupset"
	"github.com/devicechain-io/dc-microservice/blob"
	"github.com/devicechain-io/dc-microservice/durability"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dcctl/backup"
	server "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

func TestRestoreOnlyStreamNeverRestoresObjects(t *testing.T) {
	srv, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: -1, NoLog: true, NoSigs: true, JetStream: true, StoreDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(5 * time.Second) {
		t.Fatal("broker did not start")
	}
	t.Cleanup(func() { srv.Shutdown(); srv.WaitForShutdown() })
	nc, err := nats.Connect(srv.ClientURL(), nats.NoReconnect())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, _ := nc.JetStream()
	for _, item := range backupset.Items() {
		if item.BackupClass != durability.BackupRequired && item.BackupClass != durability.BackupCheckpoint || item.Kind == backupset.KindObjectStore {
			continue
		}
		name := item.Name
		if item.Kind == backupset.KindStream {
			name = messaging.StreamName("sample", name)
		}
		if _, err := js.AddStream(&nats.StreamConfig{Name: name, Subjects: []string{"seed." + name}}); err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	file := filepath.Join(root, "sample", "tenant", "ota", "firmware")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("backup object"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "set")
	export := func(ctx context.Context, w io.Writer) error {
		return backup.ExportBlobs(ctx, blob.Config{Directory: root}, "sample", w)
	}
	if err := backup.Create(context.Background(), nc, "sample", dir, export, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("newer target object"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "absent"))
	called := false
	cmd := newRestoreCmdWithRunners(func(ctx context.Context, opts backupOptions) error {
		called = true
		if !opts.RestoreStreams || !opts.OverwriteTarget || opts.BlobDirectory != "" {
			t.Fatalf("wrong stream selection/options: %+v", opts)
		}
		return opts.RestorePlan.ApplyStreams(ctx, nc, nil, opts.OverwriteTarget, opts.PlanOutput)
	}, func(context.Context, backupOptions, bool) error {
		t.Fatal("--only stream called object restore")
		return nil
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"sample", "--from", dir, "--apply", "--only", "stream", "--overwrite-target"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !called || !strings.Contains(out.String(), ": restored") || strings.Contains(out.String(), "object-store") {
		t.Fatalf("stream apply did not run alone: %s", &out)
	}
	got, err := os.ReadFile(file)
	if err != nil || string(got) != "newer target object" {
		t.Fatalf("stream selection modified objects: %q, %v", got, err)
	}
}
