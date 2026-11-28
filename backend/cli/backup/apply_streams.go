// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package backup

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/backupset"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/nats-io/nats.go"
)

// ApplyStreams restores only the captured streams, including their consumers.
// An existing stream is refused unless overwrite explicitly authorizes deleting
// it and replacing it with the snapshot. Freeze writers and stream/consumer
// creation throughout the operation; replacement is destructive, not atomic.
// No blob reader or writer is used. Layout and omission checks match Plan.
func (p *RestorePlan) ApplyStreams(ctx context.Context, nc *nats.Conn, shards *config.NatsShardGroupsConfiguration, overwrite bool, progress io.Writer, enabledAreas ...[]string) error {
	if p == nil || p.manifest == nil || nc == nil {
		return fmt.Errorf("verified plan and target broker required")
	}
	if progress == nil {
		progress = io.Discard
	}
	if p.manifest.Instance != p.instance {
		return fmt.Errorf("restoring streams into another instance is not implemented: captured stream names cannot be changed")
	}
	targetShards, err := recordShards(shards)
	if err != nil {
		return err
	}
	if !p.manifest.ShardGroups.equal(targetShards) {
		return fmt.Errorf("backup shard groups (%s) do not match the target instance's (%s)", p.manifest.ShardGroups, targetShards)
	}
	if len(enabledAreas) > 1 {
		return fmt.Errorf("one target enabled area set required")
	}
	if len(enabledAreas) == 1 {
		if err := validateAreas(enabledAreas[0]); err != nil {
			return err
		}
	}
	js, err := nc.JetStream()
	if err != nil {
		return err
	}
	// Judge the whole target before the first write. Never restore an omission
	// into an enabled area, or overlook a stream outside the declared layout.
	expected := map[string]bool{}
	for _, t := range inventory(targetShards) {
		item := t.item
		if item.Kind == backupset.KindStream {
			_, name := t.resource(p.instance)
			expected[name] = true
		}
		if !preserved(item) || item.Kind == backupset.KindObjectStore {
			continue
		}
		_, name := t.resource(p.instance)
		live, err := js.StreamInfo(name, nats.Context(ctx))
		if err != nil && !errors.Is(err, nats.ErrStreamNotFound) {
			return fmt.Errorf("stream %q: %w", name, err)
		}
		if _, included := p.streams[name]; !included {
			if item.Kind == backupset.KindStream && (len(enabledAreas) == 0 || streamEnabled(item.Name, enabledAreas[0])) {
				return fmt.Errorf("stream %q belongs to an enabled area but backup has no data", name)
			}
			if live != nil {
				return fmt.Errorf("stream %q: missing checkpoint entry for target stream", name)
			}
			continue
		}
		if live != nil && !overwrite {
			return fmt.Errorf("stream %q already exists; it may be newer than the backup. Pass --overwrite-target to replace it with the backup's messages and consumers", name)
		}
	}
	if err := refuseUndeclaredGroups(ctx, nc, p.instance, expected); err != nil {
		return err
	}
	for _, entry := range p.manifest.Entries {
		if entry.Kind != "stream" {
			continue
		}
		if err := p.applyStream(ctx, nc, js, entry, overwrite); err != nil {
			fmt.Fprintf(progress, "stream %q: failed: %v\n", entry.Name, err)
			return fmt.Errorf("stream %q: %w", entry.Name, err)
		}
		fmt.Fprintf(progress, "stream %q: restored (messages and consumers from backup)\n", entry.Name)
	}
	return nil
}

