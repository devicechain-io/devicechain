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
	for _, want := range []string{"main.tf", "variables.tf", "modules/nats/main.tf", "modules/postgres/main.tf"} {
		if !strings.Contains(files, want) {
			t.Errorf("OpenTofu assets missing %q", want)
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
