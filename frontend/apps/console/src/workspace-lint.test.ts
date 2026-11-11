// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { globSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

// Read the root's patterns, rather than keeping a second workspace list or count.
// Both tests call this computation so replacing it with [] breaks the fixture.
function missingLintScripts(root: string): string[] {
  const { workspaces } = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8')) as {
    workspaces: string[];
  };
  const manifests = globSync(workspaces.map((pattern) => `${pattern}/package.json`), { cwd: root });
  return [...new Set(manifests)].flatMap((manifest) => {
    const { name, scripts } = JSON.parse(readFileSync(join(root, manifest), 'utf8')) as {
      name: string;
      scripts?: { lint?: unknown };
    };
    return typeof scripts?.lint === 'string' && scripts.lint.trim() ? [] : [name];
  }).sort();
}

describe('workspace lint coverage', () => {
  it('every actual frontend workspace supplies a nonempty lint script', () => {
    // npm runs this workspace's Vitest from apps/console. jsdom gives
    // import.meta.url an HTTP origin, so use the filesystem working directory.
    const root = resolve(process.cwd(), '../..');
    expect(missingLintScripts(root), 'workspaces missing a nonempty lint script').toEqual([]);
  });

  it('reports fixture package names for absent, empty and whitespace scripts', () => {
    const root = mkdtempSync(join(tmpdir(), 'devicechain-workspace-lint-'));
    const writeJson = (relative: string, value: unknown) => {
      const target = join(root, relative);
      mkdirSync(dirname(target), { recursive: true });
      writeFileSync(target, JSON.stringify(value));
    };
    try {
      writeJson('package.json', { workspaces: ['apps/*', 'packages/*'] });
      writeJson('apps/ok/package.json', { name: '@fixture/ok', scripts: { lint: 'eslint src' } });
      writeJson('apps/absent/package.json', { name: '@fixture/absent' });
      writeJson('packages/missing/package.json', { name: '@fixture/missing', scripts: { test: 'vitest' } });
      writeJson('packages/empty/package.json', { name: '@fixture/empty', scripts: { lint: '' } });
      writeJson('packages/blank/package.json', { name: '@fixture/blank', scripts: { lint: ' \t ' } });
      writeJson('outside/ignored/package.json', { name: '@fixture/outside' });
      mkdirSync(join(root, 'packages/not-a-package'), { recursive: true });
      expect(missingLintScripts(root)).toEqual([
        '@fixture/absent', '@fixture/blank', '@fixture/empty', '@fixture/missing',
      ]);
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });
});
