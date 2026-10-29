// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

/// <reference types="node" />

// Embedding: the replay mounts into a page that owns :root, <html> and <body>, and must leave
// all three exactly as it found them.

import { act, cleanup, screen } from '@testing-library/react';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import type { Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { setReplayLocale } from './i18n/config';
import { mountBoardReplay } from './mount';
import { boardBytes, fixtureRecordingText, stubFetch } from './testing/harness';
import themeCss from './theme.css?inline';
import lightCss from './theme-light.css?inline';

const HOST_TOKENS = '--muted: 11 22% 33%; --border: 44 55% 66%; --foreground: 77 88% 99%;';

let hostStyle: HTMLStyleElement;
let mount: HTMLElement;

// Every root a test mounts, so the teardown can unmount it. Testing Library's cleanup() unmounts
// only the roots ITS render() created; a root from mountBoardReplay is the test's to unmount, and
// one left mounted keeps rendering -- an embed's clock autoplays on requestAnimationFrame -- after
// the test, and after the file, when React's scheduled work then runs with no window to run in.
const roots: Root[] = [];

async function mountReplay(el: HTMLElement): Promise<void> {
  await act(async () => {
    roots.push(await mountBoardReplay(el));
  });
}

function unmountReplays(): void {
  act(() => {
    for (const root of roots.splice(0)) root.unmount();
  });
}

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
  unmountReplays();
  // Unmounting empties the mount element, so anything left in it is a root no one unmounted: a
  // test that called mountBoardReplay itself rather than through mountReplay.
  expect(mount.childElementCount, 'a replay root is still mounted after the test').toBe(0);
  cleanup();
  mount.remove();
  hostStyle.remove();
  document.getElementById('board-replay-theme')?.remove();
  document.getElementById('board-replay-theme-light')?.remove();
  document.documentElement.lang = '';
  vi.unstubAllGlobals();
});

const rootToken = (name: string) => getComputedStyle(document.documentElement).getPropertyValue(name).trim();

