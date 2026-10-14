// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The dashboards package lists generator IDS; the console owns their display names. Every id
// the package offers must have a label in both languages, or a picker would show the raw id.
import { describe, expect, it } from 'vitest';
import { SYNTHETIC_GENERATORS } from '@devicechain/dashboards';
import en from '@/i18n/locales/en/dashboards.json';
import es from '@/i18n/locales/es/dashboards.json';

describe('synthetic generator labels', () => {
  it('has an en and es label for every generator id, distinct from the id', () => {
    for (const g of SYNTHETIC_GENERATORS) {
      for (const cat of [en, es] as Array<Record<string, string>>) {
        const label = cat[`syntheticGenerator_${g}`];
        expect(label, g).toBeTruthy();
        expect(label, g).not.toBe(g);
      }
    }
  });
});