func (p *RestorePlan) applyStream(ctx context.Context, nc *nats.Conn, js nats.JetStreamContext, entry Entry, overwrite bool) error {
	// Spool verified bytes to a private file before deletion or any RESTORE API
	// call. Upload reads this copy, so changing the backup after verification
	// cannot change the archive halfway through the destructive operation.
	root, err := os.OpenRoot(p.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	src, err := root.Open(entry.Snapshot)
	if err != nil {
		return err
	}
	defer src.Close()
	staged, err := os.CreateTemp("", "dcctl-stream-restore-*")
	if err != nil {
		return err
	}
	defer os.Remove(staged.Name())
	defer staged.Close()
	if err := stage(staged, src, objectRecord{Size: *entry.SizeBytes, SHA256: entry.SHA256}); err != nil {
		return err
	}
	z, err := zip.NewReader(staged, *entry.SizeBytes)
	if err != nil {
		return err
	}
	meta, err := z.Open("metadata.json")
	if err != nil {
		return err
	}
	metadata, err := io.ReadAll(meta)
	meta.Close()
	if err != nil {
		return err
	}
	// Verify the ZIP member's CRC before replacing an existing stream too.
	native, err := z.Open("snapshot.s2")
	if err != nil {
		return err
	}
	_, err = io.Copy(io.Discard, native)
	native.Close()
	if err != nil {
		return err
	}
	native, err = z.Open("snapshot.s2")
	if err != nil {
		return err
	}
	defer native.Close()
	// Check again immediately before deletion: a stream may have appeared
	// since preflight. RESTORE itself refuses an existing stream as well.
	_, err = js.StreamInfo(entry.Name, nats.Context(ctx))
	if err == nil {
		if !overwrite {
			return fmt.Errorf("already exists; pass --overwrite-target to replace it")
		}
		if err := js.DeleteStream(entry.Name, nats.Context(ctx)); err != nil {
			return fmt.Errorf("delete target: %w", err)
		}
	} else if !errors.Is(err, nats.ErrStreamNotFound) {
		return err
	}
	if err := restoreStream(ctx, nc, entry.Name, metadata, native); err != nil {
		return err
	}
	// Success requires both the final server response and observable state.
	info, err := js.StreamInfo(entry.Name, nats.Context(ctx))
	if err != nil {
		return fmt.Errorf("verify restored stream: %w", err)
	}
	want := p.streams[entry.Name].State
	if info.State.Msgs != want.Msgs || info.State.LastSeq != want.LastSeq || info.State.Consumers != want.Consumers {
		return fmt.Errorf("restored stream state does not match captured messages and consumers")
	}
	return nil
}

// restoreStream uses the public JetStream restore protocol, like the NATS CLI:
// request with config/state, request/reply chunks for flow control, then an
// empty chunk whose response must be a successful stream_create_response.
// A failed read/upload never sends EOF; the server expires its staging session.
func restoreStream(ctx context.Context, nc *nats.Conn, name string, metadata []byte, native io.Reader) error {
	ctx, cancel := context.WithTimeout(ctx, time.Hour)
	defer cancel()
	// Keep the captured JSON fields, including configuration fields newer than
	// nats.go's StreamConfig; only the snapshot response's envelope is removed.
	var req struct {
		Config json.RawMessage `json:"config"`
		State  json.RawMessage `json:"state"`
	}
	if err := json.Unmarshal(metadata, &req); err != nil {
		return err
	}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	msg, err := restoreRequest(ctx, nc, "$JS.API.STREAM.RESTORE."+name, data, 10*time.Second)
	if err != nil {
		return fmt.Errorf("restore request (requires broker restore permission): %w", err)
	}
	var response struct {
		Deliver string `json:"deliver_subject"`
	}
	if err := json.Unmarshal(msg.Data, &response); err != nil {
		return err
	}
	if response.Deliver == "" || strings.ContainsAny(response.Deliver, "*> \t\r\n") {
		return fmt.Errorf("restore response has no valid delivery subject")
	}
	chunkSize := int(min(int64(64<<10), nc.MaxPayload()))
	if chunkSize <= 0 {
		return fmt.Errorf("invalid broker maximum payload")
	}
	buf := make([]byte, chunkSize)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := native.Read(buf)
		if readErr != nil && readErr != io.EOF {
			return fmt.Errorf("read snapshot: %w", readErr)
		}
		if n > 0 {
			ack, err := restoreRequest(ctx, nc, response.Deliver, buf[:n], 30*time.Second)
			if err != nil {
				return fmt.Errorf("upload chunk: %w", err)
			}
			if len(ack.Data) != 0 {
				return fmt.Errorf("unexpected restore chunk acknowledgment: %s", ack.Data)
			}
		}
		if readErr == io.EOF {
			break
		}
	}
	final, err := restoreRequest(ctx, nc, response.Deliver, nil, time.Hour)
	if err != nil {
		return fmt.Errorf("complete restore: %w", err)
	}
	var complete struct {
		Type string `json:"type"`
		nats.StreamInfo
	}
	if err := json.Unmarshal(final.Data, &complete); err != nil {
		return err
	}
	if complete.Type != "io.nats.jetstream.api.v1.stream_create_response" || complete.Config.Name != name {
		return fmt.Errorf("invalid restore completion for %q", name)
	}
	return nil
}

func restoreRequest(ctx context.Context, nc *nats.Conn, subject string, data []byte, timeout time.Duration) (*nats.Msg, error) {
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	msg, err := nc.RequestWithContext(rctx, subject, data)
	if err != nil {
		return nil, err
	}
	if status := msg.Header.Get("Status"); status != "" && status != "200" {
		return nil, fmt.Errorf("restore status %s: %s", status, msg.Header.Get("Description"))
	}
	if len(msg.Data) > 0 {
		var resp struct {
			Error *nats.APIError `json:"error"`
		}
		if err := json.Unmarshal(msg.Data, &resp); err != nil {
			return nil, fmt.Errorf("invalid restore response: %w", err)
		}
		if resp.Error != nil {
			return nil, resp.Error
		}
	}
	return msg, nil
}
