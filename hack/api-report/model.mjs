// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The public API of one published npm package, as a map of ids to lines of text.
//
//   model = Map<id, string[]>
//
// An id names one element of the surface a consumer can depend on:
//
//   exports:<subpath>        a subpath of package.json `exports` (removing one is a break)
//   <subpath>:<Name>         a name exported by the entry that subpath resolves to
//   ~<file>:<Name>           a declaration that file defines, is not exported by the entry,
//                            and is reachable from something that is (a type in a signature)
//   css:<file>:<--name>      a custom property a shipped stylesheet defines   (brand)
//   token:<path>             a key of a shipped token document                (brand)
//
// and its lines are the declaration as the compiler prints it, one member to a line, with
// every comment removed, so that a reworded doc comment is not an API change.
//
// 🔴 WHY THIS TREE HAS ITS OWN TYPESCRIPT. The frontend pins the native compiler
// (`typescript: ~7.0.2`), which ships no JavaScript API: `createProgram` and the checker do
// not exist in it, which is also why api-extractor, tsup --dts and rollup-plugin-dts all
// crash against that pin (see frontend/scripts/build-package.mjs). Reading a declaration
// file to see what it exports needs the checker, so this tool carries 5.9.3 on its own,
// beside it, and reads the .d.ts the native compiler emitted. Declaration files are a
// stable interchange format; nothing here compiles the sources.

import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs';
import path from 'node:path';
import ts from 'typescript';

const PRINTER = ts.createPrinter({ removeComments: true, newLine: ts.NewLineKind.LineFeed });

/** The nearest package.json above a file: the package a declaration belongs to. */
const nameCache = new Map();
function owningPackage(file) {
  let dir = path.dirname(file);
  const seen = [];
  for (;;) {
    if (nameCache.has(dir)) {
      const hit = nameCache.get(dir);
      for (const s of seen) nameCache.set(s, hit);
      return hit;
    }
    seen.push(dir);
    const manifest = path.join(dir, 'package.json');
    if (existsSync(manifest)) {
      const name = JSON.parse(readFileSync(manifest, 'utf8')).name ?? null;
      for (const s of seen) nameCache.set(s, { name, dir });
      return { name, dir };
    }
    const parent = path.dirname(dir);
    if (parent === dir) return { name: null, dir: null };
    dir = parent;
  }
}

/**
 * A destructured parameter - `({ a, b }: Props)` - is printed with its binding pattern, which
 * is an implementation detail: the names a function body happens to pull out of its argument
 * are not part of its type. Printing it as `props` keeps adding a field a function reads from
 * moving the report. (Ordinary parameter names stay: they read well, and are what a reviewer
 * of an intended change wants to see.)
 */
const hideBindingPatterns = (context) => {
  const visit = (n) => {
    if (ts.isParameter(n) && (ts.isObjectBindingPattern(n.name) || ts.isArrayBindingPattern(n.name))) {
      return context.factory.updateParameterDeclaration(
        n, n.modifiers, n.dotDotDotToken, context.factory.createIdentifier('props'), n.questionToken, n.type, n.initializer,
      );
    }
    return ts.visitEachChild(n, visit, context);
  };
  return (root) => ts.visitNode(root, visit);
};

/** Lines of a printed node, trailing space removed, no blank lines (the printer adds none). */
function printNode(node) {
  const sf = node.getSourceFile();
  const { transformed } = ts.transform(node, [hideBindingPatterns]);
  const text = PRINTER.printNode(ts.EmitHint.Unspecified, transformed[0], sf);
  return text.split('\n').map((l) => l.replace(/\s+$/, '')).filter((l) => l !== '');
}

/** The statement that carries a declaration: a variable's `const` lives on its statement. */
function statementOf(decl) {
  if (ts.isVariableDeclaration(decl) && ts.isVariableDeclarationList(decl.parent) && ts.isVariableStatement(decl.parent.parent)) {
    return decl.parent.parent;
  }
  return decl;
}

const TOP_LEVEL_KINDS = new Set([
  ts.SyntaxKind.InterfaceDeclaration,
  ts.SyntaxKind.TypeAliasDeclaration,
  ts.SyntaxKind.ClassDeclaration,
  ts.SyntaxKind.EnumDeclaration,
  ts.SyntaxKind.FunctionDeclaration,
  ts.SyntaxKind.VariableDeclaration,
  ts.SyntaxKind.ModuleDeclaration,
]);

