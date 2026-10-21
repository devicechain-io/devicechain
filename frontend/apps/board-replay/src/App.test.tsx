// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

/// <reference types="node" />

// The replay page, rendered with the REAL dashboard widgets over a recorded data source.

import { createRecordedClock, effectiveBindings } from '@devicechain/dashboards';
import * as dashboards from '@devicechain/dashboards';
import { DashboardRenderer } from '@devicechain/widgets';
import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import ReplayApp from './App';
import i18n, { SUPPORTED_LOCALES, setReplayLocale } from './i18n/config';
import { anchorsOf, loadReplay } from './load';
import {
  BOARD_URL,
  EXCERPT_AVAILABLE,
  ORIGIN,
  RECORDING_URL,
  boardBytes,
  excerptOnCurrentBoardText,
  fixtureRecordingText,
  SHOWCASE_PATH,
  manualTicker,
  showcaseOnCurrentBoardText,
  stubFetch,
} from './testing/harness';
import { readFileSync } from 'node:fs';
import { hostWidgetRegistry } from '@devicechain/host-widgets';
import { canvasStrings, liveMatches, readableStrings } from './testing/live-scan';

// Counts every data source the app builds (a seek must build exactly one).
const built = vi.hoisted(() => ({ count: 0 }));
vi.mock('@devicechain/dashboards', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/dashboards')>();
  class CountingSource extends actual.RecordedDataSource {
    constructor(...args: ConstructorParameters<typeof actual.RecordedDataSource>) {
      super(...args);
      built.count++;
    }
  }
  return { ...actual, RecordedDataSource: CountingSource };
});
const { RecordedDataSource } = dashboards;

// jsdom has no WebGL: a recording WITH positions (the showcase) draws its map through the
// widget's own fallback panel, the one it shows when the renderer cannot be loaded.
vi.mock('maplibre-gl', () => {
  throw new Error('jsdom: no WebGL for the map renderer');
});

const canvasText = (globalThis as unknown as { __canvasText: string[] }).__canvasText;

// The gauge prints its number as DOM text over the dial (it used to be canvas text, which the
// readability gate cannot see), so the reading is read off the page -- off the ONE gauge named,
// and it must be there: a readout that never rendered would otherwise satisfy a "does not show".
const gaugeReading = (widgetId: string) => {
  const el = document.querySelector(`[data-widget="${widgetId}"] [data-level-readout]`);
  expect(el, `no readout on gauge ${widgetId}`).not.toBeNull();
  return el!.textContent ?? '';
};

