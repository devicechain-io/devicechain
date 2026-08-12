// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The console's section registry against the server's setting vocabulary.
//
// Only ONE direction is checkable, and it is the one that matters here. The
// console cannot be exhaustive over the server's keys — a newer instance may
// serve a key this build has never heard of, which is exactly why SettingsPage
// falls back to a raw JSON editor. But a section here for a key the server does
// NOT define is a straightforward defect: it renders a tab whose every save is
// rejected with "unknown setting", and nothing else would notice.
//
// 🔴 This reads the Go source. That is safe HERE and would not be safe in the
// mirror-image test: a Go test reading a file outside its own module does not
// invalidate the go test cache when that file changes, so it can replay a stale
// PASS. Vitest has no such cache, so the link only goes this way.

import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import { SECTIONS } from './sections';

// Relative to the workspace root vitest runs from (apps/console), not to this
// file: import.meta.url is not a file: URL under the test transform.
const SETTINGSDEFS_GO = resolve(
  process.cwd(),
  '../../../backend/services/user-management/settingsdefs/settingsdefs.go',
);

/** Every `const KeyX = "…"` the settingsdefs package declares. */
function serverSettingKeys(): string[] {
  const source = readFileSync(SETTINGSDEFS_GO, 'utf8');
  const keys = [...source.matchAll(/^const Key\w+ = "([^"]+)"$/gm)].map((m) => m[1]);
  // A parse that finds nothing would make every assertion below vacuous — the
  // file could have been renamed, or the declaration style changed.
  if (keys.length === 0) {
    throw new Error(`no setting keys parsed from ${SETTINGSDEFS_GO}; has the declaration changed?`);
  }
  return keys;
}

describe('the section registry', () => {
  it('has no editor for a key the server does not define', () => {
    const known = serverSettingKeys();
    for (const key of Object.keys(SECTIONS)) {
      expect(known, `${key} has a console editor but is not a server setting`).toContain(key);
    }
  });

  it('keys each section under its own key', () => {
    for (const [key, section] of Object.entries(SECTIONS)) {
      expect(section.key).toBe(key);
    }
  });

  // Not a requirement — the fallback exists precisely so it need not hold — but
  // while it does hold, an operator never meets the raw editor by accident. If
  // this fails because a NEW key shipped server-side, the fix is to write its
  // editor or to relax this to a warning, not to delete the key.
  it('covers every setting the server currently defines', () => {
    expect(Object.keys(SECTIONS).sort()).toEqual(serverSettingKeys().sort());
  });
});
