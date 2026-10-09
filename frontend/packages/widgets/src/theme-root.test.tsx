// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';

import { WidgetThemeRootProvider } from './frame';
import { useChartTheme } from './hooks';

afterEach(cleanup);

function Probe() {
  return <span data-testid="fg">{useChartTheme().foreground}</span>;
}

describe('chart theme root', () => {
  it('reads the tokens at the document root when no root is provided (unchanged default)', () => {
    document.documentElement.style.setProperty('--foreground', '10 20% 30%');
    render(<Probe />);
    expect(screen.getByTestId('fg').textContent).toBe('hsl(10 20% 30%)');
    document.documentElement.style.removeProperty('--foreground');
  });

  it('reads the tokens at a provided container, not the document root', () => {
    document.documentElement.style.setProperty('--foreground', '10 20% 30%');
    const host = document.createElement('div');
    host.style.setProperty('--foreground', '200 50% 60%');
    document.body.appendChild(host);
    render(
      <WidgetThemeRootProvider root={host}>
        <Probe />
      </WidgetThemeRootProvider>,
    );
    expect(screen.getByTestId('fg').textContent).toBe('hsl(200 50% 60%)');
    host.remove();
    document.documentElement.style.removeProperty('--foreground');
  });
});
