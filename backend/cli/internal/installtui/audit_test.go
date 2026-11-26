// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type resolvedModule struct {
	Path, Version, Dir, GoMod, Sum, GoModSum string
}

func TestTeatestIsTestOnly(t *testing.T) {
	err := filepath.WalkDir("../..", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != "../.." && (d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".") || strings.HasPrefix(d.Name(), "_")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			name, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			if name == "github.com/charmbracelet/x/exp/teatest" || strings.HasPrefix(name, "github.com/charmbracelet/x/exp/teatest/") {
				t.Errorf("test-only teatest imported by %s", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func auditCommand(t *testing.T, args ...string) []byte {
	t.Helper()
	// Listing the whole graph may add hashes that tidy deliberately omits.
	// Use isolated metadata so inspecting test/Windows requirements never
	// dirties the real module. Make local replacements absolute first.
	module, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	mod, err := os.ReadFile(filepath.Join(module, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(mod), "\n")
	for i, line := range lines {
		fields := strings.Fields(line)
		for j, field := range fields {
			if field == "=>" && j+1 < len(fields) && strings.HasPrefix(fields[j+1], ".") {
				absolute := filepath.Join(module, fields[j+1])
				lines[i] = strings.Replace(line, fields[j+1], strconv.Quote(filepath.ToSlash(absolute)), 1)
			}
		}
	}
	tempMod := filepath.Join(t.TempDir(), "audit.mod")
	if err := os.WriteFile(tempMod, []byte(strings.Join(lines, "\n")), 0600); err != nil {
		t.Fatal(err)
	}
	sum, err := os.ReadFile(filepath.Join(module, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(strings.TrimSuffix(tempMod, ".mod")+".sum", sum, 0600); err != nil {
		t.Fatal(err)
	}
	command := 1
	if args[0] == "mod" {
		command = 2
	}
	isolated := append([]string{}, args[:command]...)
	isolated = append(isolated, "-modfile="+tempMod)
	isolated = append(isolated, args[command:]...)
	cmd := exec.Command("go", isolated...)
	cmd.Dir = module
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go %v: %v\n%s", args, err, out)
	}
	return out
}

func resolvedLicenseGraph(t *testing.T, workspace bool) map[string]resolvedModule {
	t.Helper()
	data := []byte(nil)
	if workspace {
		module, err := filepath.Abs("../..")
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("go", "list", "-e", "-m", "-json", "all")
		cmd.Dir = module
		cmd.Env = append(os.Environ(), "GOWORK="+filepath.Join(module, "../../go.work"), "GOFLAGS=-mod=readonly")
		data, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("workspace build list: %v\n%s", err, data)
		}
	} else {
		data = auditCommand(t, "list", "-m", "-json", "all")
	}
	selected := map[string]resolvedModule{}
	dec := json.NewDecoder(bytes.NewReader(data))
	for {
		var m resolvedModule
		if err := dec.Decode(&m); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		selected[m.Path] = m
	}
	queue := []string{
		"charm.land/bubbles/v2",
		"charm.land/bubbletea/v2",
		"charm.land/lipgloss/v2",
		"github.com/charmbracelet/colorprofile",
		"github.com/charmbracelet/x/exp/teatest/v2",
		"golang.org/x/term",
	}
	closure := map[string]resolvedModule{}
	seen := map[string]bool{}
	for len(queue) != 0 {
		path := queue[0]
		queue = queue[1:]
		if seen[path] {
			continue
		}
		seen[path] = true
		m, ok := selected[path]
		if !ok {
			// Requirements omitted by Go's graph pruning are not in this
			// build list. Follow every requirement that is actually selected.
			continue
		}
		if m.Dir == "" || m.GoMod == "" {
			// A fresh module cache (a CI runner) holds only what its builds needed;
			// test-only and other-platform requirements are listed but not
			// downloaded. Fetch the exact selected version; go verifies it against
			// go.sum and the checksum database, so the audit stays deterministic.
			m = downloadAuditedModule(t, path, m.Version)
		}
		closure[path+"@"+m.Version] = m
		// Parse the selected module's own requirements with Go's parser.
		// A pruned graph can omit upstream test/tool requirements; loading
		// unrelated historical versions is also unnecessary here.
		cmd := exec.Command("go", "mod", "edit", "-json", m.GoMod)
		cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("requirements for %s: %v\n%s", path, err, out)
		}
		var metadata struct {
			Require []struct{ Path string }
		}
		if err := json.Unmarshal(out, &metadata); err != nil {
			t.Fatal(err)
		}
		for _, requirement := range metadata.Require {
			queue = append(queue, requirement.Path)
		}
	}
	return closure
}

// downloadAuditedModule fetches path@version into the module cache and returns
// where its sources and go.mod are.
func downloadAuditedModule(t *testing.T, path, version string) resolvedModule {
	t.Helper()
	cmd := exec.Command("go", "mod", "download", "-json", path+"@"+version)
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=")
	out, err := cmd.Output()
	var got struct {
		Path, Version, Dir, GoMod, Sum, GoModSum, Error string
	}
	if jerr := json.Unmarshal(out, &got); err != nil || jerr != nil || got.Error != "" || got.Dir == "" || got.GoMod == "" {
		t.Fatalf("audited dependency sources unavailable: %s@%s: %v %s", path, version, err, got.Error)
	}
	return resolvedModule{Path: got.Path, Version: got.Version, Dir: got.Dir, GoMod: got.GoMod, Sum: got.Sum, GoModSum: got.GoModSum}
}

func TestResolvedLicensesAndNotices(t *testing.T) {
	// Audit standalone and workspace MVS selections, including tests and all
	// platform requirements. Inspecting either graph leaves CLI metadata alone.
	closure := resolvedLicenseGraph(t, false)
	workspace := resolvedLicenseGraph(t, true)
	verifyWorkspaceSums(t, workspace)
	for key, module := range workspace {
		closure[key] = module
	}
	b, err := os.ReadFile("testdata/dependency-licenses.tsv")
	if err != nil {
		t.Fatal(err)
	}
	reader := csv.NewReader(bytes.NewReader(b))
	reader.Comma, reader.Comment = '	', '#'
	rows, err := reader.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 2 || strings.Join(rows[0], "	") != "module	version	license	file	sha256" {
		t.Fatal("missing audit inventory header")
	}
	notice, err := os.ReadFile("../../../../NOTICE")
	if err != nil {
		t.Fatal(err)
	}
	audited := map[string]bool{}
	for _, row := range rows[1:] {
		if len(row) != 5 {
			t.Fatalf("invalid inventory row: %v", row)
		}
		key := row[0] + "@" + row[1]
		m, ok := closure[key]
		if !ok {
			t.Fatalf("stale audit entry: %v", row)
		}
		source, err := os.ReadFile(filepath.Join(m.Dir, row[3]))
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(source)
		if hex.EncodeToString(hash[:]) != row[4] {
			t.Fatalf("license changed: %s %s", row[0], row[3])
		}
		if !bytes.Contains(notice, bytes.TrimSpace(source)) {
			t.Fatalf("NOTICE omits upstream text: %s %s", row[0], row[3])
		}
		audited[key] = true
	}
	for path := range closure {
		if !audited[path] {
			t.Errorf("dependency has no reviewed license: %s", path)
		}
	}
	// teatest's golden diff must work on Windows without an external diff
	// executable. Check both the harness and its in-process diff providers.
	for _, m := range closure {
		path := m.Path
		switch path {
		case "github.com/charmbracelet/x/exp/teatest/v2", "github.com/charmbracelet/x/exp/golden", "github.com/aymanbagabas/go-udiff":
		default:
			continue
		}
		files, err := os.ReadDir(m.Dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			if file.IsDir() || !strings.HasSuffix(file.Name(), ".go") || strings.HasSuffix(file.Name(), "_test.go") {
				continue
			}
			f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(m.Dir, file.Name()), nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, imp := range f.Imports {
				name, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					t.Fatal(err)
				}
				if name == "os/exec" {
					t.Fatalf("%s invokes external processes; re-audit the Windows golden guarantee", path)
				}
			}
		}
	}
}

func verifyWorkspaceSums(t *testing.T, modules map[string]resolvedModule) {
	t.Helper()
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "work", "edit", "-json", filepath.Join(root, "go.work"))
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("workspace metadata: %v\n%s", err, out)
	}
	var work struct {
		Use []struct{ DiskPath string }
	}
	if err := json.Unmarshal(out, &work); err != nil {
		t.Fatal(err)
	}
	files := []string{filepath.Join(root, "go.work.sum")}
	for _, use := range work.Use {
		files = append(files, filepath.Join(root, use.DiskPath, "go.sum"))
	}
	covered := map[string]bool{}
	for _, file := range files {
		b, err := os.ReadFile(file)
		if os.IsNotExist(err) {
			continue // Standard-library-only workspace modules need no go.sum.
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			covered[strings.TrimSpace(line)] = true
		}
	}
	for _, module := range modules {
		if module.Sum == "" || module.GoModSum == "" {
			// The build list omits hashes for unused upstream tools/tests.
			// Read their verified cache records outside the real module.
			cmd := exec.Command("go", "mod", "download", "-json", module.Path+"@"+module.Version)
			cmd.Dir = t.TempDir()
			cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("cached hashes for %s: %v\n%s", module.Path, err, out)
			}
			if err := json.Unmarshal(out, &module); err != nil {
				t.Fatal(err)
			}
			if module.Sum == "" || module.GoModSum == "" {
				t.Fatalf("unverified module hashes: %s@%s", module.Path, module.Version)
			}
		}
		for _, line := range []string{
			module.Path + " " + module.Version + " " + module.Sum,
			module.Path + " " + module.Version + "/go.mod " + module.GoModSum,
		} {
			if !covered[line] {
				t.Errorf("workspace checksum coverage incomplete: %s", line)
			}
		}
	}
}
