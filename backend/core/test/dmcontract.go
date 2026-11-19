// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// The device-management wire-contract boundary.
//
// device-management publishes facts that other services consume (alarm state changes,
// published detection rules, the roster, device attributes, ownership changes, fence-set
// manifests) and answers reconcile doors whose rows they decode. The shapes of all of
// those live in dc-device-management/contract, which imports nothing of device-management.
// Its model package is the service's whole domain: ~50k lines, gorm, cel-go, the x509
// stack. A consumer that imports model for one struct links all of it and couples its
// build to every change there, so the boundary is: outside device-management, PRODUCTION
// code imports contract (or proto, which encodes contract), never model.
//
// Three checks hold it, because each closes a door the others leave open:
//
//  1. A non-test file outside device-management that imports model directly must be in the
//     ledger, which pins the exact model names it uses. A new importer fails; a ledgered
//     file that starts using another model name fails; a ledgered file that stops importing
//     model fails as stale, so the ledger only shrinks.
//  2. The device-management packages that production code outside it DOES import (contract,
//     proto, schema, ...) must not reach model through their own imports. Without this,
//     proto/marshal.go importing model again would put the whole package back into every
//     consumer's binary with no consumer file changing.
//  3. A test file outside device-management may import model as a FIXTURE (standing up the
//     real Api, building a MetricDefinition), which is not linked into any binary. But a test
//     that uses only names contract exports must import contract: otherwise the wire types
//     drift back into tests that read like the boundary does not exist.
const (
	dmModulePrefix   = "github.com/devicechain-io/dc-device-management"
	dmModelImport    = dmModulePrefix + "/model"
	dmModuleDir      = "backend/services/device-management"
	dmContractSubdir = "contract"
)

// DMModelSanction is one ledgered production importer of device-management's model.
type DMModelSanction struct {
	// Names is every model.<Name> the file references, sorted. Pinned so a sanctioned file
	// cannot quietly widen what it takes from model.
	Names  []string
	Reason string
}

// dmFile is one parsed Go file and what it imports.
type dmFile struct {
	Rel     string // repo-relative, slash-separated
	Test    bool
	Imports map[string]string // import path -> local name ("" when blank or dot)
	parsed  *ast.File
}

// dmScan is everything the three checks need, gathered in one walk.
type dmScan struct {
	Outside         []dmFile            // every .go file outside device-management
	DMPackages      map[string][]string // DM import path -> its non-test files' DM imports
	ContractExports map[string]bool
	files           int
}