beforeEach(async () => {
  await setReplayLocale('en');
  canvasText.length = 0;
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

const BADGE = 'Replay of a recorded run';

const SWEEP_TIMEOUT_MS = 30_000;

async function mountApp(recordingText: string, opts: { locale?: string } = {}) {
  if (opts.locale) await setReplayLocale(opts.locale);
  const stub = stubFetch({ [RECORDING_URL]: recordingText, [BOARD_URL]: boardBytes() });
  const ticker = manualTicker();
  const view = render(
    <ReplayApp recordingUrl={RECORDING_URL} boardUrl={BOARD_URL} fetchFn={stub.fetchFn} origin={ORIGIN} ticker={ticker} />,
  );
  await screen.findByTestId('transport-bar');
  await settle();
  return { stub, ticker, view };
}

// Let the recorded source's start-of-subscription microtasks and React's follow-up renders run.
async function settle() {
  await act(async () => {
    await new Promise((r) => setTimeout(r, 30));
  });
}

const range = () => document.querySelector('input[type="range"]') as HTMLInputElement;

async function seekTo(ms: number) {
  await act(async () => {
    fireEvent.change(range(), { target: { value: String(ms) } });
    fireEvent.pointerUp(range());
  });
  await settle();
}

const label = () => screen.getByTestId('replay-label').textContent ?? '';
const bodyState = () => screen.getByTestId('replay-body').getAttribute('data-state');
const never = (() => new Promise(() => {})) as unknown as typeof fetch;

describe('the replay label is on screen in every state', () => {
  it('while loading', () => {
    render(<ReplayApp recordingUrl={RECORDING_URL} boardUrl={BOARD_URL} fetchFn={never} origin={ORIGIN} />);
    expect(bodyState()).toBe('loading');
    expect(label()).toBe(BADGE);
  });

  it('on a load error', async () => {
    const stub = stubFetch({});
    render(<ReplayApp recordingUrl={RECORDING_URL} boardUrl={BOARD_URL} fetchFn={stub.fetchFn} origin={ORIGIN} />);
    await screen.findByRole('alert');
    expect(bodyState()).toBe('error');
    expect(label()).toBe(BADGE);
  });

  it('on a recording that fails to parse', async () => {
    const stub = stubFetch({ [RECORDING_URL]: '{"nope":true}', [BOARD_URL]: boardBytes() });
    render(<ReplayApp recordingUrl={RECORDING_URL} boardUrl={BOARD_URL} fetchFn={stub.fetchFn} origin={ORIGIN} />);
    await screen.findByRole('alert');
    expect(label()).toBe(BADGE);
    expect(screen.getByRole('alert').textContent).toContain('The recording file is not valid');
  });

  it('when paused, playing, seeking and ended, with the date taken from the recording and not the clock', async () => {
    vi.useFakeTimers({ toFake: ['Date'] });
    vi.setSystemTime(new Date('2031-05-05T12:00:00Z'));
    const { ticker } = await mountApp(await fixtureRecordingText());
    const full = `${BADGE} · Jan 2, 2026 · run-fixture`;

    expect(screen.getByTestId('replay-state').textContent).toBe('Paused');
    expect(label()).toBe(full);

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Play' }));
    });
    expect(screen.getByTestId('replay-state').textContent).toBe('Playing');
    expect(bodyState()).toBe('playing');
    expect(label()).toBe(full);
    await act(async () => ticker.frame(1000));
    expect(label()).toBe(full);

    // A scrub in progress: the bar says Seeking and nothing has been committed yet.
    await act(async () => {
      fireEvent.change(range(), { target: { value: '30000' } });
    });
    expect(screen.getByTestId('replay-state').textContent).toBe('Seeking…');
    expect(bodyState()).toBe('seeking');
    expect(label()).toBe(full);
    await act(async () => {
      fireEvent.pointerUp(range());
    });
    await settle();
    expect(screen.getByTestId('replay-state').textContent).toBe('Playing');

    await seekTo(60_000);
    expect(screen.getByTestId('replay-state').textContent).toBe('End of the recording');
    expect(bodyState()).toBe('ended');
    expect(label()).toBe(full);
    expect(label()).not.toContain('2031');
  });

  // The sweeps below mount the whole replay once per language (and per state): a second or two
  // alone, all rendering, so on a busy machine they outlast the 5 s default. Their budget is
  // sized to the sweep; the single-mount cases keep the default.
  it('in every language', { timeout: SWEEP_TIMEOUT_MS }, async () => {
    const text = await fixtureRecordingText();
    for (const locale of SUPPORTED_LOCALES) {
      await mountApp(text, { locale });
      const parts = label().split(' · ');
      expect(parts).toHaveLength(3);
      expect(parts[0]).toBe(i18n.t('replayBadge'));
      expect(parts[2]).toBe('run-fixture');
      expect(parts[1]).toMatch(/2026/);
      cleanup();
    }
    await setReplayLocale('es');
    expect(i18n.t('replayBadge')).toBe('Reproducción de una ejecución grabada');
    await setReplayLocale('zh-CN');
    expect(i18n.t('replayBadge')).toBe('已录制运行的回放');
  });
});

