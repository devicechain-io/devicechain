// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { act, cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { WidgetThemeRootProvider } from './context';
import { useChartTheme } from './chart/chart-theme';

afterEach(() => {
  cleanup();
  document.body.replaceChildren();
  document.documentElement.removeAttribute('style');
  document.documentElement.classList.remove('night');
  vi.restoreAllMocks();
});

function Probe() {
  return <span data-testid="fg">{useChartTheme().foreground}</span>;
}

describe('chart theme root', () => {
  it('keeps the default document palette live', async () => {
    document.documentElement.style.setProperty('--foreground', '10 20% 30%');
    render(<Probe />);
    await act(async () => { document.documentElement.style.setProperty('--foreground', '200 50% 60%'); });
    expect(screen.getByTestId('fg').textContent).toBe('hsl(200 50% 60%)');
  });

  it('switches subscriptions when the host changes roots and reads fresh tokens on remount', async () => {
    const first = document.createElement('div');
    const second = document.createElement('div');
    first.style.setProperty('--foreground', '10 20% 30%');
    second.style.setProperty('--foreground', '200 50% 60%');
    document.body.append(first, second);
    const view = render(<WidgetThemeRootProvider root={first}><Probe /></WidgetThemeRootProvider>);
    view.rerender(<WidgetThemeRootProvider root={second}><Probe /></WidgetThemeRootProvider>);
    expect(screen.getByTestId('fg').textContent).toBe('hsl(200 50% 60%)');
    await act(async () => { first.style.setProperty('--foreground', '300 60% 70%'); });
    expect(screen.getByTestId('fg').textContent).toBe('hsl(200 50% 60%)');
    view.unmount();
    second.style.setProperty('--foreground', '300 60% 70%');
    render(<WidgetThemeRootProvider root={second}><Probe /></WidgetThemeRootProvider>);
    expect(screen.getByTestId('fg').textContent).toBe('hsl(300 60% 70%)');
  });

  it('refreshes a scoped chart when an ancestor changes the active theme', async () => {
    const rules = document.createElement('style');
    rules.textContent = '.scope { --foreground: 10 20% 30%; } .night .scope { --foreground: 200 50% 60%; }';
    const host = document.createElement('div');
    host.className = 'scope';
    document.body.append(rules, host);
    render(<WidgetThemeRootProvider root={host}><Probe /></WidgetThemeRootProvider>);
    expect(screen.getByTestId('fg').textContent).toBe('hsl(10 20% 30%)');
    await act(async () => { document.documentElement.classList.add('night'); });
    expect(screen.getByTestId('fg').textContent).toBe('hsl(200 50% 60%)');
  });

  it('updates a scoped palette after inline and class changes, independently of other roots', async () => {
    const rules = document.createElement('style');
    rules.textContent = '.alternate-palette { --foreground: 100 40% 50%; }';
    document.body.appendChild(rules);
    const host = document.createElement('div');
    host.style.setProperty('--foreground', '200 50% 60%');
    const other = document.createElement('div');
    other.style.setProperty('--foreground', '10 20% 30%');
    document.body.append(host, other);
    render(<>
      <WidgetThemeRootProvider root={host}><Probe /><Probe /></WidgetThemeRootProvider>
      <WidgetThemeRootProvider root={other}><Probe /></WidgetThemeRootProvider>
    </>);
    const colors = () => screen.getAllByTestId('fg').map((node) => node.textContent);
    expect(colors()).toEqual(['hsl(200 50% 60%)', 'hsl(200 50% 60%)', 'hsl(10 20% 30%)']);
    await act(async () => { host.style.setProperty('--foreground', '300 60% 70%'); });
    expect(colors()).toEqual(['hsl(300 60% 70%)', 'hsl(300 60% 70%)', 'hsl(10 20% 30%)']);
    await act(async () => {
      host.style.removeProperty('--foreground');
      host.classList.add('alternate-palette');
    });
    expect(colors()).toEqual(['hsl(100 40% 50%)', 'hsl(100 40% 50%)', 'hsl(10 20% 30%)']);
  });

  it('shares one observer per root and disconnects only after its last chart unmounts', () => {
    const observe = vi.spyOn(MutationObserver.prototype, 'observe');
    const disconnect = vi.spyOn(MutationObserver.prototype, 'disconnect');
    const host = document.createElement('div');
    document.body.appendChild(host);
    const first = render(<WidgetThemeRootProvider root={host}><Probe /></WidgetThemeRootProvider>);
    const second = render(<WidgetThemeRootProvider root={host}><Probe /></WidgetThemeRootProvider>);
    expect(observe.mock.calls.filter(([node]) => node === host)).toHaveLength(1);
    first.unmount();
    expect(disconnect).not.toHaveBeenCalled();
    second.unmount();
    expect(disconnect).toHaveBeenCalledTimes(1);
  });

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
