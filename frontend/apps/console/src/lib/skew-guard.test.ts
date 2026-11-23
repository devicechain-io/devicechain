// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

/// <reference types="node" />

// The "never refuses its own instance" guard for schema negotiation. Negotiation refuses any
// operation the connected instance's schema cannot serve, so a console document that does not
// validate against the SDL its own release serves would be refused by a matching instance. This
// runs EVERY generated console document through the real client (gql -> OPTIONS discovery ->
// check) against the SDL the repository serves for its area, and expects every one to be sent.

import { readFileSync, readdirSync } from 'node:fs';
import path from 'node:path';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  disableSchemaNegotiation,
  enableSchemaNegotiation,
  gql,
  resetSchemaNegotiation,
  setAuthTokenGetter,
  type TypedDocument,
} from '@devicechain/client';

const consoleRoot = path.resolve(__dirname, '../..');

// Each generated client directory and the schema file it is generated from, read from codegen.ts
// so a new area is covered the day it is added.
function areaSchemas(): Map<string, string> {
  const config = readFileSync(path.join(consoleRoot, 'codegen.ts'), 'utf8');
  const out = new Map<string, string>();
  for (const m of config.matchAll(/'\.\/src\/gql\/([^/']+)\/':\s*\{\s*schema:\s*'([^']+)'/g)) {
    out.set(m[1], path.resolve(consoleRoot, m[2]));
  }
  return out;
}

// The documents the generated client carries: the keys of its `documents` map.
function generatedDocuments(area: string): string[] {
  const source = readFileSync(path.join(consoleRoot, 'src/gql', area, 'gql.ts'), 'utf8');
  const block = source.slice(source.indexOf('const documents: Documents = {'));
  const docs: string[] = [];
  for (const m of block.matchAll(/^\s+("(?:[^"\\]|\\.)*"):\s*types\./gm)) docs.push(JSON.parse(m[1]) as string);
  // A fragment-only entry is never sent on its own (operations pull it in), so it is not an operation.
  return docs.filter((d) => /^\s*(query|mutation|subscription)\b/m.test(d));
}

beforeEach(() => {
  setAuthTokenGetter(async () => 'tok');
  enableSchemaNegotiation();
});

afterEach(() => {
  disableSchemaNegotiation();
  setAuthTokenGetter(null);
  vi.unstubAllGlobals();
});

describe('every generated console document is served by the schema its release serves', () => {
  const schemas = areaSchemas();

  it('finds the codegen areas and a generated client for each', () => {
    const generated = readdirSync(path.join(consoleRoot, 'src/gql')).filter((d) => !d.includes('.'));
    expect([...schemas.keys()].sort()).toEqual(generated.sort());
    expect(schemas.size).toBeGreaterThanOrEqual(10);
  });

  for (const [area, schemaPath] of schemas) {
    it(`${area}: all documents pass negotiation against ${path.basename(path.dirname(path.dirname(schemaPath)))}`, async () => {
      const sdl = readFileSync(schemaPath, 'utf8');
      const sent: string[] = [];
      vi.stubGlobal(
        'fetch',
        vi.fn(async (_url: string, init: RequestInit) => {
          if (init.method === 'OPTIONS') return new Response(JSON.stringify({ format: 1, schema: sdl }), { status: 200 });
          sent.push((JSON.parse(init.body as string) as { query: string }).query);
          return new Response(JSON.stringify({ data: {} }), { status: 200 });
        }),
      );
      resetSchemaNegotiation();
      const documents = generatedDocuments(area);
      const lookup = ((await import(`../gql/${area}/gql.ts`)) as { graphql: (source: string) => unknown }).graphql;
      expect(documents.length, `no documents found for ${area}`).toBeGreaterThan(0);
      const refused: string[] = [];
      for (const document of documents) {
        // Variables are left out: this is the document check. Required-variable coverage is the
        // server's, and a refused variable here would be the console's own bug, not skew.
        // graphql(source) is the generated TypedDocumentString: its toString() is the text the
        // console sends, fragments appended.
        const typed = lookup(document) as TypedDocument<unknown, Record<string, never>>;
        await gql('device-management', typed).catch((err: Error) => refused.push(`${err.message}`));
      }
      expect(refused).toEqual([]);
      expect(sent).toHaveLength(documents.length);
    });
  }

  // Negative control: the same harness DOES refuse a document the schema does not serve, so the
  // loop above cannot pass by never checking anything.
  it('refuses a document the schema does not serve', async () => {
    const sdl = readFileSync([...areaSchemas().values()][0], 'utf8');
    vi.stubGlobal(
      'fetch',
      vi.fn(async (_url: string, init: RequestInit) =>
        init.method === 'OPTIONS'
          ? new Response(JSON.stringify({ format: 1, schema: sdl }), { status: 200 })
          : new Response(JSON.stringify({ data: {} }), { status: 200 }),
      ),
    );
    const bogus = 'query { thisFieldDoesNotExist }' as unknown as TypedDocument<unknown, Record<string, never>>;
    await expect(gql('device-management', bogus)).rejects.toMatchObject({ kind: 'unsupported' });
  });
});
