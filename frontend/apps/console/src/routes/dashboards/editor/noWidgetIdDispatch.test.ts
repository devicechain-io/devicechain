// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The editor branches on a widget's DECLARATION — its channel, its datasource policy, the
// control its options name — and never on which widget it is. This reads the editor's own
// source and fails if a built-in widget id appears in code, or if the tables keyed by
// built-in id are imported at all.
//
// 🔴 WHY A SOURCE SCAN: an id comparison is invisible to the type checker and to every
// behavioural test of the built-ins (they all pass — the built-ins are exactly the ids being
// compared). The failure shows up only for a widget someone ELSE registers, as a settings
// panel that quietly behaves like a stranger's widget. So the property is asserted where it
// lives, in the text. Comments are stripped first: the files explain WHY they do not name a
// widget and have to be able to say the name.
//
// The scanner must be shown to see, or "no hits" is a green that proves nothing — the last
// test feeds it a violation and requires the hit.

import { readdirSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { describe, expect, it } from 'vitest';

import { WIDGET_TYPES } from '@devicechain/dashboards';

// Strips // and /* */ comments while leaving string and template literals alone, so a '//'
// inside a URL string is not mistaken for a comment.
export function stripComments(source: string): string {
  let out = '';
  let i = 0;
  while (i < source.length) {
    const c = source[i];
    const n = source[i + 1];
    if (c === '/' && n === '/') {
      while (i < source.length && source[i] !== '\n') i++;
    } else if (c === '/' && n === '*') {
      const end = source.indexOf('*/', i + 2);
      i = end === -1 ? source.length : end + 2;
    } else if (c === "'" || c === '"' || c === '`') {
      const quote = c;
      out += c;
      i++;
      while (i < source.length && source[i] !== quote) {
        if (source[i] === '\\') {
          out += source[i];
          i++;
        }
        out += source[i] ?? '';
        i++;
      }
      out += quote;
      i++;
    } else {
      out += c;
      i++;
    }
  }
  return out;
}

// A built-in id as a quoted literal.
const ID_LITERAL = new RegExp(`['"\`](${WIDGET_TYPES.join('|')})['"\`]`);
// The tables and helpers keyed by built-in id, and the closed union type.
const ID_KEYED = /\b(WIDGET_TYPES|WIDGET_CHANNEL|WIDGET_BINDS_DATASOURCE|WIDGET_OPTIONS|WidgetType|numberOptionSpec|enumOptionValues)\b/;

export function violations(source: string): string[] {
  const code = stripComments(source);
  const hits: string[] = [];
  code.split('\n').forEach((line, idx) => {
    const lit = ID_LITERAL.exec(line);
    if (lit) hits.push(`line ${idx + 1}: built-in id ${lit[0]}`);
    const keyed = ID_KEYED.exec(line);
    if (keyed) hits.push(`line ${idx + 1}: ${keyed[1]} (a table or type keyed by built-in id)`);
  });
  return hits;
}

const EDITOR_DIR = __dirname;
const MODEL = path.resolve(__dirname, '../../../../../../packages/dashboards/src/editor-model.ts');

function editorSources(): string[] {
  return readdirSync(EDITOR_DIR)
    .filter((f) => /\.tsx?$/.test(f) && !/\.test\.tsx?$/.test(f))
    .map((f) => path.join(EDITOR_DIR, f))
    .concat(MODEL);
}

describe('the editor does not dispatch on built-in widget ids', () => {
  const files = editorSources();

  it('scans the editor sources it means to', () => {
    const names = files.map((f) => path.basename(f));
    for (const expected of ['DashboardWorkspace.tsx', 'WidgetConfigPanel.tsx', 'widgetOptionFields.tsx', 'AddWidgetMenu.tsx', 'editor-model.ts']) {
      expect(names, `${expected} is not being scanned`).toContain(expected);
    }
  });

  it('names no built-in widget id and imports no id-keyed table', () => {
    for (const file of files) {
      expect(violations(readFileSync(file, 'utf8')), path.basename(file)).toEqual([]);
    }
  });

  // The instrument check: the scanner finds what it is for, ignores comments, and is not
  // fooled by a comment marker inside a string.
  it('can fail', () => {
    expect(violations("if (widget.type === 'alarm-table') return 1;")).toHaveLength(1);
    expect(violations('const t = WIDGET_CHANNEL[widget.type];')).toHaveLength(1);
    expect(violations("// type === 'gauge' is what this file refuses to say\nconst x = 1;")).toEqual([]);
    expect(violations("/* 'map' */ const url = 'http://x'; const y = 'gauge';")).toHaveLength(1);
    expect(WIDGET_TYPES.length).toBeGreaterThanOrEqual(11);
  });
});
