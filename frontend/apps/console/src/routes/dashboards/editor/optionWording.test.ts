// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The lookup ORDER is the contract: the host's message for a declared id, then the package's
// portable text, then (labels only) the key made readable. A built-in carries no portable
// label, so swapping the first two steps passes every built-in test — it shows up only for a
// descriptor that carries both, which is what these cases are.

import { describe, expect, it } from 'vitest';

import type { EnumOptionSpec, OptionSpec } from '@devicechain/dashboards';

import { enumChoiceLabel, fieldHelp, fieldLabel, humanizeKey, type MessageLookup } from './optionWording';

const CATALOG: Record<string, string> = { hostLabel: 'Host label', hostHelp: 'Host help', hostUp: 'Host up' };
const lookup: MessageLookup = (id) => (Object.prototype.hasOwnProperty.call(CATALOG, id) ? CATALOG[id] : undefined);

describe('option wording', () => {
  it('prefers the host message over the portable text when the host knows the id', () => {
    const spec: OptionSpec = { kind: 'string', doc: '.', labelId: 'hostLabel', label: 'Pkg label', helpId: 'hostHelp', help: 'Pkg help' };
    expect(fieldLabel('k', spec, lookup)).toBe('Host label');
    expect(fieldHelp(spec, lookup)).toBe('Host help');
  });

  it('falls back to the portable text for an id the host does not define', () => {
    const spec: OptionSpec = { kind: 'string', doc: '.', labelId: 'nope', label: 'Pkg label', helpId: 'nope', help: 'Pkg help' };
    expect(fieldLabel('k', spec, lookup)).toBe('Pkg label');
    expect(fieldHelp(spec, lookup)).toBe('Pkg help');
  });

  it('humanizes the key for a label with neither, and shows no help', () => {
    const spec: OptionSpec = { kind: 'string', doc: '.' };
    expect(fieldLabel('maxRows', spec, lookup)).toBe('Max rows');
    expect(fieldHelp(spec, lookup)).toBeUndefined();
    expect(humanizeKey('tank_fill-level')).toBe('Tank fill level');
  });

  it('words enum choices by the same order, and never from an inherited member', () => {
    const spec: EnumOptionSpec = {
      kind: 'enum',
      doc: '.',
      values: ['up', 'down', 'constructor'],
      valueLabelIds: { up: 'hostUp' },
      valueLabels: { up: 'Pkg up', down: 'Pkg down' },
    };
    expect(enumChoiceLabel(spec, 'up', lookup)).toBe('Host up');
    expect(enumChoiceLabel(spec, 'down', lookup)).toBe('Pkg down');
    expect(enumChoiceLabel(spec, 'constructor', lookup)).toBe('constructor');
  });
});
