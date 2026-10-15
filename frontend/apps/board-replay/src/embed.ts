// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The EMBED mode: the board drawn on a FIXED design canvas (960x1080 by default) and scaled,
// as one picture, to fit whatever box the host gives it. The layout is computed once, at the
// canvas size, and never again: a wide container and a narrow one show the same board at
// different scales, letterboxed, never a board reflowed into a different set of rows.
//
// Configured on the mount element, or (for a page that is itself the embed, in an iframe)
// on the page URL; the element wins:
//
//   data-embed / ?embed        1 | true   turn the mode on
//   data-canvas / ?canvas      WxH        the design size, e.g. 960x1080 (the default)
//   data-autoplay / ?autoplay  0 | 1      play on load and loop (default 1)
//   data-retime / ?retime      0 | 1      show the run's times moved to now (default 1)
//   data-theme / ?theme        dark | light   (either mode; default dark)
//
// What it leaves out is the replay's control chrome (the transport bar and the provenance
// toggle). The replay LABEL stays: an embedded board says it is a replay, and that its
// times have been moved, in every state.
//
// As everywhere in this app, a value that is present but invalid is ignored with a console
// warning, so a typo in a host page still brings the board up with the default.

export interface CanvasSize {
  width: number;
  height: number;
}

export interface EmbedOptions {
  canvas: CanvasSize;
  autoplay: boolean;
  retime: boolean;
}

export type ReplayTheme = 'dark' | 'light';

export const DEFAULT_CANVAS: CanvasSize = { width: 960, height: 1080 };
// The canvas sizes accepted: from a phone column to a 4K wall, either way up.
export const CANVAS_MIN = 320;
export const CANVAS_MAX = 3840;

type Read = (name: string) => string | null;

function reader(dataset: DOMStringMap, search: string): Read {
  const q = new URLSearchParams(search);
  return (name) => dataset[name] ?? q.get(name);
}

function flag(read: Read, name: string, fallback: boolean, warn: (m: string) => void): boolean {
  const v = read(name);
  if (v === null) return fallback;
  if (v === '1' || v === 'true') return true;
  if (v === '0' || v === 'false') return false;
  warn(`board-replay: ignoring ${name}=${JSON.stringify(v)} (expected 1 or 0)`);
  return fallback;
}

export function parseCanvas(v: string): CanvasSize | null {
  const m = /^(\d{3,4})x(\d{3,4})$/.exec(v.trim());
  if (!m) return null;
  const [width, height] = [Number(m[1]), Number(m[2])];
  const ok = (n: number) => n >= CANVAS_MIN && n <= CANVAS_MAX;
  return ok(width) && ok(height) ? { width, height } : null;
}

// parseEmbedOptions reads the embed configuration, or null when the mode is off.
export function parseEmbedOptions(
  dataset: DOMStringMap,
  search: string,
  warn: (message: string) => void = (m) => console.warn(m),
): EmbedOptions | null {
  const read = reader(dataset, search);
  if (!flag(read, 'embed', false, warn)) return null;
  let canvas = DEFAULT_CANVAS;
  const c = read('canvas');
  if (c !== null) {
    const parsed = parseCanvas(c);
    if (parsed) canvas = parsed;
    else warn(`board-replay: ignoring canvas=${JSON.stringify(c)} (expected WxH, each ${CANVAS_MIN} to ${CANVAS_MAX})`);
  }
  return { canvas, autoplay: flag(read, 'autoplay', true, warn), retime: flag(read, 'retime', true, warn) };
}

export function parseTheme(dataset: DOMStringMap, search: string, warn: (message: string) => void = (m) => console.warn(m)): ReplayTheme {
  const v = reader(dataset, search)('theme');
  if (v === null || v === 'dark') return 'dark';
  if (v === 'light') return 'light';
  warn(`board-replay: ignoring theme=${JSON.stringify(v)} (expected dark or light)`);
  return 'dark';
}

// fitCanvas places a canvas in a container: the largest scale at which the WHOLE canvas
// fits, centred on the other axis (letterboxed). The canvas keeps its own size; only the
// scale depends on the container.
export function fitCanvas(container: CanvasSize, canvas: CanvasSize): { scale: number; left: number; top: number } {
  const scale = Math.max(0, Math.min(container.width / canvas.width, container.height / canvas.height));
  return {
    scale,
    left: (container.width - canvas.width * scale) / 2,
    top: (container.height - canvas.height * scale) / 2,
  };
}
