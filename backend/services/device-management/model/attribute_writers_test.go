// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// attributeWriterFrozenMigrations names, file by file, the schema migrations the guard does not
// scan: each is frozen once released and runs before any pod serves (the historic attribute
// dedupe among them). A NEW file under schema/ is scanned like any other until it is added here,
// which is a reviewed decision, never a directory-wide exemption.
// attributeWriterFrozenSchema is the schema package; its frozen files are type-checked with it but
// never reported.
const attributeWriterFrozenSchema = "schema"

var attributeWriterFrozenMigrations = map[string]bool{
	"schema/baseline.go":                                    true,
	"schema/baseline_snapshot.go":                           true,
	"schema/baseline_snapshot_2.go":                         true,
	"schema/migration_agent_proposals.go":                   true,
	"schema/migration_agent_safety.go":                      true,
	"schema/migration_alarm_dispositions.go":                true,
	"schema/migration_alarm_position_derived.go":            true,
	"schema/migration_alarm_position_stamp.go":              true,
	"schema/migration_alarm_scope_anchors.go":               true,
	"schema/migration_alarm_suppression.go":                 true,
	"schema/migration_anchor_ocsp.go":                       true,
	"schema/migration_asset_property_schema.go":             true,
	"schema/migration_attribute_generations.go":             true,
	"schema/migration_attribute_writer_protocol.go":         true,
	"schema/migration_device_attribute_try_lock.go":         true,
	"schema/migration_automation_review_enforcement.go":     true,
	"schema/migration_automation_reviews.go":                true,
	"schema/migration_command_encoder.go":                   true,
	"schema/migration_credential_secret_digest.go":          true,
	"schema/migration_deletion_alarm_cascade.go":            true,
	"schema/migration_detect_reconcile_instants.go":         true,
	"schema/migration_device_aliases.go":                    true,
	"schema/migration_device_attribute_lock.go":             true,
	"schema/migration_device_configuration.go":              true,
	"schema/migration_device_configuration_reports.go":      true,
	"schema/migration_device_disabled.go":                   true,
	"schema/migration_device_disabled_index.go":             true,
	"schema/migration_device_gateway.go":                    true,
	"schema/migration_device_replacements.go":               true,
	"schema/migration_device_trust_anchors.go":              true,
	"schema/migration_device_type_command_channel.go":       true,
	"schema/migration_device_type_index.go":                 true,
	"schema/migration_drop_alarm_message.go":                true,
	"schema/migration_enrollment_secret_digest.go":          true,
	"schema/migration_entity_attribute_unique_key.go":       true,
	"schema/migration_geofence_geometry_blobs.go":           true,
	"schema/migration_geofence_snapshot_backfill.go":        true,
	"schema/migration_geofences.go":                         true,
	"schema/migration_group_access_scope.go":                true,
	"schema/migration_installed_position.go":                true,
	"schema/migration_integrations.go":                      true,
	"schema/migration_issued_customer_tokens.go":            true,
	"schema/migration_list_order_indexes.go":                true,
	"schema/migration_maintenance_window.go":                true,
	"schema/migration_metric_expression.go":                 true,
	"schema/migration_pending_alarm_events.go":              true,
	"schema/migration_pending_entity_deletions.go":          true,
	"schema/migration_pending_ownership_facts.go":           true,
	"schema/migration_pending_session_kicks.go":             true,
	"schema/migration_profile_activations.go":               true,
	"schema/migration_profile_configuration_declaration.go": true,
	"schema/migration_profile_decoders.go":                  true,
	"schema/migration_profile_location_declaration.go":      true,
	"schema/migration_revocation_expansion_markers.go":      true,
	"schema/migration_revocation_kick_queue.go":             true,
	"schema/migration_self_registration.go":                 true,
	"schema/migration_semantic_bindings.go":                 true,
	"schema/migration_x509_revocation.go":                   true,
}

// attributeWriterAllowList is every function allowed to write attribute rows or generation heads,
// each with its reason. TestAttributeWritersGoThroughPrimitive fails on an entry that names no
// function the scan saw, so the list cannot go stale silently.
var attributeWriterAllowList = map[string]string{
	"model/api_attribute_owner.go:applyAttributePatchTx":          "the locked attribute patch primitive",
	"model/api_attribute_owner.go:upsertAttributeOnTx":            "the primitive's single-row upsert",
	"model/api_attribute_generation.go:bumpAttributeGenerationTx": "the primitive's monotone head bump",
	"model/api_delete.go:deleteEntityAttributesOnTx":              "locked entity deletion, including its generation cascade",
	"model/api_attribute_orphans.go:deleteAttributeOrphanBatch":   "bounded marked deletion of rows with no surviving owner",
}

