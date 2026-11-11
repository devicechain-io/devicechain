// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

// testdata/area_config_keys_v1.4.0.json freezes the per-area decoding schema at
// v1.4.0: {"areas":{"<area>":{"retired":{path:reason},"tree":NODE}}}, using
// the same NODE shape as the instance snapshot. The per-area documents are the
// dct-<instance>-config ConfigMap entries, each decoded strictly by its area's
// typed config. An old pod must START against the new release's rendered config.
//
// REGENERATION RECIPE: git worktree add <temp-dir> v1.4.0 (or the new snapshot
// tag). In that worktree, add a throwaway main package importing every
// backend/services/*/config package. Reflect over each area's root
// New<X>Configuration() type following encoding/json decoding rules: use the json
// tag name, otherwise the Go field name, and match case-insensitively; skip
// json:"-" and unexported fields; promote embedded untagged structs; maps become
// map nodes, slices become elem nodes, and interface, json.RawMessage, and
// json.Unmarshaler types become any:true nodes. Include RetiredConfigKeys() where
// the type has it, and emit the JSON above. Delete the generator afterward. The
// snapshot MUST come from the tag's code, never the current code.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/releaseutil"
	"sigs.k8s.io/yaml"
)

const areaConfigSnapshotTag = "v1.4.0"

// instanceConfigSnapshotTag names the tag of instance_config_keys_v1.4.0.json.
const instanceConfigSnapshotTag = "v1.4.0"

func loadV130AreaConfigSnapshot(t *testing.T) map[string]v130ConfigSnapshot {
	t.Helper()
	raw, err := os.ReadFile("testdata/area_config_keys_v1.4.0.json")
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Areas map[string]v130ConfigSnapshot `json:"areas"`
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Areas) == 0 {
		t.Fatal("v1.4.0 area config snapshot has no areas")
	}
	return snapshot.Areas
}

func renderAreaConfigDocuments(t *testing.T, ch *chart.Chart, vals map[string]interface{}) map[string][]byte {
	t.Helper()
	manifest, err := renderChartClientSide(t.Context(), ch, vals)
	if err != nil {
		t.Fatal(err)
	}
	id := ""
	for _, src := range []map[string]interface{}{vals, ch.Values} {
		if inst, ok := src["instance"].(map[string]interface{}); ok {
			if v, ok := inst["id"].(string); ok && v != "" {
				id = v
				break
			}
		}
	}
	want := fmt.Sprintf("dct-%s-config", id)
	for _, doc := range releaseutil.SplitManifests(manifest) {
		var obj struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Data map[string]string `json:"data"`
		}
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			t.Fatalf("decoding rendered manifest: %v", err)
		}
		if obj.Kind != "ConfigMap" || obj.Metadata.Name != want {
			continue
		}
		out := make(map[string][]byte, len(obj.Data))
		for area, raw := range obj.Data {
			out[area] = []byte(raw)
		}
		return out
	}
	t.Fatalf("no rendered area configuration ConfigMap %q", want)
	return nil
}

func TestAreaConfigDocumentsDecodeOnV130(t *testing.T) {
	snapshot := loadV130AreaConfigSnapshot(t)
	ch, err := loadEmbeddedChart()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	nonEmpty := 0
	check := func(t *testing.T, vals map[string]interface{}) {
		t.Helper()
		for name, raw := range renderAreaConfigDocuments(t, ch, vals) {
			area, ok := snapshot[name]
			if !ok {
				t.Logf("skipping post-v1.4.0 area %q: no v1.4.0 pod exists for it", name)
				continue
			}
			seen[name] = true
			var doc map[string]any
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Errorf("area %q configuration must be a JSON object: %v", name, err)
				continue
			}
			if doc == nil {
				t.Errorf("area %q configuration must be a JSON object, got null", name)
				continue
			}
			if len(doc) > 0 {
				nonEmpty++
			}
			if paths := unknownV130ConfigPaths(doc, area.Tree, area.Retired, ""); len(paths) != 0 {
				t.Errorf("area %q has unknown v1.4.0 configuration keys: %s; a v1.4.0 pod of that area decodes the document strictly and refuses an unknown key, so a key added after v1.4.0 must be left out of the document while at its default", name, strings.Join(paths, ", "))
			}
		}
	}
	t.Run("bare defaults", func(t *testing.T) {
		check(t, map[string]interface{}{"instance": map[string]interface{}{
			"id": "dctest",
			"config": map[string]interface{}{"infrastructure": map[string]interface{}{
				"secrets": map[string]interface{}{"rootKey": base64.StdEncoding.EncodeToString(make([]byte, 32))},
			}},
		}})
	})
	for i, p := range renderProfiles {
		t.Run(p.name, func(t *testing.T) {
			check(t, profileValues(t, ch, p, profileInstance(i)))
		})
	}
	if nonEmpty == 0 {
		t.Error("every rendered area document was empty; the check compared nothing")
	}
	// Subtests are sequential: check coverage after all scenarios have run.
	for name := range snapshot {
		if !seen[name] {
			t.Errorf("snapshot area %q was never exercised by a rendered configuration document", name)
		}
	}
}

