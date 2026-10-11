// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
//
// Post-build check (npm `postbuild`): the built CSS still defines the search dropdown's
// class names that src/theme/SearchBar reads. If a plugin upgrade renames them, the
// wrapper would see no results and send NOTHING; this turns that silent loss into a failed
// build. Usage: node scripts/check-search-selectors.mjs [buildDir]
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { REQUIRED_CLASS_PREFIXES } from '../src/search/selectors.mjs';

function cssFiles(dir) {
  const out = [];
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) out.push(...cssFiles(p));
    else if (name.endsWith('.css')) out.push(p);
  }
  return out;
}

/** Returns the required prefixes with no `.<prefix>…` class selector in the built CSS. */
export function missingSelectors(buildDir) {
  const files = cssFiles(buildDir);
  if (files.length === 0) {
    throw new Error(`no CSS found under ${buildDir}; was the site built?`);
  }
  const css = files.map((f) => readFileSync(f, 'utf8')).join('\n');
  return REQUIRED_CLASS_PREFIXES.filter(
    (prefix) => !new RegExp(`\\.${prefix}[A-Za-z0-9_-]+`).test(css),
  );
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const dir = resolve(process.argv[2] ?? 'build');
  const missing = missingSelectors(dir);
  if (missing.length > 0) {
    console.error(
      `search selectors missing from the built CSS: ${missing.join(', ')}.\n` +
        'The search plugin renamed the dropdown classes src/theme/SearchBar reads; ' +
        'update src/search/selectors.mjs and the wrapper, or search analytics goes silent.',
    );
    process.exit(1);
  }
  console.log(`search selectors present in built CSS (${REQUIRED_CLASS_PREFIXES.join(', ')})`);
}
