// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package export runs a small exporter INSIDE a checked-out tree's own package, so the
// gate reads a release's real command tree and real topic builders rather than a
// description of them.
//
// The exporter is a test file the tool carries (inject/). It is handed to `go test`
// through -overlay, which makes the compiler see it without anything being written
// into the tree: the working tree stays exactly as the developer left it, and the
// baseline (a `git archive` of a release tag, whose sources predate this tool) needs no
// cooperation from the release.
package export

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

//go:embed inject/cli_export.go.txt
var cliExporter []byte

//go:embed inject/topics_export.go.txt
var topicsExporter []byte

// CommandTree writes the dcctl command tree of the repository tree at root to out.
func CommandTree(root, out string) error {
	return run(root, "backend/cli", "cmd", "zz_apicompat_export_test.go", cliExporter, "TestAPICompatExportCommandTree", out)
}

// DeviceTopics writes the topic layout one device is granted by the repository tree at
// root to out.
func DeviceTopics(root, out string) error {
	return run(root, "backend/core", "natsauth", "zz_apicompat_export_test.go", topicsExporter, "TestAPICompatExportTopics", out)
}

// run injects src as <module>/<pkg>/<name> via -overlay, runs the named test there and
// checks the exporter really ran and wrote out.
func run(root, module, pkg, name string, src []byte, test, out string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	out, err = filepath.Abs(out)
	if err != nil {
		return err
	}
	moduleDir := filepath.Join(root, filepath.FromSlash(module))
	if _, err := os.Stat(filepath.Join(moduleDir, "go.mod")); err != nil {
		return fmt.Errorf("%s has no %s module to export from: %w", root, module, err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.work")); err != nil {
		return fmt.Errorf("%s has no go.work; the export builds through the workspace: %w", root, err)
	}
	scratch, err := os.MkdirTemp("", "apicompat-export-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)

	injected := filepath.Join(scratch, name)
	if err := os.WriteFile(injected, src, 0o644); err != nil {
		return err
	}
	overlay, err := json.Marshal(map[string]map[string]string{
		"Replace": {filepath.Join(moduleDir, filepath.FromSlash(pkg), name): injected},
	})
	if err != nil {
		return err
	}
	overlayFile := filepath.Join(scratch, "overlay.json")
	if err := os.WriteFile(overlayFile, overlay, 0o644); err != nil {
		return err
	}

	_ = os.Remove(out)
	cmd := exec.Command("go", "test", "-count=1", "-overlay", overlayFile, "-run", "^"+test+"$", "-v", "./"+pkg)
	cmd.Dir = moduleDir
	cmd.Env = append(os.Environ(),
		"GOWORK="+filepath.Join(root, "go.work"),
		"DC_APICOMPAT_OUT="+out,
	)
	b, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go test %s/%s in %s failed:\n%s", module, pkg, root, tail(string(b), 4000))
	}
	// A skipped test passes. Without this check an exporter that did not run (an
	// environment that dropped the variable, a renamed test) would leave a stale or
	// missing file and read as success.
	if !strings.Contains(string(b), "--- PASS: "+test) {
		return fmt.Errorf("the exporter %s did not run in %s:\n%s", test, root, tail(string(b), 4000))
	}
	if st, err := os.Stat(out); err != nil || st.Size() == 0 {
		return fmt.Errorf("the exporter %s ran but wrote nothing to %s", test, out)
	}
	return nil
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}