func attributeWriterAllowed(file, function string) bool {
	_, ok := attributeWriterAllowList[file+":"+function]
	return ok
}

func attributeWriterSources(root string) (map[string][]byte, error) {
	sources := map[string][]byte{}
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			data, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			sources[filepath.ToSlash(rel)] = data
		}
		return nil
	})
	return sources, err
}

type attributeWriterScan struct {
	fset       *token.FileSet
	loader     *attributeTypeLoader
	pkg        *types.Package
	file       *ast.File
	objects    map[*ast.Object]types.Object
	bindings   map[any][]ast.Expr
	valueTypes map[any]types.Type
	tainted    map[any]bool
	functions  map[*types.Func]*ast.FuncDecl
	funcFiles  map[*types.Func]*ast.File
	returns    map[*types.Func]bool
	aliases    map[*ast.Object]bool
	typeErr    error
}

func (s *attributeWriterScan) key(id *ast.Ident) any {
	if obj := s.loader.info.Defs[id]; obj != nil {
		return obj
	}
	if id.Obj != nil {
		if obj := s.objects[id.Obj]; obj != nil {
			return obj
		}
		return id.Obj
	}
	if scope := s.pkg.Scope().Innermost(id.Pos()); scope != nil {
		if _, obj := scope.LookupParent(id.Name, id.Pos()); obj != nil {
			return obj
		}
	}
	// Generic receiver parameters belong to the checked function scope even
	// when go/types has deliberately not checked its body.
	for _, decl := range s.file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Pos() <= id.Pos() && id.Pos() <= fn.End() {
			if scope := s.loader.info.Scopes[fn.Type]; scope != nil {
				if _, obj := scope.LookupParent(id.Name, token.NoPos); obj != nil {
					return obj
				}
			}
			if obj, ok := s.loader.info.Defs[fn.Name].(*types.Func); ok {
				sig := obj.Type().(*types.Signature)
				for _, params := range []*types.TypeParamList{sig.TypeParams(), sig.RecvTypeParams()} {
					for i := 0; i < params.Len(); i++ {
						if param := params.At(i).Obj(); param.Name() == id.Name {
							return param
						}
					}
				}
			}
		}
	}
	if obj := s.pkg.Scope().Lookup(id.Name); obj != nil {
		return obj
	}
	return nil
}

func attributeType(t types.Type) bool {
	return attributeTypeSeen(t, map[types.Type]bool{})
}

func attributeTypeSeen(t types.Type, seen map[types.Type]bool) bool {
	if t == nil {
		return false
	}
	switch t := types.Unalias(t).(type) {
	case *types.Named:
		if seen[t] {
			return false
		}
		seen[t] = true
		obj := t.Obj()
		if obj.Pkg() != nil && obj.Pkg().Path() == attributeModule+"/model" &&
			(obj.Name() == "EntityAttribute" || obj.Name() == "AttributeGeneration") {
			return true
		}
		// Defined slices/pointers still carry their element type.
		return attributeTypeSeen(t.Underlying(), seen)
	case *types.Pointer:
		return attributeTypeSeen(t.Elem(), seen)
	case *types.Slice:
		return attributeTypeSeen(t.Elem(), seen)
	case *types.Array:
		return attributeTypeSeen(t.Elem(), seen)
	case *types.Map:
		return attributeTypeSeen(t.Elem(), seen)
	}
	return false
}

func (s *attributeWriterScan) imported(id *ast.Ident) *types.Package {
	if obj, ok := s.key(id).(*types.PkgName); ok {
		return obj.Imported()
	}
	for _, imp := range s.file.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		name := attributeImportName(p)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		if name == id.Name && id.Obj == nil {
			if pkg := s.loader.packages[p]; pkg != nil {
				return pkg
			}
			return s.loader.external[p]
		}
	}
	return nil
}

