// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Kiosk mode, the URL deep link, the theme, and the access-token refresh, driven through the
// real App. Only the transport (gql), the board renderer and the published-dashboard fetch are
// faked: the renderer because a chart library has no business in these assertions, the fetch
// because it needs a server.

import { GraphQLRequestError } from '@devicechain/client';
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({
  gql: vi.fn(),
  load: vi.fn(),
  getter: null as null | (() => Promise<string | null>),
  // The props the board renderer was last mounted with.
  rendered: null as null | { timeRange?: { spanSeconds: number; paused: boolean; pausedAt?: number } },
  // What the hub says of its socket.
  connection: 'live' as 'live' | 'reconnecting' | 'offline',
}));

vi.mock('@devicechain/client', async (orig) => ({
  ...(await orig<typeof import('@devicechain/client')>()),
  gql: (...a: unknown[]) => h.gql(...a),
  setAuthTokenGetter: (g: null | (() => Promise<string | null>)) => {
    h.getter = g;
  },
}));
vi.mock('@devicechain/widgets', async (orig) => {
  const React = await import('react');
  return {
    ...(await orig<Record<string, unknown>>()),
    DashboardRenderer: (props: { timeRange?: { spanSeconds: number; paused: boolean; pausedAt?: number } }) => {
      h.rendered = props;
      return React.createElement('div', { 'data-testid': 'board' });
    },
    useDashboardHub: () => ({}),
    useHubConnectionState: () => h.connection,
    useSlotCandidates: () => undefined,
  };
});
vi.mock('@devicechain/dashboards', async (orig) => ({
  ...(await orig<Record<string, unknown>>()),
  createDeviceResolver: () => ({}),
  createEntityLister: () => ({}),
}));
vi.mock('./load', async (orig) => ({
  ...(await orig<typeof import('./load')>()),
  loadPublishedDashboard: (...a: unknown[]) => h.load(...a),
}));

import App from './App';
import i18n from './i18n/config';
import { LOGIN, REFRESH, SELECT_TENANT } from './queries';
import { leaveSsoReturn, resetSsoStateForTests, startSso } from './sso';

const t = (key: string) => i18n.t(key);
const HANDOFF = 'dc-dash-sso-session';
const NOW = Date.UTC(2026, 9, 14, 12, 0, 0);

function jwt(expSecondsFromNow: number): string {
  return `h.${btoa(JSON.stringify({ exp: Math.floor(Date.now() / 1000) + expSecondsFromNow }))}.s`;
}

function landOn(search: string) {
  window.history.replaceState(null, '', '/dash/' + search);
}

const board = {
  loaded: {
    definition: { schemaVersion: 1, title: 'Wall board', canvas: {}, slots: {}, widgets: [] },
    manifest: {},
    registry: {},
  },
};

// One mock serves the whole flow, routed by document.
function serve(over: { select?: () => unknown; refresh?: (vars: { refreshToken: string }) => unknown } = {}) {
  h.gql.mockImplementation(async (_area: string, doc: unknown, vars: any) => {
    if (doc === LOGIN) {
      return { login: { identityToken: 'id', memberships: [{ tenant: 'acme', roles: [], ssoRequired: false }] } };
    }
    if (doc === SELECT_TENANT) {
      return over.select ? over.select() : { selectTenant: { accessToken: jwt(3600), refreshToken: jwt(86400) } };
    }
    if (doc === REFRESH) {
      if (!over.refresh) throw new Error('unexpected refresh');
      return over.refresh(vars);
    }
    throw new Error('unexpected document');
  });
}

async function signIn() {
  fireEvent.change(screen.getByLabelText(t('signIn:emailLabel')), { target: { value: 'a@b.co' } });
  fireEvent.change(screen.getByLabelText(t('signIn:passwordLabel')), { target: { value: 'pw' } });
  fireEvent.click(screen.getByRole('button', { name: t('signIn:submit') }));
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
}

