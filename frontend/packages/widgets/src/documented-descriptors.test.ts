// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The runtime descriptors carry no option docs (so a host that only renders boards does not
// download prose it never shows); the documented ones the package publishes get them from
// builtin-option-docs.ts. This is where "every built-in option is documented" -- the rule
// validateWidgetDescriptor applies to an extension -- is enforced for the built-ins.

import { describe, expect, it } from 'vitest';

import { builtinWidgetCatalog as runtimeCatalog, builtinWidgetDescriptors as runtime } from './builtin-descriptors';
import { OPTION_DOCS, OPTION_DOC_OVERRIDES } from './builtin-option-docs';
import { builtinWidgetCatalog, builtinWidgetDescriptors } from './documented-descriptors';
import * as published from './index';

type Specs = Record<string, { doc?: string; item?: Specs }>;

// Every (path, spec) in an option table, list item fields as `key.field`.
function paths(specs: Specs, prefix = ''): [string, { doc?: string; item?: Specs }][] {
  return Object.entries(specs).flatMap(([key, spec]) => [
    [`${prefix}${key}`, spec] as [string, { doc?: string; item?: Specs }],
    ...(spec.item ? paths(spec.item, `${prefix}${key}.`) : []),
  ]);
}

describe('built-in option docs', () => {
  it('leaves the runtime descriptors doc-free, the point of the split', () => {
    for (const [id, d] of Object.entries(runtime)) {
      for (const [path, spec] of paths(d.options as Specs)) {
        expect(spec.doc, `${id}.${path} carries a doc on the runtime descriptor; put it in builtin-option-docs.ts`).toBeUndefined();
      }
    }
  });

  it('documents every option and list item field of every built-in, non-empty', () => {
    for (const [id, d] of Object.entries(builtinWidgetDescriptors)) {
      for (const [path, spec] of paths(d.options as Specs)) {
        expect(spec.doc?.length, `${id}.${path} has no doc in builtin-option-docs.ts`).toBeGreaterThan(0);
      }
    }
  });

  it('keeps no stale entries: every default is used somewhere, every override names a real option', () => {
    const used = new Set<string>();
    for (const [id, d] of Object.entries(runtime)) {
      const own = paths(d.options as Specs).map(([p]) => p);
      own.forEach((p) => used.add(p));
      for (const path of Object.keys(OPTION_DOC_OVERRIDES[id] ?? {})) {
        expect(own, `override ${id}.${path} names no option`).toContain(path);
        expect(OPTION_DOC_OVERRIDES[id][path], `override ${id}.${path} repeats the default`).not.toBe(OPTION_DOCS[path]);
      }
    }
    for (const path of Object.keys(OPTION_DOC_OVERRIDES)) expect(Object.keys(runtime), path).toContain(path);
    for (const path of Object.keys(OPTION_DOCS)) expect(used, `OPTION_DOCS.${path} is used by no widget`).toContain(path);
  });

  it('publishes descriptors equal to the runtime ones plus docs, as fresh objects', () => {
    const strip = (v: unknown): unknown =>
      Array.isArray(v)
        ? v.map(strip)
        : v && typeof v === 'object'
          ? Object.fromEntries(Object.entries(v).filter(([k]) => k !== 'doc').map(([k, x]) => [k, strip(x)]))
          : v;
    for (const id of Object.keys(runtime) as (keyof typeof runtime)[]) {
      expect(strip(builtinWidgetDescriptors[id]), id).toEqual(strip(runtime[id]));
      expect(builtinWidgetDescriptors[id], id).not.toBe(runtime[id]);
      expect(builtinWidgetDescriptors[id].options, id).not.toBe(runtime[id].options);
    }
    expect(builtinWidgetCatalog.list().map((d) => d.id)).toEqual(runtimeCatalog.list().map((d) => d.id));
  });

  it('exports the documented forms from the package entry', () => {
    expect(published.builtinWidgetDescriptors).toBe(builtinWidgetDescriptors);
    expect(published.builtinWidgetCatalog).toBe(builtinWidgetCatalog);
    expect((published.builtinWidgetDescriptors.gauge.options.min as { doc?: string }).doc).toMatch(/gauge scale/);
  });
});