func (s *attributeWriterScan) object(expr ast.Expr) types.Object {
	switch e := expr.(type) {
	case *ast.Ident:
		if obj, ok := s.key(e).(types.Object); ok {
			return obj
		}
		return types.Universe.Lookup(e.Name)
	case *ast.SelectorExpr:
		if id, ok := e.X.(*ast.Ident); ok {
			if pkg := s.imported(id); pkg != nil {
				return pkg.Scope().Lookup(e.Sel.Name)
			}
		}
		if typ := s.typeOf(e.X); typ != nil {
			obj, _, _ := types.LookupFieldOrMethod(typ, true, s.pkg, e.Sel.Name)
			return obj
		}
	case *ast.IndexExpr:
		return s.object(e.X)
	case *ast.IndexListExpr:
		return s.object(e.X)
	case *ast.ParenExpr:
		return s.object(e.X)
	}
	return nil
}

func (s *attributeWriterScan) typeOf(expr ast.Expr) types.Type {
	if typ := s.loader.info.TypeOf(expr); typ != nil {
		return typ
	}
	switch e := expr.(type) {
	case *ast.Ident:
		if typ := s.valueTypes[s.key(e)]; typ != nil {
			return typ
		}
		if obj, ok := s.key(e).(types.Object); ok {
			return obj.Type()
		}
		if e.Obj != nil {
			if spec, ok := e.Obj.Decl.(*ast.TypeSpec); ok {
				key := s.key(e)
				if spec.Assign.IsValid() {
					if s.aliases[e.Obj] {
						s.typeErr = fmt.Errorf("attribute guard: recursive type alias at %s", s.fset.Position(e.Pos()))
						return nil
					}
					s.aliases[e.Obj] = true
					typ := s.typeOf(spec.Type)
					delete(s.aliases, e.Obj)
					s.valueTypes[key] = typ
					return typ
				}
				// Install local container identities before resolving recursive types.
				obj := types.NewTypeName(e.Pos(), s.pkg, e.Name, nil)
				named := types.NewNamed(obj, types.NewStruct(nil, nil), nil)
				s.valueTypes[key] = named
				if typ := s.typeOf(spec.Type); typ != nil {
					named.SetUnderlying(typ.Underlying())
				}
				return named
			}
		}
		if obj := s.object(e); obj != nil {
			return obj.Type()
		}
	case *ast.SelectorExpr:
		if obj := s.object(e); obj != nil {
			return obj.Type()
		}
	case *ast.CompositeLit:
		return s.typeOf(e.Type)
	case *ast.StructType:
		var fields []*types.Var
		for _, field := range e.Fields.List {
			typ := s.typeOf(field.Type)
			if typ == nil {
				typ = types.NewInterfaceType(nil, nil).Complete()
			}
			for _, name := range field.Names {
				fields = append(fields, types.NewField(name.Pos(), s.pkg, name.Name, typ, false))
			}
			if len(field.Names) == 0 {
				base := typ
				if pointer, ok := base.(*types.Pointer); ok {
					base = pointer.Elem()
				}
				if named, ok := types.Unalias(base).(*types.Named); ok {
					fields = append(fields, types.NewField(field.Pos(), s.pkg, named.Obj().Name(), typ, true))
				}
			}
		}
		return types.NewStruct(fields, nil)
	case *ast.UnaryExpr:
		typ := s.typeOf(e.X)
		if e.Op == token.AND && typ != nil {
			return types.NewPointer(typ)
		}
		return typ
	case *ast.StarExpr:
		if p, ok := s.typeOf(e.X).(*types.Pointer); ok {
			return p.Elem()
		}
		if typ := s.typeOf(e.X); typ != nil {
			return types.NewPointer(typ) // Type syntax, e.g. new(*EntityAttribute).
		}
	case *ast.ArrayType:
		if typ := s.typeOf(e.Elt); typ != nil {
			return types.NewSlice(typ)
		}
	case *ast.MapType:
		if k, v := s.typeOf(e.Key), s.typeOf(e.Value); k != nil && v != nil {
			return types.NewMap(k, v)
		}
	case *ast.ParenExpr:
		return s.typeOf(e.X)
	case *ast.IndexExpr:
		switch typ := s.typeOf(e.X).(type) {
		case *types.Slice:
			return typ.Elem()
		case *types.Array:
			return typ.Elem()
		case *types.Map:
			return typ.Elem()
		}
	case *ast.CallExpr:
		if id, ok := e.Fun.(*ast.Ident); ok && len(e.Args) > 0 {
			switch id.Name {
			case "new":
				if typ := s.typeOf(e.Args[0]); typ != nil {
					return types.NewPointer(typ)
				}
			case "make", "append":
				return s.typeOf(e.Args[0])
			}
		}
		if sig, ok := s.typeOf(e.Fun).(*types.Signature); ok {
			if sig.Results().Len() == 1 {
				return sig.Results().At(0).Type()
			}
			return sig.Results()
		}
		if _, ok := s.object(e.Fun).(*types.TypeName); ok {
			return s.typeOf(e.Fun)
		}
	}
	return nil
}

