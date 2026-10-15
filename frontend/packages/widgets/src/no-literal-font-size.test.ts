// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// @vitest-environment node

// No numeric font sizes in the built-ins.
//
// A built-in widget's text size comes from the type ramp (`fontVar('md')` -> --font-md, which
// multiplies by the board's density scale) or from the shared value curve (fitValueFont /
// fitValueFontCss / useWidgetScale). A `fontSize: 12` is a size density cannot reach: on a wall
// display it stays 12 px while everything around it grows. The failure is quiet -- it looks
// right at the default density -- so the SOURCE is scanned: every built-in widget module and
// the widget frame. (Chart option builders hand ECharts numeric fonts because a canvas cannot
// read a CSS variable; they take the density scale as a parameter and live outside this scan.)
/// <reference types="node" />

import { readdirSync, readFileSync } from 'node:fs';
import { dirname, join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';

import { describe, expect, it } from 'vitest';

const SRC = dirname(fileURLToPath(import.meta.url));
const SCANNED_DIRS = [join(SRC, 'widgets')];
const SCANNED_FILES = [join(SRC, '..', '..', 'widget-sdk', 'src', 'frame.tsx')];

// `fontSize: 12`, `fontSize: '12px'`, `fontSize={12}`, `fontSize: 1.5` -- a font size whose
// value STARTS with a digit. A variable, a token call or a calc() does not match.
export const LITERAL_FONT_SIZE = /\bfontSize\s*[:=]\s*\{?\s*['"`]?\s*\d/;

function stripComments(source: string): string {
  return source.replace(/\/\*[\s\S]*?\*\//g, '').replace(/(^|[^:])\/\/.*$/gm, '$1');
}

function walk(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const p = join(dir, e.name);
    if (e.isDirectory()) return walk(p);
    return /\.tsx?$/.test(e.name) && !/\.test\.tsx?$/.test(e.name) ? [p] : [];
  });
}

describe('numeric font sizes', () => {
  it('the pattern matches the shapes it must catch and passes the ones it must not', () => {
    for (const bad of ['fontSize: 12,', "fontSize: '12px'", 'fontSize={13}', 'fontSize:11', 'fontSize: 1.5']) {
      expect(LITERAL_FONT_SIZE.test(bad), bad).toBe(true);
    }
    for (const ok of ["fontSize: fontVar('md')", 'fontSize: valueFont', 'fontSize,', 'fontSize: `calc(${n}px * 2)`']) {
      expect(LITERAL_FONT_SIZE.test(ok), ok).toBe(false);
    }
  });

  it('are absent from every built-in widget module and the widget frame', () => {
    const files = [...SCANNED_DIRS.flatMap(walk), ...SCANNED_FILES];
    expect(files.length).toBeGreaterThan(10); // the scan found the sources at all
    const offenders = files.filter((f) => LITERAL_FONT_SIZE.test(stripComments(readFileSync(f, 'utf8'))));
    expect(offenders.map((f) => relative(SRC, f))).toEqual([]);
  });
});