describe('the word "live" is not on the page', () => {
  it('in any state of the page, in any of the three languages', { timeout: SWEEP_TIMEOUT_MS }, async () => {
    const text = await fixtureRecordingText();
    for (const locale of SUPPORTED_LOCALES) {
      await setReplayLocale(locale);
      const loading = render(<ReplayApp recordingUrl={RECORDING_URL} boardUrl={BOARD_URL} fetchFn={never} origin={ORIGIN} />);
      expect(readableStrings(loading.container).flatMap(liveMatches), `${locale} loading`).toEqual([]);
      cleanup();

      const bad = render(<ReplayApp recordingUrl="https://elsewhere.test/x.json" boardUrl={BOARD_URL} origin={ORIGIN} />);
      await screen.findByRole('alert');
      expect(readableStrings(bad.container).flatMap(liveMatches), `${locale} error`).toEqual([]);
      cleanup();

      // Ready, with an alarm on the board and the provenance panel open.
      const { view } = await mountApp(text, { locale });
      await seekTo(25_000);
      await act(async () => {
        fireEvent.click(screen.getAllByRole('button').find((b) => b.getAttribute('aria-expanded') === 'false')!);
      });
      expect(screen.getByTestId('provenance-panel')).toBeTruthy();
      const strings = readableStrings(view.container);
      expect(strings.length).toBeGreaterThan(100);
      expect(strings.flatMap(liveMatches), `${locale} ready`).toEqual([]);
      expect(canvasStrings().flatMap(liveMatches), `${locale} canvas`).toEqual([]);
      expect(canvasStrings().length).toBeGreaterThan(0);
      cleanup();
    }
  });

  it('control: the same board without a selection wiring does print it, and the scanner sees it', async () => {
    const stub = stubFetch({ [RECORDING_URL]: await fixtureRecordingText(), [BOARD_URL]: boardBytes() });
    const { recording, definition, siteAnchors } = await loadReplay({
      recordingUrl: RECORDING_URL,
      boardUrl: BOARD_URL,
      fetchFn: stub.fetchFn,
      origin: ORIGIN,
    });
    const source = new RecordedDataSource(recording, createRecordedClock(recording), { siteAnchors });
    const bare = render(
      <DashboardRenderer definition={definition} hub={source} seedHistory={false} bindings={effectiveBindings(definition)} />,
    );
    await settle();
    expect(bare.container.textContent).toContain('Selection is available on the live dashboard.');
    expect(readableStrings(bare.container).flatMap(liveMatches)).not.toEqual([]);
  });
});

describe('a recording cannot act', () => {
  it('renders no Ack, Clear or Send control, with an active alarm on the board', async () => {
    await mountApp(await fixtureRecordingText());
    await seekTo(25_000);
    // The alarm is on the board, so the assertion below is about a board that could have them.
    expect(document.body.textContent).toContain('tyre-pressure-low');
    const names = [
      ...screen.getAllByRole('button').map((b) => `${b.textContent} ${b.getAttribute('aria-label') ?? ''}`),
      ...Array.from(document.querySelectorAll('[aria-label]')).map((e) => e.getAttribute('aria-label') ?? ''),
    ];
    for (const n of names) expect(n, n).not.toMatch(/\b(ack(nowledge)?|clear|send|retry)\b/i);
  });
});

describe('transport', () => {
  it('plays at 1x, 4x and 16x, pauses, and restarts after the end', async () => {
    const { ticker } = await mountApp(await fixtureRecordingText());
    const at = () => Number(range().value);
    expect(at()).toBe(0);
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Play' }));
    });
    await act(async () => ticker.frame(2000));
    expect(at()).toBe(2000);

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: '4×' }));
    });
    expect(screen.getByText('Playback 4×')).toBeTruthy();
    await act(async () => ticker.frame(1000));
    expect(at()).toBe(6000);

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: '16×' }));
    });
    await act(async () => ticker.frame(1000));
    expect(at()).toBe(22_000);

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Pause' }));
    });
    expect(ticker.running()).toBe(false);
    expect(screen.getByTestId('replay-state').textContent).toBe('Paused');

    await seekTo(60_000);
    expect(screen.getByTestId('replay-state').textContent).toBe('End of the recording');
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Play again' }));
    });
    expect(at()).toBe(0);
    expect(screen.getByTestId('replay-state').textContent).toBe('Playing');
  });

  it('seeks to a chapter and shows its note', async () => {
    await mountApp(await fixtureRecordingText());
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /tyre alarm/ }));
    });
    await settle();
    expect(range().value).toBe('20000');
    expect(screen.getByText('tyre alarm: presenter prepared')).toBeTruthy();
  });

  it('shows the playback rate chip only when it differs from 1x', async () => {
    await mountApp(await fixtureRecordingText());
    expect(screen.queryByText(/^Playback /)).toBeNull();
  });
});

