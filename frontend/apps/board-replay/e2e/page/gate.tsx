// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The readability gate's page: one committed board, drawn by the replay's own Board (the real
// dashboard renderer), at whatever viewport the browser has, in light or dark.
//
//   gate.html?board=sim/<name>        a board in frontend/testdata/sim-dashboards
//   gate.html?board=fixture/<name>    a board in e2e/fixtures (the negative control)
//   &theme=dark                       sets `.dark` on <html>; anything else is light
//   &embed=WxH                        draws the board through the replay's embed (EmbedStage):
//                                     a fixed WxH canvas scaled to fit the viewport
//   &embed=WxH&reflow=1               the NEGATIVE CONTROL of the embed checks: the same board
//                                     laid out at the viewport's width instead, which is what
//                                     the embed exists to prevent (readability.e2e.ts)
//   &embed=WxH&host=flow               the embed in a host with a width and NO height (a block in
//                                     a page's flow), instead of one filling the viewport
//   &recording=showcase               draw sitepulse from the showcase recording, not the
//                                     first-run excerpt
//
// DATA. A board with a committed recording is drawn from it (sitepulse: the first recorded
// run's excerpt), with the clock paused at the run's first chapter and `Date.now` pinned to
// the recorded instant there, so its values, device ids and alarm rows are real ones and its
// ages read the same on every run. Every other board is drawn from the synthetic source with
// min = max, so each reading is ONE constant, and with `Date.now` pinned (SYNTHETIC_NOW), so
// every timestamp the source stamps is one instant too. The text is then the same on every
// run, at whatever time of day it runs: a clock that read 3:05 one run and 12:58 the next would
// change the width of every time column. 888.8 is three integer digits and a fraction, the
// widest reading the sim boards carry (tyre pressure in kPa), and the instant prints as wide as
// a date and time can (12/28/2026, 10:58:58 PM). Only `Date.now` is pinned, not the Date
// constructor, which the chart library's animation clock reads.
//
// THEME AND TYPE. The tokens are /dash's own stylesheet (light on :root, dark on `.dark`), so
// the gate reads the colours the viewer ships rather than a copy that could drift. The face is
// Inter, which the console sets and which is the widest of the faces a board is drawn in: it is
// bundled rather than taken from the system, so the gate measures the same glyphs on a Windows
// laptop and on a Linux runner.
//
// The page reports through attributes on <html>, never text: data-gate="ready" with
// data-gate-widgets (how many widgets the definition holds) and data-gate-charts /
// data-gate-charts-empty (the first frame's charts, and those of them not yet drawn), or
// data-gate="error" with data-gate-error.

import '@fontsource-variable/inter';
import '../../../dashboard/src/theme.css';
import './gate.css';

import {
  RecordedDataSource,
  SyntheticDataSource,
  createRecordedClock,
  createRecordedLister,
  createRecordedResolver,
  migrateToSlots,
  parseBoardRecording,
  parseDashboardDefinition,
  recordedWallTimeMs,
  type DashboardDefinition,
  type DatasourceSelector,
  type DeviceResolver,
  type EntityCandidateLister,
  type WidgetDataSource,
  type WidgetStreamSink,
} from '@devicechain/dashboards';
import { hostWidgetRegistry, type WidgetRegistry } from '@devicechain/host-widgets';
import { preloadWidgets } from '@devicechain/widgets';
import { useEffect } from 'react';
import { createRoot } from 'react-dom/client';

import { Board } from '../../src/Board';
import { parseCanvas } from '../../src/embed';
import { EmbedStage } from '../../src/EmbedStage';
import { anchorsOf } from '../../src/load';

type Loader = () => Promise<string>;

const SIM_BOARDS = import.meta.glob<string>('../../../../testdata/sim-dashboards/*.json', {
  query: '?raw',
  import: 'default',
});
const FIXTURE_BOARDS = import.meta.glob<string>('../fixtures/*.json', { query: '?raw', import: 'default' });

// The committed recording for a board, by board parameter. A recording is captured against one
// board, so only the board it was captured against is drawn from it.
// A Map, not an object: the key comes from the URL, and a plain object would answer
// inherited names (`constructor`, `__proto__`) with something that is not a loader.
const RECORDINGS = new Map<string, Loader>([
  ['sim/sp-dashboard', () => import('../../../../testdata/board-replay/take2-tyre-excerpt.json?raw').then((m) => m.default)],
  ['sim/sp-dashboard|showcase', () => import('../../../../testdata/board-replay/sitepulse-showcase.json?raw').then((m) => m.default)],
]);