beforeEach(async () => {
  vi.useFakeTimers();
  h.connection = 'live';
  vi.setSystemTime(NOW);
  h.gql.mockReset();
  h.load.mockReset();
  h.load.mockResolvedValue(board);
  h.getter = null;
  resetSsoStateForTests();
  window.sessionStorage.clear();
  window.localStorage.clear();
  document.documentElement.classList.remove('dark');
  await i18n.changeLanguage('en');
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.restoreAllMocks();
  landOn('');
});

describe('kiosk mode', () => {
  it('hides the chrome and keeps only the fullscreen control', async () => {
    landOn('?dashboard=wall&kiosk=1');
    Object.defineProperty(document, 'fullscreenEnabled', { configurable: true, value: true });
    serve();
    render(<App />);
    await signIn();

    expect(screen.getByTestId('board')).toBeTruthy();
    expect(screen.queryByRole('button', { name: t('common:signOut') })).toBeNull();
    expect(screen.queryByRole('button', { name: t('view:change') })).toBeNull();
    expect(screen.queryByText('Wall board')).toBeNull();
    expect(screen.queryByRole('group', { name: t('common:languagePicker') })).toBeNull();
    expect(screen.getByRole('button', { name: t('view:enterFullscreen') })).toBeTruthy();
    // The chrome goes, the connection state stays: a frozen wall board must not look live.
    expect(screen.getByRole('status')).toBeTruthy();
    Object.defineProperty(document, 'fullscreenEnabled', { configurable: true, value: undefined });
  });

  it('requests fullscreen on the document when the button is pressed', async () => {
    landOn('?dashboard=wall&kiosk=1');
    Object.defineProperty(document, 'fullscreenEnabled', { configurable: true, value: true });
    const request = vi.fn().mockResolvedValue(undefined);
    document.documentElement.requestFullscreen = request;
    serve();
    render(<App />);
    await signIn();
    fireEvent.click(screen.getByRole('button', { name: t('view:enterFullscreen') }));
    expect(request).toHaveBeenCalledTimes(1);
    Object.defineProperty(document, 'fullscreenEnabled', { configurable: true, value: undefined });
  });

  it('chrome=0 also hides the floating connection badge and fullscreen button, for a capture', async () => {
    landOn('?dashboard=wall&kiosk=1&chrome=0');
    Object.defineProperty(document, 'fullscreenEnabled', { configurable: true, value: true });
    serve();
    render(<App />);
    await signIn();
    expect(screen.getByTestId('board')).toBeTruthy();
    expect(screen.queryByRole('button', { name: t('view:enterFullscreen') })).toBeNull();
    expect(screen.queryByRole('status')).toBeNull();
    Object.defineProperty(document, 'fullscreenEnabled', { configurable: true, value: undefined });
  });

  it('chrome=0 never hides that the board has lost its connection', async () => {
    for (const state of ['reconnecting', 'offline'] as const) {
      h.connection = state;
      landOn('?dashboard=wall&kiosk=1&chrome=0');
      serve();
      const { unmount } = render(<App />);
      await signIn();
      expect(screen.getByTestId('board')).toBeTruthy();
      expect(screen.getByRole('status').textContent).toContain(t(state === 'offline' ? 'view:connectionOffline' : 'view:connectionReconnecting'));
      unmount();
    }
  });

  it('chrome=0 never hides that a board is paused', async () => {
    landOn('?dashboard=wall&kiosk=1&chrome=0&paused=1');
    serve();
    render(<App />);
    await signIn();
    expect(screen.getByTestId('board')).toBeTruthy();
    expect(document.body.textContent).toContain(t('view:paused'));
  });

  it('without kiosk=1 the chrome is there', async () => {
    landOn('?dashboard=wall');
    serve();
    render(<App />);
    await signIn();
    expect(screen.getByRole('button', { name: t('common:signOut') })).toBeTruthy();
    expect(screen.getByRole('button', { name: t('view:change') })).toBeTruthy();
    expect(screen.getByText('Wall board')).toBeTruthy();
  });
});

