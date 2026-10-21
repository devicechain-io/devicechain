// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// @vitest-environment node

// No central built-in-ID dispatch.
//
// A built-in is a registration like any extension's, so the runtime (renderer, channel
// adapters, registry) may branch on a widget's CHANNEL, binding policy and implementation
// kind -- never on its id. The failure this guards is quiet: an `if (widget.type === 'gauge')`
// or an id-keyed component table works for the eleven built-ins and silently fails for the
// twelfth, which an extension package supplies. So the SOURCE is scanned.
//
// Where an id may appear is an explicit allow-list, not a pattern the scan learns: the
// descriptors file DECLARES the ids as data, and each widget module looks up its own
// descriptor once. Everything else in this package and the SDK is held to zero.
/// <reference types="node" />

import { readdirSync, readFileSync } from 'node:fs';
import { dirname, join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';

import { WIDGET_TYPES } from '@devicechain/dashboards';
import { describe, expect, it } from 'vitest';

const SRC = dirname(fileURLToPath(import.meta.url));
const ROOTS = [SRC, join(SRC, '..', '..', 'widget-sdk', 'src')];

// Files that legitimately name a built-in id. builtin-descriptors.ts declares them; builtin-option-docs.ts
// is the same descriptor DATA kept apart (a widget's option wording where it differs from the shared
// one), looked up by the id of the descriptor being documented and never branched on.
const DECLARES_IDS = ['builtin-descriptors.ts', 'builtin-option-docs.ts'];

// The only id-keyed tables that may exist, and only as READ-ONLY views derived from the
// descriptors. No other source may mention them: a renderer reaching for one is dispatch.
const DERIVED_VIEWS = /\b(WIDGET_CHANNEL|WIDGET_BINDS_DATASOURCE|WIDGET_OPTIONS|WIDGET_TYPES|WIDGET_CONSTRAINTS)\b/;
const VIEW_HOLDERS = ['registry.ts', 'options.ts', 'index.ts'];

// The removed per-channel component tables must not come back in any form.
const COMPONENT_TABLES = /\b(?:ALARM_|CONTROL_|SELECTION_|LOCATION_)?WIDGET_REGISTRY\b/;

function stripComments(source: string): string {
  const blank = (m: string) => m.replace(/[^\n]/g, ' ');
  return source.replace(/\/\*[\s\S]*?\*\//g, blank).replace(/(^|[^:])\/\/.*$/gm, (m, p1) => p1 + blank(m.slice(p1.length)));
}

function sources(): Array<{ file: string; code: string }> {
  const out: Array<{ file: string; code: string }> = [];
  const walk = (dir: string) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const full = join(dir, entry.name);
      if (entry.isDirectory()) {
        if (entry.name === 'typetests') continue;
        walk(full);
      } else if (/\.tsx?$/.test(entry.name) && !/\.(test|typetest)\.tsx?$/.test(entry.name)) {
        out.push({ file: relative(join(SRC, '..', '..'), full).split('\\').join('/'), code: stripComments(readFileSync(full, 'utf8')) });
      }
    }
  };
  for (const root of ROOTS) walk(root);
  return out;
}

// Escapes every regex metacharacter (backslash included), not just the `-` today's ids use.
const escapeRegExp = (s: string) => s.replace(/[\\^$.*+?()[\]{}|/-]/g, '\\$&');
const quoted = (id: string) => new RegExp(`['"\`]${escapeRegExp(id)}['"\`]`);

