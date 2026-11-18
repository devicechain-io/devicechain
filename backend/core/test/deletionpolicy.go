// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	deletionPurge     = "purge"
	deletionTerminate = "terminate"
	deletionHistory   = "history"
	deletionBounded   = "bounded"
)

type deletionPolicy struct {
	Policy string
	Why    string
}

// deletionHoldersUnder reads package-level structs, not text matches (which would
// count function-local migration snapshots). Model packages are covered in full,
// including their request/read shapes. Elsewhere a package qualifies when one of
// its structs declares GORM storage through a tag or an embedded gorm.Model.
// This is syntax-based across modules: it does not resolve promoted fields or
// aliases, nor prove that a policy's explanation matches its implementation.
func deletionHoldersUnder(root string) ([]string, error) {
	services := filepath.Join(root, "backend", "services")
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	modelDirs := map[string]bool{}
	err := filepath.WalkDir(services, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != services && skipDirInScan(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") ||
			strings.HasPrefix(name, "migration_") || strings.HasPrefix(name, "baseline") {
			return nil
		}
		rel, err := filepath.Rel(services, path)
		if err != nil {
			return err
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) < 3 {
			return nil // not a service package
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return fmt.Errorf("parsing %s: %w", path, err)
		}
		files[path] = f
		dir := filepath.Dir(path)
		if parts[1] == "model" {
			modelDirs[dir] = true
		}
		for _, st := range deletionStructs(f) {
			if deletionGORMStruct(st) {
				modelDirs[dir] = true
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no live service Go files found under %s", services)
	}
	var holders []string
	for path, f := range files {
		if !modelDirs[filepath.Dir(path)] {
			continue
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil, err
		}
		rel = filepath.ToSlash(rel)
		for name, st := range deletionStructs(f) {
			fields := map[string]bool{}
			for _, field := range st.Fields.List {
				for _, id := range field.Names {
					fields[id.Name] = true
				}
			}
			holder := fields["DeviceToken"] || fields["OriginatorToken"] || fields["EntityToken"] ||
				fields["SourceDeviceToken"] || fields["AnchorToken"] && (fields["AnchorKind"] || fields["AnchorType"])
			if strings.HasPrefix(rel, "backend/services/device-management/") {
				// The owner uses row IDs for its entity graph and alarms. Its own
				// deletion/session outboxes and device request/event shapes are not
				// foreign holders. Cross-entity group/customer references DO exist,
				// as do profile decoder samples naming a device; keep those covered.
				crossEntity := fields["CustomerToken"] || fields["AreaToken"] || fields["AssetToken"] || fields["GroupToken"]
				if holder && deletionGORMStruct(st) {
					switch name {
					case "PendingEntityDeletion", "PendingSessionKick", "PendingRevocationKick": // owner's deletion/session bookkeeping
					default:
						crossEntity = true
					}
				}
				holder = crossEntity
			}
			// Only a PERSISTED row can outlive its entity: a request, criteria or event shape that
			// names a device lives for one call and holds nothing a re-created device could inherit.
			if holder && deletionPersisted(st, fields) {
				holders = append(holders, rel+":"+name)
			}
		}
	}
	sort.Strings(holders)
	return holders, nil
}

func deletionStructs(f *ast.File) map[string]*ast.StructType {
	structs := map[string]*ast.StructType{}
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			ts := spec.(*ast.TypeSpec)
			if st, ok := ts.Type.(*ast.StructType); ok {
				structs[ts.Name.Name] = st
			}
		}
	}
	return structs
}

// deletionPersisted reports whether a struct is a stored row: it carries gorm tags or embeds
// gorm.Model, has a TenantId column, or embeds rdb.TenantScoped.
func deletionPersisted(st *ast.StructType, fields map[string]bool) bool {
	if deletionGORMStruct(st) || fields["TenantId"] {
		return true
	}
	for _, field := range st.Fields.List {
		if len(field.Names) == 0 {
			if sel, ok := field.Type.(*ast.SelectorExpr); ok && sel.Sel.Name == "TenantScoped" {
				return true
			}
		}
	}
	return false
}

func deletionGORMStruct(st *ast.StructType) bool {
	for _, field := range st.Fields.List {
		if field.Tag != nil {
			tag, err := strconv.Unquote(field.Tag.Value)
			if err == nil && strings.Contains(tag, "gorm:\"") {
				return true
			}
		}
		if len(field.Names) == 0 {
			if sel, ok := field.Type.(*ast.SelectorExpr); ok && sel.Sel.Name == "Model" {
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "gorm" {
					return true
				}
			}
		}
	}
	return false
}

// deletionPolicyProblems checks both directions so deleting/renaming a model also
// requires updating its decision. Sort diagnostics to keep failures reproducible.
func deletionPolicyProblems(holders []string, policies map[string]deletionPolicy) []string {
	seen := map[string]bool{}
	var problems []string
	for _, holder := range holders {
		seen[holder] = true
		if _, ok := policies[holder]; !ok {
			problems = append(problems, fmt.Sprintf("%s: holder has no deletion policy; decide purge, terminate, history or bounded and explain Why", holder))
		}
	}
	for key, policy := range policies {
		if !seen[key] {
			problems = append(problems, key+": stale deletion policy: type no longer exists or is not a holder")
		}
		switch policy.Policy {
		case deletionPurge, deletionTerminate, deletionHistory, deletionBounded:
		default:
			problems = append(problems, fmt.Sprintf("%s: unknown deletion policy %q; choose purge, terminate, history or bounded", key, policy.Policy))
		}
		if utf8.RuneCountInString(strings.TrimSpace(policy.Why)) < 20 {
			problems = append(problems, key+": deletion policy Why must contain at least 20 characters explaining the mechanism or bound")
		}
	}
	sort.Strings(problems)
	return problems
}
