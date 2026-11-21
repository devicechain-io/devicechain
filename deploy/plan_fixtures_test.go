// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package assets

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Plan-gate fixtures are states of real-shaped clusters, so they are the one place a
// careless regeneration could commit something confidential: a project id, an endpoint, a
// certificate. hack/fixtures/gke-extraction/gen-state.py derives everything from the example
// project; this scan is what keeps it that way.

const sanitizedProject = "example-project"

var (
	certBlockRe   = regexp.MustCompile(`-----BEGIN [A-Z ]*(CERTIFICATE|PRIVATE KEY|RSA PRIVATE KEY|EC PRIVATE KEY)-----`)
	masterAuthRe  = regexp.MustCompile(`"(master_auth|client_certificate|client_key|cluster_ca_certificate)"\s*:\s*(\[|\{|")`)
	projectPathRe = regexp.MustCompile(`projects/([A-Za-z0-9_.:-]+)/`)
	projectAttrRe = regexp.MustCompile(`"(?:project|project_id)"\s*:\s*"([^"]*)"`)
	endpointRe    = regexp.MustCompile(`"endpoint"\s*:\s*"[^"]+"`)
)

// scanFixture returns one finding per confidential-looking thing in a fixture's text.
func scanFixture(text string) []string {
	var out []string
	if certBlockRe.MatchString(text) {
		out = append(out, "a certificate or private key block")
	}
	if masterAuthRe.MatchString(text) {
		out = append(out, "master_auth or a client credential attribute with a value")
	}
	if endpointRe.MatchString(text) {
		out = append(out, "a cluster endpoint")
	}
	for _, m := range projectPathRe.FindAllStringSubmatch(text, -1) {
		if m[1] != sanitizedProject {
			out = append(out, "project id "+m[1]+" in a resource path")
		}
	}
	for _, m := range projectAttrRe.FindAllStringSubmatch(text, -1) {
		if m[1] != sanitizedProject {
			out = append(out, "project id "+m[1])
		}
	}
	return out
}

func TestPlanFixturesAreSanitized(t *testing.T) {
	var scanned int
	for _, dir := range []string{"gke/fixtures", "../hack/fixtures/gke-extraction"} {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".json") {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			scanned++
			for _, f := range scanFixture(string(b)) {
				t.Errorf("%s holds %s: fixtures must be sanitized (regenerate with gen-state.py)", path, f)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	// A scan over nothing passes: the real fixtures exist, so insist on seeing them.
	if scanned < 4 {
		t.Fatalf("scanned %d fixture files; expected the real and synthetic states and maps", scanned)
	}
}

func TestPlanFixtureNamesAreNotGitignored(t *testing.T) {
	// .gitignore drops *.tfstate, *.tfstate.* and *.tfplan, so a fixture with that name would
	// never be committed and the gate would pass over nothing in CI.
	for _, dir := range []string{"gke/fixtures", "../hack/fixtures/gke-extraction"} {
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				n := d.Name()
				if strings.HasSuffix(n, ".tfstate") || strings.Contains(n, ".tfstate.") || strings.HasSuffix(n, ".tfplan") ||
					strings.HasSuffix(n, ".tfvars") || strings.HasSuffix(n, ".tfvars.json") || strings.HasSuffix(n, ".env") {
					t.Errorf("%s: fixture is named so that .gitignore would drop it; use *.state.json, *.plan.json, *.vars.json and fixture.conf", path)
				}
			}
			return nil
		})
	}
}

// Negative controls: the scan above must be able to fail.
func TestScanFixtureSeesWhatItIsFor(t *testing.T) {
	cases := map[string]string{
		"certificate":  "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----",
		"private key":  "-----BEGIN RSA PRIVATE KEY-----\nabc",
		"master_auth":  `{"master_auth": [{"cluster_ca_certificate": "x"}]}`,
		"endpoint":     `{"endpoint": "34.1.2.3"}`,
		"project path": `{"id": "projects/acme-prod-1234/locations/us-east4-b/clusters/c"}`,
		"project attr": `{"project": "acme-prod-1234"}`,
	}
	for name, text := range cases {
		if len(scanFixture(text)) == 0 {
			t.Errorf("%s was not detected", name)
		}
	}
	clean := `{"id": "projects/example-project/locations/us-central1-a/clusters/c", "project": "example-project"}`
	if f := scanFixture(clean); len(f) != 0 {
		t.Errorf("a sanitized fixture was flagged: %v", f)
	}
}
