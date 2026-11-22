// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package assets

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The fleet roots are what a manager runs, so what they can be told to do and where they keep
// their state are held by tests on the files themselves rather than trusted to review. The roots
// live under deploy/fleet, outside the dcctl embed, so no fleet state file can ride into a binary.

var fleetRoots = []string{"gke-gcs", "gke-pg"}

func readFleet(t *testing.T, root, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("fleet", root, name))
	if err != nil {
		t.Fatalf("read fleet/%s/%s: %v", root, name, err)
	}
	return string(b)
}

// block returns the text between the braces that follow the first match of header, with nested
// braces balanced, or "" if there is none. It is a text scan, not an HCL parse: the files it reads
// are small and written to keep it honest, and a malformed one fails the tofu validation step first.
func block(text string, header *regexp.Regexp) string {
	loc := header.FindStringIndex(text)
	if loc == nil {
		return ""
	}
	i := strings.Index(text[loc[1]-1:], "{")
	if i < 0 {
		return ""
	}
	start := loc[1] - 1 + i
	depth := 0
	for j := start; j < len(text); j++ {
		switch text[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[start+1 : j]
			}
		}
	}
	return ""
}

func names(re *regexp.Regexp, text string) []string {
	var out []string
	for _, m := range re.FindAllStringSubmatch(text, -1) {
		out = append(out, m[1])
	}
	sort.Strings(out)
	return out
}

func TestFleetRootsExistAndAreTheOnesThisTestKnows(t *testing.T) {
	entries, err := os.ReadDir("fleet")
	if err != nil {
		t.Fatalf("deploy/fleet is missing: %v", err)
	}
	var found []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		// A directory with no .tf file is not a root (fleet/premises is the Go package that
		// reads the recorded premises), so it is not for this test to review.
		if tf, _ := filepath.Glob(filepath.Join("fleet", e.Name(), "*.tf")); len(tf) > 0 {
			found = append(found, e.Name())
		}
	}
	sort.Strings(found)
	if strings.Join(found, ",") != strings.Join(fleetRoots, ",") {
		t.Fatalf("deploy/fleet holds %v, this test knows %v: a new fleet root needs its own review here", found, fleetRoots)
	}
}

// The root's provider set is exactly {google}: no kubernetes, helm or any in-cluster provider.
func TestFleetRootsDeclareExactlyTheGoogleProvider(t *testing.T) {
	pin := ""
	for _, r := range fleetRoots {
		versions := readFleet(t, r, "versions.tf")
		rp := block(versions, regexp.MustCompile(`required_providers\s*\{`))
		if rp == "" {
			t.Fatalf("%s: no required_providers", r)
		}
		got := names(regexp.MustCompile(`(?m)^\s*([a-z0-9_]+)\s*=\s*\{`), rp)
		if strings.Join(got, ",") != "google" {
			t.Errorf("%s: required_providers is %v, want exactly [google]", r, got)
		}
		m := regexp.MustCompile(`version\s*=\s*"([^"]+)"`).FindStringSubmatch(rp)
		if m == nil || !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(m[1]) {
			t.Errorf("%s: the google provider must be pinned EXACTLY (it is what the premise record binds to), got %v", r, m)
			continue
		}
		if pin != "" && pin != m[1] {
			t.Errorf("%s pins %s but another fleet root pins %s: the two roots differ only in the backend", r, m[1], pin)
		}
		pin = m[1]
		if !strings.Contains(versions, `required_version = ">= 1.10"`) {
			t.Errorf("%s: required_version must be the literal \">= 1.10\"", r)
		}
		for _, f := range []string{"main.tf", "providers.tf"} {
			if regexp.MustCompile(`(?m)^\s*provider\s+"(kubernetes|helm|kubectl)"`).MatchString(readFleet(t, r, f)) {
				t.Errorf("%s/%s configures an in-cluster provider", r, f)
			}
		}
	}
}

