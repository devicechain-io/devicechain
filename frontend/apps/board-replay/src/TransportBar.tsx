// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import {
  PLAYBACK_RATES,
  recordingBounds,
  simulationScaleAt,
  type BoardRecording,
  type PlaybackRate,
  type RecordedClock,
} from '@devicechain/dashboards';
import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';

import { formatOffset, formatRecordedClock } from './format';
import { buttonStyle } from './ReplayHeader';

export type ReplayState = 'playing' | 'paused' | 'seeking' | 'ended';

interface ClockView {
  timeMs: number;
  playing: boolean;
  rate: PlaybackRate;
  ended: boolean;
}

function read(clock: RecordedClock): ClockView {
  return { timeMs: clock.timeMs, playing: clock.playing, rate: clock.rate, ended: clock.ended };
}

// The clock's state as React state, refreshed on every clock event.
export function useClockView(clock: RecordedClock): ClockView {
  const [view, setView] = useState(() => read(clock));
  useEffect(() => {
    setView(read(clock));
    return clock.subscribe(() => setView(read(clock)));
  }, [clock]);
  return view;
}

export function replayState(view: ClockView, seeking: boolean): ReplayState {
  if (seeking) return 'seeking';
  if (view.ended) return 'ended';
  return view.playing ? 'playing' : 'paused';
}

// The transport bar: play / pause, a scrub bar with the recording's chapters, the speed
// choices, and the clock chip.
//
// Scrubbing does not seek on every input event: each seek retires the data source and
// re-subscribes the whole board, so a drag would rebuild it dozens of times. The bar tracks
// the drag locally and commits ONE seek when the pointer or key is released.
export function TransportBar({
  clock,
  recording,
  seeking,
  onSeeking,
}: {
  clock: RecordedClock;
  recording: BoardRecording;
  seeking: boolean;
  onSeeking: (seeking: boolean) => void;
}) {
  const { t } = useTranslation();
  const view = useClockView(clock);
  const [scrub, setScrub] = useState<number | null>(null);
  // The committed value is read from a ref so the three release events (pointer, key,
  // blur) can all call commit and only the first one seeks.
  const pending = useRef<number | null>(null);
  const { fromMs, toMs } = recordingBounds(recording);
  const shown = scrub ?? view.timeMs;
  const state = replayState(view, seeking);

  const commit = () => {
    const at = pending.current;
    if (at === null) return;
    pending.current = null;
    clock.seek(at);
    setScrub(null);
    onSeeking(false);
  };

  const togglePlay = () => {
    if (view.playing) {
      clock.pause();
      return;
    }
    if (view.ended) clock.seek(fromMs);
    clock.play();
  };

  const sim = simulationScaleAt(recording, shown);
  const chapter = [...recording.chapters].reverse().find((c) => c.tMs <= shown);
  const stateLabel = {
    playing: t('statePlaying'),
    paused: t('statePaused'),
    seeking: t('stateSeeking'),
    ended: t('stateEnded'),
  }[state];

  return (
    <footer
      data-testid="transport-bar"
      data-state={state}
      style={{
        flex: '0 0 auto',
        display: 'flex',
        flexDirection: 'column',
        gap: 6,
        padding: '8px 16px',
        borderTop: '1px solid hsl(var(--border))',
      }}
    >
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap' }}>
        <button type="button" onClick={togglePlay} style={buttonStyle}>
          {view.playing ? t('pause') : view.ended ? t('playAgain') : t('play')}
        </button>
        <input
          type="range"
          aria-label={t('seek')}
          aria-valuetext={formatOffset(shown - fromMs)}
          min={fromMs}
          max={toMs}
          step={100}
          value={shown}
          onChange={(e) => {
            pending.current = Number(e.target.value);
            setScrub(pending.current);
            onSeeking(true);
          }}
          onPointerUp={commit}
          onKeyUp={commit}
          onBlur={commit}
          style={{ flex: '1 1 240px', minWidth: 120 }}
        />
        <span style={{ fontVariantNumeric: 'tabular-nums' }}>
          {formatOffset(shown - fromMs)} / {formatOffset(toMs - fromMs)}
        </span>
        <div role="group" aria-label={t('speed')} style={{ display: 'flex', gap: 4 }}>
          {PLAYBACK_RATES.map((rate) => (
            <button
              key={rate}
              type="button"
              aria-pressed={view.rate === rate}
              onClick={() => clock.setRate(rate)}
              style={{
                ...buttonStyle,
                background: view.rate === rate ? 'hsl(var(--muted))' : 'transparent',
              }}
            >
              {t('speedOption', { rate })}
            </button>
          ))}
        </div>
      </div>
      <div
        style={{
          display: 'flex',
          gap: 12,
          flexWrap: 'wrap',
          alignItems: 'center',
          fontSize: 13,
          color: 'hsl(var(--muted-foreground))',
        }}
      >
        <span data-testid="replay-state">{stateLabel}</span>
        <span>{t('recordedTime', { time: formatRecordedClock(recording, shown) })}</span>
        {view.rate !== 1 && <span>{t('playbackRate', { rate: view.rate })}</span>}
        {sim && sim.scale !== 1 && <span>{t('simulationScale', { scale: sim.scale })}</span>}
      </div>
      {recording.chapters.length > 0 && (
        <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center', fontSize: 13 }}>
          <span style={{ color: 'hsl(var(--muted-foreground))' }}>{t('chapters')}</span>
          {recording.chapters.map((c) => (
            <button
              key={c.tMs}
              type="button"
              title={c.note}
              onClick={() => clock.seek(c.tMs)}
              style={buttonStyle}
            >
              {c.name} · {formatOffset(c.tMs - fromMs)}
            </button>
          ))}
          {chapter && <span>{t('chapterNote', { name: chapter.name, note: chapter.note })}</span>}
        </div>
      )}
    </footer>
  );
}
