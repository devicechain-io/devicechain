// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package backup

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/devicechain-io/dc-microservice/backupset"
	"github.com/devicechain-io/dc-microservice/durability"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/nats-io/nats.go"
)

// Create exports declared checkpoint-preserved streams (including consumers) and
// the instance blob store. Callers must freeze writers for a consistent cut.
// Destination must not exist. A failed export never publishes manifest.json.
func Create(ctx context.Context, nc *nats.Conn, instance, destination string, blobs func(context.Context, io.Writer) error) error {
	if instance == "" || filepath.Base(instance) != instance || instance == "." || instance == ".." || destination == "" || blobs == nil {
		return fmt.Errorf("instance, new destination and blob exporter are required")
	}
	if err := backupset.ValidateBackupPolicy(); err != nil {
		return err
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		return err
	}
	m := Manifest{Version: ManifestVersion}
	write := func(kind, name string, class durability.BackupClass, export func(io.Writer) error) error {
		path := fmt.Sprintf("%06d.snapshot", len(m.Entries))
		f, err := os.OpenFile(filepath.Join(destination, path), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		h := sha256.New()
		err = export(io.MultiWriter(f, h))
		if err == nil {
			err = f.Sync()
		}
		info, statErr := f.Stat()
		closeErr := f.Close()
		if err != nil {
			return fmt.Errorf("%s %q: %w", kind, name, err)
		}
		if statErr != nil {
			return statErr
		}
		if closeErr != nil {
			return closeErr
		}
		size := info.Size()
		m.Entries = append(m.Entries, Entry{Kind: kind, Name: name, Class: class, Snapshot: path, SizeBytes: &size, SHA256: hex.EncodeToString(h.Sum(nil))})
		return nil
	}
	js, err := nc.JetStream()
	if err != nil {
		return err
	}
	for _, item := range backupset.Items() {
		if item.BackupClass != durability.BackupRequired && item.BackupClass != durability.BackupCheckpoint {
			continue
		}
		if item.Kind == backupset.KindObjectStore {
			continue
		}
		name := item.Name
		if item.Kind == backupset.KindStream {
			name = messaging.StreamName(instance, name)
		} else if item.Kind != backupset.KindGatewayStream {
			return fmt.Errorf("unsupported preserved kind %q", item.Kind)
		}
		_, err := js.StreamInfo(name, nats.Context(ctx))
		// Gateway state is absent when MQTT has never been enabled. Required
		// platform streams must exist, including empty streams.
		if err == nats.ErrStreamNotFound && item.Kind == backupset.KindGatewayStream {
			continue
		}
		if err != nil {
			return fmt.Errorf("required stream %q: %w", name, err)
		}
		if err := write("stream", name, item.BackupClass, func(w io.Writer) error { return streamBundle(ctx, nc, name, w) }); err != nil {
			return err
		}
	}
	if err := write("object-store", backupset.ObjectStoreName, durability.BackupRequired, func(w io.Writer) error { return blobs(ctx, w) }); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.validate(); err != nil {
		return err
	}
	root, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, entry := range m.Entries {
		if err := verify(root, entry); err != nil {
			return fmt.Errorf("verify exported %q: %w", entry.Name, err)
		}
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(destination, "manifest.pending")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmp, filepath.Join(destination, "manifest.json"))
}

// The v2 native archive carries messages and consumer state; its restore request
// also needs the API response's stream configuration. Keep both in a ZIP envelope
// so the v1 manifest covers every byte, without changing the native archive.
func streamBundle(ctx context.Context, nc *nats.Conn, name string, w io.Writer) error {
	z := zip.NewWriter(w)
	native, err := z.CreateHeader(&zip.FileHeader{Name: "snapshot.s2", Method: zip.Store})
	if err != nil {
		return err
	}
	var metadata []byte
	if err := snapshot(ctx, nc, name, native, &metadata); err != nil {
		return err
	}
	meta, err := z.Create("metadata.json")
	if err != nil {
		return err
	}
	if _, err := meta.Write(metadata); err != nil {
		return err
	}
	return z.Close()
}

// nats.go v1.55.0 has no snapshot helper. Use the server's public
// $JS.API.STREAM.SNAPSHOT API: jsck checks stored messages, consumers are
// included, each chunk is acknowledged only after writing, and empty data is EOF.
func snapshot(ctx context.Context, nc *nats.Conn, name string, w io.Writer, metadata *[]byte) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	inbox := nc.NewInbox()
	sub, err := nc.SubscribeSync(inbox)
	if err != nil {
		return err
	}
	defer sub.Unsubscribe()
	// Confirm the server registered the inbox subscription before the snapshot
	// request: core NATS drops a chunk that arrives with no subscriber.
	if err := nc.FlushWithContext(ctx); err != nil {
		return err
	}
	if err := sub.SetPendingLimits(1024, 32<<20); err != nil {
		return err
	}
	req, _ := json.Marshal(map[string]any{"deliver_subject": inbox, "chunk_size": 128 << 10, "jsck": true})
	requestCtx, requestCancel := context.WithTimeout(ctx, 10*time.Second)
	msg, err := nc.RequestWithContext(requestCtx, "$JS.API.STREAM.SNAPSHOT."+name, req)
	requestCancel()
	if err != nil {
		return err
	}
	var response struct {
		Error *struct {
			Description string `json:"description"`
		} `json:"error"`
		Config json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(msg.Data, &response); err != nil {
		return err
	}
	if response.Error != nil {
		return fmt.Errorf("snapshot: %s", response.Error.Description)
	}
	if len(response.Config) == 0 {
		return fmt.Errorf("snapshot response has no configuration")
	}
	*metadata = append([]byte(nil), msg.Data...)
	var received int64
	for {
		chunkCtx, chunkCancel := context.WithTimeout(ctx, 30*time.Second)
		msg, err := sub.NextMsgWithContext(chunkCtx)
		chunkCancel()
		if err != nil {
			return err
		}
		if status := msg.Header.Get("Status"); status != "" && status != "204" {
			return fmt.Errorf("snapshot status: %s", msg.Header.Get("Status"))
		}
		if len(msg.Data) == 0 {
			if received == 0 {
				return fmt.Errorf("empty snapshot archive")
			}
			return nil
		}
		if _, err := w.Write(msg.Data); err != nil {
			return err
		}
		received += int64(len(msg.Data))
		// The last partial chunk has no acknowledgement subject.
		if msg.Reply != "" {
			if err := msg.Respond(nil); err != nil {
				return err
			}
		}
	}
}