// Resolve fragments by binding identity, including package constants in other
// files. Joining with no separator preserves split table names; the spaced view
// preserves SQL tokens across fmt.Sprintf's format/argument boundary.
func (s *attributeWriterScan) fragments(n ast.Node, seen map[any]bool) []string {
	var result []string
	ast.Inspect(n, func(n ast.Node) bool {
		switch e := n.(type) {
		case *ast.CallExpr:
			if fn, ok := s.object(e.Fun).(*types.Func); ok && !seen[fn] {
				if decl := s.functions[fn]; decl != nil && decl.Body != nil {
					seen[fn] = true
					oldPkg, oldFile := s.pkg, s.file
					s.pkg, s.file = s.loader.packages[fn.Pkg().Path()], s.funcFiles[fn]
					ast.Inspect(decl.Body, func(n ast.Node) bool {
						if _, closure := n.(*ast.FuncLit); closure {
							return false
						}
						if ret, ok := n.(*ast.ReturnStmt); ok {
							for _, value := range ret.Results {
								result = append(result, s.fragments(value, seen)...)
							}
						}
						return true
					})
					s.pkg, s.file = oldPkg, oldFile
				}
			}
		case *ast.SelectorExpr:
			if obj := s.object(e); obj != nil && !seen[obj] {
				seen[obj] = true
				for _, rhs := range s.bindings[obj] {
					result = append(result, s.fragments(rhs, seen)...)
				}
			}
			return false
		case *ast.BasicLit:
			if e.Kind == token.STRING {
				v, _ := strconv.Unquote(e.Value)
				result = append(result, strings.ToLower(v))
			}
		case *ast.Ident:
			key := s.key(e)
			if key != nil && !seen[key] {
				seen[key] = true
				for _, rhs := range s.bindings[key] {
					result = append(result, s.fragments(rhs, seen)...)
				}
			}
		}
		return true
	})
	return result
}

func attributeTable(text string) bool {
	return strings.Contains(text, "entity_attributes") || strings.Contains(text, "entity_attribute_generations")
}

func (s *attributeWriterScan) touches(n ast.Node) bool {
	if expr, ok := n.(ast.Expr); ok && attributeType(s.typeOf(expr)) {
		return true
	}
	switch e := n.(type) {
	case *ast.Ident:
		return s.tainted[s.key(e)]
	case *ast.CallExpr:
		if fn, ok := s.object(e.Fun).(*types.Func); ok && s.returns[fn] {
			return true
		}
		// A call of a local closure returns what its body returns.
		if id, ok := e.Fun.(*ast.Ident); ok {
			for _, bound := range s.bindings[s.key(id)] {
				if lit, ok := bound.(*ast.FuncLit); ok && s.funcLitReturnsAttribute(lit) {
					return true
				}
			}
		}
		if s.attributeAssociation(e) {
			return true
		}
		if sel, ok := e.Fun.(*ast.SelectorExpr); ok {
			if s.touches(sel.X) {
				return true
			}
			switch sel.Sel.Name {
			case "Model", "Create", "CreateInBatches", "Save", "Update", "Updates", "UpdateColumn", "UpdateColumns", "Delete", "FirstOrCreate", "Append", "Replace":
				for _, arg := range e.Args {
					if s.touches(arg) {
						return true
					}
				}
			}
		}
	case *ast.CompositeLit:
		for _, value := range e.Elts {
			if s.touches(value) {
				return true
			}
		}
	case *ast.KeyValueExpr:
		return s.touches(e.Value)
	case *ast.UnaryExpr:
		return s.touches(e.X)
	case *ast.StarExpr:
		return s.touches(e.X)
	case *ast.ParenExpr:
		return s.touches(e.X)
	case *ast.IndexExpr:
		return s.touches(e.X)
	case *ast.SliceExpr:
		return s.touches(e.X)
	}
	return false
}

