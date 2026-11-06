// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The automation canvas's zoom controls follow the app theme. xyflow paints them from
// its own defaults (white in light mode) unless the --xy-controls-* variables are set, and
// the app never sets xyflow's colorMode, so in dark mode the controls were a white box.
//
// Read with node:fs rather than `?raw`: Vitest stubs CSS imports, so a raw import is the
// empty string and every assertion below would pass over nothing (see contrast.test.ts).
// `reads the stylesheet at all` is the guard for that.
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

// A helper taking the path as a variable: Vite rewrites a literal `new URL('./x', import.meta.url)`
// as an asset reference, which is not a file: URL under Vitest.
const read = (rel: string) => readFileSync(fileURLToPath(new URL(rel, import.meta.url)), 'utf8');

const css = read('./index.css').replace(/\/\*[\s\S]*?\*\//g, '');

function reactFlowBlock(): string {
  const m = /(^|\n)\.react-flow\s*\{([^}]*)\}/.exec(css);
  return m ? m[2] : '';
}

describe('canvas zoom controls theming', () => {
  it('reads the stylesheet at all', () => {
    expect(css).toContain('--background');
  });

  it.each([
    ['--xy-controls-button-background-color', '--card'],
    ['--xy-controls-button-background-color-hover', '--accent'],
    ['--xy-controls-button-color', '--foreground'],
    ['--xy-controls-button-color-hover', '--accent-foreground'],
    ['--xy-controls-button-border-color', '--border'],
  ])('maps %s onto the theme token %s, which already has a dark value', (variable, token) => {
    const body = reactFlowBlock();
    expect(body, 'no .react-flow override block').not.toBe('');
    expect(body).toContain(`${variable}: hsl(var(${token}));`);
    // The token itself is redefined for dark mode, so the mapping follows the theme.
    const dark = css.slice(css.indexOf('.dark {'));
    expect(dark.slice(0, dark.indexOf('}'))).toContain(`${token}:`);
  });
});
