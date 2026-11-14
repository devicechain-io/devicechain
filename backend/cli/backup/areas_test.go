// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package backup

import (
	"bytes"
	"context"
	"net"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-k8s/functionalarea"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	server "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

func profileAreas(t *testing.T, profile string) []string {
	t.Helper()
	areas, err := functionalarea.ResolveAndValidate(profile, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(areas))
	for i, a := range areas {
		out[i] = string(a)
	}
	return out
}

// Provision the actual profile through broker reconciliation, not an artificial
// fixture that creates every checkpoint stream regardless of deployment.
func profileBroker(t *testing.T, areas []string) *nats.Conn {
	t.Helper()
	s, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: -1, NoLog: true, NoSigs: true, JetStream: true, StoreDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	go s.Start()
	if !s.ReadyForConnections(5 * time.Second) {
		t.Fatal("NATS not ready")
	}
	t.Cleanup(func() { s.Shutdown(); s.WaitForShutdown() })
	addr := s.Addr().(*net.TCPAddr)
	ms := &core.Microservice{InstanceId: "sample", Readiness: core.NewReadinessGate()}
	ms.InstanceConfiguration.ApplyDefaults()
	n := &ms.InstanceConfiguration.Infrastructure.Nats
	n.Hostname, n.Port = addr.IP.String(), uint32(addr.Port)
	n.StreamMaxBytes, n.StreamMaxBytesCold, n.KvStateMaxBytes, n.KvCacheMaxBytes = 16<<20, 4<<20, 4<<20, 2<<20
	admin := messaging.NewBrokerAdmin(ms)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := admin.ExecuteInitialize(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.ReconcileBroker(ctx, areas); err != nil {
		t.Fatal(err)
	}
	nc, err := nats.Connect(s.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	return nc
}

func TestProfileBackupAndRestore(t *testing.T) {
	for _, profile := range []string{"default", "telemetry", "ingest-only"} {
		t.Run(profile, func(t *testing.T) {
			areas := profileAreas(t, profile)
			nc := profileBroker(t, areas)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			blobs := emptyBlobs(t)
			dir := filepath.Join(t.TempDir(), "set")
			if err := Create(ctx, nc, "sample", dir, blobs, areas); err != nil {
				t.Fatal(err)
			}
			m, err := Inspect(dir)
			if err != nil {
				t.Fatal(err)
			}
			dead := messaging.StreamName("sample", streams.ConnectorDispatchDead)
			if !slices.Contains(m.DisabledStreams, dead) {
				t.Fatalf("disabled connector stream not attested: %+v", m)
			}
			capture := messaging.StreamName("sample", streams.DeviceEventsCapture)
			if slices.Contains(m.DisabledStreams, capture) {
				t.Fatal("enabled capture skipped")
			}
			found := false
			for _, e := range m.Entries {
				if e.Name == capture {
					found = true
				}
			}
			if !found {
				t.Fatal("enabled capture missing")
			}
			p, err := PrepareRestore(dir, "sample", false)
			if err != nil {
				t.Fatal(err)
			}
			actions, err := p.Plan(ctx, nc, blobs, areas)
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := WritePlan(&output, actions); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), "source area disabled: backup has no data") {
				t.Fatal(output.String())
			}
			// A smaller target must still plan every included snapshot, even for
			// streams its areas no longer open (they may contain a backlog).
			smaller, err := p.Plan(ctx, nc, blobs, profileAreas(t, "ingest-only"))
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range m.Entries {
				if e.Kind != "stream" {
					continue
				}
				found := false
				for _, a := range smaller {
					if a.Name == e.Name && (a.Action == "replace" || a.Action == "create") {
						found = true
					}
				}
				if !found {
					t.Fatalf("smaller target dropped snapshot %q", e.Name)
				}
			}
			// Refuse even if the target's newly enabled streams have not been
			// reconciled yet: target intent alone establishes missing coverage.
			if _, err := p.Plan(ctx, nc, blobs, profileAreas(t, "full")); err == nil || !strings.Contains(err.Error(), "backup has no data") {
				t.Fatalf("larger target accepted: %v", err)
			}
			js, _ := nc.JetStream()
			if _, err := js.AddStream(&nats.StreamConfig{Name: dead, Subjects: []string{"leftover.connector"}, Storage: nats.FileStorage}); err != nil {
				t.Fatal(err)
			}
			if _, err := p.Plan(ctx, nc, blobs, areas); err == nil {
				t.Fatal("target with unattested leftover accepted")
			}
			// Disabled does not mean disposable: export leftover data too.
			leftover := filepath.Join(t.TempDir(), "set")
			if err := Create(ctx, nc, "sample", leftover, blobs, areas); err != nil {
				t.Fatal(err)
			}
			left, err := Inspect(leftover)
			if err != nil {
				t.Fatal(err)
			}
			if slices.Contains(left.DisabledStreams, dead) {
				t.Fatal("existing disabled stream omitted")
			}
			if err := js.DeleteStream(capture); err != nil {
				t.Fatal(err)
			}
			if err := Create(ctx, nc, "sample", filepath.Join(t.TempDir(), "set"), blobs, areas); err == nil || !strings.Contains(err.Error(), capture) {
				t.Fatalf("missing enabled capture accepted: %v", err)
			}
			// Both inspect and restore reject forged absence of an enabled stream.
			editRestoreManifest(t, dir, func(m *Manifest) { m.DisabledStreams = append(m.DisabledStreams, capture) })
			if _, err := Inspect(dir); err == nil {
				t.Fatal("inspect accepted enabled absence")
			}
			if _, err := PrepareRestore(dir, "sample", false); err == nil {
				t.Fatal("restore accepted enabled absence")
			}
		})
	}
}