describe('the deep link', () => {
  it('loads the named dashboard straight after sign-in, with the parameters intact', async () => {
    landOn('?dashboard=wall&kiosk=1&theme=dark&density=wall');
    serve();
    render(<App />);
    expect(h.load).not.toHaveBeenCalled(); // not before sign-in
    await signIn();

    expect(h.load).toHaveBeenCalledTimes(1);
    expect(h.load.mock.calls[0][0]).toBe('wall');
    expect(screen.getByTestId('board')).toBeTruthy();
    expect(window.location.search).toBe('?dashboard=wall&kiosk=1&theme=dark&density=wall');
  });

  it('lets Change leave the deep-linked board instead of reloading it', async () => {
    landOn('?dashboard=wall');
    serve();
    render(<App />);
    await signIn();
    fireEvent.click(screen.getByRole('button', { name: t('view:change') }));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(screen.queryByTestId('board')).toBeNull();
    expect(screen.getByPlaceholderText(t('load:tokenPlaceholder'))).toBeTruthy();
    expect(h.load).toHaveBeenCalledTimes(1);
  });

  it('falls back to the load form, token filled in, when the deep-linked board fails', async () => {
    landOn('?dashboard=missing');
    h.load.mockResolvedValue({ error: { code: 'dashboardNotFound' } });
    serve();
    render(<App />);
    await signIn();
    const field = screen.getByPlaceholderText(t('load:tokenPlaceholder')) as HTMLInputElement;
    expect(field.value).toBe('missing');
  });

  it('survives a single sign-on round trip: the query is put back where the return page leaves', async () => {
    const realLocation = window.location;
    const replace = vi.fn();
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: { origin: realLocation.origin, pathname: '/dash/', search: '?dashboard=wall&kiosk=1', replace },
    });
    try {
      h.gql.mockResolvedValue({ beginSso: { authorizationUrl: 'https://idp.example.com/a' } });
      await startSso('a@b.co');
      leaveSsoReturn();
      expect(replace).toHaveBeenCalledWith('/dash/?dashboard=wall&kiosk=1');
      // Taken once: a later plain visit does not inherit it.
      leaveSsoReturn();
      expect(replace).toHaveBeenLastCalledWith('/dash/');
    } finally {
      Object.defineProperty(window, 'location', { configurable: true, value: realLocation });
    }
  });
});

describe('theme', () => {
  it('?theme=dark puts .dark on <html>, and ?theme=light leaves it off', async () => {
    landOn('?theme=dark');
    serve();
    const { unmount } = render(<App />);
    expect(document.documentElement.classList.contains('dark')).toBe(true);
    unmount();

    landOn('?theme=light');
    render(<App />);
    expect(document.documentElement.classList.contains('dark')).toBe(false);
  });

  it('the toggle flips .dark outside kiosk and remembers the choice, but a param is not saved', async () => {
    landOn('?dashboard=wall&theme=dark');
    serve();
    render(<App />);
    expect(window.localStorage.getItem('dc-dash-theme')).toBeNull();
    await signIn();

    fireEvent.click(screen.getByRole('button', { name: t('view:themeToLight') }));
    expect(document.documentElement.classList.contains('dark')).toBe(false);
    expect(window.localStorage.getItem('dc-dash-theme')).toBe('light');
    fireEvent.click(screen.getByRole('button', { name: t('view:themeToDark') }));
    expect(document.documentElement.classList.contains('dark')).toBe(true);
  });

  it('has no toggle in kiosk mode', async () => {
    landOn('?dashboard=wall&kiosk=1&theme=dark');
    serve();
    render(<App />);
    await signIn();
    expect(screen.queryByRole('button', { name: t('view:themeToLight') })).toBeNull();
    expect(document.documentElement.classList.contains('dark')).toBe(true);
  });
});