// What a widget module may write: its own `builtinWidgetDescriptors[<id>]` lookup.
const OWN_DESCRIPTOR_LOOKUP = /builtinWidgetDescriptors\[\s*['"][a-z-]+['"]\s*\]/g;

// Returns the violations found in one file's comment-stripped code.
function dispatchViolations(file: string, code: string): string[] {
  const base = file.split('/').pop() ?? file;
  const found: string[] = [];
  if (COMPONENT_TABLES.test(code)) found.push('references a per-channel component table');
  if (!VIEW_HOLDERS.includes(base) && !DECLARES_IDS.includes(base) && DERIVED_VIEWS.test(code)) {
    found.push('reads a derived by-id view (WIDGET_CHANNEL/OPTIONS/...) instead of the registry');
  }
  // The chart helpers speak ECharts' vocabulary (a series of type 'gauge'), which is not a widget id.
  if (!DECLARES_IDS.includes(base) && !file.startsWith('widget-sdk/src/chart/')) {
    const scrubbed = file.includes('/widgets/') ? code.replace(OWN_DESCRIPTOR_LOOKUP, '') : code;
    for (const id of WIDGET_TYPES) {
      if (quoted(id).test(scrubbed)) found.push(`names the built-in id '${id}'`);
    }
  }
  return found;
}

describe('no central built-in-ID dispatch in widgets/src or widget-sdk/src', () => {
  // The reach control: an absence claim over an empty or mis-rooted scan passes vacuously.
  it('scans the renderer, the adapters, the registry and every widget', () => {
    const files = sources().map((s) => s.file);
    for (const needed of [
      'widgets/src/connected-widget.tsx',
      'widgets/src/dashboard-renderer.tsx',
      'widgets/src/binding-policy.ts',
      'widgets/src/registry.ts',
      'widgets/src/builtin-registry.ts',
      'widgets/src/widgets/gauge.tsx',
      'widgets/src/widgets/map.tsx',
      'widget-sdk/src/registry.ts',
      'widget-sdk/src/hooks.ts',
    ]) {
      expect(files, `${needed} is not being scanned`).toContain(needed);
    }
  });

  // Negative control: the scanner flags each shape of dispatch it exists to catch, so a green
  // scan below means the code is clean, not that the pattern stopped matching.
  describe('the scanner', () => {
    it.each([
      ['an id comparison', "if (widget.type === 'gauge') return 1;"],
      ['a switch case', "switch (w.type) { case 'alarm-count': break; }"],
      ['an id-keyed object', "const sizes = { 'alarm-count': 50 };"],
      ['a derived view read', 'const c = WIDGET_CHANNEL[widget.type];'],
      ['a revived component table', 'const C = ALARM_WIDGET_REGISTRY[type];'],
    ])('flags %s', (_name, code) => {
      expect(dispatchViolations('widgets/src/connected-widget.tsx', code)).not.toEqual([]);
    });

    it('does not flag a widget module looking up its own descriptor', () => {
      expect(dispatchViolations('widgets/src/widgets/gauge.tsx', "descriptor: builtinWidgetDescriptors['gauge'],")).toEqual(
        [],
      );
    });

    it('flags that same lookup outside a widget module', () => {
      expect(dispatchViolations('widgets/src/connected-widget.tsx', "builtinWidgetDescriptors['gauge']")).not.toEqual([]);
    });

    // The ECharts exemption is the chart helpers' directory alone: the rest of the SDK (its
    // registry, hooks, frame) is held to zero like this package.
    it('holds the SDK outside its chart helpers to the same rule', () => {
      for (const file of ['widget-sdk/src/registry.ts', 'widget-sdk/src/hooks.ts', 'widget-sdk/src/echarts.ts']) {
        expect(dispatchViolations(file, "if (widget.type === 'gauge') return 1;"), file).not.toEqual([]);
      }
      expect(dispatchViolations('widget-sdk/src/chart/chart-options.ts', "series: [{ type: 'gauge' }]")).toEqual([]);
    });
  });

  it('holds every source to zero violations', () => {
    const report = sources().flatMap(({ file, code }) => dispatchViolations(file, code).map((v) => `${file}: ${v}`));
    expect(report).toEqual([]);
  });

  it('lets each widget module register exactly its own id', () => {
    const widgetFiles = sources().filter((s) => /widgets\/src\/widgets\/[a-z-]+\.tsx$/.test(s.file));
    const registered = widgetFiles.flatMap((s) =>
      [...s.code.matchAll(/builtinWidgetDescriptors\[\s*'([a-z-]+)'\s*\]/g)].map((m) => m[1]),
    );
    // One registration per built-in, none shared or missing.
    expect([...registered].sort()).toEqual([...WIDGET_TYPES].sort());
    // ...and each through the public defineWidget. It is an identity function, so a module
    // that built the object by hand would register identically at runtime; only the source
    // shows the built-ins took the third-party path.
    const viaDefineWidget = widgetFiles.flatMap((s) =>
      [...s.code.matchAll(/defineWidget\(\{\s*descriptor:\s*builtinWidgetDescriptors\[\s*'([a-z-]+)'\s*\]/g)].map(
        (m) => m[1],
      ),
    );
    expect([...viaDefineWidget].sort()).toEqual([...WIDGET_TYPES].sort());
  });
});