describe('scrubbing', () => {
  it('commits one seek on release, not one per input event', async () => {
    await mountApp(await fixtureRecordingText());
    const before = built.count;
    await act(async () => {
      for (const v of [5000, 10_000, 15_000, 20_000, 25_000]) fireEvent.change(range(), { target: { value: String(v) } });
    });
    expect(built.count).toBe(before);
    expect(bodyState()).toBe('seeking');
    await act(async () => {
      fireEvent.pointerUp(range());
    });
    expect(built.count).toBe(before + 1);
    expect(range().value).toBe('25000');
    // Later release events with nothing pending do not seek again.
    await act(async () => {
      fireEvent.keyUp(range());
      fireEvent.blur(range());
    });
    expect(built.count).toBe(before + 1);
  });

  it.each([
    ['pointercancel', (el: Element) => fireEvent.pointerCancel(el)],
    ['lostpointercapture', (el: Element) => fireEvent.lostPointerCapture(el)],
  ])('abandons the scrub on %s: no seek, no stuck "Seeking…"', async (_name, cancel) => {
    await mountApp(await fixtureRecordingText());
    await seekTo(10_000);
    const before = built.count;
    await act(async () => {
      fireEvent.change(range(), { target: { value: '40000' } });
    });
    expect(bodyState()).toBe('seeking');
    await act(async () => {
      cancel(range());
    });
    expect(bodyState()).toBe('paused');
    expect(range().value).toBe('10000');
    expect(built.count).toBe(before);
  });
});

describe('simulation clock chip', () => {
  const withScale = (scale: number) =>
    fixtureRecordingText((d) => {
      d.clock = [{ fromMs: 11, mode: 'accelerated', scale }];
    });

  it('names an accelerated clock, a slowed one, and says nothing at 1x', async () => {
    await mountApp(await withScale(4));
    await seekTo(1000);
    expect(screen.getByText('Accelerated simulation clock ×4')).toBeTruthy();
    cleanup();
    await mountApp(await withScale(0.5));
    await seekTo(1000);
    expect(screen.getByText('Slowed simulation clock ×0.5')).toBeTruthy();
    expect(screen.queryByText(/Accelerated/)).toBeNull();
    cleanup();
    await mountApp(await withScale(1));
    await seekTo(1000);
    expect(screen.queryByText(/simulation clock/)).toBeNull();
  });
});

describe('times are on the recording clock, not the viewer clock', () => {
  const TZ = process.env.TZ;
  afterEach(() => {
    if (TZ === undefined) delete process.env.TZ;
    else process.env.TZ = TZ;
  });

  it('prints the raised time of an alarm in UTC even when the viewer is a day behind', async () => {
    process.env.TZ = 'America/New_York';
    const occ = new Date(Date.UTC(2026, 0, 2, 3, 4, 24, 700)).toISOString();
    // The viewer's own rendering is a different wall time (the control: the zone took effect).
    const local = new Date(occ).toLocaleString();
    const utc = new Date(occ).toLocaleString(undefined, { timeZone: 'UTC', timeZoneName: 'short' });
    expect(local).not.toBe(new Date(occ).toLocaleString(undefined, { timeZone: 'UTC' }));

    await mountApp(await fixtureRecordingText());
    await seekTo(25_000);
    expect(screen.getByText(utc)).toBeTruthy();
    expect(screen.queryByText(local)).toBeNull();
    // The header, the transport and the table agree on the date and the clock.
    expect(label()).toContain('Jan 2, 2026');
    expect(utc).toContain('UTC');
    expect(screen.getByText(/Recorded time 03:04:30 UTC/)).toBeTruthy();
  });
});

describe('provenance', () => {
  it('shows the run, build, board commit and hash, and says what the recording is', async () => {
    await mountApp(await fixtureRecordingText());
    expect(screen.queryByTestId('provenance-panel')).toBeNull();
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'About this recording' }));
    });
    const panel = within(screen.getByTestId('provenance-panel'));
    expect(panel.getByText('run-fixture')).toBeTruthy();
    expect(panel.getByText('abcdef123456 / fedcba654321')).toBeTruthy();
    expect(panel.getByText('b'.repeat(40))).toBeTruthy();
    expect(panel.getByText('Recorded against a running DeviceChain instance.')).toBeTruthy();
    expect(panel.getByText('3 simulated machines, each its own DeviceChain device.')).toBeTruthy();
    expect(panel.getByText('Widget text and board titles are shown in English.')).toBeTruthy();
    expect(panel.getByText(/never interpolated/)).toBeTruthy();
    expect(label()).toContain(BADGE);
  });

  it('is translated, including the line saying widget text is English', async () => {
    await mountApp(await fixtureRecordingText(), { locale: 'es' });
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Acerca de esta grabación' }));
    });
    expect(screen.getByText('El texto de los widgets y los títulos del panel se muestran en inglés.')).toBeTruthy();
  });
});