// The closed input surface: adding a variable is a reviewed act, because each one is a way for the
// caller to change what the root does.
func TestFleetRootVariablesAreAClosedList(t *testing.T) {
	want := "cluster_name,cluster_uid,data_class,database_machine_type,database_node_count,location,network,ownership_nonce,project_id,services_machine_type,services_node_count,spot,subnetwork"
	for _, r := range fleetRoots {
		got := names(regexp.MustCompile(`(?m)^variable\s+"([a-z0-9_]+)"`), readFleet(t, r, "variables.tf"))
		if strings.Join(got, ",") != want {
			t.Errorf("%s: variables are %v\nwant %s", r, got, want)
		}
	}
}

// Trust-affecting attributes are literals in main.tf, not variables.
func TestFleetRootTrustAttributesAreLiteralsNotVariables(t *testing.T) {
	for _, r := range fleetRoots {
		vars := readFleet(t, r, "variables.tf")
		main := readFleet(t, r, "main.tf")
		for _, banned := range []string{"node_service_account", "workload_pool", "release_channel", "labels", "wait_for_cluster_operations", "ownership_labels"} {
			if regexp.MustCompile(`variable\s+"` + banned + `"`).MatchString(vars) {
				t.Errorf("%s: %s must be a literal, not a variable", r, banned)
			}
			if regexp.MustCompile(`(?m)^\s*` + banned + `\s*=\s*var\.`).MatchString(main) {
				t.Errorf("%s: main.tf wires %s from a variable", r, banned)
			}
		}
		for attr, want := range map[string]string{
			"node_service_account":        `format("dcf-node@%s.iam.gserviceaccount.com", var.project_id)`,
			"workload_pool":               `format("%s.svc.id.goog", var.project_id)`,
			"wait_for_cluster_operations": `false`,
		} {
			if !regexp.MustCompile(`(?m)^\s*` + attr + `\s*=\s*` + regexp.QuoteMeta(want) + `\s*$`).MatchString(main) {
				t.Errorf("%s: main.tf does not set %s = %s as a literal", r, attr, want)
			}
		}
	}
}

// The root exports identity only: no credential material and no kube context.
func TestFleetRootOutputsAreIdentityOnly(t *testing.T) {
	want := "cluster_id,cluster_name,location,pool_names"
	for _, r := range fleetRoots {
		got := names(regexp.MustCompile(`(?m)^output\s+"([a-z0-9_]+)"`), readFleet(t, r, "outputs.tf"))
		if strings.Join(got, ",") != want {
			t.Errorf("%s: outputs are %v, want %s", r, got, want)
		}
		for _, banned := range []string{"get_credentials_command", "kube_context", "get-credentials", "kubeconfig", "token"} {
			if strings.Contains(readFleet(t, r, "outputs.tf"), banned) {
				t.Errorf("%s: outputs mention %q", r, banned)
			}
		}
	}
}

// State and plan encryption are enforced in the files, so that a key supplied by CI cannot hide a
// missing `enforced`.
func TestFleetRootsEnforceStateAndPlanEncryption(t *testing.T) {
	for _, r := range fleetRoots {
		enc := block(readFleet(t, r, "encryption.tf"), regexp.MustCompile(`encryption\s*\{`))
		if enc == "" {
			t.Fatalf("%s: no encryption block", r)
		}
		for _, section := range []string{"state", "plan"} {
			body := block(enc, regexp.MustCompile(`(?m)^\s*`+section+`\s*\{`))
			if !regexp.MustCompile(`(?m)^\s*enforced\s*=\s*true\s*$`).MatchString(body) {
				t.Errorf("%s: encryption.%s does not say enforced = true", r, section)
			}
		}
		// No key and no passphrase in a file: the key provider arrives through TF_ENCRYPTION.
		for _, f := range []string{"encryption.tf", "versions.tf", "backend.tf", "main.tf"} {
			if strings.Contains(readFleet(t, r, f), "key_provider") && f != "encryption.tf" {
				t.Errorf("%s/%s configures a key provider; keys are supplied through TF_ENCRYPTION", r, f)
			}
		}
		if strings.Contains(readFleet(t, r, "encryption.tf"), "passphrase") {
			t.Errorf("%s: encryption.tf holds a passphrase", r)
		}
	}
}

