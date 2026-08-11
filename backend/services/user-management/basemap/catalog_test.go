// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package basemap_test

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-user-management/basemap"
)

// The console's provider catalog, validated by the SERVER'S OWN rules.
//
// 🔴 THE CROSS-LANGUAGE REACH IS THE ENTIRE VALUE OF THIS FILE, and a TypeScript test
// beside the catalog would be strictly worse than no test at all.
//
// The console carries client-side basemap checks, and they are deliberately WEAKER
// than these — a client check stricter than the server's would refuse a value the
// platform accepts, so BasemapPage's own comment says to keep them weaker on purpose.
// A catalog test written against those checks would therefore pass a catalog entry the
// API refuses, while reporting green: it would measure the lenient instrument and call
// the result compliance.
//
// What must actually hold is that no catalog entry can ship a value THIS package
// rejects — because the first person to pick that provider would hit an error we
// authored, in the one feature whose premise is getting the credit line right. The
// only way to check that claim is to run the real validator over the real file.
const catalogPath = "../../../../frontend/apps/console/src/components/basemap/catalog.json"

// The two shapes a composed key can arrive in, because the console URL-encodes it.
//
// The first is what an ordinary provider key composes to: `-_.~` and alphanumerics are
// exactly the characters encodeURIComponent leaves alone, so this is byte-identical on
// both sides. The second is what a key containing `&` or a space becomes — percent
// escapes, which is a shape the URL rules here have to accept and which nothing else in
// this package's tests exercises.
var sampleAPIKeys = []string{
	"k3y-with_odd.chars~09",
	"k3y%26with%20escapes",
}

type catalogFile struct {
	Providers []catalogProvider `json:"providers"`
}

type catalogProvider struct {
	ID                string          `json:"id"`
	Name              string          `json:"name"`
	TermsURL          string          `json:"termsUrl"`
	TemplateSource    string          `json:"templateSource"`
	AttributionSource string          `json:"attributionSource"`
	KeyURL            *string         `json:"keyUrl"`
	Sources           []catalogSource `json:"sources"`
}

type catalogSource struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	TileURL     string `json:"tileUrl"`
	Attribution string `json:"attribution"`
}

func loadCatalog(t *testing.T) catalogFile {
	t.Helper()
	raw, err := os.ReadFile(catalogPath)
	// 🔴 FAIL, never t.Skip. A skip on a missing file turns a moved or deleted catalog
	// into a green run — the gate would report success precisely when it had stopped
	// examining anything, which is the one outcome it exists to prevent.
	if err != nil {
		t.Fatalf("cannot read the console basemap catalog at %s: %v\n"+
			"This test reaches across the repo on purpose (see the comment above). If the "+
			"catalog moved, update catalogPath; do not delete or skip this test.", catalogPath, err)
	}
	var cat catalogFile
	if err := json.Unmarshal(raw, &cat); err != nil {
		t.Fatalf("catalog is not valid JSON: %v", err)
	}
	if len(cat.Providers) == 0 {
		t.Fatal("catalog has no providers: every assertion below would vacuously pass")
	}
	return cat
}

// compose mirrors the console's composeTileUrl. It is restated here rather than
// shared because the two languages cannot share it — and a divergence is caught by
// TestCatalogComposedURLsMatchTheConsole in the console suite, which asserts the same
// composed strings from the other side.
func compose(tileURL, key string) string {
	return strings.ReplaceAll(tileURL, "{apiKey}", key)
}

// Every catalog entry, composed exactly as the picker composes it, must survive the
// validator that guards the mutation. Spec §8.
func TestCatalogEntriesPassValidate(t *testing.T) {
	cat := loadCatalog(t)

	checked := 0
	for _, p := range cat.Providers {
		for _, s := range p.Sources {
			for _, key := range sampleAPIKeys {
				t.Run(p.ID+"/"+s.ID+"/"+key, func(t *testing.T) {
					url := compose(s.TileURL, key)
					attr := s.Attribution
					if err := basemap.Validate(basemap.Basemap{TileURL: &url, Attribution: &attr}); err != nil {
						t.Errorf("catalog entry %s/%s would be REFUSED by the API it prefills: %v\n"+
							"  tileUrl:     %s\n  attribution: %s", p.ID, s.ID, err, url, attr)
					}
				})
				checked++
			}
		}
	}
	// The loop above is only meaningful if it ran. A catalog whose providers all carry
	// an empty `sources` array would satisfy every t.Run above by never entering one.
	if checked == 0 {
		t.Fatal("no catalog sources were validated: the assertions above are vacuous")
	}
	t.Logf("validated %d catalog sources across %d providers", checked, len(cat.Providers))
}