describe('integrity', () => {
  it('refuses to render a board that does not match the recording header', async () => {
    const text = await fixtureRecordingText((d) => {
      d.board.sha256 = 'f'.repeat(64);
    });
    const stub = stubFetch({ [RECORDING_URL]: text, [BOARD_URL]: boardBytes() });
    render(<ReplayApp recordingUrl={RECORDING_URL} boardUrl={BOARD_URL} fetchFn={stub.fetchFn} origin={ORIGIN} />);
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('does not match');
    expect(screen.queryByTestId('transport-bar')).toBeNull();
    expect(document.body.textContent).not.toContain('Active alarms');
    expect(label()).toBe(BADGE);
  });
});

describe('no network beyond the two files', () => {
  it('fetches the recording and the board and nothing else, through a full session', async () => {
    const trapped: string[] = [];
    const { stub, ticker } = await (async () => {
      const mounted = await mountApp(await fixtureRecordingText());
      // Installed after the two loads on purpose: from here on, ANY dial-out is a failure.
      for (const name of ['fetch', 'WebSocket', 'EventSource', 'XMLHttpRequest']) {
        vi.stubGlobal(name, function trap() {
          trapped.push(name);
          throw new Error(`network call attempted: ${name}`);
        });
      }
      return mounted;
    })();
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Play' }));
    });
    await act(async () => ticker.frame(3000));
    await seekTo(25_000);
    await act(async () => {
      fireEvent.change(screen.getByLabelText('Machine'), { target: { value: 'sp-hauler-02' } });
    });
    await settle();

    expect(stub.calls).toEqual([RECORDING_URL, BOARD_URL]);
    expect(trapped).toEqual([]);
    for (const u of stub.calls) expect(new URL(u).origin).toBe(ORIGIN);
  });
});

describe('selection', () => {
  it('lists the Machine selector candidates from the recording, so it is wired and not inert', async () => {
    await mountApp(await fixtureRecordingText());
    const pick = screen.getByLabelText('Machine') as HTMLSelectElement;
    expect(Array.from(pick.options).map((o) => o.value)).toEqual(
      expect.arrayContaining(['sp-hauler-01', 'sp-hauler-02', 'sp-plant-01']),
    );
  });

  it('keeps the selected machine across a seek', async () => {
    await mountApp(await fixtureRecordingText());
    const pick = () => screen.getByLabelText('Machine') as HTMLSelectElement;
    await act(async () => {
      fireEvent.change(pick(), { target: { value: 'sp-hauler-02' } });
    });
    await settle();
    expect(pick().value).toBe('sp-hauler-02');
    await seekTo(40_000);
    expect(pick().value).toBe('sp-hauler-02');
  });
});

