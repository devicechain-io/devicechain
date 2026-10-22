// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// catalogUse is one place a service asks the broker layer for something the stream and bucket
// declarations (core/streams, core/kv) describe: the area it runs in, what it asks for, and
// the stream suffix or bucket it asks for.
type catalogUse struct {
	Area string // the service directory, which is the functional area
	Role string // write | read | replay | live | bucket
	Name string // a stream suffix, or a logical bucket name
	Pos  string // file:line, for the message
}

// Role names, shared with the test that reads the declarations.
const (
	useWrite  = "write"
	useRead   = "read"
	useReplay = "replay"
	useLive   = "live"
	useBucket = "bucket"
)

// useCall says how a NatsManager method call is read: which role it is, which argument names
// the stream or bucket, and, for a method that fixes the bucket itself, which one (arg -1).
type useCall struct {
	role   string
	arg    int
	bucket string
}

// useMethods are the methods through which a service reaches a stream or a bucket, matched by
// NAME like the read-loop guard next door, and with the same limit: a differently-named
// wrapper would not be seen. Every one is a NatsManager method, so the catalog (messaging's
// catalog.go) refuses an undeclared call at start; this scan holds the same calls to the
// declaration from the other side, so a declared role nothing uses is found too.
var useMethods = map[string]useCall{
	"NewWriter":                    {role: useWrite},
	"NewOrderedWriter":             {role: useWrite},
	"NewAsyncWriter":               {role: useWrite},
	"NewReader":                    {role: useRead},
	"NewReplayReader":              {role: useReplay},
	"NewReplayReaderFromTime":      {role: useReplay},
	"NewRangeReader":               {role: useReplay},
	"SubscribeLive":                {role: useLive, arg: 2},
	"KeyValueStore":                {role: useBucket},
	"NewCache":                     {role: useBucket},
	"NewDistributedLease":          {role: useBucket, arg: -1, bucket: "dc_leases"},
	"NewDistributedLock":           {role: useBucket, arg: -1, bucket: "dc_locks"},
	"CredentialAttemptStore":       {role: useBucket, arg: -1, bucket: "dc_credential_attempts"},
	"DeviceCredentialAttemptStore": {role: useBucket, arg: -1, bucket: "dc_device_credential_attempts"},
}

// scanCatalogUses reads every production Go file under servicesDir/<area>/ and returns each
// use of useMethods it can resolve to a stream suffix or bucket name.
//
// unresolved lists the calls whose argument it could NOT resolve and that are not in dynamic:
// a stream chosen at run time. They are returned rather than skipped, because a scan that
// quietly drops what it cannot read reports "nothing undeclared" for exactly the call nobody
// could check. dynamic maps "<file relative to servicesDir>#<method>" to what that call site
// is known to open; aliases maps "pkg.Const" to the bucket it names.
func scanCatalogUses(servicesDir, streamsFile, kvFile string, dynamic map[string][]string,
	aliases map[string]string) (uses []catalogUse, unresolved []string, usedDynamic map[string]bool, err error) {
	usedDynamic = map[string]bool{}
	streamConsts, err := stringConsts(streamsFile)
	if err != nil {
		return nil, nil, nil, err
	}
	kvConsts, err := stringConsts(kvFile)
	if err != nil {
		return nil, nil, nil, err
	}
	areas, err := os.ReadDir(servicesDir)
	if err != nil {
		return nil, nil, nil, err
	}
	scanned := 0
	for _, a := range areas {
		if !a.IsDir() {
			continue
		}
		area := a.Name()
		root := filepath.Join(servicesDir, area)
		pkgConsts := map[string]map[string]string{} // dir -> const name -> value
		fset := token.NewFileSet()
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
			if werr != nil {
				return werr
			}
			if d.IsDir() {
				if path != root && skipDirInScan(d.Name()) {
					return fs.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
				return nil
			}
			f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if perr != nil {
				return fmt.Errorf("parsing %s: %w", path, perr)
			}
			scanned++
			dir := filepath.Dir(path)
			if pkgConsts[dir] == nil {
				c, cerr := dirStringConsts(dir, streamConsts, kvConsts)
				if cerr != nil {
					return cerr
				}
				pkgConsts[dir] = c
			}
			imports := importNames(f)
			rel, _ := filepath.Rel(servicesDir, path)
			rel = filepath.ToSlash(rel)
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				uc, ok := useMethods[sel.Sel.Name]
				if !ok {
					return true
				}
				// bufio.NewReader, bytes.NewReader, tabwriter.NewWriter...: a function of an
				// imported package, not a method of the manager.
				if id, isIdent := sel.X.(*ast.Ident); isIdent && imports[id.Name] {
					return true
				}
				pos := fset.Position(call.Pos())
				where := fmt.Sprintf("%s:%d", rel, pos.Line)
				if uc.arg == -1 {
					uses = append(uses, catalogUse{area, uc.role, uc.bucket, where})
					return true
				}
				if uc.arg >= len(call.Args) {
					return true // not the manager's method of that name
				}
				name, resolved := resolveName(call.Args[uc.arg], pkgConsts[dir], streamConsts, kvConsts, aliases)
				if resolved {
					uses = append(uses, catalogUse{area, uc.role, name, where})
					return true
				}
				if names, ok := dynamic[rel+"#"+sel.Sel.Name]; ok {
					usedDynamic[rel+"#"+sel.Sel.Name] = true
					for _, nm := range names {
						uses = append(uses, catalogUse{area, uc.role, nm, where})
					}
					return true
				}
				unresolved = append(unresolved, fmt.Sprintf("%s: %s(%s)", where, sel.Sel.Name, exprString(call.Args[uc.arg])))
				return true
			})
			return nil
		})
		if err != nil {
			return nil, nil, nil, err
		}
	}
	if scanned == 0 {
		return nil, nil, nil, fmt.Errorf("no Go files under %s, so the scan asserts nothing", servicesDir)
	}
	return uses, unresolved, usedDynamic, nil
}

