// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package backup

import (
	"archive/tar"
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"

	"github.com/devicechain-io/dc-microservice/backupset"
	"github.com/devicechain-io/dc-microservice/blob"
	"github.com/devicechain-io/dc-microservice/durability"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/nats-io/nats.go"
)

// RestorePlan is verified local evidence, prepared before any cluster access.
// Its fields are private so callers cannot bypass verification.
type RestorePlan struct {
	instance string
	manifest *Manifest
	streams  map[string]nats.StreamInfo
	objects  map[string]objectRecord
}

// Action describes one resource. Plans never execute these actions.
type Action struct {
	Kind, Name, Action, Reason string
	CurrentMessages            uint64
	CurrentObjects             int
}

// PrepareRestore reuses inspect --verify and additionally requires identity,
// inventory coverage, and usable snapshot metadata. Tenant "*" means the whole
// instance: deployed instance configuration has no individual tenant selector.
func PrepareRestore(dir, instance string, forceForeign bool) (*RestorePlan, error) {
	m, err := Inspect(dir)
	if err != nil {
		return nil, err
	}
	if m.Instance == "" || m.Tenant == "" {
		return nil, fmt.Errorf("backup instance and tenant identity are required")
	}
	if !forceForeign && (m.Instance != instance || m.Tenant != "*") {
		return nil, fmt.Errorf("backup identity instance=%q tenant=%q does not match target instance=%q tenant=*: use --force-foreign", m.Instance, m.Tenant, instance)
	}
	if err := backupset.ValidateBackupPolicy(); err != nil {
		return nil, err
	}
	p := &RestorePlan{instance: instance, manifest: m, streams: map[string]nats.StreamInfo{}}
	entries := map[string]Entry{}
	for _, e := range m.Entries {
		entries[e.Kind+"/"+e.Name] = e
	}
	expected := map[string]bool{}
	absent, gateways := map[string]bool{}, map[string]bool{}
	for _, name := range m.AbsentGatewayStreams {
		if absent[name] {
			return nil, fmt.Errorf("duplicate absent gateway %q", name)
		}
		absent[name] = true
	}
	for _, item := range backupset.Items() {
		if !preserved(item) {
			continue
		}
		kind, name := resource(item, m.Instance)
		key := kind + "/" + name
		if item.Kind == backupset.KindGatewayStream {
			gateways[name] = true
		}
		expected[key] = true
		e, ok := entries[key]
		if ok && absent[name] {
			return nil, fmt.Errorf("gateway %q is both present and declared absent", name)
		}
		if !ok && item.Kind == backupset.KindGatewayStream && absent[name] {
			continue
		} // New exports explicitly attest that MQTT was disabled at the source.
		if !ok && item.Kind == backupset.KindStream && slices.Contains(m.DisabledStreams, name) {
			continue
		}
		if !ok {
			return nil, fmt.Errorf("missing must-survive/checkpoint entry %s %q", kind, name)
		}
		if e.Class != item.BackupClass {
			return nil, fmt.Errorf("entry %q has incorrect backup policy", name)
		}
	}
	for name := range absent {
		if !gateways[name] {
			return nil, fmt.Errorf("unknown absent gateway %q", name)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	for _, e := range m.Entries {
		if !expected[e.Kind+"/"+e.Name] {
			return nil, fmt.Errorf("unclassified restore entry %s %q", e.Kind, e.Name)
		}
		f, err := root.Open(e.Snapshot)
		if err != nil {
			return nil, err
		}
		if e.Kind == "object-store" {
			p.objects, err = readObjects(f, m.Instance)
		} else {
			var z *zip.Reader
			z, err = zip.NewReader(f, *e.SizeBytes)
			if err == nil {
				var r io.ReadCloser
				r, err = z.Open("metadata.json")
				if err == nil {
					var info struct {
						Config *nats.StreamConfig `json:"config"`
						State  *nats.StreamState  `json:"state"`
						Error  json.RawMessage    `json:"error"`
					}
					dec := json.NewDecoder(r)
					err = dec.Decode(&info)
					if err == nil && dec.Decode(new(any)) != io.EOF {
						err = fmt.Errorf("trailing snapshot metadata")
					}
					r.Close()
					if err == nil && (info.Config == nil || info.State == nil || len(info.Error) != 0 || info.Config.Name != e.Name || info.State.Msgs > 0 && (info.State.LastTime.IsZero() || info.State.LastSeq == 0)) {
						err = fmt.Errorf("invalid snapshot state for %q", e.Name)
					}
					if err == nil {
						p.streams[e.Name] = nats.StreamInfo{Config: *info.Config, State: *info.State}
					}
				}
			}
			if err == nil {
				if len(z.File) != 2 {
					err = fmt.Errorf("snapshot bundle must contain native archive and metadata only")
				} else {
					native, openErr := z.Open("snapshot.s2")
					if openErr != nil {
						err = openErr
					} else {
						native.Close()
					}
				}
			}
		}
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("%s %q: %w", e.Kind, e.Name, err)
		}
	}
	return p, nil
}

func preserved(item backupset.Item) bool {
	return item.BackupClass == durability.BackupRequired || item.BackupClass == durability.BackupCheckpoint
}

func resource(item backupset.Item, instance string) (string, string) {
	switch item.Kind {
	case backupset.KindStream:
		return "stream", messaging.StreamName(instance, item.Name)
	case backupset.KindGatewayStream:
		return "stream", item.Name
	case backupset.KindBucket:
		return "bucket", item.Name
	default:
		return "object-store", item.Name
	}
}

// Plan queries StreamInfo and reads the blob export only. It does not reconcile,
// create consumers, subscribe to application data, or mutate target storage.
// All actions are returned together only after every refusal check succeeds.
// enabledAreas describes the target declaration. Without it, an area-disabled
// source omission cannot be proven safe and is refused.
func (p *RestorePlan) Plan(ctx context.Context, nc *nats.Conn, blobs func(context.Context, io.Writer) error, enabledAreas ...[]string) ([]Action, error) {
	if p == nil || p.manifest == nil || blobs == nil {
		return nil, fmt.Errorf("verified plan and target blob reader required")
	}
	if len(enabledAreas) > 1 {
		return nil, fmt.Errorf("one target enabled area set required")
	}
	if len(enabledAreas) == 1 {
		if err := validateAreas(enabledAreas[0]); err != nil {
			return nil, err
		}
	}
	js, err := nc.JetStream()
	if err != nil {
		return nil, err
	}
	var out []Action
	for _, item := range backupset.Items() {
		kind, name := resource(item, p.instance)
		a := Action{Kind: kind, Name: name}
		if !preserved(item) {
			a.Action, a.Reason = "skip", string(item.BackupClass)+": "+item.Why
			out = append(out, a)
			continue
		}
		if item.Kind == backupset.KindObjectStore {
			current, err := targetObjects(ctx, blobs, p.instance)
			if err != nil {
				return nil, fmt.Errorf("target object store: %w", err)
			}
			// With no trusted per-object cut time, any divergent or extra target
			// object could be newer. Identical existing objects are safe to retain.
			for key, obj := range current {
				sourceKey := p.manifest.Instance + strings.TrimPrefix(key, p.instance)
				saved, ok := p.objects[sourceKey]
				liveRef, _ := blob.ParseRef(obj.Ref)
				savedRef, _ := blob.ParseRef(saved.Ref)
				if !ok || obj.SHA256 != saved.SHA256 || obj.Size != saved.Size || liveRef.Backend != savedRef.Backend {
					return nil, fmt.Errorf("target object %q may contain newer data; replacement would mean loss", key)
				}
			}
			a.Action = "replace"
			a.CurrentObjects = len(current)
			if len(current) == 0 {
				a.Action = "create"
			} else if len(current) == len(p.objects) {
				a.Action, a.Reason = "skip", "all target objects match verified backup bytes"
			}
			out = append(out, a)
			continue
		}
		_, source := resource(item, p.manifest.Instance)
		saved, included := p.streams[source]
		live, err := js.StreamInfo(name, nats.Context(ctx))
		if err != nil && !errors.Is(err, nats.ErrStreamNotFound) {
			return nil, fmt.Errorf("target stream %q: %w", name, err)
		}
		if !included {
			if item.Kind == backupset.KindStream {
				if len(enabledAreas) == 0 || streamEnabled(item.Name, enabledAreas[0]) {
					return nil, fmt.Errorf("target stream %q belongs to an enabled area but backup has no data: source area was disabled; cannot restore this coverage", name)
				}
			}
			if live != nil {
				return nil, fmt.Errorf("missing checkpoint entry for target stream %q", name)
			}
			a.Action, a.Reason = "skip", "gateway absent from backup and target"
			if item.Kind == backupset.KindStream {
				a.Reason = "source area disabled: backup has no data; stream also disabled and absent on target"
			}
		} else if errors.Is(err, nats.ErrStreamNotFound) {
			a.Action = "create"
		} else {
			a.CurrentMessages = live.State.Msgs
			if live.State.Msgs > 0 && (live.State.LastTime.IsZero() || live.State.LastTime.After(saved.State.LastTime) || live.State.LastSeq > saved.State.LastSeq || live.State.Msgs > saved.State.Msgs) {
				return nil, fmt.Errorf("target stream %q has newer data (%d current messages); replacement would mean loss", name, live.State.Msgs)
			}
			a.Action = "replace"
		}
		out = append(out, a)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func targetObjects(ctx context.Context, export func(context.Context, io.Writer) error, instance string) (map[string]objectRecord, error) {
	r, w := io.Pipe()
	done := make(chan error, 1)
	go func() { err := export(ctx, w); w.CloseWithError(err); done <- err }()
	objects, err := readObjects(r, instance)
	r.CloseWithError(err)
	exportErr := <-done
	if err != nil {
		return nil, err
	}
	return objects, exportErr
}

// readObjects verifies archive contents against the embedded index, including
// identity-bearing keys. It streams object bytes rather than buffering blobs.
func readObjects(r io.Reader, instance string) (map[string]objectRecord, error) {
	tr := tar.NewReader(r)
	actual := map[string]objectRecord{}
	var index []objectRecord
	indexed := false
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("non-regular object archive entry %q", h.Name)
		}
		if h.Name == "index.json" {
			if indexed {
				return nil, fmt.Errorf("duplicate object index")
			}
			indexed = true
			if err := json.NewDecoder(tr).Decode(&index); err != nil {
				return nil, err
			}
			continue
		}
		key := strings.TrimPrefix(h.Name, "objects/")
		parts := strings.Split(key, "/")
		if key == h.Name || !fs.ValidPath(key) || strings.ContainsAny(key, "\\:") || len(parts) != 4 || parts[0] != instance || parts[1] == "" || parts[2] == "" || parts[3] == "" {
			return nil, fmt.Errorf("foreign or invalid object identity %q", h.Name)
		}
		if _, ok := actual[key]; ok {
			return nil, fmt.Errorf("duplicate object %q", key)
		}
		sum := sha256.New()
		n, err := io.Copy(sum, tr)
		if err != nil {
			return nil, err
		}
		actual[key] = objectRecord{Size: n, SHA256: hex.EncodeToString(sum.Sum(nil))}
	}
	if !indexed || len(index) != len(actual) {
		return nil, fmt.Errorf("missing object index or content")
	}
	seen := map[string]bool{}
	for _, record := range index {
		ref, err := blob.ParseRef(record.Ref)
		if err != nil {
			return nil, err
		}
		if ref.Backend != blob.BackendFilesystem && ref.Backend != blob.BackendS3 {
			return nil, fmt.Errorf("unsupported object backend %q", ref.Backend)
		}
		a, ok := actual[ref.Key]
		if !ok || seen[ref.Key] || record.Size != a.Size || record.SHA256 != a.SHA256 {
			return nil, fmt.Errorf("object index mismatch %q", record.Ref)
		}
		seen[ref.Key] = true
		actual[ref.Key] = record
	}
	return actual, nil
}

// WritePlan prints only a completely successful plan. Replacement rows include
// the observed current message count; skip rows always explain the policy.
func WritePlan(w io.Writer, actions []Action) error {
	for _, a := range actions {
		var err error
		switch a.Action {
		case "replace":
			if a.Kind == "object-store" {
				_, err = fmt.Fprintf(w, "%s %q: replace (current objects: %d)\n", a.Kind, a.Name, a.CurrentObjects)
			} else {
				_, err = fmt.Fprintf(w, "%s %q: replace (current messages: %d)\n", a.Kind, a.Name, a.CurrentMessages)
			}
		case "skip":
			_, err = fmt.Fprintf(w, "%s %q: skip (%s)\n", a.Kind, a.Name, a.Reason)
		default:
			_, err = fmt.Fprintf(w, "%s %q: create\n", a.Kind, a.Name)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
