// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The catalog half of the option-issue wording gate.
//
// The Record<OptionIssueCode, string> in optionIssues.ts makes tsc refuse a new issue
// code until it is mapped to a translation key. That is only half the contract: a key
// mapped to a string no catalog defines still builds, and the UI then shows something
// wrong in a way nothing else notices. This closes the other half.
//
// 🔴 AND IT DOES NOT ASK t() WHETHER A LOCALE HAS A STRING, because t() cannot answer.
// config.ts sets `fallbackLng: 'en'`, so a key missing from the Spanish catalog resolves
// to the ENGLISH sentence — a perfectly ordinary-looking string, in the wrong language,
// which a test built on t() passes without a murmur. (Verified: deleting an `es` entry
// left the first draft of this file green.) So the per-locale check reads the catalog
// through getResource, which returns undefined for a key that locale does not define,
// so nothing here goes through t() at all.

import i18n, { SUPPORTED_LOCALES } from '@/i18n/config';
import { describe, expect, it } from 'vitest';

import type { WidgetInstance, WidgetType } from '@devicechain/dashboards';

import { OPTION_ISSUE_MESSAGE_KEYS, widgetLabel } from './optionIssues';

const NS = 'dashboards';
const CODES = Object.entries(OPTION_ISSUE_MESSAGE_KEYS);

describe('option issue wording', () => {
  // This also covers the mapping pointing at a key NO catalog defines — i18next would
  // render such a key as the key itself. A separate t()-based test for that was deleted
  // as a control that could not fail: SUPPORTED_LOCALES includes `en`, so proving the
  // English resource exists already proves t() has something to return.
  it('gives every issue code its own string in every locale the console ships', () => {
    for (const locale of SUPPORTED_LOCALES) {
      for (const [code, key] of CODES) {
        const where = `${locale.code}/${code}`;
        const text = i18n.getResource(locale.code, NS, key) as string | undefined;
        expect(text, `${where} is missing from the ${locale.code} catalog`).toBeTypeOf('string');
        expect((text ?? '').length, where).toBeGreaterThan(0);
        // Every one of these names the option it is about. An entry that dropped the
        // interpolation would read as a general complaint on a board where only one of
        // twelve widgets is wrong.
        expect(text, `${where} does not interpolate the option name`).toContain('{{key}}');
      }
    }
  });

  // The counterweight: the checks above are only meaningful while the locales differ.
  // 🔴 PER KEY, not per catalog — comparing the whole rendered list would pass while
  // one entry was an untranslated copy of the English, since the other six still
  // differ. (Verified: that is exactly what the first draft of this file did.)
  it('is actually translated, not copied, in each locale', () => {
    const other = SUPPORTED_LOCALES.filter((l) => l.code !== 'en');
    expect(other.length, 'nothing to compare against — this check compares nothing').toBeGreaterThan(0);
    for (const locale of other) {
      for (const [code, key] of CODES) {
        expect(
          i18n.getResource(locale.code, NS, key),
          `${locale.code}/${code} is the English string verbatim`,
        ).not.toBe(i18n.getResource('en', NS, key));
      }
    }
  });
});

describe('widgetLabel', () => {
  const widget = (id: string, type: WidgetType, options?: Record<string, unknown>): WidgetInstance => ({
    id,
    type,
    layout: { base: { col: 0, row: 0, colSpan: 4, rowSpan: 2, z: 0 } },
    options,
  });

  it('prefers the title an author gave the widget', () => {
    const board = { widgets: [widget('w1', 'image', { title: 'Floor plan', url: 'u' })] };
    expect(widgetLabel(board, 'w1')).toBe('Floor plan');
  });

  // A title that is absent, blank, whitespace or not a string is not a label. Rendering
  // one would give the author a nameless row and nothing to search the board for — and
  // whitespace is the case a plain `title || id` fallback gets wrong.
  it('falls back to the id for anything that is not a usable title', () => {
    for (const bad of ['', '   ', 42, null, undefined, { text: 'x' }]) {
      const board = { widgets: [widget('w1', 'image', { title: bad })] };
      expect(widgetLabel(board, 'w1'), `title ${JSON.stringify(bad)}`).toBe('w1');
    }
  });

  it('falls back to the id for a widget the board does not carry', () => {
    expect(widgetLabel({ widgets: [] }, 'gone')).toBe('gone');
  });
});
