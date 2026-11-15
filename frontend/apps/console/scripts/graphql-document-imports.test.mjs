// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { expect, it } from 'vitest';
import { build } from 'vite';
import path from 'node:path';

// Exercise the production config: ordinary API tests use the development lookup
// helper and would pass even if the optimizer stopped running in production.
it('ships the API and its generated documents without the runtime lookup table', async () => {
  const result = await build({
    configFile: path.resolve('vite.config.ts'),
    logLevel: 'silent',
    build: {
      write: false,
      minify: false,
      rolldownOptions: {
        input: path.resolve('src/lib/api/ai-inference-admin.ts'),
        preserveEntrySignatures: 'strict',
      },
    },
  });
  const code = result.output.filter((entry) => entry.type === 'chunk').map((entry) => entry.code).join('\n');
  expect(code).toContain('function listAiProviders(');
  expect(code).toContain('query AiProviders($criteria: AiProviderSearchCriteria!)');
  expect(code).not.toMatch(/function graphql[\w$]*\(/);
}, 30_000);
