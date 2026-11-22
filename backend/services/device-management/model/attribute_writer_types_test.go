// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"path"
	"strconv"
	"strings"
)

const attributeModule = "github.com/devicechain-io/dc-device-management"

// The guard needs local signatures, not dependency implementations. Check a
// declaration-only view of EVERY file in a package, with opaque external types.
// This avoids reloading the service's dependency graph in every model test run.
// Bodies and initializers remain in the original AST for the dataflow pass.
// No go/types error is discarded: a broken local type graph fails the guard.
type attributeTypeLoader struct {
	fset     *token.FileSet
	files    map[string][]*ast.File
	packages map[string]*types.Package
	info     *types.Info
	external map[string]*types.Package
	loading  map[string]bool
}

func (l *attributeTypeLoader) Import(importPath string) (*types.Package, error) {
	if _, local := l.files[importPath]; local {
		return l.check(importPath)
	}
	frozen := attributeModule + "/" + attributeWriterFrozenSchema
	if strings.HasPrefix(importPath, attributeModule+"/") && importPath != frozen && !strings.HasPrefix(importPath, frozen+"/") {
		return nil, fmt.Errorf("attribute guard: unresolved local package %q", importPath)
	}
	if p := l.external[importPath]; p != nil {
		return p, nil
	}
	return nil, fmt.Errorf("attribute guard: unresolved import %q", importPath)
}

// Check explicit body type references as well as package signatures. Missing
// local types must fail instead of turning writer arguments into unknown values.
func (s *attributeWriterScan) validateType(expr ast.Expr) error {
	if expr == nil || s.loader.info.TypeOf(expr) != nil {
		return nil
	}
	fields := func(list *ast.FieldList) error {
		if list != nil {
			for _, field := range list.List {
				if err := s.validateType(field.Type); err != nil {
					return err
				}
			}
		}
		return nil
	}
	switch e := expr.(type) {
	case *ast.Ident:
		if e.Obj != nil && e.Obj.Kind == ast.Typ {
			return nil
		}
		if _, ok := s.object(e).(*types.TypeName); ok {
			return nil
		}
	case *ast.SelectorExpr:
		if id, ok := e.X.(*ast.Ident); ok {
			if p := s.imported(id); p != nil {
				if _, local := s.loader.files[p.Path()]; !local {
					return nil // External dependency types are opaque by design.
				}
				if _, ok := p.Scope().Lookup(e.Sel.Name).(*types.TypeName); ok {
					return nil
				}
			}
		}
	case *ast.StarExpr:
		return s.validateType(e.X)
	case *ast.ArrayType:
		return s.validateType(e.Elt)
	case *ast.MapType:
		if err := s.validateType(e.Key); err != nil {
			return err
		}
		return s.validateType(e.Value)
	case *ast.ChanType:
		return s.validateType(e.Value)
	case *ast.Ellipsis:
		return s.validateType(e.Elt)
	case *ast.StructType:
		return fields(e.Fields)
	case *ast.InterfaceType:
		return fields(e.Methods)
	case *ast.FuncType:
		for _, list := range []*ast.FieldList{e.TypeParams, e.Params, e.Results} {
			if err := fields(list); err != nil {
				return err
			}
		}
		return nil
	case *ast.IndexExpr:
		if err := s.validateType(e.X); err != nil {
			return err
		}
		return s.validateType(e.Index)
	case *ast.IndexListExpr:
		if err := s.validateType(e.X); err != nil {
			return err
		}
		for _, arg := range e.Indices {
			if err := s.validateType(arg); err != nil {
				return err
			}
		}
		return nil
	case *ast.ParenExpr:
		return s.validateType(e.X)
	case *ast.UnaryExpr:
		return s.validateType(e.X)
	case *ast.BinaryExpr:
		if err := s.validateType(e.X); err != nil {
			return err
		}
		return s.validateType(e.Y)
	}
	return fmt.Errorf("attribute guard cannot resolve type at %s", s.fset.Position(expr.Pos()))
}

func (l *attributeTypeLoader) check(importPath string) (*types.Package, error) {
	if p := l.packages[importPath]; p != nil {
		return p, nil
	}
	if l.loading[importPath] {
		return nil, fmt.Errorf("attribute guard: import cycle at %s", importPath)
	}
	l.loading[importPath] = true
	defer delete(l.loading, importPath)
	var declarations []*ast.File
	for _, original := range l.files[importPath] {
		file := *original
		file.Decls = nil
		for _, d := range original.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				fn := *d
				file.Decls = append(file.Decls, &fn)
			case *ast.GenDecl:
				decl := *d
				decl.Specs = nil
				for _, s := range d.Specs {
					if v, ok := s.(*ast.ValueSpec); ok {
						value := *v
						value.Values = nil
						if d.Tok == token.CONST {
							// SQL constants are evaluated from their original expressions below.
							value.Type = nil
							for range value.Names {
								value.Values = append(value.Values, &ast.BasicLit{Kind: token.INT, Value: "0"})
							}
						} else if value.Type == nil {
							value.Type = ast.NewIdent("any")
						}
						decl.Specs = append(decl.Specs, &value)
					} else {
						decl.Specs = append(decl.Specs, s)
					}
				}
				file.Decls = append(file.Decls, &decl)
			}
		}
		declarations = append(declarations, &file)
	}
	cfg := types.Config{Importer: l, IgnoreFuncBodies: true, DisableUnusedImportCheck: true}
	p, err := cfg.Check(importPath, l.fset, declarations, l.info)
	if err != nil {
		return nil, fmt.Errorf("attribute guard cannot resolve package %s: %w", importPath, err)
	}
	l.packages[importPath] = p
	return p, nil
}

