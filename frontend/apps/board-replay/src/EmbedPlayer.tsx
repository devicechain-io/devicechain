// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The embed's player (loaded only in embed mode): the fixed canvas holding the replay label and
// the board, with no transport bar.
// It starts at the recording's first chapter (the moment the recording was cut to be shown
// from, with its history behind it), plays, and at the end starts over from there.
//
// Re-timed (the default), every view of the recording is built with the shift that makes
// its cursor read as NOW (retimeShiftMs): samples, alarms, aggregates and positions all move
// by that one amount, so the board's ages read seconds and nothing on it looks stale. A
// loop's seek builds a new view, with a new shift, so the times stay fresh on every pass.

import {
  RecordedDataSource,
  createRecordedClock,
  createRecordedLister,
  createRecordedResolver,
  recordingBounds,
  retimeShiftMs,
  type BoardRecording,
  type ClockTicker,
  type RecordedClock,
  type RecordedSiteOptions,
  type WidgetDataSource,
} from '@devicechain/dashboards';
import { useEffect, useMemo, useState } from 'react';

import { Board } from './Board';
import type { EmbedOptions } from './embed';
import { EmbedLabel } from './EmbedLabel';
import { EmbedStage } from './EmbedStage';
import type { LoadedReplay } from './load';
import { replayState, useClockView } from './TransportBar';

// Where an embed starts, and where its loop returns to.
export function embedStartMs(recording: BoardRecording): number {
  return recording.chapters[0]?.tMs ?? recordingBounds(recording).fromMs;
}

function viewAt(recording: BoardRecording, clock: RecordedClock, site: RecordedSiteOptions, retime: boolean): RecordedDataSource {
  return new RecordedDataSource(recording, clock, {
    ...site,
    timeShiftMs: retime ? retimeShiftMs(recording, clock.timeMs) : 0,
  });
}

export default function EmbedPlayer({
  replay,
  options,
  ticker,
  themeRoot,
}: {
  replay: LoadedReplay;
  options: EmbedOptions;
  ticker?: ClockTicker;
  themeRoot: Element | undefined;
}) {
  const { recording, definition, registry, siteAnchors } = replay;
  const startMs = embedStartMs(recording);
  const clock = useMemo(() => {
    const c = createRecordedClock(recording, { ticker });
    c.seek(startMs);
    return c;
  }, [recording, ticker, startMs]);
  const site = useMemo(() => ({ siteAnchors }), [siteAnchors]);
  const resolver = useMemo(() => createRecordedResolver(recording, site), [recording, site]);
  const lister = useMemo(() => createRecordedLister(recording, site), [recording, site]);
  const [source, setSource] = useState<WidgetDataSource>(() => viewAt(recording, clock, site, options.retime));
  const view = useClockView(clock);

  useEffect(() => {
    const off = clock.subscribe((ev) => {
      if (ev.reason === 'seek') setSource(viewAt(recording, clock, site, options.retime));
      // The end of the run: start over from the embed's start, and keep playing.
      else if (ev.reason === 'tick' && clock.ended && options.autoplay) {
        clock.seek(startMs);
        clock.play();
      }
    });
    if (options.autoplay) clock.play();
    return () => {
      off();
      clock.pause();
    };
  }, [clock, recording, site, options.retime, options.autoplay, startMs]);

  return (
    <EmbedStage canvas={options.canvas}>
      <EmbedLabel retimed={options.retime} />
      <main data-testid="replay-body" data-state={replayState(view, false)} style={{ flex: '1 1 auto', minHeight: 0, overflow: 'hidden' }}>
        <Board
          definition={definition}
          registry={registry}
          source={source}
          resolver={resolver}
          lister={lister}
          themeRoot={themeRoot}
          // Re-timed instants are the viewer's own present, so they print in the viewer's zone.
          timeZone={options.retime ? undefined : 'UTC'}
        />
      </main>
    </EmbedStage>
  );
}
