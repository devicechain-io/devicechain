// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/release"
)

// 🔴 THE THIRD SOURCE HAS TO BE CONSULTED, NOT MERELY CORRECT. Cutting the body of
// releasedInstances down to `return nil, nil` survived every other test in this package:
// deviceChainReleases is exercised directly and passes, the source list still names three
// sources, and the one that answers for a pre-v0.17.0 cluster silently stops answering.
// That cluster then reads as EMPTY — and it is the one cluster state where reading empty
// is most expensive, because a pre-declaration instance stamped no Secrets and wrote no
// declaration, so nothing else left behind would contradict it.
func TestTheReleaseSourceActuallyAnswers(t *testing.T) {
	cfg, _ := inMemoryHelm(t, deviceChainRelease("dc-alpha", "alpha"))
	ids, err := releasedInstances(cfg)
	if err != nil {
		t.Fatalf("the release source failed: %v", err)
	}
	if len(ids) != 1 || ids[0] != "alpha" {
		t.Fatalf("got %v, want [alpha] — the source that answers for a cluster with no "+
			"declaration and no stamped Secrets returned nothing, so such a cluster reads as "+
			"empty and a second instance is installed into it", ids)
	}

	// The negative control: an empty cluster still has to read as empty, or the boundary
	// refuses the first bootstrap of every cluster.
	empty, _ := inMemoryHelm(t)
	if ids, err := releasedInstances(empty); err != nil || len(ids) != 0 {
		t.Fatalf("an empty cluster answered %v (err %v)", ids, err)
	}
}

// 🔴 AND THE READER HAS TO ASK ALL THREE, IN WRITE ORDER. clusterInstancesFor needs a live
// cluster, so no unit test reaches the assembly itself — a source dropped from the list,
// or the release moved ahead of the credentials, would break nothing here while changing
// which half-built clusters are answered and which report "I cannot tell".
func TestTheClusterReaderAsksEverySourceInWriteOrder(t *testing.T) {
	want := []string{"declaredInstances", "ownedSecretInstances", "releasedInstances"}
	got := callsWithin(t, "clusterInstancesFor", want...)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("clusterInstancesFor consults %v; want %v, in that order. A bootstrap writes "+
			"the declaration, then the credentials, then the release, so asking in write order "+
			"is what lets a run that died part-way be answered by whatever it did get to",
			got, want)
	}
}

// 🔴 NOTHING INSTALLS UNDER THE LEGACY NAME. Pointing helmInstall's release name back at
// the constant survived every behavioural test in this package — the destroy sweep then
// finds the release it just installed and removes it, so a single instance works end to
// end while two instances silently share one release again. The write side has no unit
// test because helmInstall loads the embedded chart, renders it and talks to a cluster;
// what can be checked without one is that the name it reaches for is the derived one.
func TestOnlyTheSweepMayMentionTheLegacyReleaseName(t *testing.T) {
	allowed := map[string]bool{
		"helmReleaseNameFor":     true, // builds the new name out of the old prefix
		"uninstallLegacyRelease": true, // the one reader of releases installed before the rename
	}
	for fn, names := range identifiersByFunction(t) {
		if allowed[fn] {
			continue
		}
		for _, n := range names {
			if n == "legacyHelmReleaseName" {
				t.Errorf("%s reaches for legacyHelmReleaseName. Only the sweep may: everything "+
					"else addressing a release by that name is an instance installing, reading "+
					"or removing the one release a whole cluster used to share", fn)
			}
		}
	}

	// The positive half, and it is not redundant: the gate above is satisfied by writing
	// the literal "dc" instead of the constant, which would be the same defect wearing a
	// different name.
	if got := callsWithin(t, "helmInstall", "helmReleaseNameFor"); len(got) == 0 {
		t.Fatal("helmInstall no longer derives its release name from helmReleaseNameFor, so " +
			"what it installs under is not the name every reader of this cluster looks for")
	}
}

// packageFiles parses this package's non-test sources.
func packageFiles(t *testing.T) (*token.FileSet, []*ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading this package's directory: %v", err)
	}
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		// 🔴 AN EMPTY PARSE WOULD MAKE EVERY GATE IN THIS FILE PASS BY FINDING NOTHING.
		t.Fatal("parsed no source files, so these gates checked nothing")
	}
	return fset, files
}

// callsWithin returns, in source order, which of the named functions are called inside fn.
func callsWithin(t *testing.T, fn string, of ...string) []string {
	t.Helper()
	interesting := map[string]bool{}
	for _, n := range of {
		interesting[n] = true
	}
	_, files := packageFiles(t)
	var found []string
	seen := map[string]bool{}
	for _, f := range files {
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Name.Name != fn || fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				id, ok := call.Fun.(*ast.Ident)
				if ok && interesting[id.Name] && !seen[id.Name] {
					seen[id.Name] = true
					found = append(found, id.Name)
				}
				return true
			})
		}
	}
	return found
}

// identifiersByFunction maps each top-level function to the identifiers its body mentions.
func identifiersByFunction(t *testing.T) map[string][]string {
	t.Helper()
	_, files := packageFiles(t)
	out := map[string][]string{}
	for _, f := range files {
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok {
					out[fd.Name.Name] = append(out[fd.Name.Name], id.Name)
				}
				return true
			})
		}
	}
	return out
}

// unused keeps the release import honest if the helpers above are ever trimmed.
var _ = release.Release{}