func TestAreaConfigWalker(t *testing.T) {
	snapshot := loadV130AreaConfigSnapshot(t)
	device, ok := snapshot["device-management"]
	if !ok {
		t.Fatal("snapshot lacks device-management")
	}
	for _, tc := range []struct {
		document string
		want     []string
	}{
		{`{"noSuchKeyPlanted":1,"x":{"y":2}}`, []string{"noSuchKeyPlanted", "x"}},
		{`{}`, nil},
	} {
		var doc map[string]any
		if err := json.Unmarshal([]byte(tc.document), &doc); err != nil {
			t.Fatal(err)
		}
		if got := unknownV130ConfigPaths(doc, device.Tree, device.Retired, ""); !slices.Equal(got, tc.want) {
			t.Errorf("document %s: unknown paths = %v, want %v", tc.document, got, tc.want)
		}
	}
	events, ok := snapshot["event-sources"]
	if !ok {
		t.Fatal("snapshot lacks event-sources")
	}
	// lpwanDownlink was added in v1.4.0, so it must be KNOWN to the frozen schema; a key
	// that is in no release must still be reported.
	var doc map[string]any
	if err := json.Unmarshal([]byte(`{"lpwanDownlink":true,"noSuchKeyPlanted":1}`), &doc); err != nil {
		t.Fatal(err)
	}
	if got := unknownV130ConfigPaths(doc, events.Tree, events.Retired, ""); !slices.Equal(got, []string{"noSuchKeyPlanted"}) {
		t.Errorf("event-sources unknown paths = %v, want [noSuchKeyPlanted]", got)
	}
}

var finalReleaseTagPattern = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)

func parseFinalReleaseTag(tag string) ([3]int, bool) {
	var version [3]int
	parts := finalReleaseTagPattern.FindStringSubmatch(tag)
	if parts == nil {
		return version, false
	}
	for i := range version {
		n, err := strconv.Atoi(parts[i+1])
		if err != nil {
			return version, false
		}
		version[i] = n
	}
	return version, true
}

func latestFinalReleaseTag(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("git", "tag", "--list", "v*")
	cmd.Dir = "."
	raw, err := cmd.Output()
	if err != nil {
		if _, lookupErr := exec.LookPath("git"); lookupErr != nil {
			t.Skip("git is missing; CI fetches tags; a skip here only happens in a tagless checkout")
		}
		t.Fatalf("listing release tags: %v", err)
	}
	latest := ""
	var highest [3]int
	for _, tag := range strings.Fields(string(raw)) {
		version, ok := parseFinalReleaseTag(tag)
		if ok && (latest == "" || slices.Compare(version[:], highest[:]) > 0) {
			latest, highest = tag, version
		}
	}
	if latest == "" {
		t.Skip("no final vMAJOR.MINOR.PATCH tag is present; CI fetches tags; a skip here only happens in a tagless checkout")
	}
	return latest
}

func snapshotStale(snapshot, latest string) bool {
	s, sok := parseFinalReleaseTag(snapshot)
	l, lok := parseFinalReleaseTag(latest)
	return sok && lok && (l[0] > s[0] || (l[0] == s[0] && l[1] > s[1]))
}

func TestSnapshotTagsAreCurrent(t *testing.T) {
	latest := latestFinalReleaseTag(t)
	for _, snapshot := range []struct{ name, tag string }{
		{"area configuration snapshot", areaConfigSnapshotTag},
		{"instance configuration snapshot", instanceConfigSnapshotTag},
	} {
		if _, ok := parseFinalReleaseTag(snapshot.tag); !ok {
			t.Fatalf("invalid %s tag %q", snapshot.name, snapshot.tag)
		}
		if snapshotStale(snapshot.tag, latest) {
			t.Fatalf("%s has tag %s, newest release tag is %s: regenerate the snapshot from the newest release tag (recipe at the top of area_config_v130_compat_test.go) and rename the testdata files", snapshot.name, snapshot.tag, latest)
		}
	}
}

func TestSnapshotStale(t *testing.T) {
	for _, tc := range []struct {
		snapshot, latest string
		want             bool
	}{
		{"v1.3.0", "v1.3.0", false},
		{"v1.3.0", "v1.3.5", false},
		{"v1.3.0", "v1.4.0", true},
		{"v1.3.0", "v2.0.0", true},
		{"v1.4.0", "v1.3.0", false},
	} {
		t.Run(tc.snapshot+" vs "+tc.latest, func(t *testing.T) {
			if got := snapshotStale(tc.snapshot, tc.latest); got != tc.want {
				t.Errorf("snapshotStale(%q, %q) = %v, want %v", tc.snapshot, tc.latest, got, tc.want)
			}
		})
	}
}
