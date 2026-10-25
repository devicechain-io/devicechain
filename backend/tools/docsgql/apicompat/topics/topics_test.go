// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package topics

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/devicechain-io/dc-docsgql/apicompat/decl"
)

func layout() Layout {
	return Layout{
		Publish:   []string{"{instance}/{tenant}/devices/{device}/events", "{instance}/{tenant}/command-responses/{device}"},
		Subscribe: []string{"{instance}/{tenant}/device-commands/{device}"},
	}
}

func TestAChangedShapeIsARemovalNamingTheOldTopic(t *testing.T) {
	head := layout()
	head.Publish = []string{"{instance}/{tenant}/devices/{device}/telemetry", "{instance}/{tenant}/command-responses/{device}"}
	cs := Compare(layout(), head)
	if len(cs) != 2 {
		t.Fatalf("got %v", cs)
	}
	var removed, added bool
	for _, c := range cs {
		switch {
		case c.Kind == "topic-removed" && c.Severity == decl.Breaking && c.Coord == "publish:{instance}/{tenant}/devices/{device}/events":
			removed = true
		case c.Kind == "topic-added" && c.Severity == decl.Safe:
			added = true
		}
	}
	if !removed || !added {
		t.Fatalf("removed=%v added=%v: %v", removed, added, cs)
	}
}

func TestADirectionMattersAndIdenticalLayoutsAreQuiet(t *testing.T) {
	if cs := Compare(layout(), layout()); len(cs) != 0 {
		t.Fatalf("got %v", cs)
	}
	// The same topic moving from subscribe to publish is a removal of the subscription.
	head := layout()
	head.Subscribe = []string{"{instance}/{tenant}/other/{device}"}
	head.Publish = append(head.Publish, "{instance}/{tenant}/device-commands/{device}")
	found := false
	for _, c := range Compare(layout(), head) {
		if c.Kind == "topic-removed" && c.Coord == "subscribe:{instance}/{tenant}/device-commands/{device}" {
			found = true
		}
	}
	if !found {
		t.Fatal("a topic a device could subscribe to and now cannot is a break even if it can publish there")
	}
}

func TestAnEmptyLayoutIsRefused(t *testing.T) {
	p := filepath.Join(t.TempDir(), "l.json")
	if err := os.WriteFile(p, []byte(`{"publish":[],"subscribe":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(p); err == nil {
		t.Fatal("an export that captured no topics must not read as a layout")
	}
}
