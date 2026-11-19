// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	server "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

func threeGroups() *config.NatsShardGroupsConfiguration {
	return &config.NatsShardGroupsConfiguration{Count: 3, Suffixes: []string{streams.InboundEvents, streams.ResolvedEvents}}
}

// groupStreams names every group stream of shards, base (group zero) included.
func groupStreams(shards *config.NatsShardGroupsConfiguration) []string {
	var out []string
	for _, suffix := range shards.Suffixes {
		for g := 0; g < shards.Count; g++ {
			out = append(out, messaging.StreamName("sample", messaging.GroupSuffix(suffix, g)))
		}
	}
	return out
}

// publishUnread stores one message in the stream, on a concrete subject its
// configuration accepts, and leaves it unread.
func publishUnread(t *testing.T, nc *nats.Conn, name string) {
	t.Helper()
	js, _ := nc.JetStream()
	info, err := js.StreamInfo(name)
	if err != nil {
		t.Fatal(err)
	}
	subject := strings.NewReplacer("*", "t1", ">", "x").Replace(info.Config.Subjects[0])
	if _, err := js.Publish(subject, []byte("unread in "+name)); err != nil {
		t.Fatalf("%s via %s: %v", name, subject, err)
	}
}

func TestShardedBackupExportsEveryGroupStream(t *testing.T) {
	areas := profileAreas(t, "default")
	shards := threeGroups()
	nc := shardedBroker(t, areas, shards)
	for _, name := range groupStreams(shards) {
		publishUnread(t, nc, name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "set")
	if err := Create(ctx, nc, "sample", dir, emptyBlobs(t), shards, areas); err != nil {
		t.Fatal(err)
	}
	m, err := Inspect(dir)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, e := range m.Entries {
		have[e.Name] = true
	}
	for _, name := range groupStreams(shards) {
		if !have[name] {
			t.Errorf("backup of a sharded instance omits %q", name)
		}
	}
	if m.ShardGroups == nil || m.ShardGroups.Count != 3 || !slices.Equal(m.ShardGroups.Suffixes, []string{streams.InboundEvents, streams.ResolvedEvents}) {
		t.Fatalf("manifest does not record the layout: %+v", m.ShardGroups)
	}

	// The restore plan lists every group stream, each carrying its unread message.
	p, err := PrepareRestore(dir, "sample", false)
	if err != nil {
		t.Fatal(err)
	}
	actions, err := p.Plan(ctx, nc, emptyBlobs(t), shards, areas)
	if err != nil {
		t.Fatal(err)
	}
	planned := map[string]Action{}
	for _, a := range actions {
		planned[a.Name] = a
	}
	for _, name := range groupStreams(shards) {
		if a := planned[name]; a.Action != "replace" || a.CurrentMessages != 1 {
			t.Errorf("restore plan for %q: %+v", name, a)
		}
	}
	var out bytes.Buffer
	if err := WritePlan(&out, actions); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `stream "sample_resolved-events-g2": replace (current messages: 1)`) {
		t.Fatal(out.String())
	}

	// A backup restores only into the same layout. Control: the same layout,
	// as a separately built value with the suffixes in another order, plans.
	reordered := &config.NatsShardGroupsConfiguration{Count: 3, Suffixes: []string{streams.ResolvedEvents, streams.InboundEvents}}
	if _, err := p.Plan(ctx, nc, emptyBlobs(t), reordered, areas); err != nil {
		t.Fatalf("same layout refused: %v", err)
	}
	for _, other := range []*config.NatsShardGroupsConfiguration{
		nil,
		{Count: 2, Suffixes: []string{streams.InboundEvents, streams.ResolvedEvents}},
		{Count: 4, Suffixes: []string{streams.InboundEvents, streams.ResolvedEvents}},
		{Count: 3, Suffixes: []string{streams.InboundEvents}},
	} {
		if _, err := p.Plan(ctx, nc, emptyBlobs(t), other, areas); err == nil || !strings.Contains(err.Error(), "same shard-group layout") {
			t.Errorf("target layout %+v accepted: %v", other, err)
		}
	}

	// Coverage: a manifest that drops one group entry, or forgets the layout,
	// does not prepare.
	group := messaging.StreamName("sample", messaging.GroupSuffix(streams.ResolvedEvents, 2))
	dropped := copySet(t, dir)
	editRestoreManifest(t, dropped, func(m *Manifest) {
		m.Entries = slices.DeleteFunc(m.Entries, func(e Entry) bool { return e.Name == group })
	})
	if _, err := PrepareRestore(dropped, "sample", false); err == nil || !strings.Contains(err.Error(), group) {
		t.Fatalf("manifest missing %q prepared: %v", group, err)
	}
	forgotten := copySet(t, dir)
	editRestoreManifest(t, forgotten, func(m *Manifest) { m.ShardGroups = nil })
	if _, err := PrepareRestore(forgotten, "sample", false); err == nil || !strings.Contains(err.Error(), "unclassified") {
		t.Fatalf("group entries without a recorded layout prepared: %v", err)
	}
	for _, bad := range []*ShardGroups{
		{Count: 1, Suffixes: []string{streams.InboundEvents}},
		{Count: 17, Suffixes: []string{streams.InboundEvents}},
		{Count: 3},
		{Count: 3, Suffixes: []string{streams.ResolvedEvents, streams.InboundEvents}},
		{Count: 3, Suffixes: []string{streams.DeviceEventsCapture}},
	} {
		forged := copySet(t, dir)
		editRestoreManifest(t, forged, func(m *Manifest) { m.ShardGroups = bad })
		if _, err := Inspect(forged); err == nil {
			t.Errorf("manifest layout %+v accepted", bad)
		}
	}
}

