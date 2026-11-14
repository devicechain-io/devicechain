// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	server "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

func protocolServer(t *testing.T) *nats.Conn {
	t.Helper()
	s, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: -1, NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	go s.Start()
	if !s.ReadyForConnections(5 * time.Second) {
		t.Fatal("server not ready")
	}
	t.Cleanup(func() { s.Shutdown(); s.WaitForShutdown() })
	nc, err := nats.Connect(s.ClientURL(), nats.NoReconnect())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	return nc
}

func TestSnapshotProtocol(t *testing.T) {
	for _, tc := range []string{"success", "api error", "malformed", "status error", "no completion", "empty archive", "write error"} {
		t.Run(tc, func(t *testing.T) {
			nc := protocolServer(t)
			_, err := nc.Subscribe("$JS.API.STREAM.SNAPSHOT.test", func(m *nats.Msg) {
				var req struct {
					Deliver     string `json:"deliver_subject"`
					Check       bool   `json:"jsck"`
					NoConsumers bool   `json:"no_consumers"`
				}
				if err := json.Unmarshal(m.Data, &req); err != nil {
					t.Error(err)
					return
				}
				if !req.Check || req.NoConsumers {
					t.Errorf("snapshot did not check messages and preserve consumers: %s", m.Data)
				}
				if tc == "api error" {
					m.Respond([]byte(`{"error":{"description":"snapshot failed"}}`))
					return
				}
				if tc == "malformed" {
					m.Respond([]byte(`bad`))
					return
				}
				m.Respond([]byte(`{"config":{"name":"test"}}`))
				if tc == "status error" {
					nc.Publish(req.Deliver, []byte("partial archive"))
					nc.PublishMsg(&nats.Msg{Subject: req.Deliver, Header: nats.Header{"Status": []string{"500"}}})
					return
				}
				if tc != "empty archive" {
					nc.Publish(req.Deliver, []byte("native archive bytes"))
				}
				if tc != "no completion" {
					nc.Publish(req.Deliver, nil)
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := nc.Flush(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			var out bytes.Buffer
			var w io.Writer = &out
			if tc == "write error" {
				w = brokenWriter{}
			}
			var metadata []byte
			err = snapshot(ctx, nc, "test", w, &metadata)
			if tc == "success" {
				if err != nil || out.String() != "native archive bytes" {
					t.Fatalf("snapshot failed: %q %v", out.String(), err)
				}
			} else if err == nil {
				t.Fatal("snapshot failure accepted")
			}
		})
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestCoordinatorSnapshotAPIErrorLeavesNoManifest(t *testing.T) {
	nc := protocolServer(t)
	nc.Subscribe("$JS.API.STREAM.INFO.*", func(m *nats.Msg) { m.Respond([]byte(`{"config":{"name":"test"},"state":{}}`)) })
	nc.Subscribe("$JS.API.STREAM.SNAPSHOT.*", func(m *nats.Msg) { m.Respond([]byte(`{"error":{"description":"snapshot disk failure"}}`)) })
	if err := nc.Flush(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "backup")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := Create(ctx, nc, "sample", dir, func(context.Context, io.Writer) error { t.Error("blob export ran after snapshot failure"); return nil })
	if err == nil || !strings.Contains(err.Error(), "snapshot disk failure") {
		t.Fatalf("wrong snapshot error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); !os.IsNotExist(err) {
		t.Fatalf("partial manifest published: %v", err)
	}
}