export const SYNTHETIC_READING = 888.8;
export const SYNTHETIC_NOW = Date.UTC(2026, 11, 28, 22, 58, 58);

function loaderFor(param: string | null): Loader {
  const m = /^(sim|fixture)\/([a-z0-9-]+)$/.exec(param ?? '');
  if (!m) throw new Error(`board must be sim/<name> or fixture/<name>, got '${param}'`);
  const [, dir, name] = m;
  const table = dir === 'sim' ? SIM_BOARDS : FIXTURE_BOARDS;
  const base = dir === 'sim' ? '../../../../testdata/sim-dashboards' : '../fixtures';
  const key = `${base}/${name}.json`;
  const load = Object.prototype.hasOwnProperty.call(table, key) ? table[key] : undefined;
  if (!load) throw new Error(`no such board: ${param}`);
  return load;
}

// The synthetic boards' stand-ins for the device graph: an anchor has two members, every
// device exists, and a root selector offers two entities of each kind, named like real ones.
const syntheticResolver: DeviceResolver = {
  devicesForAnchor: async () => ['gate-device-01', 'gate-device-02'],
  deviceExists: async () => true,
};
const syntheticLister: EntityCandidateLister = async (kind) => [
  { token: `gate-${kind}-01`, name: `Gate ${kind} 01` },
  { token: `gate-${kind}-02`, name: `Gate ${kind} 02` },
];

// THE EXPLAINED EMPTY STATES (fixture/empty-states). A source that has nothing to say, in the five ways
// a widget can have nothing: a device whose profile does not declare the measurement
// (gate-fuelless), one that declares it and is quiet (gate-quiet), one whose only reading is from
// before the window (gate-old, which also gives the last-value hint), a refusal (gate-forbidden)
// and a failure (gate-broken). An unbound slot needs no source at all.
class EmptyStatesSource extends SyntheticDataSource {
  override subscribeWidget(datasource: DatasourceSelector, sink: WidgetStreamSink): () => void {
    const token = datasource.kind === 'device' ? datasource.deviceToken : '';
    if (token === 'gate-forbidden') sink.error?.([{ message: 'forbidden: missing required authority', extensions: { code: 'FORBIDDEN' } }]);
    else if (token === 'gate-broken') sink.error?.(new Error('socket closed'));
    else if (token === 'gate-old') {
      const at = new Date(SYNTHETIC_NOW - 3 * 86_400_000).toISOString();
      sink.next({ id: 'old', deviceToken: token, eventType: 0, occurredTime: at, name: 'temperature', value: 21.4, classifier: null });
    }
    return () => {};
  }

  async declaredMetricsForDevice(deviceToken: string): Promise<readonly string[] | null> {
    return deviceToken === 'gate-fuelless' ? ['temperature'] : ['temperature', 'fuel_level'];
  }
}

interface Prepared {
  definition: DashboardDefinition;
  registry: WidgetRegistry;
  source: WidgetDataSource;
  resolver: DeviceResolver;
  lister: EntityCandidateLister;
}