describe('the showcase recording (format 2)', () => {
  // The stat cards' aggregates come from the recording itself (RecordedDataSource folds its own
  // rows into the buckets a card asks for). The oracle here is the excerpt's rows, read straight
  // from the file and folded by hand over the window a card over an hour reads: whole minutes,
  // ending at the last minute boundary before the cursor.
  it('computes Avg and Peak throughput from the recorded rows, exactly', async () => {
    const at = JSON.parse(readFileSync(SHOWCASE_PATH, 'utf-8')).chapters[0].tMs as number;
    const text = await showcaseOnCurrentBoardText();
    await mountApp(text);
    await seekTo(at);
    await settle();
    const doc = JSON.parse(text);
    const start = Date.parse(doc.startedAtUtc);
    const end = Math.floor((start + at) / 60_000) * 60_000;
    const from = Math.ceil((end - 3_600_000) / 60_000) * 60_000;
    const n = doc.channels.measurements.indexOf('throughput_tph');
    const values: number[] = [];
    doc.measurements.t.forEach((t: number, i: number) => {
      if (doc.measurements.n[i] === n && start + t >= from && start + t <= end - 1 && t <= at) values.push(doc.measurements.v[i]);
    });
    expect(values.length).toBeGreaterThan(0);
    const avg = values.reduce((a, b) => a + b, 0) / values.length;
    const card = (id: string) => document.querySelector(`[data-widget="${id}"]`)?.textContent ?? '';
    expect(card('sp-plant-throughput-avg')).toContain(`${Math.round(avg)}t/h`);
    expect(card('sp-plant-throughput-max')).toContain(`${Math.round(Math.max(...values))}t/h`);
    expect(document.body.textContent).not.toMatch(/cannot compute/i);
  });

  // The alarm table's Value column shows the reading that raised each alarm, as the live board
  // does. The oracle is the file: each alarm's last record at the chapter, folded by hand.
  it('shows the reading that raised every active alarm, never a dash', async () => {
    const doc = JSON.parse(readFileSync(SHOWCASE_PATH, 'utf-8'));
    const at = doc.chapters[0].tMs as number;
    const last = new Map<string, { state: string; val?: number; dev: string }>();
    for (const s of doc.alarms.snapshots) if (s.tMs <= at) for (const a of s.alarms) last.set(a.id, a);
    for (const e of doc.alarms.events) if (e.tMs <= at) last.set(e.id, { ...e, val: e.val ?? last.get(e.id)?.val });
    const active = [...last.values()].filter((a) => a.state === 'ACTIVE');
    expect(active.length).toBeGreaterThan(0);
    await mountApp(await showcaseOnCurrentBoardText());
    await seekTo(at);
    await settle();
    const table = document.querySelector('[data-widget="sp-alarms"]');
    const rows = [...(table?.querySelectorAll('tbody tr') ?? [])].map((r) => r.textContent ?? '');
    expect(rows).toHaveLength(active.length);
    for (const a of active) {
      expect(a.val, `${a.dev}: the recording holds no raising reading`).toEqual(expect.any(Number));
      expect(rows.find((r) => r.includes(a.dev))).toContain(a.val!.toFixed(1));
    }
    expect(rows.join('')).not.toContain('—');
  });

  // A stat card's Avg and Peak fold every row of its span, so a measurement any aggregate
  // widget reads must be recorded at the run's full rate: a thinned history drops the peak
  // reading and weights the average towards the unthinned tail. The registry says which
  // widgets aggregate; the run reported every 5 s.
  it('records every measurement the board aggregates at full rate', () => {
    const doc = JSON.parse(readFileSync(SHOWCASE_PATH, 'utf-8'));
    const board = JSON.parse(boardBytes().toString('utf-8'));
    const registry = hostWidgetRegistry();
    const aggregated = new Set<string>(
      board.widgets
        .filter((w: { type: string }) => registry.descriptor(w.type)?.channel === 'aggregate')
        .flatMap((w: { datasource?: { measurements?: string[] } }) => w.datasource?.measurements ?? []),
    );
    expect([...aggregated]).toEqual(['throughput_tph']);
    for (const name of aggregated) {
      const n = doc.channels.measurements.indexOf(name);
      for (let d = 0; d < doc.devices.length; d++) {
        const times = doc.measurements.t.filter((_: number, i: number) => doc.measurements.n[i] === n && doc.measurements.d[i] === d);
        for (let i = 1; i < times.length; i++) {
          expect(times[i] - times[i - 1], `${doc.devices[d].id} ${name}: a gap at ${times[i - 1]} ms`).toBeLessThanOrEqual(6_000);
        }
      }
    }
  });

  it('places the machines on the map from the recorded positions', async () => {
    await mountApp(await showcaseOnCurrentBoardText());
    await seekTo(JSON.parse(readFileSync(SHOWCASE_PATH, 'utf-8')).chapters[0].tMs);
    await settle();
    const map = document.querySelector('[data-widget="sp-map"]')?.textContent ?? '';
    expect(map).not.toContain('Not in this recording');
    expect(map).not.toContain('No positions reported');
    expect(map).toContain('positions are shown');
  });
});

