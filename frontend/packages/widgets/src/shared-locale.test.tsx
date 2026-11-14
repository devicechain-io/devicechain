// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { parseDashboardDefinition, type DashboardHub, type WidgetInstance } from '@devicechain/dashboards';
import { WidgetCandidatesProvider, WidgetSelectProvider } from '@devicechain/widget-sdk';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ConnectedWidget } from './connected-widget';
import { WidgetContainment } from './widget-containment';
import { TimeRangeBar } from './time-range-bar';
import { EntitySelector } from './widgets/entity-selector';
import { MapWidget } from './widgets/map';
import { AlarmCount } from './widgets/alarm-count';
import { Image } from './widgets/image';

let lang: string | null;
beforeEach(() => { lang = document.documentElement.getAttribute('lang'); });
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  if (lang === null) document.documentElement.removeAttribute('lang');
  else document.documentElement.lang = lang;
});

const widget: WidgetInstance = {
  id: 'selector', type: 'entity-selector', options: { selectionTarget: 'pump' },
  layout: { base: { mode: 'grid', col: 0, row: 0, cols: 4, rows: 3 } },
};

describe.each([
  { locale: 'es-MX', prompt: 'Seleccionar…', label: 'Seleccionar pump', failed: 'No se pudieron cargar las opciones.',
    reset: 'Restablecer widget', renderFailed: 'No se pudo mostrar este widget', pause: 'Pausar',
    custom: 'Intervalo personalizado', newer: 'Este widget necesita un visor más reciente', location: 'Ubicación no permitida' },
  { locale: 'zh-CN', prompt: '请选择…', label: '选择 pump', failed: '无法加载选项。',
    reset: '重置组件', renderFailed: '无法显示此组件', pause: '暂停',
    custom: '自定义范围', newer: '此组件需要更新版本的查看器', location: '无权查看位置' },
])('shared widget prose in $locale', (words) => {
  beforeEach(() => { document.documentElement.lang = words.locale; });

  it('translates selector prompts and accessible names while preserving candidate labels and bindings', async () => {
    const select = vi.fn();
    render(
      <WidgetSelectProvider select={select}>
        <WidgetCandidatesProvider candidates={async () => [{
          binding: { kind: 'device', deviceToken: 'pump-3' }, label: 'Pump Three', selected: false,
        }]}>
          <EntitySelector widget={widget} data={null} />
        </WidgetCandidatesProvider>
      </WidgetSelectProvider>,
    );
    await screen.findByRole('option', { name: 'Pump Three' });
    expect(screen.getByRole('option', { name: words.prompt })).toBeTruthy();
    fireEvent.change(screen.getByRole('combobox', { name: words.label }), { target: { value: 'pump-3' } });
    expect(select).toHaveBeenCalledWith({ slot: 'pump', binding: { kind: 'device', deviceToken: 'pump-3' } });
  });

  it('translates a failed candidate lookup', async () => {
    render(
      <WidgetSelectProvider select={vi.fn()}>
        <WidgetCandidatesProvider candidates={async () => { throw new Error('server failure'); }}>
          <EntitySelector widget={widget} data={null} />
        </WidgetCandidatesProvider>
      </WidgetSelectProvider>,
    );
    expect(await screen.findByText(words.failed)).toBeTruthy();
  });

  it('translates the error boundary and resets the failing widget', () => {
    vi.spyOn(console, 'error').mockImplementation(() => {});
    let broken = true;
    function Body() { if (broken) throw new Error('diagnostic'); return <span>tenant content</span>; }
    render(<WidgetContainment resetKey="one"><Body /></WidgetContainment>);
    expect(screen.getByText(words.renderFailed)).toBeTruthy();
    broken = false;
    fireEvent.click(screen.getByRole('button', { name: words.reset }));
    expect(screen.getByText('tenant content')).toBeTruthy();
  });

  it('translates the unknown-widget state without opening a channel', () => {
    const hub = { subscribeWidget: vi.fn() } as unknown as DashboardHub;
    const board = parseDashboardDefinition({ widgets: [{ ...widget, type: 'future-widget' }] });
    render(<ConnectedWidget widget={board.widgets[0]} hub={hub} />);
    expect(screen.getByText(words.newer)).toBeTruthy();
    expect(screen.getByText(/future-widget/)).toBeTruthy();
    expect(hub.subscribeWidget).not.toHaveBeenCalled();
  });

  it('uses localized time controls even when the host supplies no labels', () => {
    const pause = vi.fn();
    render(<TimeRangeBar state={{ spanSeconds: 3600, paused: false }} controls={{
      setSpan: vi.fn(), pause, resume: vi.fn(), setAbsolute: vi.fn(),
    }} />);
    fireEvent.click(screen.getByRole('button', { name: words.pause }));
    expect(pause).toHaveBeenCalledOnce();
    expect(screen.getByRole('button', { name: words.custom })).toBeTruthy();
  });

  it('translates the map permission state before checking host configuration', () => {
    render(<MapWidget widget={{ ...widget, type: 'map' }} data={{
      deviceTokens: [], locations: [], loading: false, error: null, forbidden: true,
    }} />);
    expect(screen.getByText(words.location)).toBeTruthy();
  });
});

it.each([
  { locale: 'es', caption: 'alarmas', worst: 'la peor: Crítica', aria: '2 alarmas, mayor severidad: Crítica', image: 'No se ha indicado la URL de la imagen' },
  { locale: 'zh-CN', caption: '条告警', worst: '最高严重程度：危急', aria: '2 条告警，最高严重程度：危急', image: '尚未设置图片 URL' },
])('translates static placeholders and the alarm count in $locale', (words) => {
  document.documentElement.lang = words.locale;
  render(<>
    <AlarmCount widget={{ ...widget, type: 'alarm-count' }} data={{
      total: 2, loading: false, error: null,
      alarms: [{
        token: 'alarm-1', severity: 'CRITICAL', originatorType: 'device', originatorToken: 'pump-3',
        alarmKey: 'temperature', metricKey: 'temperature', state: 'ACTIVE', acknowledged: false,
        raisedTime: '2026-10-22T14:00:00Z', clearedTime: null, acknowledgedTime: null,
        acknowledgedBy: null, lastValue: 80,
      }],
    }} />
    <Image widget={{ ...widget, type: 'image', options: {} }} data={null} />
  </>);
  expect(screen.getByLabelText(words.aria)).toBeTruthy();
  expect(screen.getByText(words.caption)).toBeTruthy();
  expect(screen.getByText(`· ${words.worst}`)).toBeTruthy();
  expect(screen.getByText(words.image)).toBeTruthy();
});