func (s *attributeWriterScan) attributeAssociation(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Association" || len(call.Args) != 1 {
		return false
	}
	name, ok := call.Args[0].(*ast.BasicLit)
	if !ok || name.Kind != token.STRING {
		return false
	}
	field, _ := strconv.Unquote(name.Value)
	found := false
	ast.Inspect(sel.X, func(n ast.Node) bool {
		model, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		method, ok := model.Fun.(*ast.SelectorExpr)
		if ok && method.Sel.Name == "Model" && len(model.Args) == 1 {
			if typ := s.typeOf(model.Args[0]); typ != nil {
				obj, _, _ := types.LookupFieldOrMethod(typ, true, s.pkg, field)
				found = obj != nil && attributeType(obj.Type())
			}
		}
		return !found
	})
	return found
}

func (s *attributeWriterScan) tableHandle(n ast.Node) bool {
	switch e := n.(type) {
	case *ast.CallExpr:
		if sel, ok := e.Fun.(*ast.SelectorExpr); ok {
			if sel.Sel.Name == "Table" {
				parts := s.fragments(e, map[any]bool{})
				return attributeTable(strings.Join(parts, ""))
			}
			if sel.Sel.Name == "Clauses" {
				for _, arg := range e.Args {
					if s.tableHandle(arg) {
						return true
					}
				}
			}
			return s.tableHandle(sel.X)
		}
	case *ast.CompositeLit:
		isTable := false
		if e.Type != nil {
			ast.Inspect(e.Type, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "Table" {
					if id, ok := sel.X.(*ast.Ident); ok {
						p := s.imported(id)
						isTable = p != nil && p.Path() == "gorm.io/gorm/clause"
					}
				}
				return !isTable
			})
		}
		if isTable {
			parts := s.fragments(e, map[any]bool{})
			return attributeTable(strings.Join(parts, ""))
		}
		for _, value := range e.Elts {
			if s.tableHandle(value) {
				return true
			}
		}
	case *ast.KeyValueExpr:
		return s.tableHandle(e.Value)
	case *ast.UnaryExpr:
		return s.tableHandle(e.X)
	case *ast.ParenExpr:
		return s.tableHandle(e.X)
	case *ast.Ident:
		return s.tainted[s.key(e)]
	}
	return false
}

func scanAttributeWriters(name string, source []byte) ([]string, error) {
	if !strings.Contains(name, "/") {
		name = "model/" + name
	}
	// Existing marker fixtures intentionally omit imports. Authority references
	// are forbidden regardless of whether the rest of a scratch package resolves.
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, source, 0)
	if err != nil {
		return nil, err
	}
	var violations []string
	for _, decl := range f.Decls {
		function := "<package>"
		if fn, ok := decl.(*ast.FuncDecl); ok {
			function = fn.Name.Name
		}
		ast.Inspect(decl, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && attributeRestrictedReference(name, function, id.Name) {
				violations = append(violations, fset.Position(id.Pos()).String())
			}
			return true
		})
	}
	if len(violations) > 0 {
		return violations, nil
	}
	return scanAttributeWriterPackages(map[string][]byte{name: source})
}