func importNames(f *ast.File) map[string]bool {
	out := map[string]bool{}
	for _, im := range f.Imports {
		p, _ := strconv.Unquote(im.Path.Value)
		name := p[strings.LastIndex(p, "/")+1:]
		if im.Name != nil {
			name = im.Name.Name
		}
		out[name] = true
	}
	return out
}

func resolveName(e ast.Expr, local, streamConsts, kvConsts map[string]string, aliases map[string]string) (string, bool) {
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind == token.STRING {
			s, err := strconv.Unquote(x.Value)
			return s, err == nil
		}
	case *ast.Ident:
		v, ok := local[x.Name]
		return v, ok
	case *ast.SelectorExpr:
		if id, ok := x.X.(*ast.Ident); ok {
			switch id.Name {
			case "streams":
				v, ok := streamConsts[x.Sel.Name]
				return v, ok
			case "kv":
				v, ok := kvConsts[x.Sel.Name]
				return v, ok
			}
			if v, ok := aliases[id.Name+"."+x.Sel.Name]; ok {
				return v, true
			}
		}
	}
	return "", false
}

func exprString(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return exprString(x.X) + "." + x.Sel.Name
	case *ast.CallExpr:
		return exprString(x.Fun) + "(...)"
	case *ast.BasicLit:
		return x.Value
	default:
		return fmt.Sprintf("%T", e)
	}
}

// stringConsts reads a file's top-level string constants, folding the two shapes the
// declarations use: a literal, and another constant joined to a literal with "+".
func stringConsts(path string) (map[string]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	collectConsts(f, out, nil, nil)
	if len(out) == 0 {
		return nil, fmt.Errorf("%s declares no string constants, so nothing could be resolved against it", path)
	}
	return out, nil
}

// dirStringConsts reads every production file of one package directory, resolving a constant
// that is defined as streams.X or kv.X to its value.
func dirStringConsts(dir string, streamConsts, kvConsts map[string]string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, e.Name()), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		collectConsts(f, out, streamConsts, kvConsts)
	}
	return out, nil
}

func collectConsts(f *ast.File, out, streamConsts, kvConsts map[string]string) {
	for pass := 0; pass < 2; pass++ { // a second pass settles constants that name later ones
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, sp := range gd.Specs {
				vs := sp.(*ast.ValueSpec)
				for i, n := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					if v, ok := foldString(vs.Values[i], out, streamConsts, kvConsts); ok {
						out[n.Name] = v
					}
				}
			}
		}
	}
}

func foldString(e ast.Expr, local, streamConsts, kvConsts map[string]string) (string, bool) {
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind == token.STRING {
			s, err := strconv.Unquote(x.Value)
			return s, err == nil
		}
	case *ast.Ident:
		v, ok := local[x.Name]
		return v, ok
	case *ast.SelectorExpr:
		if id, ok := x.X.(*ast.Ident); ok {
			switch id.Name {
			case "streams":
				v, ok := streamConsts[x.Sel.Name]
				return v, ok
			case "kv":
				v, ok := kvConsts[x.Sel.Name]
				return v, ok
			}
		}
	case *ast.BinaryExpr:
		if x.Op == token.ADD {
			l, lok := foldString(x.X, local, streamConsts, kvConsts)
			r, rok := foldString(x.Y, local, streamConsts, kvConsts)
			return l + r, lok && rok
		}
	}
	return "", false
}
