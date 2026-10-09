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
//                   to sp-dashboard.json beside the recording
//   data-locale     the page's language (en, es, zh-CN); defaults to <html lang>

import { StrictMode } from 'react';
import { createRoot, type Root } from 'react-dom/client';

import ReplayApp from './App';
import { setReplayLocale } from './i18n/config';
import themeCss from './theme.css?inline';

export const DEFAULT_BOARD_FILE = 'sp-dashboard.json';

export function defaultBoardUrl(recordingUrl: string | null): string | null {
  if (!recordingUrl) return null;
  const cut = recordingUrl.lastIndexOf('/');
  return `${recordingUrl.slice(0, cut + 1)}${DEFAULT_BOARD_FILE}`;
}

// The theme is injected as a style element rather than linked, so the page needs only the
// one module script. (Inline styles are within the site's content-security policy.)
function installTheme(): void {
  if (document.getElementById('board-replay-theme')) return;
  const style = document.createElement('style');
  style.id = 'board-replay-theme';
  style.textContent = themeCss;
  document.head.appendChild(style);
  // The widgets read the dark token set off <html>.
  document.documentElement.classList.add('dark');
}

export async function mountBoardReplay(el: HTMLElement): Promise<Root> {
  installTheme();
  await setReplayLocale(el.dataset.locale ?? document.documentElement.lang);
  const recordingUrl = el.dataset.recording ?? null;
  const boardUrl = el.dataset.board ?? defaultBoardUrl(recordingUrl);
  const root = createRoot(el);
  root.render(
    <StrictMode>
      <ReplayApp recordingUrl={recordingUrl} boardUrl={boardUrl} />
    </StrictMode>,
  );
  return root;
}
