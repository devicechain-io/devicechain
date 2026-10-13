// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Fetches the user-guide screenshots into static/img/guide/ (gitignored).
//
// The images live in the public devicechain-io/guide-assets repository, not here.
// docs/guide-assets.lock holds the one commit this tree is built against; this
// script downloads exactly that commit and refuses to continue on any error, so a
// build never renders against missing, partial or different assets.
import { spawnSync } from 'node:child_process';
import { mkdtemp, mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const REPO = 'devicechain-io/guide-assets';
const STAMP = '.commit';

export function parseLock(text) {
  const sha = text.trim();
  if (!/^[0-9a-f]{40}$/.test(sha)) {
    throw new Error(`guide-assets.lock must hold one 40-character commit sha, got ${JSON.stringify(sha)}`);
  }
  return sha;
}

export function validateManifest(manifest) {
  if (!manifest || manifest.schemaVersion !== 1 || typeof manifest.shots !== 'object' ||
      manifest.shots === null || Array.isArray(manifest.shots)) {
    throw new Error('guide-assets manifest.json is not a schemaVersion 1 manifest');
  }
  return manifest;
}

export async function fetchGuideAssets(docsRoot, { fetchImpl = fetch } = {}) {
  const sha = parseLock(await readFile(path.join(docsRoot, 'guide-assets.lock'), 'utf8'));
  const dest = path.join(docsRoot, 'static', 'img', 'guide');

  try {
    if ((await readFile(path.join(dest, STAMP), 'utf8')).trim() === sha) {
      validateManifest(JSON.parse(await readFile(path.join(dest, 'manifest.json'), 'utf8')));
      console.log(`guide-assets: ${sha.slice(0, 12)} already present`);
      return sha;
    }
  } catch {
    // no usable copy: fetch below
  }

  const url = `https://codeload.github.com/${REPO}/tar.gz/${sha}`;
  const res = await fetchImpl(url);
  if (!res.ok) throw new Error(`guide-assets: GET ${url} -> HTTP ${res.status}`);
  const archive = Buffer.from(await res.arrayBuffer());

  const work = await mkdtemp(path.join(tmpdir(), 'guide-assets-'));
  try {
    await writeFile(path.join(work, 'a.tgz'), archive);
    await mkdir(path.join(work, 'out'));
    // Relative archive name + cwd: GNU tar reads "C:\..." as a remote host.
    const x = spawnSync('tar', ['-xzf', 'a.tgz', '--strip-components=1', '-C', 'out'], {
      cwd: work, encoding: 'utf8',
    });
    if (x.error || x.status !== 0) {
      throw new Error(`guide-assets: extracting archive failed: ${x.error ?? x.stderr}`);
    }
    const manifest = validateManifest(
      JSON.parse(await readFile(path.join(work, 'out', 'manifest.json'), 'utf8')));
    await rm(dest, { recursive: true, force: true });
    await mkdir(path.dirname(dest), { recursive: true });
    const mv = spawnSync(process.execPath, ['-e',
      'require("node:fs").cpSync(process.argv[1], process.argv[2], {recursive:true})',
      path.join(work, 'out'), dest], { encoding: 'utf8' });
    if (mv.status !== 0) throw new Error(`guide-assets: install failed: ${mv.stderr}`);
    await writeFile(path.join(dest, STAMP), sha + '\n');
    console.log(`guide-assets: fetched ${sha.slice(0, 12)} (${Object.keys(manifest.shots).length} shots)`);
  } finally {
    await rm(work, { recursive: true, force: true });
  }
  return sha;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const docsRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
  fetchGuideAssets(docsRoot).catch(err => {
    console.error(String(err?.message ?? err));
    process.exit(1);
  });
}
