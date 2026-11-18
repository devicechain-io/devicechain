// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { npmCommand, runSync } from './launch.mjs';

const frontend = fileURLToPath(new URL('../', import.meta.url));

// Inspect npm's inventory, so an allowlist entry pointing to a missing file also fails.
for (const name of ['client', 'widget-sdk']) {
  test(`${name} publishes its agent guidance`, () => {
    const cwd = path.join(frontend, 'packages', name);
    const result = runSync(npmCommand(['pack', '--dry-run', '--json', '--ignore-scripts']), {
      cwd,
      encoding: 'utf8',
    });
    assert.equal(result.status, 0, result.stderr);
    const reports = JSON.parse(result.stdout);
    assert.equal(reports.length, 1);
    assert.equal(reports[0].name, `@devicechain/${name}`);
    assert.ok(reports[0].files.some(({ path }) => path === 'AGENTS.md'),
      'AGENTS.md must reach consumers in the npm tarball');
    assert.match(readFileSync(path.join(cwd, 'AGENTS.md'), 'utf8'), /Rules that save time/);
  });
}
