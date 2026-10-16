// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The host's options that are read before anything is drawn, in the first download: the theme
// (both modes) and whether the host asked for the embed at all. The embed's own options are
// parsed by embed.ts, which is fetched only when this says the host asked for one, so a page
// that is not an embed never downloads them.
//
// Each option is read from the mount element first and the page URL second (embed.ts lists
// them). A value that is present but invalid is ignored with a console warning.

export type ReplayTheme = 'dark' | 'light';

export type Read = (name: string) => string | null;

export function hostReader(dataset: DOMStringMap, search: string): Read {
  const q = new URLSearchParams(search);
  return (name) => dataset[name] ?? q.get(name);
}

// Whether the host names the embed at all. It is embed.ts that decides whether the value
// turns the mode on (data-embed="0" names it and leaves it off).
export function embedRequested(dataset: DOMStringMap, search: string): boolean {
  return hostReader(dataset, search)('embed') !== null;
}

export function parseTheme(dataset: DOMStringMap, search: string, warn: (message: string) => void = (m) => console.warn(m)): ReplayTheme {
  const v = hostReader(dataset, search)('theme');
  if (v === null || v === 'dark') return 'dark';
  if (v === 'light') return 'light';
  warn(`board-replay: ignoring theme=${JSON.stringify(v)} (expected dark or light)`);
  return 'dark';
}
