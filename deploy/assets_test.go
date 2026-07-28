// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package assets

import (
	"io/fs"
	"strings"
	"testing"
)

// collect returns every file path (not directories) in an fs.FS.
func collect(t *testing.T, fsys fs.FS) []string {
	t.Helper()
	var out []string
	if err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			out = append(out, p)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestNoSecretsEmbedded guards the precise embed globs: the local terraform
// state and tfvars hold secrets/per-machine values and must never ship in a
// binary.
func TestNoSecretsEmbedded(t *testing.T) {
	all := strings.Join(append(collect(t, OpenTofu()), collect(t, HelmChart())...), " ")
	for _, forbidden := range []string{"tfstate", "tfvars", ".terraform"} {
		if strings.Contains(all, forbidden) {
			t.Errorf("embedded asset matches %q — secret/state leak into the binary", forbidden)
		}
	}
}

// TestOpenTofuComplete checks the infra root and a module are present.
func TestOpenTofuComplete(t *testing.T) {
	files := strings.Join(collect(t, OpenTofu()), " ")
	for _, want := range []string{"main.tf", "variables.tf", "modules/nats/main.tf", "modules/cnpg-cluster/main.tf"} {
		if !strings.Contains(files, want) {
			t.Errorf("OpenTofu assets missing %q", want)
		}
	}
}

// TestOpenTofuEmbedsNonTerraformModuleFiles pins something the embed globs make
// easy to break and impossible to notice.
//
// Every file in the OpenTofu tree used to be a .tf, so "did the modules ship?"
// and "did the .tf files ship?" were the same question. The cnpg-cluster module
// (ADR-020 A2.3) breaks that: it carries a Helm chart, because a Cluster custom
// resource cannot be a kubernetes_manifest without its CRD existing at plan
// time. Those are .yaml files inside modules/.
//
// The `all:` prefix on the modules glob is what carries them. Narrow that glob
// to *.tf — an entirely reasonable-looking tidy-up — and `go build` still
// succeeds, every existing test still passes, and dcctl bootstrap fails on a
// USER's machine with a Helm "chart not found" error, because the chart exists
// in the repo and not in the binary. Nothing in a source checkout reproduces it.
func TestOpenTofuEmbedsNonTerraformModuleFiles(t *testing.T) {
	files := strings.Join(collect(t, OpenTofu()), " ")
	for _, want := range []string{
		"modules/cnpg-cluster/chart/Chart.yaml",
		"modules/cnpg-cluster/chart/values.yaml",
		"modules/cnpg-cluster/chart/templates/cluster.yaml",
	} {
		if !strings.Contains(files, want) {
			t.Errorf("OpenTofu assets missing %q — the cnpg-cluster chart did not survive go:embed, "+
				"so `dcctl bootstrap` would fail with a Helm chart-not-found error against a repo where the file is present", want)
		}
	}
}

// TestHelmChartComplete checks the chart is whole — in particular that the
// _-prefixed named-template library survived go:embed (which skips _-prefixed
// files unless the all: prefix is used).
func TestHelmChartComplete(t *testing.T) {
	files := strings.Join(collect(t, HelmChart()), " ")
	for _, want := range []string{"Chart.yaml", "values.yaml", "templates/_helpers.tpl", "templates/deployment.yaml"} {
		if !strings.Contains(files, want) {
			t.Errorf("Helm chart assets missing %q", want)
		}
	}
}
