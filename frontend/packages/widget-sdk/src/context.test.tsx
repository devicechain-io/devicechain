// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { render } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import {
  WidgetCandidatesProvider,
  WidgetSelectProvider,
  WidgetSubjectProvider,
  WidgetThemeRootProvider,
  WidgetTimeZoneProvider,
  useWidgetCandidates,
  useWidgetSelect,
  useWidgetSubject,
  useWidgetThemeRoot,
  useWidgetTimeZone,
} from './context';

function Probe({ onRead }: { onRead: (v: Record<string, unknown>) => void }) {
  onRead({
    select: useWidgetSelect(),
    candidates: useWidgetCandidates(),
    subject: useWidgetSubject(),
    timeZone: useWidgetTimeZone(),
    themeRoot: useWidgetThemeRoot(),
  });
  return null;
}

describe('ambient widget context', () => {
  it('reads undefined where no host provides it', () => {
    let seen: Record<string, unknown> = {};
    render(<Probe onRead={(v) => (seen = v)} />);
    expect(seen).toEqual({
      select: undefined,
      candidates: undefined,
      subject: undefined,
      timeZone: undefined,
      themeRoot: undefined,
    });
  });

  it('delivers what each provider supplies', () => {
    const select = () => undefined;
    const candidates = async () => [];
    const root = document.createElement('div');
    let seen: Record<string, unknown> = {};
    render(
      <WidgetSelectProvider select={select}>
        <WidgetCandidatesProvider candidates={candidates}>
          <WidgetSubjectProvider label="Pump 7">
            <WidgetTimeZoneProvider timeZone="UTC">
              <WidgetThemeRootProvider root={root}>
                <Probe onRead={(v) => (seen = v)} />
              </WidgetThemeRootProvider>
            </WidgetTimeZoneProvider>
          </WidgetSubjectProvider>
        </WidgetCandidatesProvider>
      </WidgetSelectProvider>,
    );
    expect(seen.select).toBe(select);
    expect(seen.candidates).toBe(candidates);
    expect(seen.subject).toBe('Pump 7');
    expect(seen.timeZone).toBe('UTC');
    expect(seen.themeRoot).toBe(root);
  });
});
