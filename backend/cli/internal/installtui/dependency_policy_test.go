// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
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
		"charm.land/bubbles/v2":                     "v2.2.1",
		"charm.land/bubbletea/v2":                   "v2.1.1",
		"charm.land/lipgloss/v2":                    "v2.1.0",
		"github.com/charmbracelet/colorprofile":     "v0.4.3",
		"github.com/charmbracelet/x/exp/teatest/v2": "v2.0.0-20260311145557-c83711a11ffa",
		"golang.org/x/term":                         "v0.48.0",
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if banned, why := bannedUIModule(fields[0]); banned {
			t.Fatalf("unapproved UI dependency %s: %s", fields[0], why)
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

// bannedUIModule names the terminal libraries dcctl must not depend on at all.
//
// 🔴 THE CHARM v1 FAMILY QUERIES THE TERMINAL FROM A PACKAGE INITIALIZER. Bubble Tea v1's
// tea_init.go calls lipgloss.HasDarkBackground(), and termenv answers it by switching the
// terminal out of canonical mode with echo off, writing an OSC 11 query and a cursor report
// to stdout, and waiting for a reply. An initializer runs for every dcctl command, before any
// flag is read, so linking it in makes `dcctl --help` and `dcctl install --ui=plain` touch
// the terminal — and stall for five seconds on one that does not answer. Every v1 release
// carries it; the v2 family does not.
func bannedUIModule(path string) (bool, string) {
	for _, b := range []struct{ prefix, why string }{
		{"github.com/charmbracelet/bubbletea", "Charm v1: queries the terminal at init"},
		{"github.com/charmbracelet/lipgloss", "Charm v1: the renderer v1 Bubble Tea queries at init"},
		{"github.com/charmbracelet/bubbles", "Charm v1: pulls in v1 Bubble Tea"},
		{"github.com/charmbracelet/x/exp/teatest", "Charm v1 test harness: pulls in v1 Bubble Tea"},
		{"github.com/muesli/termenv", "the v1 colour library whose background query v1 Bubble Tea runs at init"},
		{"github.com/charmbracelet/huh", "not adopted"},
		{"charm.land/huh", "not adopted"},
	} {
		if path == b.prefix || strings.HasPrefix(path, b.prefix+"/") {
			if strings.HasPrefix(path, "github.com/charmbracelet/x/exp/teatest/v2") {
				continue
			}
			return true, b.why
		}
	}
	return false, ""
}

// bannedUIPackage names packages that must never be linked, though their module is allowed.
var bannedUIPackages = map[string]string{
	// lipgloss/v2/compat's initializer asks the terminal for its background colour.
	"charm.land/lipgloss/v2/compat": "queries the terminal at init",
}

// reviewedInitializers are the package initializers in the terminal stack, each read and
// found not to touch a terminal. A new one fails TestNoTerminalQueryAtInit until reviewed:
// an initializer runs for every dcctl command, before any flag is read.
var reviewedInitializers = map[string]string{
	"github.com/charmbracelet/x/ansi:method.go":             "reads RUNEWIDTH_EASTASIAN",
	"github.com/charmbracelet/x/ansi:wcwidth.go":            "fills a width table",
	"github.com/charmbracelet/x/termios:syscalls_linux.go":  "fills termios flag tables; no syscall",
	"github.com/charmbracelet/x/termios:syscalls_darwin.go": "fills a termios flag table; no syscall",
	"github.com/charmbracelet/ultraviolet:decoder.go":       "fills a key table",
	"github.com/mattn/go-runewidth:runewidth.go":            "fills width tables; reads RUNEWIDTH_EASTASIAN and the locale",
	"github.com/mattn/go-isatty:isatty_windows.go":          "resolves two Windows API procedures",
	"golang.org/x/sys/cpu:cpu.go":                           "reads CPU features",
	"golang.org/x/sys/cpu:runtime_auxv_go121.go":            "binds the runtime's auxv accessor",
}

// TestNoTerminalQueryAtInit lists every package the dcctl binary and the terminal UI link,
// on each release platform, and refuses a banned package or an unreviewed initializer in
// the terminal stack.
func TestNoTerminalQueryAtInit(t *testing.T) {
	module, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	roots := []string{".", "./internal/installtui/...",
		"charm.land/bubbletea/v2", "charm.land/lipgloss/v2",
		"charm.land/bubbles/v2/progress", "charm.land/bubbles/v2/textinput", "charm.land/bubbles/v2/viewport",
		"charm.land/bubbles/v2/spinner", "charm.land/bubbles/v2/key", "charm.land/bubbles/v2/help"}
	found := map[string]bool{}
	for _, goos := range []string{"linux", "darwin", "windows"} {
		cmd := exec.Command("go", append([]string{"list", "-deps", "-f",
			"{{if not .Standard}}{{.ImportPath}}\t{{.Dir}}\t{{join .GoFiles \"\\t\"}}{{end}}"}, roots...)...)
		cmd.Dir = module
		cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH=amd64", "CGO_ENABLED=0")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("listing the %s link graph: %v", goos, err)
		}
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Split(strings.TrimSpace(line), "\t")
			if len(fields) < 2 {
				continue
			}
			pkg, dir := fields[0], fields[1]
			if why, ok := bannedUIPackages[pkg]; ok {
				t.Errorf("%s links %s, which %s", goos, pkg, why)
			}
			if banned, why := bannedUIModule(pkg); banned {
				t.Errorf("%s links %s: %s", goos, pkg, why)
			}
			if !terminalStack(pkg) {
				continue
			}
			for _, file := range fields[2:] {
				f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, file), nil, parser.SkipObjectResolution)
				if err != nil {
					t.Fatal(err)
				}
				for _, decl := range f.Decls {
					if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == "init" {
						found[pkg+":"+file] = true
					}
				}
			}
		}
	}
	for init := range found {
		if _, ok := reviewedInitializers[init]; !ok {
			t.Errorf("unreviewed package initializer %s: read it, and add it to reviewedInitializers only if it never touches a terminal", init)
		}
	}
	for init := range reviewedInitializers {
		if !found[init] {
			t.Errorf("reviewed initializer %s is no longer linked; drop it", init)
		}
	}
}

// terminalStack reports whether pkg belongs to the terminal UI's libraries.
func terminalStack(pkg string) bool {
	for _, p := range []string{"charm.land/", "github.com/charmbracelet/", "github.com/muesli/",
		"github.com/lucasb-eyer/", "github.com/rivo/", "github.com/mattn/", "github.com/clipperhouse/",
		"github.com/aymanbagabas/", "github.com/xo/", "golang.org/x/term", "golang.org/x/sys"} {
		if strings.HasPrefix(pkg, p) {
			return true
		}
	}
	return false
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
