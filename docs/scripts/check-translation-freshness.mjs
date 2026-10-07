// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// English is the reference. Freshness is advisory by default: translations may
// be refreshed in batches, independently of English documentation changes.
import { createHash } from 'node:crypto';
import { readFile, readdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

export function hashText(text) {
  return createHash('sha256').update(text.replace(/\r\n?/g, '\n')).digest('hex');
}

export function pagePath(value) {
  if (!value || value.includes('\\') || path.posix.isAbsolute(value) ||
      value.split('/').some(part => !part || part === '.' || part === '..') ||
      !/\.(md|mdx)$/.test(value)) {
    throw new Error(`Expected a docs-relative Markdown path, got ${JSON.stringify(value)}`);
  }
  return value;
}

async function files(dir, prefix = '') {
  const found = [];
  for (const entry of await readdir(dir, { withFileTypes: true })) {
    const name = prefix + entry.name;
    if (entry.isDirectory()) found.push(...await files(path.join(dir, entry.name), name + '/'));
    else if (entry.isFile() && /\.(md|mdx)$/.test(name)) found.push(name);
  }
  return found.sort();
}

export async function checkFreshness(docsRoot, locale, record = []) {
  if (!/^[a-z]{2}(?:-[A-Za-z0-9]+)*$/.test(locale)) throw new Error('Invalid locale');
  const sourceDir = path.join(docsRoot, 'docs');
  const localeDir = path.join(docsRoot, 'i18n', locale);
  const translatedDir = path.join(localeDir, 'docusaurus-plugin-content-docs', 'current');
  const manifestPath = path.join(localeDir, 'source-manifest.json');
  const manifest = JSON.parse(await readFile(manifestPath, 'utf8'));
  if (manifest.schemaVersion !== 1 || manifest.locale !== locale ||
      !manifest.pages || typeof manifest.pages !== 'object' || Array.isArray(manifest.pages)) {
    throw new Error(`Invalid translation manifest: ${manifestPath}`);
  }
  for (const [name, entry] of Object.entries(manifest.pages)) {
    pagePath(name);
    if (!/^[a-f0-9]{64}$/.test(entry?.sourceSha256) ||
        !/^[a-f0-9]{64}$/.test(entry?.translationSha256)) {
      throw new Error(`Invalid hashes for ${name}`);
    }
  }

  // Validate and read every requested page before writing anything. A typo or
  // missing translation must not mark an earlier page as refreshed.
  const updates = [];
  for (const value of record) {
    const name = pagePath(value);
    const [source, translation] = await Promise.all([
      readFile(path.join(sourceDir, name), 'utf8'),
      readFile(path.join(translatedDir, name), 'utf8'),
    ]);
    updates.push([name, { sourceSha256: hashText(source), translationSha256: hashText(translation) }]);
  }
  if (updates.length) {
    for (const [name, entry] of updates) manifest.pages[name] = entry;
    manifest.pages = Object.fromEntries(Object.entries(manifest.pages).sort(([a], [b]) => a.localeCompare(b)));
    await writeFile(manifestPath, JSON.stringify(manifest, null, 2) + '\n');
  }

  const sourceFiles = await files(sourceDir);
  const translatedFiles = await files(translatedDir);
  const sourceSet = new Set(sourceFiles);
  const translatedSet = new Set(translatedFiles);
  const issues = [];
  for (const name of sourceFiles) {
    if (!translatedSet.has(name)) {
      issues.push({ page: name, status: 'missing translation' });
      continue;
    }
    const entry = manifest.pages[name];
    if (!entry) {
      issues.push({ page: name, status: 'not recorded' });
      continue;
    }
    const [source, translation] = await Promise.all([
      readFile(path.join(sourceDir, name), 'utf8'),
      readFile(path.join(translatedDir, name), 'utf8'),
    ]);
    if (hashText(source) !== entry.sourceSha256) issues.push({ page: name, status: 'English changed' });
    if (hashText(translation) !== entry.translationSha256) issues.push({ page: name, status: 'translation changed; review and record' });
  }
  for (const name of new Set([...translatedFiles, ...Object.keys(manifest.pages)])) {
    if (!sourceSet.has(name)) issues.push({ page: name, status: 'English source removed' });
    else if (!translatedSet.has(name) && manifest.pages[name]) issues.push({ page: name, status: 'recorded translation removed' });
  }
  return { issues, recorded: updates.length, total: sourceFiles.length };
}

async function main(args) {
  let locale = 'zh-CN', strict = false;
  const record = [];
  for (let i = 0; i < args.length; i++) {
    if (args[i] === '--locale' && args[i + 1]) locale = args[++i];
    else if (args[i] === '--strict') strict = true;
    else if (args[i] === '--record' && args[i + 1] && !args[i + 1].startsWith('--')) record.push(args[++i]);
    else throw new Error('Usage: node scripts/check-translation-freshness.mjs [--locale zh-CN] [--strict] [--record concepts/architecture.md ...] (repeat --record for each reviewed page)');
  }
  const docsRoot = fileURLToPath(new URL('..', import.meta.url));
  const result = await checkFreshness(docsRoot, locale, record);
  if (result.recorded) console.log(`Recorded ${result.recorded} reviewed ${locale} page(s).`);
  for (const issue of result.issues) console.log(`${locale}/${issue.page}: ${issue.status}`);
  console.log(`${locale}: ${result.total} English page(s), ${result.issues.length} freshness notice(s). English remains the reference.`);
  if (strict && result.issues.length) process.exitCode = 1;
}

if (process.argv[1] && pathToFileURL(path.resolve(process.argv[1])).href === import.meta.url) {
  main(process.argv.slice(2)).catch(error => {
    console.error(error.message);
    process.exitCode = 1;
  });
}