describe.skipIf(!EXCERPT_AVAILABLE)('the first recorded run (converter excerpt)', () => {
  // The chapter's own time, on the same clock as the measurements (ms from the run start).
  const T_ALARM = 441_124;

  it('golden at 441.2 s: one MAJOR alarm, and SP-HL-0003 tyre pressure 599.6 after the drill', async () => {
    await mountApp(await excerptOnCurrentBoardText());
    await seekTo(441_200);

    const text = document.body.textContent ?? '';
    expect(text).toContain('Critical alarmsacme-earthworks0alarms');
    expect(text).toContain('Major alarmsacme-earthworks1alarm');
    expect(text).toContain('tyre-pressure-low');

    // Before the drill the tyre gauge is on the default machine, the first haul truck: it must
    // be showing that truck's reading, not the drilled truck's.
    expect(gaugeReading('sp-tyre')).not.toContain('599.6');

    // The alarm's originator and the fleet table's row name the truck the SAME way, so a viewer
    // can carry it from one to the other: two drills, one name.
    const alarms = document.querySelector<HTMLElement>('[data-widget="sp-alarms"]')!;
    const fleet = document.querySelector<HTMLElement>('[data-widget="sp-fleet"]')!;
    expect(within(fleet).getByRole('button', { name: 'sp-hauler-03' })).toBeTruthy();
    await act(async () => {
      fireEvent.click(within(alarms).getByRole('button', { name: 'sp-hauler-03' }));
    });
    await settle();
    expect((screen.getByLabelText('Machine') as HTMLSelectElement).value).toBe('sp-hauler-03');
    expect(gaugeReading('sp-tyre')).toContain('599.6');
  });

  it('the fleet table row drills to the same gauge as the alarm', async () => {
    await mountApp(await excerptOnCurrentBoardText());
    await seekTo(441_200);
    const fleet = document.querySelector<HTMLElement>('[data-widget="sp-fleet"]')!;
    await act(async () => {
      fireEvent.click(within(fleet).getByRole('button', { name: 'sp-hauler-03' }));
    });
    await settle();
    expect((screen.getByLabelText('Machine') as HTMLSelectElement).value).toBe('sp-hauler-03');
    expect(gaugeReading('sp-tyre')).toContain('599.6');
  });

  it('the Machine selector drives the same gauge', async () => {
    await mountApp(await excerptOnCurrentBoardText());
    await seekTo(441_200);
    await act(async () => {
      fireEvent.change(screen.getByLabelText('Machine'), { target: { value: 'sp-hauler-03' } });
    });
    await settle();
    expect(gaugeReading('sp-tyre')).toContain('599.6');
  });

  it('shows the map as "Not in this recording", never as an empty map', async () => {
    await mountApp(await excerptOnCurrentBoardText());
    expect(document.body.textContent).toContain('Not in this recording');
  });

  it('a chapter that asserts an alarm lands on a board that shows it', async () => {
    const { view } = await mountApp(await excerptOnCurrentBoardText());
    const chapter = screen.getByRole('button', { name: /tyre alarm/ });
    expect(chapter.textContent).toContain('0:21');
    await act(async () => {
      fireEvent.click(chapter);
    });
    await settle();
    expect(range().value).toBe(String(T_ALARM));
    expect(view.container.textContent).toContain('tyre-pressure-low');
    expect(view.container.textContent).toContain('Major alarmsacme-earthworks1alarm');
  });

  it('control: one millisecond before the chapter the alarm is not yet on the board', async () => {
    const { view } = await mountApp(await excerptOnCurrentBoardText());
    await seekTo(T_ALARM - 1);
    expect(view.container.textContent).not.toContain('tyre-pressure-low');
    expect(view.container.textContent).toContain('Major alarmsacme-earthworks0alarms');
  });
});

describe('anchors', () => {
  it('are read from the board, not assumed', async () => {
    const stub = stubFetch({ [RECORDING_URL]: await fixtureRecordingText(), [BOARD_URL]: boardBytes() });
    const { definition } = await loadReplay({
      recordingUrl: RECORDING_URL,
      boardUrl: BOARD_URL,
      fetchFn: stub.fetchFn,
      origin: ORIGIN,
    });
    expect(anchorsOf(definition)).toHaveLength(1);
    expect(anchorsOf({ ...definition, slots: {} })).toEqual([]);
  });
});
