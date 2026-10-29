// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Mount the replay onto a host element. The host page configures it with data attributes,
// so the same bundle serves every page (and every language) as a plain module script:
//
//   <div id="board-replay" data-recording="/assets/.../run.json" data-locale="es"></div>
//   <script type="module" src="/assets/.../main.js"></script>
//
//   data-recording  the recording file (required, same-origin)
//   data-board      the board definition it was captured against (same-origin); defaults
//                   to the board file the recording itself names, beside the recording
//   data-locale     the page's language (en, es, zh-CN); defaults to <html lang>
//   data-theme      dark (the default) or light
//   data-embed      1 for the fixed-canvas embed (embed.ts lists it and its options, and
//                   how a page that IS the embed sets them on its URL instead)

import { StrictMode } from 'react';
import { createRoot, type Root } from 'react-dom/client';

import ReplayApp, { loadEmbed } from './App';
import { embedRequested, parseTheme } from './host-options';
import { setReplayLocale } from './i18n/config';
import themeCss from './theme.css?inline';

// The theme is injected as a style element rather than linked, so the page needs only the
// one module script. (Inline styles are within the site's content-security policy.)
//
// The light tokens (theme-light.css) are a second style element, fetched only for a light
// page, alongside the page's language; if they cannot be fetched, the replay draws in the dark
// theme every token falls back to.
//
// 🔴 THE HOST PAGE OWNS :root, <html> AND <body>. Every rule in both files is scoped under
// .board-replay-root (the app's own root element), and this file touches nothing outside
// the mount element: no class, attribute or style on <html> or <body>, and the language is
// set on the app root, not on the document.
function installStyle(id: string, css: string): void {
  if (document.getElementById(id)) return;
  const style = document.createElement('style');
  style.id = id;
  style.textContent = css;
  document.head.appendChild(style);
}

export async function mountBoardReplay(el: HTMLElement): Promise<Root> {
  const theme = parseTheme(el.dataset, location.search);
  installStyle('board-replay-theme', themeCss);
  await Promise.all([
    setReplayLocale(el.dataset.locale ?? document.documentElement.lang),
    theme === 'light' &&
      import('./theme-light.css?inline').then(
        (m) => installStyle('board-replay-theme-light', m.default),
        (err: unknown) => console.warn('board-replay: the light theme could not be loaded; showing the dark one', err),
      ),
  ]);
  // (Read-only: the page's language is a default for the replay, never something it sets.)
  const recordingUrl = el.dataset.recording ?? null;
  const boardUrl = el.dataset.board ?? null; // null: the recording names its own board (load.ts)
  // The embed's code (its option parser and player) is fetched only when the host asks for an
  // embed, and before the first render, so the embed's first frame already carries its label.
  const embed = embedRequested(el.dataset, location.search) ? (await loadEmbed()).parseEmbedOptions(el.dataset, location.search) : null;
  const root = createRoot(el);
  root.render(
    <StrictMode>
      <ReplayApp recordingUrl={recordingUrl} boardUrl={boardUrl} embed={embed} theme={theme} />
    </StrictMode>,
  );
  return root;
}
