// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The readability gate's page: one committed board, drawn by the replay's own Board (the real
// dashboard renderer), at whatever viewport the browser has, in light or dark.
//
//   gate.html?board=sim/<name>        a board in frontend/testdata/sim-dashboards
//   gate.html?board=fixture/<name>    a board in e2e/fixtures (the negative control)
//   &theme=dark                       sets `.dark` on <html>; anything else is light
//
// DATA. A board with a committed recording is drawn from it (sitepulse: the first recorded
// run's excerpt), with the clock paused at the run's first chapter, so its values, device ids
// and alarm rows are real ones. Every other board is drawn from the synthetic source with
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
// data-gate-widgets (how many widgets the definition holds), or data-gate="error" with
// data-gate-error.

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
  type DashboardDefinition,
  type DeviceResolver,
  type EntityCandidateLister,
  type WidgetDataSource,
} from '@devicechain/dashboards';
import { hostWidgetRegistry, type WidgetRegistry } from '@devicechain/host-widgets';
import { useEffect } from 'react';
import { createRoot } from 'react-dom/client';

import { Board } from '../../src/Board';
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

interface Prepared {
  definition: DashboardDefinition;
  registry: WidgetRegistry;
  source: WidgetDataSource;
  resolver: DeviceResolver;
  lister: EntityCandidateLister;
}

async function prepare(boardParam: string | null): Promise<Prepared> {
  const load = loaderFor(boardParam);
  const registry = hostWidgetRegistry();
  const definition = migrateToSlots(parseDashboardDefinition(JSON.parse(await load()), { catalog: registry.catalog }));
  const recordingLoader = RECORDINGS.get(boardParam ?? '');
  if (!recordingLoader) {
    Date.now = () => SYNTHETIC_NOW;
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
  const site = { siteAnchors: anchorsOf(definition) };
  return {
    definition,
    registry,
    source: new RecordedDataSource(recording, clock, site),
    resolver: createRecordedResolver(recording, site),
    lister: createRecordedLister(recording, site),
  };
}

function Ready({ prepared }: { prepared: Prepared }) {
  useEffect(() => {
    const root = document.documentElement;
    root.dataset.gateWidgets = String(prepared.definition.widgets.length);
    root.dataset.gate = 'ready';
  }, [prepared]);
  return (
    <Board
      definition={prepared.definition}
      registry={prepared.registry}
      source={prepared.source}
      resolver={prepared.resolver}
      lister={prepared.lister}
      themeRoot={undefined}
    />
  );
}

async function main() {
  const params = new URLSearchParams(window.location.search);
  document.documentElement.classList.toggle('dark', params.get('theme') === 'dark');
  try {
    // Load every weight a widget sets BEFORE the first render, so nothing is measured in a
    // fallback face and then reflowed.
    await Promise.all(['400', '500', '600', '700'].map((w) => document.fonts.load(`${w} 16px "Inter Variable"`)));
    const prepared = await prepare(params.get('board'));
    createRoot(document.getElementById('gate-root')!).render(<Ready prepared={prepared} />);
  } catch (err) {
    document.documentElement.dataset.gateError = err instanceof Error ? err.message : String(err);
    document.documentElement.dataset.gate = 'error';
  }
}

void main();
