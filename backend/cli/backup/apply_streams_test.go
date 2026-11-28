// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

func TestApplyStreamsRoundTrip(t *testing.T) {
	source, _ := restoreServer(t)
	name := seedRestoreStreams(t, source)
	js, _ := source.JetStream()
	data := make([]byte, 320<<10)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := js.Publish("seed."+name, data); err != nil {
			t.Fatal(err)
		}
	}
	sub, err := js.PullSubscribe("seed."+name, "checkpoint", nats.BindStream(name))
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := sub.Fetch(2)
	if err != nil {
		t.Fatal(err)
	}
	if err := msgs[0].AckSync(); err != nil {
		t.Fatal(err)
	}
	want, err := js.ConsumerInfo(name, "checkpoint")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "set")
	if err := Create(context.Background(), source, "sample", dir, blobFixture(t), nil); err != nil {
		t.Fatal(err)
	}
	p, err := PrepareRestore(dir, "sample", false)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := restoreServer(t)
	var out bytes.Buffer
	if err := p.ApplyStreams(context.Background(), target, nil, false, &out); err != nil {
		t.Fatal(err)
	}
	tjs, _ := target.JetStream()
	for seq := uint64(1); seq <= 3; seq++ {
		msg, err := tjs.GetMsg(name, seq)
		if err != nil || !bytes.Equal(msg.Data, data) {
			t.Fatalf("message %d did not round trip: %v", seq, err)
		}
	}
	got, err := tjs.ConsumerInfo(name, "checkpoint")
	if err != nil {
		t.Fatal(err)
	}
	// JetStream restores sequence and pending state, but its informational
	// last-delivery/ack timestamps are not persisted in the native archive.
	if got.Config.Durable != want.Config.Durable || got.Config.AckPolicy != want.Config.AckPolicy || got.Delivered.Consumer != want.Delivered.Consumer || got.Delivered.Stream != want.Delivered.Stream || got.AckFloor.Consumer != want.AckFloor.Consumer || got.AckFloor.Stream != want.AckFloor.Stream || got.NumAckPending != want.NumAckPending || got.NumPending != want.NumPending {
		t.Fatalf("consumer state changed: got %+v, want %+v", got, want)
	}
	if !strings.Contains(out.String(), "stream \""+name+"\": restored") || strings.Contains(out.String(), "object-store") {
		t.Fatalf("wrong per-stream report: %s", &out)
	}
	// The backup contains objects. A stream apply has no blob reader/writer and
	// never creates a JetStream object bucket or any uncaptured stream.
	count := 0
	for info := range tjs.StreamsInfo() {
		if _, ok := p.streams[info.Config.Name]; !ok {
			t.Fatalf("restored another resource: %s", info.Config.Name)
		}
		count++
	}
	if count != len(p.streams) {
		t.Fatalf("restored %d streams, want %d", count, len(p.streams))
	}
}

