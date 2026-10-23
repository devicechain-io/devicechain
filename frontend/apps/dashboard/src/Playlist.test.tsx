// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The kiosk playlist's clock and care: it advances on time, reads the next board before it is shown,
// skips one that will not open, pauses while the board is in use and when the viewer says so, and
// says each of those on the page. The boards are stand-ins; the clock is a fake one.

import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import i18n from './i18n/config';
import type { Loaded, LoadError } from './load';
import { Playlist, type PlaylistProps } from './Playlist';
import { IDLE_RESUME_MS } from './rotation';

const t = (key: string, params?: Record<string, unknown>) => i18n.t(key, params);

const boardOf = (title: string, slots: string[] = []): Loaded =>
  ({
    definition: { schemaVersion: 1, title, canvas: {}, slots: Object.fromEntries(slots.map((s) => [s, { type: 'device', label: s }])), widgets: [] },
    manifest: {},
    registry: {},
  }) as unknown as Loaded;

type Result = { loaded: Loaded } | { error: LoadError };

function setup(over: Partial<PlaylistProps> & { results?: Record<string, Result | Error> } = {}) {
  const results = over.results ?? {
    a: { loaded: boardOf('Board A', ['pump']) },
    b: { loaded: boardOf('Board B') },
    c: { loaded: boardOf('Board C') },
  };
  const load = vi.fn(async (token: string): Promise<Result> => {
    const r = results[token];
    if (r instanceof Error) throw r;
    return r ?? { error: { code: 'dashboardNotFound' } };
  });
  const renderBoard = vi.fn(({ loaded, token, search }: { loaded: Loaded; token: string; search: string }) => (
    <div data-testid="board" data-token={token} data-search={search}>
      {loaded.definition.title}
    </div>
  ));
  const props: PlaylistProps = {
    tokens: ['a', 'b', 'c'],
    everySeconds: 30,
    load,
    renderBoard,
    search: '?rotate=a,b,c&bind.pump=pump-1&range=6h',
    chrome: true,
    ...over,
  };
  const view = render(<Playlist {...props} />);
  return { ...view, load, renderBoard };
}

const flush = () => act(async () => void (await vi.advanceTimersByTimeAsync(0)));
const advance = (ms: number) => act(async () => void (await vi.advanceTimersByTimeAsync(ms)));
const shownToken = () => screen.queryByTestId('board')?.getAttribute('data-token');
const bar = () => Number(document.querySelector('[data-rotate-progress]')?.getAttribute('data-rotate-progress'));