// A root has one backend, declared, and never the implicit local one: a Job pod would silently keep
// its state on its own disk.
func TestFleetRootsDeclareARealBackend(t *testing.T) {
	backends := map[string]string{"gke-gcs": "gcs", "gke-pg": "pg"}
	for _, r := range fleetRoots {
		var all strings.Builder
		entries, _ := os.ReadDir(filepath.Join("fleet", r))
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".tf") {
				all.WriteString(readFleet(t, r, e.Name()))
			}
		}
		got := names(regexp.MustCompile(`(?m)^\s*backend\s+"([a-z]+)"`), all.String())
		if len(got) != 1 || got[0] != backends[r] {
			t.Errorf("%s: backends are %v, want exactly [%s]", r, got, backends[r])
		}
	}
	// pg: the schema, tables and indexes are pre-created, so all three creation steps are off.
	pg := block(readFleet(t, "gke-pg", "backend.tf"), regexp.MustCompile(`backend\s+"pg"\s*\{`))
	for _, s := range []string{"skip_schema_creation", "skip_table_creation", "skip_index_creation"} {
		if !regexp.MustCompile(`(?m)^\s*` + s + `\s*=\s*true\s*$`).MatchString(pg) {
			t.Errorf("gke-pg: %s must be true: a per-run role owns nothing and cannot create", s)
		}
	}
	if strings.Contains(readFleet(t, "gke-pg", "backend.tf"), "conn_str") {
		t.Error("gke-pg: the connection string must come from PG_CONN_STR, never a file")
	}
}

// The two roots differ ONLY in the backend: everything else is byte-identical, so that what was
// tested on one is what runs on the other.
func TestFleetRootsDifferOnlyInTheBackend(t *testing.T) {
	a, b := fleetRoots[0], fleetRoots[1]
	files := func(root string) map[string]string {
		out := map[string]string{}
		_ = filepath.WalkDir(filepath.Join("fleet", root), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(filepath.Join("fleet", root), path)
			if strings.Contains(rel, ".terraform") {
				return nil
			}
			bs, _ := os.ReadFile(path)
			out[filepath.ToSlash(rel)] = string(bs)
			return nil
		})
		return out
	}
	fa, fb := files(a), files(b)
	if len(fa) < 6 {
		t.Fatalf("only %d files in %s: the comparison would pass over nothing", len(fa), a)
	}
	for name, text := range fa {
		if name == "backend.tf" {
			continue
		}
		if fb[name] != text {
			t.Errorf("%s differs between %s and %s: the roots may differ only in backend.tf", name, a, b)
		}
	}
	for name := range fb {
		if _, ok := fa[name]; !ok {
			t.Errorf("%s exists only in %s", name, b)
		}
	}
	if fa["backend.tf"] == fb["backend.tf"] {
		t.Error("the two roots have the same backend.tf: they should differ in exactly that")
	}
}

// No fleet-root file may name a fixture-like secret: the plan gate's states are sanitized the same way.
func TestFleetRootsHoldNoCredentialMaterial(t *testing.T) {
	for _, r := range fleetRoots {
		entries, _ := os.ReadDir(filepath.Join("fleet", r))
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			for _, f := range scanFixture(readFleet(t, r, e.Name())) {
				t.Errorf("fleet/%s/%s holds %s", r, e.Name(), f)
			}
		}
	}
}

// Negative controls for the scanner used above.
func TestBlockScannerSeesWhatItIsFor(t *testing.T) {
	text := "terraform {\n  encryption {\n    state {\n      enforced = true\n    }\n    plan {\n    }\n  }\n}\n"
	enc := block(text, regexp.MustCompile(`encryption\s*\{`))
	if !regexp.MustCompile(`enforced\s*=\s*true`).MatchString(block(enc, regexp.MustCompile(`state\s*\{`))) {
		t.Error("enforced under state was not found")
	}
	if regexp.MustCompile(`enforced\s*=\s*true`).MatchString(block(enc, regexp.MustCompile(`plan\s*\{`))) {
		t.Error("enforced was attributed to plan, which has none")
	}
	if block("no braces here", regexp.MustCompile(`x`)) != "" {
		t.Error("a missing block returned text")
	}
}
