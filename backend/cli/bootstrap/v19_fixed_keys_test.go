// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"encoding/json"
	"strings"
	"testing"
)

// THE v1.9 DRILL'S MUST-CHECK: a v1.8.0 pod restarted on the v1.9 rendered configuration must
// START, with all four settings that v1.9 added as FIXED keys absent.
//
// A FIXED key is read from a mounted file by a strict loader, and a v1.8.0 pod's loader refuses a key
// it has never heard of. So a key that v1.9 RENDERS by default would crash-loop every v1.8.0 pod
// that restarts during the rolling upgrade, which is the v1.4 drill-5 failure mode. The four
// keys are therefore opt-in: an operator turns one on in a separate upgrade, after every replica of its
// area runs v1.9. This pins that the default render, for every way dcctl installs, does not carry them.
//
// The broader frozen-schema tests (TestDefaultInstanceConfigDocumentDecodesOnV130 and
// TestAreaConfigDocumentsDecodeOnV130) pin the release an upgrade starts from. This
// historical drill names the four keys explicitly, so its omission check and negative
// control keep working when the previous-release snapshot advances.
var v19FixedKeys = []struct {
	name string
	// doc is "instance" for the instance configuration document, or the area whose
	// configuration document carries the key.
	doc  string
	path string
}{
	{"updateManagement.rolloutSafety", "instance", "infrastructure.updateManagement.rolloutSafety"},
	{"mcp.commandProposals", "instance", "infrastructure.mcp.commandProposals"},
	{"capture.mqttStoreEnvelopes", "event-sources", "capture.mqttStoreEnvelopes"},
	{"nats.shardGroups.seed", "instance", "infrastructure.nats.shardGroups.seed"},
}

// hasConfigPath reports whether the dotted path is present in doc, matching names the way
// encoding/json does (case-insensitively), so a key spelled in either case is still seen.
func hasConfigPath(doc map[string]any, path string) bool {
	var cur any = doc
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		next, found := any(nil), false
		for k, v := range m {
			if strings.EqualFold(k, part) {
				next, found = v, true
				break
			}
		}
		if !found {
			return false
		}
		cur = next
	}
	return true
}

func decodeConfigDoc(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decoding a rendered configuration document: %v", err)
	}
	if len(doc) == 0 {
		t.Fatal("a rendered configuration document is empty, so this check would compare nothing")
	}
	return doc
}

// v19Documents are the documents the four keys live in for one install: the instance document
// as dcctl composes it, and the per-area documents the chart renders.
type v19Documents struct {
	instance map[string]any
	areas    map[string]map[string]any
}

func (d v19Documents) has(key struct{ name, doc, path string }) bool {
	if key.doc == "instance" {
		return hasConfigPath(d.instance, key.path)
	}
	area, ok := d.areas[key.doc]
	return ok && hasConfigPath(area, key.path)
}

func TestV19FixedKeysAreAbsentFromEveryDefaultRender(t *testing.T) {
	ch, err := loadEmbeddedChart()
	if err != nil {
		t.Fatal(err)
	}
	sawEventSources := false
	check := func(t *testing.T, st *State, vals map[string]interface{}, instanceDoc []byte) {
		t.Helper()
		docs := v19Documents{instance: decodeConfigDoc(t, instanceDoc), areas: map[string]map[string]any{}}
		for area, raw := range renderAreaConfigDocuments(t, ch, vals) {
			var doc map[string]any
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatalf("area %q: %v", area, err)
			}
			docs.areas[area] = doc
		}
		if _, ok := docs.areas["event-sources"]; ok {
			sawEventSources = true
		}
		for _, k := range v19FixedKeys {
			if docs.has(struct{ name, doc, path string }(k)) {
				t.Errorf("the default render carries %s (%s in the %s document): a v1.8.0 pod "+
					"decodes that document strictly and would refuse to start on it; the key must stay opt-in",
					k.name, k.path, k.doc)
			}
		}
	}
	for i, p := range renderProfiles {
		t.Run(p.name, func(t *testing.T) {
			st := p.st(profileInstance(i))
			_, doc, err := releaseValues(t.Context(), ch, st, nil)
			if err != nil {
				t.Fatal(err)
			}
			check(t, st, profileValues(t, ch, p, profileInstance(i)), doc)
		})
	}
	if !sawEventSources {
		t.Error("no profile rendered an event-sources document, so capture.mqttStoreEnvelopes was never examined")
	}
}

// THE NEGATIVE CONTROL. A check that has never failed certifies nothing, so each key is turned ON
// the way an operator (or, for the seed, a sharding level) would, and the check above must SEE it.
// The moving previous-release snapshots now know these keys; this control pins the explicit
// presence check independently of those snapshots.
func TestV19FixedKeysAreSeenWhenTurnedOn(t *testing.T) {
	ch, err := loadEmbeddedChart()
	if err != nil {
		t.Fatal(err)
	}
	base := renderProfiles[0]

	nested := func(path string, value interface{}) map[string]interface{} {
		parts := strings.Split(path, ".")
		leaf := map[string]interface{}{parts[len(parts)-1]: value}
		for i := len(parts) - 2; i >= 0; i-- {
			leaf = map[string]interface{}{parts[i]: leaf}
		}
		return leaf
	}

	for _, k := range v19FixedKeys {
		t.Run(k.name, func(t *testing.T) {
			var docs v19Documents
			st := base.st(profileInstance(0))
			switch {
			case k.name == "nats.shardGroups.seed":
				// Rendered by a sharding level, never by an upgrade.
				st.Sharding = ShardingScale
			case k.doc == "instance":
				// Set the way an operator's --values overlay does, under instance.config.
				st.ValuesOverlay = map[string]interface{}{"instance": map[string]interface{}{"config": nested(k.path, true)}}
			default:
				st.ValuesOverlay = map[string]interface{}{"functionalAreas": map[string]interface{}{
					k.doc: map[string]interface{}{"config": nested(k.path, true)}}}
			}
			vals, raw, err := releaseValues(t.Context(), ch, st, nil)
			if err != nil {
				t.Fatal(err)
			}
			docs = v19Documents{instance: decodeConfigDoc(t, raw), areas: map[string]map[string]any{}}
			for area, rawArea := range renderAreaConfigDocuments(t, ch, vals) {
				var d map[string]any
				if err := json.Unmarshal(rawArea, &d); err != nil {
					t.Fatal(err)
				}
				docs.areas[area] = d
			}
			if !docs.has(struct{ name, doc, path string }(k)) {
				t.Fatalf("with %s turned on the render does not carry it, so the absence check above could never fail", k.name)
			}
		})
	}
}
