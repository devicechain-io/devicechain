// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// D2-6: CI must exercise the language floor dcctl ships. OpenTofu <1.10
// evaluates both sides of &&. Keep this coupled to Init's -upgrade guarantee:
// a lock file from an older init keeps old providers even after their pins move.
func TestOpenTofuVersionPolicy(t *testing.T) {
	repo := filepath.Join("..", "..", "..")
	read := func(path string) string {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	floor := regexp.MustCompile(`(?m)^\s*required_version\s*=\s*"([^"]+)"`)
	roots := 0
	err := filepath.WalkDir(filepath.Join(repo, "deploy"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".tf" {
			return nil
		}
		rel, err := filepath.Rel(repo, path)
		if err != nil {
			return err
		}
		for _, match := range floor.FindAllStringSubmatch(read(rel), -1) {
			roots++
			if match[1] != ">= 1.10" {
				t.Errorf("%s: required_version = %q; move all floors and CI pins together, want >= 1.10", rel, match[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if roots < 4 {
		t.Fatalf("found %d version constraints; expected at least cluster, instance, GKE and playground", roots)
	}

	workflows, err := filepath.Glob(filepath.Join(repo, ".github", "workflows", "*.y*ml"))
	if err != nil {
		t.Fatal(err)
	}
	pin := regexp.MustCompile(`(?m)^\s*tofu_version:\s*([^\s#]+)`)
	setups := 0
	for _, path := range workflows {
		source := read(filepath.ToSlash(filepath.Join(".github", "workflows", filepath.Base(path))))
		count := strings.Count(source, "uses: opentofu/setup-opentofu@")
		if count == 0 {
			continue
		}
		setups += count
		matches := pin.FindAllStringSubmatch(source, -1)
		if len(matches) != count {
			t.Errorf("%s: %d setup-opentofu actions but %d explicit pins", path, count, len(matches))
		}
		for _, match := range matches {
			if match[1] != "1.10.10" {
				t.Errorf("%s: tofu_version = %q, want newest stable 1.10.x (1.10.10)", path, match[1])
			}
		}
	}
	if setups < 3 {
		t.Fatalf("found %d setup-opentofu actions, expected at least CI, loadtest and upgrade", setups)
	}
	for _, path := range []string{
		"docs/docs/deployment/bootstrap.md",
		"docs/i18n/es/docusaurus-plugin-content-docs/current/deployment/bootstrap.md",
		"docs/i18n/zh-CN/docusaurus-plugin-content-docs/current/deployment/bootstrap.md",
	} {
		if !strings.Contains(read(path), "OpenTofu 1.10") {
			t.Errorf("%s must document the OpenTofu 1.10 floor", path)
		}
	}
}
