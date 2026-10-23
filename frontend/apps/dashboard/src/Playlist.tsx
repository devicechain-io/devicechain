// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The kiosk playlist: `/dash/?rotate=tokA,tokB&every=60` cycles the named published boards on a wall
// display. The viewer's own settings (the time range or fixed window, the theme, density, `bind.*`)
// ride along on every board; what this adds is the clock and the care a screen nobody is watching
// needs:
//
//   - the NEXT board is read before it is shown, so the switch is to a board that is already here;
//   - a board that cannot be opened is skipped, said on the page, and the rest keep playing (a
//     rotation that stopped on one bad token would leave a wall frozen on a single board);
//   - touching the board (a click, a key, a wheel, a touch) pauses the rotation so nobody is
//     carried off a widget they are reading, and it resumes after the board has been left alone;
//   - a pause the viewer chose stays until they resume, and a paused rotation always says so;
//   - a thin bar along the bottom edge shows how far through the board is.
//
// Each board is its own mount (its own hub and subscriptions), torn down when the next one takes
// over, so nothing one board bound or selected leaks into the next.

import type { ReactNode } from 'react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';

import { NoteStack, skippedNote, type Note } from './LinkNotes';
import type { Loaded, LoadError } from './load';
import { errorDetail } from './load';
import { IDLE_RESUME_MS, PRELOAD_LEAD_MS, TICK_MS, boardAddress, progressOf, stepIndex, type Skipped } from './rotation';

type LoadResult = { loaded: Loaded } | { error: LoadError };

export interface PlaylistProps {
  tokens: string[];
  everySeconds: number;
  // Reads one board's published snapshot (and the code of its widgets). Never expected to throw.
  load: (token: string) => Promise<LoadResult>;
  // Draws a board. `search` is the link with its `bind.*` kept only where this board has the slot.
  renderBoard: (board: { loaded: Loaded; token: string; search: string }) => ReactNode;
  // The link the viewer opened, for the per-board `bind.*` filter.
  search: string;
  // Show the pill's controls (false with `chrome=0`); the bar and a paused or skipped state always show.
  chrome: boolean;
}

interface Shown {
  index: number;
  // Distinguishes two visits to the same index, so a board that appears twice remounts.
  visit: number;
  loaded: Loaded;
}

// Events that mean somebody is using the board. Pointer MOVES do not: a cursor left on the screen
// would hold the rotation forever.
const USE_EVENTS = ['pointerdown', 'keydown', 'wheel', 'touchstart'] as const;