async function prepare(boardParam: string | null, recordingParam: string | null): Promise<Prepared> {
  const load = loaderFor(boardParam);
  const registry = hostWidgetRegistry();
  const definition = migrateToSlots(parseDashboardDefinition(JSON.parse(await load()), { catalog: registry.catalog }));
  // As the replay's loadReplay does: the board's lazily loaded code (widget chunks, the chart
  // engine) arrives before the first render, which is what makes the first frame whole.
  await preloadWidgets(registry, definition.widgets.map((w) => w.type));
  const recordingKey = recordingParam ? `${boardParam}|${recordingParam}` : (boardParam ?? '');
  const recordingLoader = RECORDINGS.get(recordingKey);
  if (recordingParam && !recordingLoader) throw new Error(`no recording '${recordingParam}' for ${boardParam}`);
  if (!recordingLoader) {
    Date.now = () => SYNTHETIC_NOW;
    if (boardParam === 'fixture/empty-states') {
      return { definition, registry, source: new EmptyStatesSource(), resolver: syntheticResolver, lister: syntheticLister };
    }
    return {
      definition,
      registry,
      source: new SyntheticDataSource({ generator: 'sine', min: SYNTHETIC_READING, max: SYNTHETIC_READING }),
      resolver: syntheticResolver,
      lister: syntheticLister,
    };
  }
  // The recording's board pin is provenance (the board it was captured against); the gate
  // asks whether the data still draws the board as it is NOW, as the replay's own suites do.
  const recording = parseBoardRecording(JSON.parse(await recordingLoader()));
  // A clock that is never ticked: it stays where it is put.
  const clock = createRecordedClock(recording, { ticker: { start: () => () => {} } });
  const chapter = recording.chapters[0]?.tMs ?? recording.durationMs;
  clock.seek(chapter);
  // NOW is the recorded instant at the cursor, so every age reads as the embed draws it (the
  // embed re-times the recording so that its cursor reads as now) and the text is the same on
  // every run. Left at the wall clock, an age grows by the hour, and two renders a few seconds
  // apart can straddle "13 hr. ago" -> "14 hr. ago": a text change the embed's layout check
  // reads as a reflow.
  const recordedNow = recordedWallTimeMs(recording, clock.timeMs);
  Date.now = () => recordedNow;
  const site = { siteAnchors: anchorsOf(definition) };
  return {
    definition,
    registry,
    source: new RecordedDataSource(recording, clock, site),
    resolver: createRecordedResolver(recording, site),
    lister: createRecordedLister(recording, site),
  };
}

interface EmbedCase {
  canvas: { width: number; height: number };
  reflow: boolean;
  flow: boolean;
}

function Ready({ prepared, embed }: { prepared: Prepared; embed: EmbedCase | null }) {
  useEffect(() => {
    const root = document.documentElement;
    root.dataset.gateWidgets = String(prepared.definition.widgets.length);
    // The FIRST commit's charts, read in its last passive effect (a parent's runs after its
    // children's, so every chart's own init has run): how many there are, and how many have not
    // drawn because the chart engine was not here when they mounted -- an empty first frame.
    root.dataset.gateCharts = String(document.querySelectorAll('[data-echart]').length);
    root.dataset.gateChartsEmpty = String(document.querySelectorAll('[data-echart]:not([_echarts_instance_])').length);
    root.dataset.gate = 'ready';
  }, [prepared]);
  const board = (
    <Board
      definition={prepared.definition}
      registry={prepared.registry}
      source={prepared.source}
      resolver={prepared.resolver}
      lister={prepared.lister}
      themeRoot={undefined}
    />
  );
  if (!embed) return board;
  if (embed.flow) {
    return (
      <div style={{ width: '100%' }}>
        <EmbedStage canvas={embed.canvas}>{board}</EmbedStage>
      </div>
    );
  }
  // The viewport is the embed's host box, as an iframe's would be.
  return (
    <div style={{ position: 'fixed', inset: 0 }}>
      {embed.reflow ? (
        <div data-testid="embed-host" style={{ width: '100%', height: '100%', overflow: 'hidden' }}>
          <div data-testid="embed-canvas" data-scale="1" style={{ width: '100%', height: '100%', overflow: 'hidden' }}>
            {board}
          </div>
        </div>
      ) : (
        <EmbedStage canvas={embed.canvas}>{board}</EmbedStage>
      )}
    </div>
  );
}

async function main() {
  const params = new URLSearchParams(window.location.search);
  document.documentElement.classList.toggle('dark', params.get('theme') === 'dark');
  try {
    // Load every weight a widget sets BEFORE the first render, so nothing is measured in a
    // fallback face and then reflowed.
    await Promise.all(['400', '500', '600', '700'].map((w) => document.fonts.load(`${w} 16px "Inter Variable"`)));
    const prepared = await prepare(params.get('board'), params.get('recording'));
    let embed: EmbedCase | null = null;
    const e = params.get('embed');
    if (e !== null) {
      const canvas = parseCanvas(e);
      if (!canvas) throw new Error(`embed must be WxH, got '${e}'`);
      embed = { canvas, reflow: params.get('reflow') === '1', flow: params.get('host') === 'flow' };
    }
    createRoot(document.getElementById('gate-root')!).render(<Ready prepared={prepared} embed={embed} />);
  } catch (err) {
    document.documentElement.dataset.gateError = err instanceof Error ? err.message : String(err);
    document.documentElement.dataset.gate = 'error';
  }
}

void main();