describe('mounting into a host page', () => {
  it('leaves the host root tokens, <html> and <body> untouched, and puts its own on its root', async () => {
    expect(rootToken('--muted')).toBe('11 22% 33%');
    const files = stubFetch({
      [`${location.origin}/a/run.json`]: await fixtureRecordingText(),
      [`${location.origin}/a/board.json`]: boardBytes(),
    });
    vi.stubGlobal('fetch', files.fetchFn);
    mount.dataset.recording = '/a/run.json';
    mount.dataset.locale = 'zh-CN';

    await mountReplay(mount);
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
    const css = (themeCss + lightCss).replace(/\/\*[\s\S]*?\*\//g, '');
    const selectors = [...css.matchAll(/([^{}]+)\{[^{}]*\}/g)].flatMap((m) =>
      m[1].split(',').map((s) => s.trim()).filter(Boolean),
    );
    expect(selectors.length).toBeGreaterThan(1);
    for (const s of selectors) expect(s, s).toMatch(/^\.board-replay-root(\[data-theme='light'\])?(\s|$)/);
  });

  it('repeats the brand tokens it needs exactly as the generated brand stylesheet has them', () => {
    const brand = readFileSync(path.resolve(import.meta.dirname, '../../../packages/brand/css/shadcn.css'), 'utf8');
    const dark = brand.slice(brand.indexOf('.dark {'));
    const light = brand.slice(brand.indexOf(':root {'), brand.indexOf('.dark {'));
    for (const token of ['--primary', '--primary-foreground', '--ring']) {
      const re = new RegExp(`${token}:\\s*([^;]+);`);
      const want = dark.match(re)?.[1];
      expect(want, `${token} in the brand .dark block`).toBeTruthy();
      expect(themeCss.match(re)?.[1], token).toBe(want);
      expect(lightCss.match(re)?.[1], `light ${token}`).toBe(light.match(re)?.[1]);
    }
  });

  it('takes its light tokens from the viewer, so a light replay reads the colours /dash ships', () => {
    const dash = readFileSync(path.resolve(import.meta.dirname, '../../dashboard/src/theme.css'), 'utf8');
    const dashLight = dash.slice(dash.indexOf(':root {'), dash.indexOf('.dark {'));
    const tokens = ['--background', '--foreground', '--card', '--card-foreground', '--muted', '--muted-foreground', '--destructive', '--border', '--input'];
    for (const token of tokens) {
      const re = new RegExp(`${token}:\\s*([^;]+);`);
      expect(dashLight.match(re)?.[1], `${token} in /dash's light block`).toBeTruthy();
      expect(lightCss.match(re)?.[1], token).toBe(dashLight.match(re)?.[1]);
    }
  });
});

describe('the host options', () => {
  async function mountWith(data: Record<string, string>) {
    const files = stubFetch({
      [`${location.origin}/a/run.json`]: await fixtureRecordingText(),
      [`${location.origin}/a/board.json`]: boardBytes(),
    });
    vi.stubGlobal('fetch', files.fetchFn);
    Object.assign(mount.dataset, { recording: '/a/run.json', ...data });
    await mountReplay(mount);
  }

  it('installs the light tokens only for a light page, on its own root', async () => {
    await mountWith({ theme: 'light' });
    expect(document.getElementById('board-replay-theme-light')?.textContent).toBe(lightCss);
    const root = screen.getByTestId('replay-root');
    expect(root.getAttribute('data-theme')).toBe('light');
    expect(getComputedStyle(root).getPropertyValue('--muted').trim()).toBe('240 4.8% 95.9%');
    expect(rootToken('--muted')).toBe('11 22% 33%');
  });

  it('leaves the light tokens out of a dark page', async () => {
    await mountWith({});
    expect(document.getElementById('board-replay-theme-light')).toBeNull();
    expect(getComputedStyle(screen.getByTestId('replay-root')).getPropertyValue('--muted').trim()).toBe('210 20% 16%');
  });

  it('draws an embed with its label on the first frame, before the recording has loaded', async () => {
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    const files = stubFetch({
      [`${location.origin}/a/run.json`]: await fixtureRecordingText(),
      [`${location.origin}/a/board.json`]: boardBytes(),
    });
    vi.stubGlobal('fetch', ((...a: Parameters<typeof fetch>) => gate.then(() => files.fetchFn(...a))) as typeof fetch);
    Object.assign(mount.dataset, { recording: '/a/run.json', embed: '1' });
    await mountReplay(mount);
    expect(screen.getByTestId('replay-root').hasAttribute('data-embed')).toBe(true);
    // In the page's language (<html lang="es">), from the embed's own catalog.
    expect(screen.getByTestId('replay-label').textContent).toBe('Reproducción de una ejecución grabada · horas trasladadas al momento actual');
    expect(screen.getByTestId('replay-body').getAttribute('data-state')).toBe('loading');
    release();
    await screen.findByTestId('embed-canvas');
  });

  it('leaves nothing running once the test has unmounted it', async () => {
    const files = stubFetch({
      [`${location.origin}/a/run.json`]: await fixtureRecordingText(),
      [`${location.origin}/a/board.json`]: boardBytes(),
    });
    vi.stubGlobal('fetch', files.fetchFn);
    // The embed autoplays, so its clock is asking for animation frames from the first frame on.
    Object.assign(mount.dataset, { recording: '/a/run.json', embed: '1' });
    await mountReplay(mount);
    await screen.findByTestId('embed-canvas');
    expect(typeof requestAnimationFrame, 'the clock ticks on animation frames here').toBe('function');
    const frames = vi.spyOn(globalThis, 'requestAnimationFrame');
    await new Promise((r) => setTimeout(r, 100));
    expect(frames.mock.calls.length, 'the embed is playing before the teardown').toBeGreaterThan(0);

    unmountReplays();
    frames.mockClear();
    await new Promise((r) => setTimeout(r, 100));
    expect(frames.mock.calls.length, 'frames requested after the teardown').toBe(0);
    frames.mockRestore();
  });

  it('draws the full replay page when the host names the embed and turns it off', async () => {
    await mountWith({ embed: '0' });
    await screen.findByTestId('transport-bar');
    expect(screen.getByTestId('replay-root').hasAttribute('data-embed')).toBe(false);
  });
});
