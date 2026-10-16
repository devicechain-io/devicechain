// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSharedCacheFixture writes files (relative path → source) under a fresh root. Each
// source spells the parameter as CACHE_PARAM, replaced here, so this file holds no literal
// of the kind the repository scan refuses.
func writeSharedCacheFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, src := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		src = strings.ReplaceAll(src, "CACHE_PARAM", sharedCacheParam)
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatalf("writing fixture: %v", err)
		}
	}
	return root
}

func scanSharedCache(t *testing.T, root string, owners ...string) (allowed, refused []sharedCacheLiteral) {
	t.Helper()
	allowed, refused, _, err := sharedCacheLiteralsUnder(root, owners)
	if err != nil {
		t.Fatalf("scanning fixture: %v", err)
	}
	return allowed, refused
}

func TestSharedCacheScanRefusesAFixtureDSN(t *testing.T) {
	for name, src := range map[string]string{
		"interpreted": "package p\nvar dsn = \"file:x?mode=memory&CACHE_PARAM\"\n",
		"raw":         "package p\nvar dsn = `file:x?mode=memory&CACHE_PARAM`\n",
		"in a call":   "package p\nfunc f(n string) string { return \"file:\" + n + \"?mode=memory&CACHE_PARAM\" }\n",
		"upper case":  "package p\nvar dsn = \"file:x?mode=memory&UPPER_PARAM\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			src = strings.ReplaceAll(src, "UPPER_PARAM", strings.ToUpper(sharedCacheParam))
			_, refused := scanSharedCache(t, writeSharedCacheFixture(t, map[string]string{"a/fixture_test.go": src}))
			if len(refused) != 1 || refused[0].File != "a/fixture_test.go" {
				t.Fatalf("refused = %+v, want exactly the one literal in a/fixture_test.go", refused)
			}
		})
	}
}

// The owner file's literal is reported as allowed, not refused — and is still FOUND, which
// is what the repository test leans on as its known positive.
func TestSharedCacheScanAllowsOnlyTheOwner(t *testing.T) {
	root := writeSharedCacheFixture(t, map[string]string{
		"own/helper.go":   "package own\nconst dsn = \"?mode=memory&CACHE_PARAM\"\n",
		"other/helper.go": "package other\nconst dsn = \"?mode=memory&CACHE_PARAM\"\n",
	})
	allowed, refused := scanSharedCache(t, root, "own/helper.go")
	if len(allowed) != 1 || allowed[0].File != "own/helper.go" {
		t.Fatalf("allowed = %+v, want the owner's one literal", allowed)
	}
	if len(refused) != 1 || refused[0].File != "other/helper.go" {
		t.Fatalf("refused = %+v, want the same literal in a non-owner file", refused)
	}
}

// The counterweight: what is NOT a shared-cache DSN literal must not be reported.
func TestSharedCacheScanIgnoresWhatIsNotADSN(t *testing.T) {
	root := writeSharedCacheFixture(t, map[string]string{
		"a/a.go": "package a\n// a comment may say CACHE_PARAM freely\n" +
			"var private = \"file:x?mode=memory\"\nvar bare = \":memory:\"\n",
	})
	allowed, refused := scanSharedCache(t, root)
	if len(allowed)+len(refused) != 0 {
		t.Fatalf("found %+v %+v in a file with no shared-cache literal", allowed, refused)
	}
}

func TestSharedCacheScanSkipsWhatTheGoToolSkips(t *testing.T) {
	root := writeSharedCacheFixture(t, map[string]string{
		"keep.go":              "package root\n",
		"_legacy/old.go":       "package old\nvar dsn = \"?CACHE_PARAM\"\n",
		"testdata/fixture.go":  "package fixture\nvar dsn = \"?CACHE_PARAM\"\n",
		".worktree/a/stale.go": "package a\nvar dsn = \"?CACHE_PARAM\"\n",
	})
	if _, refused := scanSharedCache(t, root); len(refused) != 0 {
		t.Fatalf("refused %+v from directories the go tool does not build", refused)
	}
}

func TestSharedCacheScanRefusesToReportCleanOverNothing(t *testing.T) {
	if _, _, _, err := sharedCacheLiteralsUnder(t.TempDir(), nil); err == nil {
		t.Fatal("a scan of a tree with no Go files reported clean instead of failing")
	}
}