func TestShardedBackupFailsClosed(t *testing.T) {
	areas := profileAreas(t, "default")
	shards := threeGroups()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	refused := func(t *testing.T, nc *nats.Conn, cfg *config.NatsShardGroupsConfiguration, want string) {
		t.Helper()
		dir := filepath.Join(t.TempDir(), "set")
		err := Create(ctx, nc, "sample", dir, emptyBlobs(t), cfg, areas)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("want a refusal naming %q, got %v", want, err)
		}
		if _, statErr := os.Stat(filepath.Join(dir, "manifest.json")); !os.IsNotExist(statErr) {
			t.Fatalf("a refused backup published its manifest: %v", statErr)
		}
	}
	t.Run("configured group stream missing", func(t *testing.T) {
		nc := shardedBroker(t, areas, shards)
		missing := messaging.StreamName("sample", messaging.GroupSuffix(streams.InboundEvents, 2))
		js, _ := nc.JetStream()
		if err := js.DeleteStream(missing); err != nil {
			t.Fatal(err)
		}
		refused(t, nc, shards, missing)
	})
	t.Run("group stream the configuration does not declare", func(t *testing.T) {
		nc := shardedBroker(t, areas, shards)
		refused(t, nc, nil, "sample_inbound-events-g1")
		refused(t, nc, &config.NatsShardGroupsConfiguration{Count: 2, Suffixes: shards.Suffixes}, "-g2")
		refused(t, nc, &config.NatsShardGroupsConfiguration{Count: 3, Suffixes: []string{streams.InboundEvents}}, "sample_resolved-events-g")
	})
	t.Run("invalid configuration", func(t *testing.T) {
		nc := shardedBroker(t, areas, shards)
		refused(t, nc, &config.NatsShardGroupsConfiguration{Count: 1, Suffixes: shards.Suffixes}, "at least 2")
	})
	t.Run("control: an unsharded instance backs up as before", func(t *testing.T) {
		nc := shardedBroker(t, areas, nil)
		dir := filepath.Join(t.TempDir(), "set")
		if err := Create(ctx, nc, "sample", dir, emptyBlobs(t), nil, areas); err != nil {
			t.Fatal(err)
		}
		m, err := Inspect(dir)
		if err != nil {
			t.Fatal(err)
		}
		if m.ShardGroups != nil {
			t.Fatalf("unsharded manifest records a layout: %+v", m.ShardGroups)
		}
		raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "shard_groups") {
			t.Fatal("unsharded manifest carries shard_groups, which an older dcctl refuses")
		}
	})
}

// copySet copies a backup set so a test can forge its manifest.
func copySet(t *testing.T, dir string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "copy")
	if err := os.CopyFS(out, os.DirFS(dir)); err != nil {
		t.Fatal(err)
	}
	return out
}

