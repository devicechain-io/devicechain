// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

/// <reference types="node" />

// Embedding: the replay mounts into a page that owns :root, <html> and <body>, and must leave
// all three exactly as it found them.

import { act, cleanup, screen } from '@testing-library/react';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { setReplayLocale } from './i18n/config';
import { mountBoardReplay } from './mount';
import { boardBytes, fixtureRecordingText, stubFetch } from './testing/harness';
import themeCss from './theme.css?inline';

const HOST_TOKENS = '--muted: 11 22% 33%; --border: 44 55% 66%; --foreground: 77 88% 99%;';

let hostStyle: HTMLStyleElement;
let mount: HTMLElement;

beforeEach(async () => {
  document.documentElement.lang = 'es';
  document.documentElement.className = '';
  document.body.className = '';
  document.body.removeAttribute('style');
  hostStyle = document.createElement('style');
  hostStyle.textContent = `:root { ${HOST_TOKENS} }`;
  document.head.appendChild(hostStyle);
  mount = document.createElement('div');
  mount.id = 'board-replay';
  document.body.appendChild(mount);
  await setReplayLocale('en');
});

afterEach(() => {
  cleanup();
  mount.remove();
  hostStyle.remove();
  document.getElementById('board-replay-theme')?.remove();
  document.documentElement.lang = '';
  vi.unstubAllGlobals();
});

const rootToken = (name: string) => getComputedStyle(document.documentElement).getPropertyValue(name).trim();

describe('mounting into a host page', () => {
  it('leaves the host root tokens, <html> and <body> untouched, and puts its own on its root', async () => {
    expect(rootToken('--muted')).toBe('11 22% 33%');
    const files = stubFetch({
      [`${location.origin}/a/run.json`]: await fixtureRecordingText(),
      [`${location.origin}/a/sp-dashboard.json`]: boardBytes(),
    });
    vi.stubGlobal('fetch', files.fetchFn);
    mount.dataset.recording = '/a/run.json';
    mount.dataset.locale = 'zh-CN';

    await act(async () => {
      await mountBoardReplay(mount);
    });
    await screen.findByTestId('transport-bar');

    // The host's page-level state is exactly what it was.
    expect(document.documentElement.className).toBe('');
    expect(document.documentElement.getAttribute('lang')).toBe('es');
    expect(document.documentElement.getAttribute('style')).toBeNull();
    expect(document.body.className).toBe('');
    expect(document.body.getAttribute('style')).toBeNull();
    expect(rootToken('--muted')).toBe('11 22% 33%');
    expect(rootToken('--border')).toBe('44 55% 66%');
    expect(rootToken('--foreground')).toBe('77 88% 99%');

    // The app carries its own tokens and its own language on its own root.
    const root = screen.getByTestId('replay-root');
    expect(root.className).toContain('board-replay-root');
    expect(root.getAttribute('lang')).toBe('zh-CN');
    expect(mount.contains(root)).toBe(true);
    expect(getComputedStyle(root).getPropertyValue('--muted').trim()).toBe('210 20% 16%');
  });

  it('declares only rules under its own root: no :root, html, body or bare * selector', () => {
    const css = themeCss.replace(/\/\*[\s\S]*?\*\//g, '');
    const selectors = [...css.matchAll(/([^{}]+)\{[^{}]*\}/g)].flatMap((m) =>
      m[1].split(',').map((s) => s.trim()).filter(Boolean),
    );
    expect(selectors.length).toBeGreaterThan(1);
    for (const s of selectors) expect(s, s).toMatch(/^\.board-replay-root(\s|$)/);
  });

  it('repeats the brand tokens it needs exactly as the generated brand stylesheet has them', () => {
    const brand = readFileSync(path.resolve(import.meta.dirname, '../../../packages/brand/css/shadcn.css'), 'utf8');
    const dark = brand.slice(brand.indexOf('.dark {'));
    for (const token of ['--primary', '--primary-foreground', '--ring']) {
      const re = new RegExp(`${token}:\\s*([^;]+);`);
      const want = dark.match(re)?.[1];
      expect(want, `${token} in the brand .dark block`).toBeTruthy();
      expect(themeCss.match(re)?.[1], token).toBe(want);
    }
  });
});