export function Playlist({ tokens, everySeconds, load, renderBoard, search, chrome }: PlaylistProps) {
  const { t } = useTranslation();
  const everyMs = everySeconds * 1000;
  const [shown, setShown] = useState<Shown | null>(null);
  const [status, setStatus] = useState<'starting' | 'running' | 'failed'>('starting');
  const [elapsed, setElapsed] = useState(0);
  const [manual, setManual] = useState(false);
  const [using, setUsing] = useState(false);
  const [switching, setSwitching] = useState(false);
  const [skips, setSkips] = useState<Skipped[]>([]);
  const [dismissed, setDismissed] = useState<ReadonlySet<string>>(new Set());
  const [attempt, setAttempt] = useState(0);

  const loadRef = useRef(load);
  loadRef.current = load;
  const tokensRef = useRef(tokens);
  tokensRef.current = tokens;
  // Boards read ahead of their turn, by index.
  const cache = useRef(new Map<number, Promise<LoadResult>>());
  const busy = useRef(false);
  const visits = useRef(0);
  const alive = useRef(true);
  const preloaded = useRef(-1);

  useEffect(() => {
    alive.current = true;
    const pending = cache.current;
    return () => {
      alive.current = false;
      pending.clear();
    };
  }, []);

  const ask = useCallback((index: number): Promise<LoadResult> => {
    let pending = cache.current.get(index);
    if (!pending) {
      // A reader that throws is a board that failed, not a playlist that crashed.
      pending = loadRef.current(tokensRef.current[index]).catch(
        (err: unknown): LoadResult => ({ error: { code: 'dashboardFetchFailed', detail: errorDetail(err) } }),
      );
      cache.current.set(index, pending);
    }
    return pending;
  }, []);

  // Move to the board `step` from `from` (null: the first usable board, from the top). A board that
  // cannot be opened is skipped and noted; when no other board can be opened the current one stays.
  const go = useCallback(
    async (from: number | null, step: 1 | -1) => {
      if (busy.current) return;
      busy.current = true;
      setSwitching(true);
      try {
        const count = tokensRef.current.length;
        let index = from === null ? 0 : stepIndex(from, step, count);
        for (let tried = 0; tried < count; tried++) {
          if (from !== null && index === from) break;
          const result = await ask(index);
          cache.current.delete(index);
          if (!alive.current) return;
          const token = tokensRef.current[index];
          if ('loaded' in result) {
            visits.current += 1;
            setSkips((s) => s.filter((x) => x.token !== token));
            setShown({ index, visit: visits.current, loaded: result.loaded });
            setElapsed(0);
            setStatus('running');
            return;
          }
          setSkips((s) => [...s.filter((x) => x.token !== token), { token, error: result.error }].slice(-3));
          setDismissed((d) => {
            const next = new Set(d);
            next.delete(`skip:${token}`);
            return next;
          });
          index = stepIndex(index, step, count);
        }
        if (from === null) setStatus('failed');
        else setElapsed(0);
      } finally {
        busy.current = false;
        if (alive.current) setSwitching(false);
      }
    },
    [ask],
  );

  // The first board, and again on "try again".
  useEffect(() => {
    setStatus('starting');
    void go(null, 1);
  }, [go, attempt]);

  const paused = manual || using;

  // The clock. It runs while a board is on screen and nobody has paused it; a board that is still
  // being fetched holds the bar full rather than counting past the end.
  useEffect(() => {
    if (status !== 'running' || shown === null || paused) return undefined;
    const timer = setInterval(() => {
      if (busy.current) return;
      setElapsed((e) => Math.min(everyMs, e + TICK_MS));
    }, TICK_MS);
    return () => clearInterval(timer);
  }, [status, shown, paused, everyMs]);

  // Read the next board shortly before its turn, once per visit.
  useEffect(() => {
    if (shown === null || tokens.length < 2 || preloaded.current === shown.visit) return;
    if (elapsed >= everyMs - PRELOAD_LEAD_MS) {
      preloaded.current = shown.visit;
      void ask(stepIndex(shown.index, 1, tokens.length));
    }
  }, [shown, elapsed, everyMs, ask, tokens.length]);

  // Time is up: on to the next board.
  useEffect(() => {
    if (shown !== null && elapsed >= everyMs && !busy.current) void go(shown.index, 1);
  }, [shown, elapsed, everyMs, go]);

  // Using the board pauses the rotation, and leaving it alone resumes it.
  useEffect(() => {
    let idle: ReturnType<typeof setTimeout> | undefined;
    const onUse = (event: Event) => {
      // The rotation's own controls are not "using the board": pressing pause must not also
      // start the idle pause, nor dismissing a note.
      if (event.target instanceof Element && event.target.closest('[data-rotate-controls], [data-rotate-notes]')) return;
      setUsing(true);
      clearTimeout(idle);
      idle = setTimeout(() => setUsing(false), IDLE_RESUME_MS);
    };
    for (const name of USE_EVENTS) window.addEventListener(name, onUse, { capture: true, passive: true });
    return () => {
      clearTimeout(idle);
      for (const name of USE_EVENTS) window.removeEventListener(name, onUse, { capture: true });
    };
  }, []);

  // The board, drawn once per visit. The clock re-renders this component four times a second, and a
  // wall display runs for days: the board under it must not re-render with it.
  const boardSearch = shown ? boardAddress(search, shown.loaded.definition) : '';
  const shownToken = shown ? tokens[shown.index] : undefined;
  const board = useMemo(
    () => (shown && shownToken !== undefined ? renderBoard({ loaded: shown.loaded, token: shownToken, search: boardSearch }) : null),
    [shown, shownToken, boardSearch, renderBoard],
  );

  const notes: Note[] = skips
    .map((s) => ({ id: `skip:${s.token}`, text: skippedNote(s, t) }))
    .filter((n) => !dismissed.has(n.id));
  const dismiss = (id: string) => setDismissed((d) => new Set(d).add(id));

  if (status === 'starting') {
    return (
      <div role="status" style={{ height: '100%', display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 24 }}>
        {t('load:loading')}
      </div>
    );
  }
  if (status === 'failed' || shown === null) {
    return (
      <div
        data-rotate-failed=""
        role="alert"
        style={{ height: '100%', display: 'flex', flexDirection: 'column', gap: 12, alignItems: 'center', justifyContent: 'center', padding: 24, textAlign: 'center' }}
      >
        <div>{t('view:rotateNoneLoaded')}</div>
        <NoteStack notes={notes} onDismiss={dismiss} />
        <button type="button" data-rotate-controls="" onClick={() => setAttempt((n) => n + 1)} style={buttonStyle}>
          {t('view:linkRetry')}
        </button>
      </div>
    );
  }

  const title = shown.loaded.definition.title || t('view:untitled');
  const percent = Math.round(progressOf(elapsed, everySeconds) * 100);
  const count = tokens.length;
  return (
    <div data-rotate="" data-rotate-index={shown.index} data-rotate-paused={paused ? '' : undefined} style={{ height: '100%', position: 'relative' }}>
      <div key={`${shown.index}:${shown.visit}`} style={{ height: '100%' }}>
        {board}
      </div>
      <NoteStack notes={notes} onDismiss={dismiss} />
      <div
        role="progressbar"
        aria-label={t('view:rotateProgress', { n: shown.index + 1, total: count })}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={percent}
        data-rotate-progress={percent}
        style={{ position: 'fixed', left: 0, right: 0, bottom: 0, height: 3, zIndex: 10, pointerEvents: 'none', background: 'hsl(var(--border) / 0.5)' }}
      >
        <div
          style={{
            height: '100%',
            width: '100%',
            transformOrigin: 'left',
            transform: `scaleX(${percent / 100})`,
            background: paused ? 'hsl(var(--muted-foreground))' : 'hsl(var(--primary))',
          }}
        />
      </div>
      {chrome || paused || switching ? (
        <div
          data-rotate-controls=""
          style={{
            position: 'fixed',
            bottom: 8,
            left: '50%',
            transform: 'translateX(-50%)',
            zIndex: 10,
            display: 'flex',
            alignItems: 'center',
            gap: 6,
            padding: '2px 6px',
            fontSize: 12,
            borderRadius: 999,
            border: '1px solid hsl(var(--border))',
            background: 'hsl(var(--card))',
            color: 'hsl(var(--muted-foreground))',
            opacity: 0.85,
            maxWidth: 'calc(100% - 16px)',
          }}
        >
          {chrome ? (
            <>
              <button type="button" aria-label={t('view:rotatePrevious')} title={t('view:rotatePrevious')} onClick={() => void go(shown.index, -1)} style={buttonStyle}>
                ‹
              </button>
              <span data-rotate-position="" title={title} style={{ whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', minWidth: 0 }}>
                {t('view:rotateProgress', { n: shown.index + 1, total: count })} · {title}
              </span>
              <button
                type="button"
                data-rotate-toggle=""
                aria-pressed={manual}
                aria-label={manual ? t('view:rotateResume') : t('view:rotatePause')}
                title={manual ? t('view:rotateResume') : t('view:rotatePause')}
                onClick={() => setManual((m) => !m)}
                style={buttonStyle}
              >
                {manual ? PLAY_GLYPH : PAUSE_GLYPH}
              </button>
              <button type="button" aria-label={t('view:rotateNext')} title={t('view:rotateNext')} onClick={() => void go(shown.index, 1)} style={buttonStyle}>
                ›
              </button>
            </>
          ) : null}
          {paused ? (
            <span data-rotate-paused-note="" role="status">
              {manual ? t('view:rotatePaused') : t('view:rotatePausedWhileUsed')}
            </span>
          ) : null}
          {switching && elapsed >= everyMs ? <span role="status">{t('view:rotateLoadingNext')}</span> : null}
        </div>
      ) : null}
    </div>
  );
}

// Symbols, not words: the buttons' names are the translated labels above.
const PLAY_GLYPH = '▶';
const PAUSE_GLYPH = '❚❚';

const buttonStyle = {
  border: 0,
  background: 'transparent',
  color: 'inherit',
  cursor: 'pointer',
  padding: '0 4px',
  fontSize: 12,
  lineHeight: 1.6,
} as const;