// The cross-check's listing must fail on an API error, on any page, rather than
// read as a short list that hides an undeclared group stream.
func TestStreamNamesFailsOnAnyPage(t *testing.T) {
	s, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: -1, NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	go s.Start()
	if !s.ReadyForConnections(5 * time.Second) {
		t.Fatal("NATS not ready")
	}
	t.Cleanup(func() { s.Shutdown(); s.WaitForShutdown() })
	nc, err := nats.Connect(s.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	pages := map[int]string{
		0: `{"total":3,"offset":0,"limit":1,"streams":["sample_inbound-events"]}`,
		1: `{"error":{"code":500,"description":"listing failed"}}`,
	}
	sub, err := nc.Subscribe("$JS.API.STREAM.NAMES", func(m *nats.Msg) {
		var req struct{ Offset int }
		_ = json.Unmarshal(m.Data, &req)
		_ = m.Respond([]byte(pages[req.Offset]))
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if names, err := streamNames(ctx, nc); err == nil || !strings.Contains(err.Error(), "listing failed") {
		t.Fatalf("an error on the second page read as %v, %v", names, err)
	}
	if err := refuseUndeclaredGroups(ctx, nc, "sample", map[string]bool{}); err == nil {
		t.Fatal("a failed listing passed the cross-check")
	}
	// Control: the same pages without the error list every name.
	pages[1] = `{"total":3,"offset":1,"limit":2,"streams":["sample_resolved-events","sample_inbound-events-g1"]}`
	names, err := streamNames(ctx, nc)
	if err != nil || !slices.Equal(names, []string{"sample_inbound-events", "sample_resolved-events", "sample_inbound-events-g1"}) {
		t.Fatalf("listing: %v %v", names, err)
	}
	if err := refuseUndeclaredGroups(ctx, nc, "sample", map[string]bool{}); err == nil || !strings.Contains(err.Error(), "sample_inbound-events-g1") {
		t.Fatalf("undeclared group on a later page passed: %v", err)
	}

	// A stream deleted between pages shrinks the total: the name that shifted
	// below the offset already read must not go unseen.
	pages[1] = `{"total":2,"offset":1,"limit":1,"streams":[]}`
	if names, err := streamNames(ctx, nc); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("a listing that shrank between pages read as %v, %v", names, err)
	}
	// A page that answers another offset is refused too.
	pages[1] = `{"total":3,"offset":0,"limit":2,"streams":["sample_resolved-events","sample_inbound-events-g1"]}`
	if _, err := streamNames(ctx, nc); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("a page at the wrong offset accepted: %v", err)
	}
	// Two consistent but different listings are refused.
	calls := 0
	_ = sub.Unsubscribe()
	sub, err = nc.Subscribe("$JS.API.STREAM.NAMES", func(m *nats.Msg) {
		calls++
		if calls == 1 {
			_ = m.Respond([]byte(`{"total":1,"offset":0,"limit":1,"streams":["sample_inbound-events"]}`))
			return
		}
		_ = m.Respond([]byte(`{"total":1,"offset":0,"limit":1,"streams":["sample_inbound-events-g1"]}`))
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := streamNames(ctx, nc); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("two different listings accepted: %v", err)
	}
}

// A target broker that still holds a group stream its configuration does not
// declare (a leftover of another layout) is refused by the restore plan.
func TestShardedRestorePlanRefusesLeftoverGroups(t *testing.T) {
	areas := profileAreas(t, "default")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	two := &config.NatsShardGroupsConfiguration{Count: 2, Suffixes: []string{streams.InboundEvents, streams.ResolvedEvents}}
	nc := shardedBroker(t, areas, two)
	dir := filepath.Join(t.TempDir(), "set")
	if err := Create(ctx, nc, "sample", dir, emptyBlobs(t), two, areas); err != nil {
		t.Fatal(err)
	}
	p, err := PrepareRestore(dir, "sample", false)
	if err != nil {
		t.Fatal(err)
	}
	// Control: the matching broker plans.
	if _, err := p.Plan(ctx, nc, emptyBlobs(t), two, areas); err != nil {
		t.Fatal(err)
	}
	js, _ := nc.JetStream()
	for _, leftover := range []string{"sample_inbound-events-g2", "sample_resolved-events-g3"} {
		if _, err := js.AddStream(&nats.StreamConfig{Name: leftover, Subjects: []string{"leftover." + leftover}, Storage: nats.FileStorage}); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Plan(ctx, nc, emptyBlobs(t), two, areas); err == nil || !strings.Contains(err.Error(), leftover) {
			t.Fatalf("target leftover %q accepted: %v", leftover, err)
		}
		if err := js.DeleteStream(leftover); err != nil {
			t.Fatal(err)
		}
	}
}