func scanAttributeWriterPackages(sources map[string][]byte) ([]string, error) {
	fset := token.NewFileSet()
	files := map[string][]*ast.File{}
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		f, err := parser.ParseFile(fset, name, sources[name], 0)
		if err != nil {
			return nil, err
		}
		pkgPath := attributeModule
		if dir := path.Dir(name); dir != "." {
			pkgPath += "/" + dir
		}
		files[pkgPath] = append(files[pkgPath], f)
	}
	loader, err := newAttributeTypeLoader(fset, files)
	if err != nil {
		return nil, err
	}
	for p := range files {
		if _, err := loader.check(p); err != nil {
			return nil, err
		}
	}
	s := &attributeWriterScan{
		fset: fset, loader: loader, objects: map[*ast.Object]types.Object{}, bindings: map[any][]ast.Expr{},
		valueTypes: map[any]types.Type{}, tainted: map[any]bool{}, functions: map[*types.Func]*ast.FuncDecl{}, returns: map[*types.Func]bool{},
		aliases:   map[*ast.Object]bool{},
		funcFiles: map[*types.Func]*ast.File{},
	}
	for id, obj := range loader.info.Defs {
		if id.Obj != nil && obj != nil {
			s.objects[id.Obj] = obj
		}
	}
	visit := func(fn func(*ast.File)) {
		for p, group := range files {
			s.pkg = loader.packages[p]
			for _, f := range group {
				s.file = f
				fn(f)
			}
		}
	}
	visit(func(f *ast.File) {
		ast.Inspect(f, func(n ast.Node) bool {
			if err != nil {
				return false
			}
			var typ ast.Expr
			switch v := n.(type) {
			case *ast.ValueSpec:
				typ = v.Type
			case *ast.TypeSpec:
				typ = v.Type
				if v.Assign.IsValid() {
					_ = s.typeOf(v.Name)
				}
			case *ast.Field:
				typ = v.Type
			case *ast.CompositeLit:
				typ = v.Type
			}
			if typ != nil {
				err = s.validateType(typ)
			}
			switch v := n.(type) {
			case *ast.FuncDecl:
				if obj, ok := loader.info.Defs[v.Name].(*types.Func); ok {
					s.functions[obj] = v
					s.funcFiles[obj] = f
				}
			case *ast.ValueSpec:
				for i, id := range v.Names {
					if i < len(v.Values) {
						s.bindings[s.key(id)] = append(s.bindings[s.key(id)], v.Values[i])
					}
				}
			case *ast.AssignStmt:
				for i, lhs := range v.Lhs {
					if id, ok := lhs.(*ast.Ident); ok && i < len(v.Rhs) {
						s.bindings[s.key(id)] = append(s.bindings[s.key(id)], v.Rhs[i])
					}
				}
			}
			return true
		})
	})
	if err != nil {
		return nil, err
	}
	// Fixed point over typed local bindings and calls, including helpers accepting
	// any and generic helpers. Object keys keep identically named locals separate.
	for changed := true; changed; {
		changed = false
		mark := func(key any, typ types.Type, taint bool) {
			if key == nil {
				return
			}
			if typ != nil && s.valueTypes[key] == nil {
				s.valueTypes[key], changed = typ, true
			}
			if taint && !s.tainted[key] {
				s.tainted[key], changed = true, true
			}
		}
		visit(func(f *ast.File) {
			ast.Inspect(f, func(n ast.Node) bool {
				bind := func(lhs []ast.Expr, rhs []ast.Expr, declared ast.Expr) {
					for i, e := range lhs {
						id, ok := e.(*ast.Ident)
						if !ok {
							continue
						}
						var typ types.Type
						var taint bool
						if declared != nil {
							typ = s.typeOf(declared)
						}
						if i < len(rhs) {
							if typ == nil {
								typ = s.typeOf(rhs[i])
							}
							// Model/Table/Clauses handles retain their target through aliases.
							taint = s.touches(rhs[i]) || s.tableHandle(rhs[i])
						} else if len(rhs) == 1 {
							if tuple, ok := s.typeOf(rhs[0]).(*types.Tuple); ok && i < tuple.Len() {
								typ = tuple.At(i).Type()
							}
						}
						if tuple, ok := typ.(*types.Tuple); ok && i < tuple.Len() {
							typ = tuple.At(i).Type()
						}
						mark(s.key(id), typ, taint || attributeType(typ))
					}
				}
				switch v := n.(type) {
				case *ast.ValueSpec:
					var lhs []ast.Expr
					for _, id := range v.Names {
						lhs = append(lhs, id)
					}
					bind(lhs, v.Values, v.Type)
				case *ast.AssignStmt:
					bind(v.Lhs, v.Rhs, nil)
				case *ast.RangeStmt:
					if id, ok := v.Value.(*ast.Ident); ok {
						var element types.Type
						if typ, ok := s.typeOf(v.X).(*types.Slice); ok {
							element = typ.Elem()
						}
						mark(s.key(id), element, s.touches(v.X))
					}
				case *ast.CallExpr:
					fn, _ := s.object(v.Fun).(*types.Func)
					if decl := s.functions[fn]; decl != nil {
						sig := fn.Type().(*types.Signature)
						for i, arg := range v.Args {
							if i < sig.Params().Len() && (s.touches(arg) || s.tableHandle(arg)) {
								mark(sig.Params().At(i), nil, true)
							}
						}
					}
				case *ast.FuncDecl:
					fn, _ := loader.info.Defs[v.Name].(*types.Func)
					if v.Body != nil && !s.returns[fn] {
						ast.Inspect(v.Body, func(n ast.Node) bool {
							if _, closure := n.(*ast.FuncLit); closure {
								return false
							}
							if ret, ok := n.(*ast.ReturnStmt); ok {
								for _, value := range ret.Results {
									if s.touches(value) && !s.returns[fn] {
										s.returns[fn], changed = true, true
									}
								}
							}
							return true
						})
					}
				}
				return true
			})
		})
	}
	if s.typeErr != nil {
		return nil, s.typeErr
	}
	var violations []string
	visit(func(f *ast.File) {
		file := fset.Position(f.Pos()).Filename
		if attributeWriterFrozenMigrations[file] {
			return // frozen migration: type-checked with its package, never reported
		}
		inspect := func(node ast.Node, function string) {
			ast.Inspect(node, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && attributeRestrictedReference(file, function, id.Name) {
					violations = append(violations, fset.Position(id.Pos()).String()+": restricted attribute authority")
				}
				if attributeWriterAllowed(file, function) {
					return true
				}
				// Pointing a statement at an attribute table by hand (tx.Statement.Table = ...) is a
				// write-path redirection whatever call follows it.
				if assign, ok := n.(*ast.AssignStmt); ok {
					for i, lhs := range assign.Lhs {
						if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "Table" && i < len(assign.Rhs) {
							parts := s.fragments(assign.Rhs[i], map[any]bool{})
							if attributeTable(strings.Join(parts, " ")) || attributeTable(strings.Join(parts, "")) {
								violations = append(violations, fset.Position(assign.Pos()).String()+": attribute writer bypasses owner")
							}
						}
					}
				}
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				bad := false
				switch sel.Sel.Name {
				case "Create", "Save", "Update", "Updates", "UpdateColumn", "UpdateColumns", "Delete":
					bad = s.touches(call) || s.tableHandle(call)
				case "CreateInBatches", "FirstOrCreate", "Append", "Replace", "Clear":
					bad = s.touches(call) || s.tableHandle(call)
				case "Exec", "Raw", "ExecContext", "QueryContext", "QueryRowContext", "PrepareContext":
					sqlArg := 0
					if strings.HasSuffix(sel.Sel.Name, "Context") {
						sqlArg = 1 // database/sql style: the context comes first
					}
					if len(call.Args) > sqlArg {
						parts := s.fragments(call.Args[sqlArg], map[any]bool{})
						sql := strings.Join(parts, " ")
						table := attributeTable(sql) || attributeTable(strings.Join(parts, ""))
						// FOR UPDATE is a read-side lock, not an UPDATE statement.
						sql = strings.ReplaceAll(sql, "for update", "")
						write := strings.Contains(sql, "insert") || strings.Contains(sql, "update") || strings.Contains(sql, "delete")
						bad = table && write
					}
				}
				if bad {
					violations = append(violations, fset.Position(call.Pos()).String()+": attribute writer bypasses owner")
				}
				return true
			})
		}
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok {
				if fn.Body != nil {
					inspect(fn.Body, fn.Name.Name)
				}
			} else {
				inspect(decl, "<package>")
			}
		}
	})
	sort.Strings(violations)
	return violations, nil
}