func TestApplyStreamsReplace(t *testing.T) {
	dir, name := restoreSet(t)
	p, err := PrepareRestore(dir, "sample", false)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := restoreServer(t)
	js, _ := target.JetStream()
	if _, err := js.AddStream(&nats.StreamConfig{Name: name, Subjects: []string{"seed." + name}}); err != nil {
		t.Fatal(err)
	}
	if _, err := js.Publish("seed."+name, []byte("newer target")); err != nil {
		t.Fatal(err)
	}
	if _, err := js.AddConsumer(name, &nats.ConsumerConfig{Durable: "target-only", AckPolicy: nats.AckExplicitPolicy}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = p.ApplyStreams(context.Background(), target, nil, false, &out)
	if err == nil || !strings.Contains(err.Error(), "--overwrite-target") || !strings.Contains(err.Error(), name) || strings.Contains(out.String(), ": restored") {
		t.Fatalf("existing stream not refused: %s, %v", &out, err)
	}
	msg, err := js.GetMsg(name, 1)
	if err != nil || string(msg.Data) != "newer target" {
		t.Fatalf("refused restore changed target: %v", err)
	}
	if err := p.ApplyStreams(context.Background(), target, nil, true, &out); err != nil {
		t.Fatal(err)
	}
	info, err := js.StreamInfo(name)
	if err != nil || info.State.Msgs != 2 || info.State.Consumers != 0 {
		t.Fatalf("stream not replaced: %+v, %v", info, err)
	}
	msg, err = js.GetMsg(name, 1)
	if err != nil || string(msg.Data) != "checkpoint" {
		t.Fatalf("backup bytes not restored: %v", err)
	}
}

func TestApplyStreamsRechecksBytesBeforeReplacement(t *testing.T) {
	for _, change := range []string{"checksum", "size"} {
		t.Run(change, func(t *testing.T) {
			dir, name := restoreSet(t)
			p, err := PrepareRestore(dir, "sample", false)
			if err != nil {
				t.Fatal(err)
			}
			e := p.manifest.Entries[0]
			file := filepath.Join(dir, e.Snapshot)
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if change == "checksum" {
				// Changing the local ZIP timestamp keeps the archive valid, so
				// only the digest check detects this same-size change.
				raw[10] ^= 1
			} else {
				raw = append(raw, 0)
			}
			if err := os.WriteFile(file, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			target, _ := restoreServer(t)
			js, _ := target.JetStream()
			if _, err := js.AddStream(&nats.StreamConfig{Name: name, Subjects: []string{"seed." + name}}); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err = p.ApplyStreams(context.Background(), target, nil, true, &out)
			if err == nil || !strings.Contains(err.Error(), name) || strings.Contains(out.String(), ": restored") {
				t.Fatalf("changed snapshot accepted: %s, %v", &out, err)
			}
			if _, err := js.StreamInfo(name); err != nil {
				t.Fatalf("corrupt backup deleted existing target: %v", err)
			}
		})
	}
}

func TestApplyStreamsBadNativeArchiveLeavesNoStream(t *testing.T) {
	dir, name := restoreSet(t)
	// Keep a valid envelope and matching manifest: the server must reject the
	// native archive, and the client must not claim the upload restored it.
	editRestoreManifest(t, dir, func(m *Manifest) {
		e := &m.Entries[0]
		file := filepath.Join(dir, e.Snapshot)
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		z, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
		if err != nil {
			t.Fatal(err)
		}
		meta, err := z.Open("metadata.json")
		if err != nil {
			t.Fatal(err)
		}
		metadata, err := io.ReadAll(meta)
		meta.Close()
		if err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		zw := zip.NewWriter(&b)
		native, _ := zw.Create("snapshot.s2")
		native.Write([]byte("broken native archive"))
		mw, _ := zw.Create("metadata.json")
		mw.Write(metadata)
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, b.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		size, sum := int64(b.Len()), sha256.Sum256(b.Bytes())
		e.SizeBytes, e.SHA256 = &size, hex.EncodeToString(sum[:])
	})
	p, err := PrepareRestore(dir, "sample", false)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := restoreServer(t)
	var out bytes.Buffer
	err = p.ApplyStreams(context.Background(), target, nil, false, &out)
	if err == nil || !strings.Contains(err.Error(), name) || strings.Contains(out.String(), ": restored") {
		t.Fatalf("server rejection reported as success: %s, %v", &out, err)
	}
	js, _ := target.JetStream()
	if _, err := js.StreamInfo(name); !errors.Is(err, nats.ErrStreamNotFound) {
		t.Fatalf("failed restore left a half-made stream: %v", err)
	}
}

func TestRestoreUploadFailures(t *testing.T) {
	for _, failure := range []string{"api error", "chunk error", "missing ack", "final error", "invalid completion", "read error"} {
		t.Run(failure, func(t *testing.T) {
			nc := protocolServer(t)
			nc.Subscribe("$JS.API.STREAM.RESTORE.test", func(m *nats.Msg) {
				if failure == "api error" {
					m.Respond([]byte(`{"error":{"description":"restore denied"}}`))
					return
				}
				m.Respond([]byte(`{"deliver_subject":"$JS.SNAPSHOT.RESTORE.test"}`))
			})
			nc.Subscribe("$JS.SNAPSHOT.RESTORE.test", func(m *nats.Msg) {
				if len(m.Data) > 0 {
					if failure == "missing ack" {
						return
					}
					if failure == "chunk error" {
						m.Respond([]byte(`{"error":{"description":"disk full"}}`))
					} else {
						m.Respond(nil)
					}
					return
				}
				if failure == "final error" {
					m.Respond([]byte(`{"error":{"description":"invalid snapshot"}}`))
				} else {
					m.Respond([]byte(`{}`))
				}
			})
			if err := nc.Flush(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			var r io.Reader = strings.NewReader("partial snapshot")
			if failure == "read error" {
				r = io.MultiReader(r, failedSnapshotReader{})
			}
			err := restoreStream(ctx, nc, "test", []byte(`{"config":{"name":"test"},"state":{}}`), r)
			if err == nil {
				t.Fatal("upload failure reported as success")
			}
		})
	}
}

type failedSnapshotReader struct{}

func (failedSnapshotReader) Read([]byte) (int, error) { return 0, errors.New("snapshot read failed") }

func TestRestoreUploadWaitsForChunkAcknowledgment(t *testing.T) {
	nc := protocolServer(t)
	const upload = "$JS.SNAPSHOT.RESTORE.test.token"
	sub, err := nc.SubscribeSync(upload)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Unsubscribe()
	nc.Subscribe("$JS.API.STREAM.RESTORE.test", func(m *nats.Msg) {
		m.Respond([]byte(`{"deliver_subject":"` + upload + `"}`))
	})
	if err := nc.Flush(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	want := bytes.Repeat([]byte("x"), (128<<10)+1)
	done := make(chan error, 1)
	go func() {
		done <- restoreStream(ctx, nc, "test", []byte(`{"config":{"name":"test"},"state":{}}`), bytes.NewReader(want))
	}()
	first, err := sub.NextMsg(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sub.NextMsg(50 * time.Millisecond); !errors.Is(err, nats.ErrTimeout) {
		t.Fatalf("client advanced before chunk acknowledgment: %v", err)
	}
	var got []byte
	for chunk := first; ; {
		if len(chunk.Data) == 0 {
			chunk.Respond([]byte(`{"type":"io.nats.jetstream.api.v1.stream_create_response","config":{"name":"test"},"state":{}}`))
			break
		}
		if len(chunk.Data) > 64<<10 {
			t.Fatalf("unbounded restore chunk: %d bytes", len(chunk.Data))
		}
		got = append(got, chunk.Data...)
		if err := chunk.Respond(nil); err != nil {
			t.Fatal(err)
		}
		chunk, err = sub.NextMsg(time.Second)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("restore upload lost bytes")
	}
}