// scanDMContractBoundary walks root (a workspace root) once.
func scanDMContractBoundary(root string) (*dmScan, error) {
	scan := &dmScan{DMPackages: map[string][]string{}, ContractExports: map[string]bool{}}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if p != root && skipDirInScan(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		scan.files++
		file := dmFile{Rel: rel, Test: strings.HasSuffix(p, "_test.go"), Imports: map[string]string{}, parsed: f}
		for _, im := range f.Imports {
			ip, _ := strconv.Unquote(im.Path.Value)
			name := path.Base(ip)
			if im.Name != nil {
				name = im.Name.Name
				if name == "_" || name == "." {
					name = ""
				}
			}
			file.Imports[ip] = name
		}
		inDM := rel == dmModuleDir || strings.HasPrefix(rel, dmModuleDir+"/")
		if !inDM {
			scan.Outside = append(scan.Outside, file)
			return nil
		}
		if file.Test {
			return nil
		}
		pkgDir := path.Dir(strings.TrimPrefix(rel, dmModuleDir+"/"))
		pkg := dmModulePrefix
		if pkgDir != "." && rel != dmModuleDir {
			pkg += "/" + pkgDir
		}
		deps := scan.DMPackages[pkg]
		for ip := range file.Imports {
			if ip == dmModulePrefix || strings.HasPrefix(ip, dmModulePrefix+"/") {
				deps = append(deps, ip)
			}
		}
		scan.DMPackages[pkg] = deps
		if pkgDir == dmContractSubdir {
			for _, decl := range f.Decls {
				for _, n := range declaredNames(decl) {
					if ast.IsExported(n) {
						scan.ContractExports[n] = true
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if scan.files == 0 {
		return nil, fmt.Errorf("no Go files scanned under %s", root)
	}
	// An empty export set would make check 3 pass every test file, so it is an error, not
	// an empty answer.
	if len(scan.ContractExports) == 0 {
		return nil, fmt.Errorf("no exported names found in %s/%s: the walk did not reach the contract package",
			dmModuleDir, dmContractSubdir)
	}
	return scan, nil
}

// declaredNames lists the package-level names a declaration introduces.
func declaredNames(decl ast.Decl) []string {
	var names []string
	switch d := decl.(type) {
	case *ast.FuncDecl:
		if d.Recv == nil {
			names = append(names, d.Name.Name)
		}
	case *ast.GenDecl:
		for _, spec := range d.Specs {
			switch s := spec.(type) {
			case *ast.TypeSpec:
				names = append(names, s.Name.Name)
			case *ast.ValueSpec:
				for _, n := range s.Names {
					names = append(names, n.Name)
				}
			}
		}
	}
	return names
}

// modelNames is every <local>.<Name> selector in the file, where <local> is the name model
// is imported under. A blank or dot import yields a sentinel instead, because neither can
// be audited name by name.
func (f dmFile) modelNames() []string {
	local, ok := f.Imports[dmModelImport]
	if !ok {
		return nil
	}
	if local == "" {
		return []string{"<blank or dot import>"}
	}
	set := map[string]bool{}
	ast.Inspect(f.parsed, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == local {
				set[sel.Sel.Name] = true
			}
		}
		return true
	})
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// dmContractBoundaryErrors runs the three checks against a scan and a ledger.
func dmContractBoundaryErrors(scan *dmScan, ledger map[string]DMModelSanction) []string {
	var issues []string
	seen := map[string]bool{}
	var entry []string // DM packages production code outside device-management imports
	entrySeen := map[string]bool{}
	for _, f := range scan.Outside {
		if !f.Test {
			for ip := range f.Imports {
				if (ip == dmModulePrefix || strings.HasPrefix(ip, dmModulePrefix+"/")) && ip != dmModelImport && !entrySeen[ip] {
					entrySeen[ip] = true
					entry = append(entry, ip)
				}
			}
		}
		if _, ok := f.Imports[dmModelImport]; !ok {
			continue
		}
		names := f.modelNames()
		if f.Test {
			// Check 3: a fixture is fine; a test that needs only contract names is not.
			needsModel := false
			for _, n := range names {
				if !scan.ContractExports[n] {
					needsModel = true
					break
				}
			}
			if !needsModel {
				issues = append(issues, fmt.Sprintf("%s imports %s but uses only names contract exports (%s): import %s/%s instead",
					f.Rel, dmModelImport, strings.Join(names, ", "), dmModulePrefix, dmContractSubdir))
			}
			continue
		}
		// Check 1.
		seen[f.Rel] = true
		sanction, ok := ledger[f.Rel]
		if !ok {
			issues = append(issues, fmt.Sprintf("%s imports %s (uses %s): production code outside device-management "+
				"imports %s/%s; move the shape there, or ledger the file with a reason",
				f.Rel, dmModelImport, strings.Join(names, ", "), dmModulePrefix, dmContractSubdir))
			continue
		}
		if strings.Join(names, ",") != strings.Join(sanction.Names, ",") {
			issues = append(issues, fmt.Sprintf("%s: model names used changed: got [%s], ledger pins [%s]",
				f.Rel, strings.Join(names, ", "), strings.Join(sanction.Names, ", ")))
		}
	}
	for file, sanction := range ledger {
		if !seen[file] {
			issues = append(issues, fmt.Sprintf("stale ledger entry: %s no longer imports %s; delete it", file, dmModelImport))
		}
		if strings.TrimSpace(sanction.Reason) == "" || len(sanction.Names) == 0 {
			issues = append(issues, fmt.Sprintf("ledger entry %s needs its names and a reason", file))
		}
	}
	// Check 2: breadth-first over device-management's own non-test import edges.
	sort.Strings(entry)
	via := map[string]string{}
	queue := []string{}
	for _, p := range entry {
		via[p] = ""
		queue = append(queue, p)
	}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		if p == dmModelImport {
			chain := []string{p}
			for q := via[p]; q != ""; q = via[q] {
				chain = append([]string{q}, chain...)
			}
			issues = append(issues, fmt.Sprintf("production code outside device-management reaches model through %s",
				strings.Join(chain, " -> ")))
			break
		}
		deps := append([]string(nil), scan.DMPackages[p]...)
		sort.Strings(deps)
		for _, d := range deps {
			if _, ok := via[d]; !ok {
				via[d] = p
				queue = append(queue, d)
			}
		}
	}
	// And contract itself imports nothing of device-management, by any route.
	for _, d := range scan.DMPackages[dmModulePrefix+"/"+dmContractSubdir] {
		issues = append(issues, fmt.Sprintf("%s/%s imports %s: the contract must import nothing of device-management",
			dmModulePrefix, dmContractSubdir, d))
	}
	sort.Strings(issues)
	return issues
}
