// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import { schemaNegotiationWanted } from './schema-negotiation';

describe('when the console negotiates its schema', () => {
  it('is off in a production build served by its own instance', () => {
    expect(schemaNegotiationWanted({ DEV: false })).toBe(false);
    expect(schemaNegotiationWanted({})).toBe(false);
  });

  it('is on under vite dev, where the proxy can reach any instance', () => {
    expect(schemaNegotiationWanted({ DEV: true })).toBe(true);
  });

  it('is on in a build made for a console hosted apart from its instance', () => {
    expect(schemaNegotiationWanted({ DEV: false, VITE_SCHEMA_NEGOTIATION: 'on' })).toBe(true);
    expect(schemaNegotiationWanted({ DEV: false, VITE_SCHEMA_NEGOTIATION: ' TRUE ' })).toBe(true);
  });

  it('can be switched off even in dev', () => {
    expect(schemaNegotiationWanted({ DEV: true, VITE_SCHEMA_NEGOTIATION: 'off' })).toBe(false);
  });

  it('ignores an unrecognised setting rather than guessing', () => {
    expect(schemaNegotiationWanted({ DEV: false, VITE_SCHEMA_NEGOTIATION: 'maybe' })).toBe(false);
    expect(schemaNegotiationWanted({ DEV: true, VITE_SCHEMA_NEGOTIATION: 'maybe' })).toBe(true);
  });
});
