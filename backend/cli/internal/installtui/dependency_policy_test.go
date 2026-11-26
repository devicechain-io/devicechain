// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestDependencyPins(t *testing.T) {
	b, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"github.com/charmbracelet/bubbles":       "v0.21.1",
		"github.com/charmbracelet/bubbletea":     "v1.3.10",
		"github.com/charmbracelet/lipgloss":      "v1.1.0",
		"github.com/charmbracelet/x/exp/teatest": "v0.0.0-20260713092006-0d683c34c74b",
		"github.com/muesli/termenv":              "v0.16.0",
		"golang.org/x/term":                      "v0.48.0",
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if strings.HasPrefix(fields[0], "github.com/charmbracelet/huh") || strings.HasPrefix(fields[0], "charm.land/") {
			t.Fatalf("unapproved UI dependency: %s", fields[0])
		}
		if pin, ok := want[fields[0]]; ok {
			if fields[1] != pin {
				t.Errorf("%s = %s, want %s", fields[0], fields[1], pin)
			}
			delete(want, fields[0])
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing pins: %v", want)
	}
}

func TestReleaseArchivesCarryNotices(t *testing.T) {
	b, err := os.ReadFile("../../.goreleaser.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Archives []struct {
			Files []struct {
				Src, Dst    string
				StripParent bool `json:"strip_parent"`
			}
		}
	}
	if err := yaml.Unmarshal(b, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Archives) == 0 {
		t.Fatal("no release archives")
	}
	for _, archive := range config.Archives {
		for _, name := range []string{"LICENSE", "NOTICE"} {
			found := false
			for _, file := range archive.Files {
				if file.Src == "../../"+name && file.Dst == "." && file.StripParent {
					found = true
					if _, err := os.Stat(filepath.Join("../..", file.Src)); err != nil {
						t.Fatal(err)
					}
				}
			}
			if !found {
				t.Errorf("archive omits repository %s", name)
			}
		}
	}
}