describe('invalid parameters', () => {
  it('are ignored with a console warning, never a crash', async () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    landOn('?dashboard=bad%20token&kiosk=maybe&theme=neon&density=huge');
    serve();
    render(<App />);
    expect(screen.getByLabelText(t('signIn:emailLabel'))).toBeTruthy();
    expect(warn).toHaveBeenCalledTimes(4);

    await signIn();
    // No dashboard token survived, so it lands on the load form with the chrome on and no .dark.
    expect(screen.getByPlaceholderText(t('load:tokenPlaceholder'))).toBeTruthy();
    expect(document.documentElement.classList.contains('dark')).toBe(false);
    expect(h.load).not.toHaveBeenCalled();
  });
});

describe('access-token refresh', () => {
  // Signed in as a single sign-on return would leave it: the pair is handed over in session storage.
  function signedInWith(accessIn: number, refreshIn = 86400) {
    window.sessionStorage.setItem(
      HANDOFF,
      JSON.stringify({ accessToken: jwt(accessIn), refreshToken: jwt(refreshIn) }),
    );
  }

  it('refreshes before the access token expires and hands the new one to the transport', async () => {
    landOn('?dashboard=wall&kiosk=1');
    signedInWith(300);
    const next = { accessToken: jwt(900), refreshToken: jwt(86400) };
    const refresh = vi.fn().mockReturnValue({ refresh: next });
    serve({ refresh });
    render(<App />);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(screen.getByTestId('board')).toBeTruthy();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(170_000);
    });
    expect(refresh).not.toHaveBeenCalled(); // 130 s of life left: outside the 120 s skew

    await act(async () => {
      await vi.advanceTimersByTimeAsync(15_000); // 115 s of life left: refreshed ahead of expiry
    });
    expect(refresh).toHaveBeenCalledTimes(1);
    // The websocket path resolves its token through this getter on every (re)connect.
    expect(await h.getter!()).toBe(next.accessToken);
    expect(screen.queryByRole('alertdialog')).toBeNull();
    expect(screen.getByTestId('board')).toBeTruthy();
  });

  it('keeps refreshing, so a display outlives many access tokens', async () => {
    landOn('?dashboard=wall&kiosk=1');
    signedInWith(300);
    let n = 0;
    const refresh = vi.fn().mockImplementation(() => ({
      refresh: { accessToken: jwt(300), refreshToken: jwt(86400 + ++n) },
    }));
    serve({ refresh });
    render(<App />);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(30 * 60_000);
    });
    // One refresh every 180 s (300 s tokens, 120 s skew): about ten in half an hour.
    expect(refresh.mock.calls.length).toBeGreaterThanOrEqual(8);
    expect(refresh.mock.calls.length).toBeLessThanOrEqual(11);
    expect(screen.queryByRole('alertdialog')).toBeNull();
    expect(screen.getByTestId('board')).toBeTruthy();
  });

  it('a failed refresh replaces the board with a visible session-ended overlay', async () => {
    vi.spyOn(console, 'warn').mockImplementation(() => {});
    landOn('?dashboard=wall&kiosk=1');
    signedInWith(300);
    serve({
      refresh: () => {
        throw new GraphQLRequestError('invalid refresh token', 200, [{ message: 'invalid refresh token' }]);
      },
    });
    render(<App />);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(screen.queryByRole('alertdialog')).toBeNull();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(185_000); // the first refresh is refused: no retry
    });
    const overlay = screen.getByRole('alertdialog');
    expect(overlay.textContent).toContain(t('common:sessionEndedTitle'));
    expect(screen.queryByTestId('board')).toBeNull(); // not left frozen behind it
    expect(await h.getter!()).toBeNull();

    // Signing in again returns to the sign-in form, and the deep link is still on the address.
    fireEvent.click(screen.getByRole('button', { name: t('common:sessionEndedAction') }));
    expect(screen.getByLabelText(t('signIn:emailLabel'))).toBeTruthy();
    expect(window.location.search).toBe('?dashboard=wall&kiosk=1');
  });

  it('a network blip during the refresh does not end the session: it is retried', async () => {
    vi.spyOn(console, 'warn').mockImplementation(() => {});
    landOn('?dashboard=wall&kiosk=1');
    signedInWith(300);
    const next = { accessToken: jwt(900), refreshToken: jwt(86400) };
    let calls = 0;
    const refresh = vi.fn().mockImplementation(() => {
      calls++;
      if (calls <= 3) throw new GraphQLRequestError('Failed to fetch', 0);
      return { refresh: next };
    });
    serve({ refresh });
    render(<App />);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(180_200); // due at +180 s; the first attempt fails
    });
    expect(refresh).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole('alertdialog')).toBeNull();
    expect(screen.getByTestId('board')).toBeTruthy();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(15_000); // backoff 1 s, 2 s, 4 s (each 50-100 %)
    });
    expect(refresh).toHaveBeenCalledTimes(4);
    expect(await h.getter!()).toBe(next.accessToken);
    expect(screen.queryByRole('alertdialog')).toBeNull();
    expect(screen.getByTestId('board')).toBeTruthy();
  });

  it('an outage that outlasts the access token ends the session when the token expires, not before', async () => {
    vi.spyOn(console, 'warn').mockImplementation(() => {});
    landOn('?dashboard=wall&kiosk=1');
    signedInWith(300);
    serve({
      refresh: () => {
        throw new GraphQLRequestError('Request failed (503)', 503);
      },
    });
    render(<App />);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(297_000); // 3 s of life left, every retry failed
    });
    expect(screen.queryByRole('alertdialog')).toBeNull();
    expect(screen.getByTestId('board')).toBeTruthy();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(3_000);
    });
    expect(screen.getByRole('alertdialog')).toBeTruthy();
    expect(screen.queryByTestId('board')).toBeNull();
  });
});

