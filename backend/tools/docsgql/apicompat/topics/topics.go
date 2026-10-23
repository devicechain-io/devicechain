// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package topics compares the MQTT topic layout a device uses between two builds.
//
// The layout is not written down anywhere to be edited: it is what the broker grant
// builder (natsauth.DevicePermissions) hands one device, which is the layout the broker
// enforces and the same declarations the ingest subscription and the parsers are built
// from. A topic the baseline granted and the head does not is a topic a deployed device
// can no longer use.
package topics

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/devicechain-io/dc-docsgql/apicompat/decl"
)

// Surface is the name topic changes are declared under.
const Surface = "topics"

// Layout is the topics one device may use, with {instance}, {tenant} and {device} as
// placeholders.
type Layout struct {
	Publish   []string `json:"publish"`
	Subscribe []string `json:"subscribe"`
}

// Read loads an exported layout and refuses an empty one: a layout with no topics would
// report every baseline topic as removed on one side and nothing on the other, and a
// layout that is empty on BOTH sides compares equal and passes.
func Read(path string) (Layout, error) {
	var l Layout
	b, err := os.ReadFile(path)
	if err != nil {
		return l, err
	}
	if err := json.Unmarshal(b, &l); err != nil {
		return l, fmt.Errorf("%s: %w", path, err)
	}
	if len(l.Publish) == 0 || len(l.Subscribe) == 0 {
		return l, fmt.Errorf("%s lists %d publish and %d subscribe topics; a device is granted both, so the export did not capture the layout", path, len(l.Publish), len(l.Subscribe))
	}
	return l, nil
}

// Kinds maps every change kind to its severity.
var Kinds = map[string]decl.Severity{
	"topic-removed": decl.Breaking,
	"topic-added":   decl.Safe,
}

// Compare classifies every difference. A changed topic shape is one removal and one
// addition, and the removal is the break.
func Compare(base, head Layout) []decl.Change {
	var out []decl.Change
	diff := func(dir string, b, h []string) {
		bs, hs := set(b), set(h)
		for _, t := range sorted(bs) {
			if !hs[t] {
				out = append(out, decl.Change{Surface: Surface, Coord: dir + ":" + t, Kind: "topic-removed", Severity: decl.Breaking,
					Detail: fmt.Sprintf("a device could %s %s and now cannot", dir, t)})
			}
		}
		for _, t := range sorted(hs) {
			if !bs[t] {
				out = append(out, decl.Change{Surface: Surface, Coord: dir + ":" + t, Kind: "topic-added", Severity: decl.Safe})
			}
		}
	}
	diff("publish", base.Publish, head.Publish)
	diff("subscribe", base.Subscribe, head.Subscribe)
	decl.Sort(out)
	return out
}

func set(s []string) map[string]bool {
	m := make(map[string]bool, len(s))
	for _, v := range s {
		m[v] = true
	}
	return m
}

func sorted(m map[string]bool) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