function isTopLevel(decl) {
  if (!TOP_LEVEL_KINDS.has(decl.kind)) return false;
  const stmt = statementOf(decl);
  return ts.isSourceFile(stmt.parent) || ts.isModuleBlock(stmt.parent);
}

/**
 * workspacePaths(packagesDir): `{ "@devicechain/client": [".../dist/index.d.ts"], ... }` for every
 * typed `exports` entry of every package in the directory, for a compiler `paths` option.
 */
export function workspacePaths(packagesDir) {
  const paths = {};
  for (const e of readdirSync(packagesDir)) {
    const manifest = path.join(packagesDir, e, 'package.json');
    if (!existsSync(manifest)) continue;
    const pkg = JSON.parse(readFileSync(manifest, 'utf8'));
    for (const [subpath, target] of Object.entries(pkg.exports ?? {})) {
      if (typeof target === 'string' || typeof target.types !== 'string') continue;
      const spec = subpath === '.' ? pkg.name : `${pkg.name}${subpath.slice(1)}`;
      paths[spec] = [path.resolve(packagesDir, e, target.types)];
    }
  }
  return paths;
}

/** `Map` keys sorted bytewise, so the order never depends on a locale. */
const byCode = (a, b) => (a < b ? -1 : a > b ? 1 : 0);

/**
 * The declaration surface of ONE entry declaration file, into `model`.
 * `distDir` is the package's declaration directory: declarations outside it belong to
 * another package and are recorded as a re-export, not copied.
 */
function addEntry(model, program, entryFile, subpath, distDir) {
  const checker = program.getTypeChecker();
  const sf = program.getSourceFile(entryFile);
  if (!sf) throw new Error(`the declaration entry ${entryFile} is not part of the program`);
  const moduleSym = checker.getSymbolAtLocation(sf);
  if (!moduleSym) {
    // A declaration file with no import/export is a script, not a module: nothing to depend on.
    throw new Error(`${entryFile} is not a module (it exports nothing); a published entry must export its API`);
  }

  const inDist = (decl) => path.resolve(decl.getSourceFile().fileName).startsWith(path.resolve(distDir) + path.sep);
  const exportedDecls = new Set();
  const queue = []; // declarations to scan for references to unexported local types

  const exports = checker.getExportsOfModule(moduleSym);
  if (exports.length === 0) throw new Error(`${entryFile} exports nothing`);
  for (const sym of exports) {
    const target = sym.flags & ts.SymbolFlags.Alias ? checker.getAliasedSymbol(sym) : sym;
    const id = `${subpath}:${sym.name}`;
    const decls = target.declarations ?? [];
    if (decls.length === 0 || decls.every((d) => !inDist(d))) {
      // Defined in another package (or unresolved): the re-export itself is the API.
      const owner = decls[0] ? owningPackage(decls[0].getSourceFile().fileName).name : null;
      model.set(id, [`re-export of ${target.name} from ${owner ?? 'an unresolved module'}`]);
      continue;
    }
    const lines = [];
    for (const d of decls) {
      if (!inDist(d)) continue;
      exportedDecls.add(d);
      lines.push(...printNode(statementOf(d)));
      queue.push(d);
    }
    // A local alias `export { A as B }` prints A's declaration; say which name it is served as.
    if (target.name !== sym.name) lines.unshift(`// served as ${sym.name}`);
    model.set(id, lines);
  }

  // Unexported declarations a signature reaches, transitively. A change inside one is a
  // change to the API even though nothing exports it by name.
  while (queue.length > 0) {
    const node = queue.pop();
    const visit = (n) => {
      if (ts.isIdentifier(n)) {
        let s = checker.getSymbolAtLocation(n);
        if (s && s.flags & ts.SymbolFlags.Alias) {
          try {
            s = checker.getAliasedSymbol(s);
          } catch {
            s = undefined;
          }
        }
        for (const d of s?.declarations ?? []) {
          if (!inDist(d) || !isTopLevel(d) || exportedDecls.has(d)) continue;
          exportedDecls.add(d);
          const rel = path.relative(distDir, d.getSourceFile().fileName).split(path.sep).join('/').replace(/\.d\.ts$/, '');
          const name = d.name?.getText?.() ?? s.name;
          model.set(`~${rel}:${name}`, (model.get(`~${rel}:${name}`) ?? []).concat(printNode(statementOf(d))));
          queue.push(d);
        }
      }
      ts.forEachChild(n, visit);
    };
    visit(node);
  }
}