func newAttributeTypeLoader(fset *token.FileSet, files map[string][]*ast.File) (*attributeTypeLoader, error) {
	l := &attributeTypeLoader{
		fset: fset, files: files, packages: map[string]*types.Package{},
		external: map[string]*types.Package{}, loading: map[string]bool{},
		info: &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}, Scopes: map[ast.Node]*types.Scope{}},
	}
	// External packages are explicit stubs of the types appearing in signatures.
	// Local imports always use the complete local package, never a stub.
	for _, group := range files {
		for _, f := range group {
			imports := map[string]*types.Package{}
			for _, imp := range f.Imports {
				p, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					return nil, err
				}
				name := attributeImportName(p)
				if imp.Name != nil {
					name = imp.Name.Name
				}
				if _, local := files[p]; local {
					continue
				}
				pkg := l.external[p]
				if pkg == nil {
					pkg = types.NewPackage(p, attributeImportName(p))
					l.external[p] = pkg
				}
				imports[name] = pkg
			}
			// Only walk type positions: external functions/values are deliberately
			// outside this declaration-only check, not guessed to be local types.
			var add func(ast.Node)
			add = func(n ast.Node) {
				ast.Inspect(n, func(n ast.Node) bool {
					if iface, ok := n.(*ast.InterfaceType); ok {
						for _, field := range iface.Methods.List {
							if len(field.Names) == 0 {
								add(field.Type)
								ast.Inspect(field.Type, func(n ast.Node) bool {
									if sel, ok := n.(*ast.SelectorExpr); ok {
										if id, ok := sel.X.(*ast.Ident); ok && imports[id.Name] != nil {
											if obj, ok := imports[id.Name].Scope().Lookup(sel.Sel.Name).(*types.TypeName); ok {
												obj.Type().(*types.Named).SetUnderlying(types.NewInterfaceType(nil, nil).Complete())
											}
										}
									}
									return true
								})
							}
						}
					}
					if array, ok := n.(*ast.ArrayType); ok {
						if array.Len != nil {
							ast.Inspect(array.Len, func(n ast.Node) bool {
								if sel, ok := n.(*ast.SelectorExpr); ok {
									if id, ok := sel.X.(*ast.Ident); ok && imports[id.Name] != nil {
										pkg := imports[id.Name]
										if pkg.Scope().Lookup(sel.Sel.Name) == nil {
											// Array sizes do not affect which model a value carries.
											pkg.Scope().Insert(types.NewConst(sel.Pos(), pkg, sel.Sel.Name, types.Typ[types.UntypedInt], constant.MakeInt64(0)))
										}
									}
								}
								return true
							})
						}
						add(array.Elt)
						return false
					}
					arity := 0
					switch indexed := n.(type) {
					case *ast.IndexExpr:
						n, arity = indexed.X, 1
					case *ast.IndexListExpr:
						n, arity = indexed.X, len(indexed.Indices)
					}
					sel, ok := n.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					id, ok := sel.X.(*ast.Ident)
					if !ok || imports[id.Name] == nil {
						return true
					}
					pkg := imports[id.Name]
					if pkg.Scope().Lookup(sel.Sel.Name) == nil {
						obj := types.NewTypeName(sel.Pos(), pkg, sel.Sel.Name, nil)
						named := types.NewNamed(obj, types.NewStruct(nil, nil), nil)
						var params []*types.TypeParam
						for i := 0; i < arity; i++ {
							param := types.NewTypeName(token.NoPos, pkg, fmt.Sprintf("T%d", i), nil)
							params = append(params, types.NewTypeParam(param, types.NewInterfaceType(nil, nil).Complete()))
						}
						named.SetTypeParams(params)
						pkg.Scope().Insert(obj)
					}
					return true
				})
			}
			for _, d := range f.Decls {
				switch d := d.(type) {
				case *ast.FuncDecl:
					add(d.Type)
					if d.Recv != nil {
						add(d.Recv)
					}
				case *ast.GenDecl:
					for _, s := range d.Specs {
						switch s := s.(type) {
						case *ast.TypeSpec:
							add(s.Type)
							if s.TypeParams != nil {
								add(s.TypeParams)
							}
						case *ast.ValueSpec:
							if s.Type != nil && d.Tok != token.CONST {
								add(s.Type)
							}
						}
					}
				}
			}
		}
	}
	for _, p := range l.external {
		p.MarkComplete()
	}
	return l, nil
}

func attributeImportName(p string) string {
	name := path.Base(p)
	if len(name) > 1 && name[0] == 'v' && name[1] >= '0' && name[1] <= '9' {
		name = path.Base(path.Dir(p))
	}
	switch name {
	case "nats.go":
		return "nats"
	case "jwt":
		return "jwt"
	case "paho.mqtt.golang":
		return "mqtt"
	case "graphql-go":
		return "graphql"
	}
	return strings.ReplaceAll(name, "-", "_")
}