beforeEach(() => {
  vi.useFakeTimers();
  document.documentElement.lang = 'en';
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

describe('the rotation', () => {
  it('shows the first board, then each in turn after the interval, wrapping round', async () => {
    setup();
    await flush();
    expect(shownToken()).toBe('a');
    await advance(30_000);
    expect(shownToken()).toBe('b');
    await advance(30_000);
    expect(shownToken()).toBe('c');
    await advance(30_000);
    expect(shownToken()).toBe('a');
  });

  it('does not move early', async () => {
    setup();
    await flush();
    await advance(29_000);
    expect(shownToken()).toBe('a');
  });

  it('draws a progress bar that fills across the interval and restarts on the next board', async () => {
    setup();
    await flush();
    expect(bar()).toBe(0);
    await advance(15_000);
    expect(bar()).toBe(50);
    expect(document.querySelector('[role="progressbar"]')?.getAttribute('aria-valuenow')).toBe('50');
    await advance(15_000);
    expect(shownToken()).toBe('b');
    expect(bar()).toBe(0);
  });

  it('names the board and its place in the playlist', async () => {
    setup();
    await flush();
    expect(document.querySelector('[data-rotate-position]')?.textContent).toBe(`${t('view:rotateProgress', { n: 1, total: 3 })} · Board A`);
  });

  it('reads the next board before its turn, once, and not earlier than the lead time', async () => {
    const { load } = setup();
    await flush();
    expect(load).toHaveBeenCalledTimes(1); // only the first board so far
    await advance(15_000);
    expect(load).toHaveBeenCalledTimes(1);
    await advance(6_000); // 21 s: inside the last 10 s's lead
    expect(load).toHaveBeenCalledTimes(2);
    expect(load).toHaveBeenLastCalledWith('b');
    await advance(9_000);
    expect(load).toHaveBeenCalledTimes(2); // switching used the board that was already read
    expect(shownToken()).toBe('b');
  });

  it('reads a short interval at once, since the whole of it is inside the lead', async () => {
    const { load } = setup({ everySeconds: 10 });
    await flush();
    await advance(250);
    expect(load).toHaveBeenCalledWith('b');
  });

  it('remounts a board that is listed twice', async () => {
    setup({ tokens: ['a', 'a'], results: { a: { loaded: boardOf('Same') } } });
    await flush();
    const first = screen.getByTestId('board');
    await advance(30_000);
    expect(screen.getByTestId('board')).not.toBe(first);
  });
});

describe('the link rides along on every board', () => {
  it('keeps a bind where the board has the slot and removes it where it does not', async () => {
    setup();
    await flush();
    expect(screen.getByTestId('board').getAttribute('data-search')).toContain('bind.pump=pump-1');
    expect(screen.getByTestId('board').getAttribute('data-search')).toContain('range=6h');
    await advance(30_000);
    const search = screen.getByTestId('board').getAttribute('data-search') ?? '';
    expect(search).not.toContain('bind.');
    expect(search).toContain('range=6h');
  });
});

describe('a board that cannot be opened', () => {
  it('is skipped, said on the page, and the rotation goes on', async () => {
    setup({ results: { a: { loaded: boardOf('Board A') }, b: { error: { code: 'dashboardNotPublished' } }, c: { loaded: boardOf('Board C') } } });
    await flush();
    await advance(30_000);
    expect(shownToken()).toBe('c');
    const note = document.querySelector('[data-rotate-note]');
    expect(note?.textContent).toContain('"b"');
    expect(note?.textContent?.length).toBeGreaterThan(10);
  });

  it('a reader that throws is a skipped board, not a crash', async () => {
    setup({ results: { a: { loaded: boardOf('Board A') }, b: new Error('socket'), c: { loaded: boardOf('Board C') } } });
    await flush();
    await advance(30_000);
    expect(shownToken()).toBe('c');
    expect(document.querySelector('[data-rotate-note]')?.textContent).toContain('socket');
  });

  it('starts on the first board that opens', async () => {
    setup({ results: { a: { error: { code: 'dashboardNotFound' } }, b: { loaded: boardOf('Board B') }, c: { loaded: boardOf('Board C') } } });
    await flush();
    expect(shownToken()).toBe('b');
  });

  it('stays on the board it has when no other opens, and keeps trying each interval', async () => {
    const { load } = setup({ results: { a: { loaded: boardOf('Board A') }, b: { error: { code: 'dashboardNotFound' } }, c: { error: { code: 'dashboardForbidden' } } } });
    await flush();
    await advance(30_000);
    expect(shownToken()).toBe('a');
    const before = load.mock.calls.length;
    await advance(30_000);
    expect(load.mock.calls.length).toBeGreaterThan(before);
    expect(shownToken()).toBe('a');
  });

  it('says so, with a way to try again, when none opens', async () => {
    const gone: Result = { error: { code: 'dashboardNotFound' } };
    const { load } = setup({ results: { a: gone, b: gone, c: gone } });
    await flush();
    expect(document.querySelector('[data-rotate-failed]')?.textContent).toContain(t('view:rotateNoneLoaded'));
    const calls = load.mock.calls.length;
    fireEvent.click(screen.getByText(t('view:linkRetry')));
    await flush();
    expect(load.mock.calls.length).toBeGreaterThan(calls);
  });

  it('a note can be dismissed', async () => {
    setup({ results: { a: { loaded: boardOf('Board A') }, b: { error: { code: 'dashboardNotFound' } }, c: { loaded: boardOf('Board C') } } });
    await flush();
    await advance(30_000);
    fireEvent.click(screen.getByLabelText(t('view:rotateDismiss')));
    expect(document.querySelector('[data-rotate-note]')).toBeNull();
  });
});

describe('pausing', () => {
  it('pauses while the board is in use and resumes after it is left alone', async () => {
    setup();
    await flush();
    await advance(10_000);
    fireEvent.pointerDown(screen.getByTestId('board'));
    await flush();
    expect(document.querySelector('[data-rotate-paused]')).not.toBeNull();
    expect(document.querySelector('[data-rotate-paused-note]')?.textContent).toBe(t('view:rotatePausedWhileUsed'));
    await advance(IDLE_RESUME_MS - 1_000);
    expect(shownToken()).toBe('a');
    expect(bar()).toBe(33); // the bar did not move while it was held
    await advance(1_500);
    expect(document.querySelector('[data-rotate-paused]')).toBeNull();
    await advance(20_000);
    expect(shownToken()).toBe('b'); // the remaining 20 s of the board it had
  });

  it.each([['keydown'], ['wheel'], ['touchstart']])('a %s counts as using the board', async (name) => {
    setup();
    await flush();
    fireEvent(window, new Event(name, { bubbles: true }));
    await flush();
    expect(document.querySelector('[data-rotate-paused]')).not.toBeNull();
  });

  it('a pointer move alone does not hold the rotation', async () => {
    setup();
    await flush();
    fireEvent.pointerMove(screen.getByTestId('board'));
    await flush();
    expect(document.querySelector('[data-rotate-paused]')).toBeNull();
  });

  it('keeps pausing while the viewer keeps using it', async () => {
    setup();
    await flush();
    for (let i = 0; i < 4; i++) {
      fireEvent.pointerDown(screen.getByTestId('board'));
      await advance(IDLE_RESUME_MS - 5_000);
    }
    expect(document.querySelector('[data-rotate-paused]')).not.toBeNull();
    expect(shownToken()).toBe('a');
  });

  it('resumes a full interval after the LAST touch, not the first', async () => {
    setup();
    await flush();
    fireEvent.pointerDown(screen.getByTestId('board'));
    await advance(20_000);
    fireEvent.pointerDown(screen.getByTestId('board'));
    await advance(20_000); // 40 s after the first touch, 20 s after the last
    expect(document.querySelector('[data-rotate-paused]')).not.toBeNull();
    await advance(IDLE_RESUME_MS - 20_000 + 500);
    expect(document.querySelector('[data-rotate-paused]')).toBeNull();
  });

  it('the pause control holds the rotation until it is pressed again, and says so', async () => {
    setup();
    await flush();
    fireEvent.click(screen.getByLabelText(t('view:rotatePause')));
    await advance(IDLE_RESUME_MS * 3);
    expect(shownToken()).toBe('a');
    expect(document.querySelector('[data-rotate-paused-note]')?.textContent).toBe(t('view:rotatePaused'));
    fireEvent.click(screen.getByLabelText(t('view:rotateResume')));
    await advance(30_000);
    expect(shownToken()).toBe('b');
  });

  it('pressing its own controls is not using the board', async () => {
    setup();
    await flush();
    fireEvent.pointerDown(screen.getByLabelText(t('view:rotatePause')));
    await flush();
    expect(document.querySelector('[data-rotate-paused]')).toBeNull();
  });

  it('dismissing a note is not using the board either', async () => {
    setup({ results: { a: { loaded: boardOf('Board A') }, b: { error: { code: 'dashboardNotFound' } }, c: { loaded: boardOf('Board C') } } });
    await flush();
    await advance(30_000);
    fireEvent.pointerDown(screen.getByLabelText(t('view:rotateDismiss')));
    await flush();
    expect(document.querySelector('[data-rotate-paused]')).toBeNull();
  });
});

describe('stepping by hand', () => {
  it('next and previous move one board and restart the bar', async () => {
    setup();
    await flush();
    await advance(10_000);
    fireEvent.click(screen.getByLabelText(t('view:rotateNext')));
    await flush();
    expect(shownToken()).toBe('b');
    expect(bar()).toBe(0);
    fireEvent.click(screen.getByLabelText(t('view:rotatePrevious')));
    await flush();
    expect(shownToken()).toBe('a');
    fireEvent.click(screen.getByLabelText(t('view:rotatePrevious')));
    await flush();
    expect(shownToken()).toBe('c');
  });
});

describe('with chrome=0', () => {
  it('keeps the bar, drops the controls, and still says when the rotation is paused', async () => {
    setup({ chrome: false });
    await flush();
    expect(document.querySelector('[role="progressbar"]')).not.toBeNull();
    expect(document.querySelector('[data-rotate-toggle]')).toBeNull();
    fireEvent.pointerDown(screen.getByTestId('board'));
    await flush();
    expect(document.querySelector('[data-rotate-paused-note]')).not.toBeNull();
    expect(document.querySelector('[data-rotate-toggle]')).toBeNull();
  });
});

describe('a wall display that runs for days', () => {
  it('draws the board once per visit, not on every tick of the clock', async () => {
    const { renderBoard } = setup();
    await flush();
    const drawn = renderBoard.mock.calls.length;
    await advance(20_000);
    expect(bar()).toBeGreaterThan(50);
    expect(renderBoard.mock.calls.length).toBe(drawn);
    await advance(10_000);
    expect(shownToken()).toBe('b');
    expect(renderBoard.mock.calls.length).toBe(drawn + 1);
  });
});

describe('cleaning up', () => {
  it('stops the clock and the listeners when it unmounts', async () => {
    const { unmount, load } = setup();
    await flush();
    unmount();
    const calls = load.mock.calls.length;
    await advance(120_000);
    expect(load.mock.calls.length).toBe(calls);
  });

  it('leaves no timer and no listener behind, even unmounted mid-pause', async () => {
    const added = vi.spyOn(window, 'addEventListener');
    const removed = vi.spyOn(window, 'removeEventListener');
    const { unmount } = setup();
    await flush();
    fireEvent.pointerDown(screen.getByTestId('board')); // starts the idle-resume timer
    await flush();
    unmount();
    expect(vi.getTimerCount()).toBe(0);
    for (const name of ['pointerdown', 'keydown', 'wheel', 'touchstart']) {
      const adds = added.mock.calls.filter(([n]) => n === name).length;
      expect(adds, name).toBeGreaterThan(0);
      expect(removed.mock.calls.filter(([n]) => n === name).length, name).toBe(adds);
    }
    added.mockRestore();
    removed.mockRestore();
  });
});