// Preserve the existing shared-marker and primitive-entry restrictions even in
// functions whose direct writes are approved above.
func attributeRestrictedReference(file, function, name string) bool {
	switch name {
	case "attrprotocol":
		return !(file == "model/api_attribute_owner.go" && (function == "lockDeviceAttributes" || function == "tryLockDeviceAttributes")) &&
			!(file == "model/api_attribute_orphans.go" && function == "markAttributeOrphanSweep")
	case "markAttributeOrphanSweep", "deleteAttributeOrphanBatch":
		return !(file == "model/api_attribute_orphans.go" && function == "SweepDeviceAttributeOrphans")
	case "deleteEntityAttributesOnTx":
		return !(file == "model/api_delete.go" && function == "deleteEdgeEntity")
	case "upsertAttributeOnTx", "bumpAttributeGenerationTx":
		return !(file == "model/api_attribute_owner.go" && function == "applyAttributePatchTx")
	}
	return false
}

// funcLitReturnsAttribute reports whether a closure returns an attribute-bearing value.
func (s *attributeWriterScan) funcLitReturnsAttribute(lit *ast.FuncLit) bool {
	found := false
	ast.Inspect(lit.Body, func(n ast.Node) bool {
		if _, nested := n.(*ast.FuncLit); nested {
			return false
		}
		if ret, ok := n.(*ast.ReturnStmt); ok {
			for _, value := range ret.Results {
				if s.touches(value) {
					found = true
				}
			}
		}
		return !found
	})
	return found
}
