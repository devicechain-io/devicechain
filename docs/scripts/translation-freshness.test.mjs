// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, readFile, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { test } from 'node:test';
import { checkFreshness, hashText, pagePath } from './check-translation-freshness.mjs';

async function fixture(t) {
  const tempRoot = path.resolve(tmpdir());
  const root = await mkdtemp(path.join(tempRoot, 'dc-translation-test-'));
  t.after(async () => {
    const target = path.resolve(root);
    if (path.dirname(target) !== tempRoot || !path.basename(target).startsWith('dc-translation-test-')) {
      throw new Error('Refusing fixture cleanup outside its temporary directory');
    }
    await rm(target, { recursive: true, force: true });
  });
  const source = path.join(root, 'docs');
  const locale = path.join(root, 'i18n', 'zh-CN');
  const translated = path.join(locale, 'docusaurus-plugin-content-docs', 'current');
  await mkdir(source, { recursive: true });
  await mkdir(translated, { recursive: true });
  const manifest = path.join(locale, 'source-manifest.json');
  await writeFile(manifest, JSON.stringify({ schemaVersion: 1, locale: 'zh-CN', pages: {} }));
  await writeFile(path.join(source, 'intro.md'), '# Introduction\n');
  await writeFile(path.join(translated, 'intro.md'), '# 简介\n');
  return { root, source, translated, manifest };
}

test('line-ending changes do not mark a translation stale', () => {
  assert.equal(hashText('a\r\nb\r\n'), hashText('a\nb\n'));
});

test('only relative Markdown page paths can be recorded', () => {
  for (const value of ['../intro.md', '/intro.md', 'C:\\intro.md', 'concepts/../intro.md', 'intro.json', './intro.md']) {
    assert.throws(() => pagePath(value));
  }
  assert.equal(pagePath('concepts/architecture.md'), 'concepts/architecture.md');
});

test('freshness notices distinguish English edits from translation edits', async t => {
  const f = await fixture(t);
  assert.deepEqual((await checkFreshness(f.root, 'zh-CN')).issues, [{ page: 'intro.md', status: 'not recorded' }]);
  assert.deepEqual((await checkFreshness(f.root, 'zh-CN', ['intro.md'])).issues, []);
  await writeFile(path.join(f.source, 'intro.md'), '# Updated introduction\n');
  assert.deepEqual((await checkFreshness(f.root, 'zh-CN')).issues, [{ page: 'intro.md', status: 'English changed' }]);
  await writeFile(path.join(f.translated, 'intro.md'), '# 更新后的简介\n');
  assert.equal((await checkFreshness(f.root, 'zh-CN')).issues.length, 2);
  assert.deepEqual((await checkFreshness(f.root, 'zh-CN', ['intro.md'])).issues, []);
});

test('an invalid batch never records its earlier valid page', async t => {
  const f = await fixture(t);
  const before = await readFile(f.manifest, 'utf8');
  await assert.rejects(checkFreshness(f.root, 'zh-CN', ['intro.md', 'missing.md']));
  assert.equal(await readFile(f.manifest, 'utf8'), before);
});

test('missing and obsolete pages remain visible', async t => {
  const f = await fixture(t);
  await writeFile(path.join(f.source, 'new.md'), '# New\n');
  await writeFile(path.join(f.translated, 'obsolete.md'), '# 旧页面\n');
  const result = await checkFreshness(f.root, 'zh-CN', ['intro.md']);
  assert.deepEqual(result.issues, [
    { page: 'new.md', status: 'missing translation' },
    { page: 'obsolete.md', status: 'English source removed' },
  ]);
});