/** Every key path of a JSON document, `a.b.c`, objects only (a leaf's value is not API). */
function keyPaths(value, prefix = '', out = []) {
  if (value && typeof value === 'object' && !Array.isArray(value)) {
    for (const [k, v] of Object.entries(value)) {
      if (k.startsWith('$')) continue; // $license, $about: commentary, not data
      const p = prefix ? `${prefix}.${k}` : k;
      out.push(p);
      keyPaths(v, p, out);
    }
  }
  return out;
}

/** The custom properties a stylesheet defines (`--name:`), comments removed first. */
function cssCustomProperties(text) {
  const bare = text.replace(/\/\*[\s\S]*?\*\//g, '');
  return [...new Set([...bare.matchAll(/(--[\w-]+)\s*:/g)].map((m) => m[1]))].sort(byCode);
}

/**
 * model(pkgDir): the API of the package in `pkgDir` as built there.
 * Throws when a declared entry has no declaration file: a report over a package that was
 * not built would be an empty one, and an empty report reads as "nothing exported".
 */
export function buildModel(pkgDir, paths = {}) {
  const manifest = JSON.parse(readFileSync(path.join(pkgDir, 'package.json'), 'utf8'));
  const model = new Map();
  const exportsMap = manifest.exports ?? {};

  const typed = [];
  for (const [subpath, target] of Object.entries(exportsMap)) {
    const cond = typeof target === 'string' ? { default: target } : target;
    // Sorted keys: the order conditions are written in is not API; which conditions exist is.
    model.set(
      `exports:${subpath}`,
      Object.keys(cond).sort(byCode).map((k) => `${k}: ${cond[k]}`),
    );
    if (typeof cond.types === 'string') typed.push({ subpath, types: path.resolve(pkgDir, cond.types) });

    const file = typeof target === 'string' ? target : target.default ?? target.import;
    if (typeof file === 'string' && file.endsWith('.css')) {
      const abs = path.resolve(pkgDir, file);
      if (!existsSync(abs)) throw new Error(`${manifest.name}: exports["${subpath}"] names ${file}, which is not there`);
      for (const prop of cssCustomProperties(readFileSync(abs, 'utf8'))) model.set(`css:${subpath}:${prop}`, []);
    }
    if (typeof file === 'string' && file.endsWith('.json') && subpath !== './package.json') {
      const abs = path.resolve(pkgDir, file);
      if (!existsSync(abs)) throw new Error(`${manifest.name}: exports["${subpath}"] names ${file}, which is not there`);
      for (const p of keyPaths(JSON.parse(readFileSync(abs, 'utf8')))) model.set(`token:${subpath}:${p}`, []);
    }
  }

  if (typed.length > 0) {
    for (const t of typed) {
      if (!existsSync(t.types)) {
        throw new Error(`${manifest.name}: exports["${t.subpath}"].types names ${path.relative(pkgDir, t.types)}, which is not there; build the packages first (npm run build:packages)`);
      }
    }
    const distDir = path.dirname(typed[0].types);
    const program = ts.createProgram({
      rootNames: typed.map((t) => t.types),
      options: {
        target: ts.ScriptTarget.ES2020,
        module: ts.ModuleKind.ESNext,
        moduleResolution: ts.ModuleResolutionKind.Bundler,
        jsx: ts.JsxEmit.ReactJSX,
        skipLibCheck: true,
        noEmit: true,
        strict: true,
        types: [],
        // The workspace's own packages, by name: a baseline tree resolves its siblings to the
        // BASELINE's declarations, not to whatever node_modules links to.
        paths,
      },
    });
    for (const t of typed) addEntry(model, program, t.types, t.subpath, distDir);
  }
  return model;
}

/** Recursively list files (used only to prove a directory was read). */
export function listFiles(dir) {
  const out = [];
  for (const e of readdirSync(dir)) {
    const p = path.join(dir, e);
    if (statSync(p).isDirectory()) out.push(...listFiles(p));
    else out.push(p);
  }
  return out;
}

/** The committed report for one package: markdown, one fenced block per id. */
export function renderReport(name, model) {
  const out = [];
  out.push(`# API report: ${name}`);
  out.push('');
  out.push('<!-- GENERATED by hack/api-report; do not edit. After an intended change to the public API run');
  out.push('     hack/check-npm-api.sh --update and commit the result: this diff is the review of that change. -->');
  out.push('');
  for (const id of [...model.keys()].sort(byCode)) {
    out.push(`## \`${id}\``);
    const lines = model.get(id);
    if (lines.length > 0) {
      out.push('');
      out.push('```ts');
      out.push(...lines);
      out.push('```');
    }
    out.push('');
  }
  return out.join('\n');
}
