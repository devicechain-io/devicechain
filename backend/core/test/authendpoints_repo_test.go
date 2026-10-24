// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// internalAuthEndpoints are the user-management endpoints that exist only for the platform's own
// services: the service-token mint and the JWKS. They are served on an INTERNAL listener that no
// route reaches, so the public port answers 404 for them with no ingress in front.
//
// The paths are matched as bare substrings, not as quoted literals, so a URL assembled in a
// format string (fmt.Sprintf("http://%s:%d/auth/jwks", ...)) is found as well as a constant.
var internalAuthEndpoints = []string{"ServiceTokenPath", "JWKSPath", "/auth/jwks", "/auth/service-token"}

// internalAuthSites are the only non-test files allowed to name one of those endpoints, and what
// each is for. A new file naming one is a new caller (or a new route), and either has to be a
// decision somebody makes here rather than one a refactor makes by accident.
//
//	definition: declares the path constants.
//	server:     registers the handler, on the internal mux.
//	client:     dials it, and must dial it through UserManagementConfiguration.InternalAddr, on
//	            the same line, so a caller cannot build the public address for it.
var internalAuthSites = map[string]string{
	"core/auth/service_token.go":       "definition",
	"core/auth/fetch.go":               "client",
	"core/svcclient/client.go":         "client",
	"services/user-management/main.go": "server",
}

// TestEveryCallerOfTheInternalAuthEndpointsUsesTheInternalAddress is the caller-side half of
// moving the mint off the public port. The server-side half (the public mux answers 404) is
// pinned in user-management; this one pins that nothing in the tree still dials the public
// address for them, which would now be a 404 that surfaces as every service failing to start.
//
// It reads files outside this module, so -count=1 is load-bearing.
func TestEveryCallerOfTheInternalAuthEndpointsUsesTheInternalAddress(t *testing.T) {
	found, err := internalAuthReferences("../..")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, ref := range found {
		role, ok := internalAuthSites[ref.file]
		if !ok {
			t.Errorf("%s:%d names an internal-only auth endpoint (%s) but is not a known site.\n"+
				"These endpoints are served on user-management's internal listener only. A new caller must dial "+
				"config.UserManagementConfiguration.InternalAddr(); add the file to internalAuthSites once it does.\n    %s",
				ref.file, ref.line, ref.name, ref.text)
			continue
		}
		seen[ref.file] = true
		if role == "client" && !strings.Contains(ref.text, "InternalAddr()") && !ref.isDefinitionOnly() {
			t.Errorf("%s:%d dials an internal auth endpoint without UserManagementConfiguration.InternalAddr() on the same line.\n    %s",
				ref.file, ref.line, ref.text)
		}
	}
	// Both directions: a listed site that no longer names an endpoint is a stale entry, and the
	// next reader would take the list as an account of who calls what.
	for file := range internalAuthSites {
		if !seen[file] {
			t.Errorf("internalAuthSites lists %s but it names no internal auth endpoint; remove the entry (or the scan is not looking where it thinks it is)", file)
		}
	}
}

type internalAuthRef struct {
	file string
	line int
	name string
	text string
}

// isDefinitionOnly is true for a line that merely declares or documents a constant rather than
// dialing it (the const block in the definition file).
func (r internalAuthRef) isDefinitionOnly() bool {
	return strings.Contains(r.text, "Path = \"/auth/")
}

func internalAuthReferences(root string) ([]internalAuthRef, error) {
	var out []internalAuthRef
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", "testdata", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// This package's own guard names the endpoints on purpose.
		if strings.HasSuffix(filepath.ToSlash(path), "core/test/authendpoints_repo_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		for i, line := range strings.Split(string(raw), "\n") {
			trim := strings.TrimSpace(line)
			if strings.HasPrefix(trim, "//") {
				continue
			}
			for _, name := range internalAuthEndpoints {
				if strings.Contains(line, name) {
					out = append(out, internalAuthRef{file: rel, line: i + 1, name: name, text: trim})
					break
				}
			}
		}
		return nil
	})
	return out, err
}