describe('the time range', () => {
  const rangeButton = (id: string) => document.querySelector(`[data-range="${id}"]`) as HTMLButtonElement;
  const pauseButton = () => document.querySelector('[data-time-range-pause]') as HTMLButtonElement;

  it('opens at the board default span, live, with the control in the header', async () => {
    landOn('?dashboard=wall');
    serve();
    render(<App />);
    await signIn();
    expect(h.rendered?.timeRange).toEqual({ spanSeconds: 3600, paused: false });
    expect(rangeButton('1h').getAttribute('aria-pressed')).toBe('true');
    expect(screen.queryByText(t('view:paused'))).toBeNull();
  });

  it('?range= and ?paused=1 open the board at that span, frozen at the load instant', async () => {
    landOn('?dashboard=wall&range=6h&paused=1');
    serve();
    render(<App />);
    await signIn();
    expect(h.rendered?.timeRange).toEqual({ spanSeconds: 21600, paused: true, pausedAt: NOW });
    expect(rangeButton('6h').getAttribute('aria-pressed')).toBe('true');
    expect(document.querySelector('[data-time-range-paused]')).not.toBeNull();
    expect(pauseButton().textContent).toBe(t('view:resume'));
  });

  it('an unusable link opens the default, live, and does not crash', async () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    landOn('?dashboard=wall&range=banana&paused=maybe');
    serve();
    render(<App />);
    await signIn();
    expect(h.rendered?.timeRange).toEqual({ spanSeconds: 3600, paused: false });
    expect(warn).toHaveBeenCalledTimes(2);
  });

  it('?from=&to= opens a fixed window: the live tail is off, the zone is shown, a preset returns to live', async () => {
    landOn('?dashboard=wall&range=6h&from=2026-10-14T02:00:00Z&to=2026-10-14T04:00:00Z');
    serve();
    render(<App />);
    await signIn();
    expect(h.rendered?.timeRange).toMatchObject({
      spanSeconds: 7200,
      paused: true,
      pausedAt: Date.UTC(2026, 9, 14, 4, 0, 0),
      absolute: { fromMs: Date.UTC(2026, 9, 14, 2, 0, 0), toMs: Date.UTC(2026, 9, 14, 4, 0, 0) },
    });
    const pill = document.querySelector('[data-time-range-absolute]') as HTMLElement;
    expect(pill.textContent).toContain(t('view:absFixed'));
    expect(pill.getAttribute('title')).toContain('(UTC)');
    expect(document.querySelector('[data-time-range-paused]')).toBeNull();
    expect(document.querySelector('[data-time-range-notice]')).toBeNull();

    fireEvent.click(rangeButton('24h'));
    expect(h.rendered?.timeRange).toEqual({ spanSeconds: 86400, paused: false });
    expect(document.querySelector('[data-time-range-absolute]')).toBeNull();
  });

  it('an unusable ?from=&to= is ignored with a visible note, and the board opens live', async () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    landOn('?dashboard=wall&from=2026-10-14T04:00:00Z&to=2026-10-14T02:00:00Z');
    serve();
    render(<App />);
    await signIn();
    expect(h.rendered?.timeRange).toEqual({ spanSeconds: 3600, paused: false });
    const note = document.querySelector('[data-time-range-notice]') as HTMLElement;
    expect(note.textContent).toContain(t('view:absProblemOrder'));
    expect(note.textContent).toContain('from=2026-10-14T04:00:00Z');
    expect(warn).toHaveBeenCalledTimes(1);
  });

  it('a zoneless ?from=&to= is refused (whose 02:00?) rather than read in the viewer zone', async () => {
    vi.spyOn(console, 'warn').mockImplementation(() => {});
    landOn('?dashboard=wall&from=2026-10-14T02:00:00&to=2026-10-14T04:00:00');
    serve();
    render(<App />);
    await signIn();
    expect(h.rendered?.timeRange).toEqual({ spanSeconds: 3600, paused: false });
    expect(document.querySelector('[data-time-range-notice]')?.textContent).toContain(t('view:absUnzoned'));
  });

  it('kiosk says a fixed window is showing', async () => {
    landOn('?dashboard=wall&kiosk=1&from=2026-10-14T02:00:00Z&to=2026-10-14T04:00:00Z');
    serve();
    render(<App />);
    await signIn();
    expect(document.querySelector('[data-time-range-bar]')).toBeNull();
    expect(document.querySelector('[data-time-range-absolute]')).not.toBeNull();
  });

  it('picking a preset changes the span; pause freezes at the clock, resume lets go', async () => {
    landOn('?dashboard=wall');
    serve();
    render(<App />);
    await signIn();

    fireEvent.click(rangeButton('24h'));
    expect(h.rendered?.timeRange).toEqual({ spanSeconds: 86400, paused: false });

    fireEvent.click(pauseButton());
    expect(h.rendered?.timeRange).toEqual({ spanSeconds: 86400, paused: true, pausedAt: NOW });
    expect(screen.getByText(t('view:paused'))).toBeTruthy();

    fireEvent.click(pauseButton());
    expect(h.rendered?.timeRange).toEqual({ spanSeconds: 86400, paused: false });
    expect(screen.queryByText(t('view:paused'))).toBeNull();
  });

  it('kiosk has no controls but still announces a frozen board', async () => {
    landOn('?dashboard=wall&kiosk=1&paused=1');
    serve();
    render(<App />);
    await signIn();
    expect(document.querySelector('[data-time-range-bar]')).toBeNull();
    expect(document.querySelector('[data-time-range-paused]')).not.toBeNull();
    expect(h.rendered?.timeRange?.paused).toBe(true);
    // A wall can still carry command forms: the pill says they are not frozen with the readings.
    expect(document.querySelector('[data-time-range-actions-live]')?.textContent).toContain(t('view:actionsLive'));
  });

  it('kiosk, live: no indicator at all', async () => {
    landOn('?dashboard=wall&kiosk=1');
    serve();
    render(<App />);
    await signIn();
    expect(document.querySelector('[data-time-range-paused]')).toBeNull();
  });
});