// The attribution is the reason this catalog exists. An entry that prefills a blank
// one is worse than an absent entry: it teaches an operator that a tile source without
// a credit line is a normal thing to save.
func TestCatalogAttributionsAreRealCreditLines(t *testing.T) {
	cat := loadCatalog(t)
	for _, p := range cat.Providers {
		for _, s := range p.Sources {
			if strings.TrimSpace(s.Attribution) == "" {
				t.Errorf("%s/%s prefills an empty attribution", p.ID, s.ID)
			}
		}
	}
}

// 🔴 A NEGATIVE CONTROL ON THE VALIDATOR ITSELF. Every assertion above is of the form
// "Validate returned nil". That is exactly the shape that keeps reporting success
// after the thing under test stops working: if Validate were changed to accept
// anything, this whole file would still be green and would still read as proof.
//
// So: take a real catalog entry and break it in each way the catalog could plausibly
// be broken by an editor, and require the validator to notice.
func TestValidateWouldCatchABrokenCatalogEntry(t *testing.T) {
	cat := loadCatalog(t)
	good := cat.Providers[0].Sources[0]

	cases := []struct {
		name        string
		tileURL     string
		attribution string
	}{
		{"attribution deleted", good.TileURL, ""},
		{"http instead of https", strings.Replace(good.TileURL, "https://", "http://", 1), good.Attribution},
		{"placeholders lost (a single tile's URL pasted)", "https://tile.example.com/5/9/12.png", good.Attribution},
		{"a Leaflet {s} subdomain left in", "https://{s}.tile.example.com/{z}/{x}/{y}.png", good.Attribution},
		{"an unsubstituted {apiKey}", "https://tile.example.com/{z}/{x}/{y}.png?apikey={apiKey}", good.Attribution},
		{"attribution carrying markup we do not allow", good.TileURL, `<img src=x onerror=alert(1)>`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url, attr := tc.tileURL, tc.attribution
			b := basemap.Basemap{TileURL: &url}
			if attr != "" {
				b.Attribution = &attr
			}
			if err := basemap.Validate(b); err == nil {
				t.Fatalf("Validate ACCEPTED a catalog entry broken by %q — every other assertion "+
					"in this file is therefore worthless, because they all assert that Validate "+
					"returns nil", tc.name)
			}
		})
	}
}

// Provenance, enforced rather than requested. §3.2 requires each entry to record where
// its attribution and template were read from and where its terms live, because the
// alternative is a value nobody can re-check without redoing the research.
func TestCatalogRecordsItsProvenance(t *testing.T) {
	cat := loadCatalog(t)
	for _, p := range cat.Providers {
		for name, u := range map[string]string{
			"termsUrl":          p.TermsURL,
			"templateSource":    p.TemplateSource,
			"attributionSource": p.AttributionSource,
		} {
			if !strings.HasPrefix(u, "https://") {
				t.Errorf("provider %s: %s must be an https URL, got %q", p.ID, name, u)
			}
		}
		// A keyed provider has to tell someone where to get a key. Derived from the
		// template rather than declared beside it, so the two cannot disagree.
		keyed := false
		for _, s := range p.Sources {
			if strings.Contains(s.TileURL, "{apiKey}") {
				keyed = true
			}
		}
		switch {
		case keyed && (p.KeyURL == nil || !strings.HasPrefix(*p.KeyURL, "https://")):
			t.Errorf("provider %s needs an API key but has no https keyUrl to send someone to", p.ID)
		case !keyed && p.KeyURL != nil:
			t.Errorf("provider %s needs no API key but carries a keyUrl", p.ID)
		}
	}
}

// Identity, so the stored `providerId/sourceId` a picker round-trips through cannot
// become ambiguous, and so two entries cannot quietly serve the same tiles under
// different names.
func TestCatalogIdentifiersAreUnique(t *testing.T) {
	cat := loadCatalog(t)
	providers := map[string]bool{}
	urls := map[string]string{}
	for _, p := range cat.Providers {
		if providers[p.ID] {
			t.Errorf("duplicate provider id %q", p.ID)
		}
		providers[p.ID] = true

		sources := map[string]bool{}
		for _, s := range p.Sources {
			if sources[s.ID] {
				t.Errorf("provider %s has a duplicate source id %q", p.ID, s.ID)
			}
			sources[s.ID] = true

			key := fmt.Sprintf("%s/%s", p.ID, s.ID)
			if prev, dup := urls[s.TileURL]; dup {
				t.Errorf("%s and %s ship the same tile URL %q", prev, key, s.TileURL)
			}
			urls[s.TileURL] = key
		}
	}
}
