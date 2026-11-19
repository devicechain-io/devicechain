// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// harborSceneFixture is the layout file the Unity showcase loads, relative to this package. It is
// outside this module, so `go test` does not track it: run with -count=1 after editing it.
var harborSceneFixture = filepath.Join("..", "..", "..", "..", "demos", "harborpulse-unity", "Assets", "Harborpulse", "Resources", "harbor_layout.json")

func harborSceneJSON(t *testing.T) []byte {
	t.Helper()
	b, err := json.MarshalIndent(HarborpulseSceneLayout(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

// TestHarborpulseSceneLayoutFixture holds the committed scene layout to the manifest. Regenerate
// with HARBORPULSE_UPDATE_SCENE=1 go test ./sim -run TestHarborpulseSceneLayoutFixture -count=1.
func TestHarborpulseSceneLayoutFixture(t *testing.T) {
	want := harborSceneJSON(t)
	if os.Getenv("HARBORPULSE_UPDATE_SCENE") == "1" {
		if err := os.WriteFile(harborSceneFixture, want, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", harborSceneFixture)
		return
	}
	got, err := os.ReadFile(harborSceneFixture)
	if err != nil {
		t.Fatalf("read the committed scene layout: %v", err)
	}
	// git on Windows may check the file out with CRLF line endings; the content is what matters
	got = bytes.ReplaceAll(got, []byte("\r\n"), []byte("\n"))
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is not the layout the manifest produces; regenerate it with HARBORPULSE_UPDATE_SCENE=1 (see the test's comment)", harborSceneFixture)
	}
}

// TestHarborpulseSceneLayoutIsTheManifestFleet: every device the scene draws is a device the
// manifest provisions, under the same external ID and type, and the other way round.
func TestHarborpulseSceneLayoutIsTheManifestFleet(t *testing.T) {
	m := NewHarborpulse(1, Load{}).Manifest()
	provisioned := map[string]string{}
	for _, p := range m.Populations {
		for _, in := range p.Instances {
			provisioned[in.ExternalId] = p.OfType
		}
	}
	layout := HarborpulseSceneLayout()
	drawn := map[string]string{}
	for _, d := range layout.Devices {
		if _, dup := drawn[d.ExternalId]; dup {
			t.Fatalf("the scene draws %s twice", d.ExternalId)
		}
		drawn[d.ExternalId] = d.DeviceType
	}
	if len(drawn) != len(provisioned) {
		t.Fatalf("the scene draws %d devices, the manifest provisions %d", len(drawn), len(provisioned))
	}
	for id, typ := range provisioned {
		if drawn[id] != typ {
			t.Errorf("%s: the manifest provisions type %q, the scene draws %q", id, typ, drawn[id])
		}
	}

	// every light carries the position the manifest gives it, and nothing else carries one
	for _, d := range layout.Devices {
		pos, isLight := HarborpulseLampPosition(d.Token)
		switch {
		case isLight && (d.Position == nil || d.Position.Lat != pos.Latitude || d.Position.Lon != pos.Longitude):
			t.Errorf("%s: the scene's position %+v is not the manifest's %+v", d.Token, d.Position, pos)
		case !isLight && d.Position != nil:
			t.Errorf("%s: the scene gives a position the manifest does not", d.Token)
		}
	}

	// the beats are the ones /status names
	if len(layout.Beats) != len(harborBeats) {
		t.Errorf("the scene has %d beats, the scenario %d", len(layout.Beats), len(harborBeats))
	}
	for _, b := range layout.Beats {
		if !strings.Contains(","+harborBeatAt((b.From+b.To)/2)+",", ","+b.Name+",") {
			t.Errorf("beat %s: /status does not name it in its own middle", b.Name)
		}
	}
}
