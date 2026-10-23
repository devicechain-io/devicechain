// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The standalone dashboard viewer — the ADR-039 reference external embedder. It
// proves the embed story: bring your OWN auth (its own login, NOT the console's
// dc-auth session), load a definition + an optional binding manifest, and render
// view-only. One definition + two manifests → two live dashboards on two different
// devices.
//
// Three steps: (1) sign in (login → selectTenant → a tenant access token held in
// React state); (2) load (fetch a dashboard's PUBLISHED snapshot by its token, or
// paste + parse an exported definition, plus a manifest); (3) view (render the
// definition read-only through a hub bound to the manifest). There is NO editing and
// no save, and the viewer only ever asks for the published snapshot — the draft is
// author-only. Paste stays for an embedder with no access to the service, and for
// previewing a definition offline.
//
// A first-time reader pasting has nothing to paste, so there are
// checked-in samples — a board definition and manifests that bind it to real simulated
// entities — at frontend/testdata/dash-samples. Read that README before pasting one:
// the scenario it names has to be bootstrapped first, or every widget renders empty.

import {
  createSessionTokens,
  decodeToken,
  gql,
  setAuthTokenGetter,
  type TokenPair,
} from '@devicechain/client';
import {
  createDeviceResolver,
  createEntityLister,
  effectiveBindings,
  withBindParams,
  type AbsoluteLinkRejection,
  type AbsoluteRange,
} from '@devicechain/dashboards';
import {
  AbsoluteIndicator,
  BoundDevicesNotice,
  DashboardRenderer,
  MapRuntimeProvider,
  PausedIndicator,
  TenantBasemapProvider,
  TimeRangeBar,
  describeBoundDevicesProblems,
  rejectedLinkNote,
  useBoardTimeRange,
  useBoundDevices,
  useDashboardHub,
  useResolvedBindings,
  useSlotCandidates,
  type AbsoluteLabels,
} from '@devicechain/widgets';
import { useEffect, useMemo, useRef, useState } from 'react';
import type { TFunction } from 'i18next';
import { useTranslation } from 'react-i18next';

import type { Basemap } from '@devicechain/client';

import { ConnectionStatus } from './ConnectionStatus';
import { LinkNotes } from './LinkNotes';
import { Playlist } from './Playlist';
import { SUPPORTED_LOCALES, setUserLocale } from './i18n/config';
import { loadErrorMessage } from './i18n/loadError';
import { signInErrorKey } from './i18n/signInError';
import { listOpenableDashboards, loadDashboard, loadPublishedDashboard, withWidgetCode, type Loaded } from './load';
import type { DashboardListItem } from './queries';
import { MAP_RUNTIME } from './map-runtime';
import { type DashDensity, type DashTheme, parseDashParams } from './params';
import { LOGIN, type Membership, REFRESH, SELECT_TENANT, TENANT_BASEMAP } from './queries';
import { endServerSession, finishSignOut } from './signOut';
import {
  SSO_COMPLETE_PATH,
  forgetSsoSession,
  leaveSsoReturn,
  leaveWithSession,
  runSsoCompletion,
  startSso,
  takeSsoSession,
} from './sso';
import { useTheme } from './theme';

// The provider-side failure classes the server names on a refused return.
const SSO_PROVIDER_ERROR_KEYS: Record<string, string> = {
  refused: 'signIn:ssoErrorRefused',
  link_required: 'signIn:ssoErrorLinkRequired',
  idp_error: 'signIn:ssoErrorIdp',
  groups_overage: 'signIn:ssoErrorGroupsOverage',
};

export default function App() {
  // The tenant access token, once signed in. Kept in state (to drive the UI) AND a
  // ref (so the token getter, registered once, always reads the latest value).
  // A single sign-on hands its session over from the return page (sso.ts, leaveWithSession);
  // otherwise both start empty.
  const [handedOver] = useState(takeSsoSession);
  const [token, setToken] = useState<string | null>(handedOver?.accessToken ?? null);
  // The held access/refresh pair. A ref, not state: it is replaced on every refresh, and a
  // re-render per refresh would have no reader (the token getter and the logout both read the
  // ref). `token` above only says whether anyone is signed in, and seeds the authorities.
  const pairRef = useRef<TokenPair | null>(
    handedOver ? { accessToken: handedOver.accessToken, refreshToken: handedOver.refreshToken } : null,
  );
  // Set when the last sign-out could not be confirmed by the server, so the sign-in screen
  // can say so rather than pretend the session is closed.
  const [signOutFailed, setSignOutFailed] = useState(false);
  // Set when the session could not be sustained (the refresh failed). Shown as a blocking
  // overlay: a board that quietly stops updating is the worst failure a wall display has.
  const [sessionEnded, setSessionEnded] = useState(false);

  // The URL parameters this page was opened with (a deep link). Parsed once: the address
  // is not rewritten, so the parameters survive the sign-in and a "sign in again".
  const [params] = useState(() => parseDashParams(window.location.search));
  const [theme, toggleTheme] = useTheme(params.theme);
  // The deep link's dashboard is loaded once per sign-in; "Change" must be able to leave it.
  const deepLinkUsed = useRef(false);

  // The parsed dashboard to view, once loaded. Null shows the load form (Step 2).
  const [loaded, setLoaded] = useState<Loaded | null>(null);

  // The refresh lifecycle (skew, one shared in-flight refresh, dropping a stale result) is
  // the SDK's; this supplies where the pair lives and what ending the session means here.
  const session = useMemo(
    () =>
      createSessionTokens({
        read: () => pairRef.current,
        apply: (pair) => {
          pairRef.current = pair;
        },
        refresh: async (refreshToken) =>
          (await gql('user-management', REFRESH, { refreshToken }, { anonymous: true })).refresh,
        // Refresh two minutes ahead of expiry rather than the default 30 s. A transient
        // failure (no network, a 5xx) is retried with backoff until the access token actually
        // expires, and an unattended wall display wants that window long enough to ride out a
        // Wi-Fi reconnect. A refusal (revoked or invalid refresh token, SSO required) still
        // ends the session at once.
        skewSeconds: 120,
        onEnd: (err) => {
          console.warn('/dash: the session ended and could not be refreshed', err);
          pairRef.current = null;
          setToken(null);
          setLoaded(null);
          setSessionEnded(true);
        },
      }),
    [],
  );

  // Register the token getter ONCE at mount. Every request and every websocket (re)connect
  // resolves its token through it, so a refreshed token reaches the hub by that route.
  useEffect(() => {
    setAuthTokenGetter(() => session.getAccessToken());
    return () => setAuthTokenGetter(null);
  }, [session]);

  // Refresh ahead of expiry while signed in. A wall display holds a websocket and makes no
  // request that would trigger the on-demand path, so nothing else would renew its token.
  useEffect(() => {
    if (!token) return undefined;
    return session.startAutoRefresh();
  }, [token, session]);

  const signOut = () => {
    const refreshToken = pairRef.current?.refreshToken ?? null;
    pairRef.current = null;
    session.reset();
    forgetSsoSession();
    // Local state clears at once so the viewer is out whatever the network does; the
    // server call runs behind it and reports a failure on the sign-in screen.
    setToken(null);
    setLoaded(null);
    // Only once the server confirmed does it go on to end the session at the identity
    // provider too, which sends the browser back to this app's sign-in screen.
    void finishSignOut(refreshToken, setSignOutFailed);
  };

  const onAuthed = (accessToken: string, refreshToken: string) => {
    pairRef.current = { accessToken, refreshToken };
    deepLinkUsed.current = false;
    setSignOutFailed(false);
    setSessionEnded(false);
    setToken(accessToken);
  };

  // The identity provider's return lands on /dash/sso/complete. The query has already been
  // stripped (main.tsx). This document is never signed in: the return page only ever leaves
  // by a full navigation (sso.ts explains why), so it renders nothing else.
  if (window.location.pathname === SSO_COMPLETE_PATH) return <SsoComplete />;
  if (sessionEnded) return <SessionEnded onSignIn={() => setSessionEnded(false)} />;
  if (!token) return <SignIn onAuthed={onAuthed} signOutFailed={signOutFailed} />; // Step 1
  // What the link's rotate / every set aside, said on the page: a wall display has no console to
  // warn in. Shown over whichever step the link opened.
  const linkNotes = params.rotateIssues ? <LinkNotes issues={params.rotateIssues} /> : null;
  // A playlist (rotate=a,b): the boards take turns, each read by token like a deep link, and the
  // viewer's other settings apply to every one of them.
  if (params.playlist) {
    const { tokens, everySeconds } = params.playlist;
    const authorities = decodeToken(token)?.authorities ?? [];
    return (
      <>
        {linkNotes}
        <Playlist
          tokens={tokens}
          everySeconds={everySeconds}
          load={(boardToken) => loadPublishedDashboard(boardToken, '')}
          search={window.location.search}
          chrome={params.kioskChrome}
          renderBoard={({ loaded: board, search }) => (
            <View
              loaded={board}
              authorities={authorities}
              onChange={() => {}}
              onSignOut={signOut}
              kiosk
              kioskChrome={params.kioskChrome}
              density={params.density}
              range={params.range}
              paused={params.paused}
              absolute={params.absolute}
              rejectedWindow={params.rejected}
              linkSearch={search}
              theme={theme}
              onToggleTheme={toggleTheme}
            />
          )}
        />
      </>
    ); // Step 2 and 3, taking turns
  }
  if (!loaded) {
    return (
      <>
        {linkNotes}
        <Load
          onRender={setLoaded}
          onSignOut={signOut}
          autoLoadToken={deepLinkUsed.current ? null : params.dashboard}
          onAutoLoadSettled={() => {
            deepLinkUsed.current = true;
          }}
        />
      </>
    ); // Step 2
  }
  // The viewer's authorities gate action controls (alarm ack/clear); the server enforces
  // them regardless. Decoded from the access token this reference viewer holds.
  const authorities = decodeToken(token)?.authorities ?? [];
  return (
    <>
    {linkNotes}
    <View
      loaded={loaded}
      authorities={authorities}
      onChange={() => {
        // Leaving this board for another: the devices a link bound belong to THIS board's slots.
        const { pathname, search, hash } = window.location;
        const kept = withBindParams(search, {});
        window.history.replaceState(window.history.state, '', `${pathname}${kept ? `?${kept}` : ''}${hash}`);
        setLoaded(null);
      }}
      onSignOut={signOut}
      kiosk={params.kiosk}
      kioskChrome={params.kioskChrome}
      density={params.density}
      range={params.range}
      paused={params.paused}
      absolute={params.absolute}
      rejectedWindow={params.rejected}
      theme={theme}
      onToggleTheme={toggleTheme}
    />
    </>
  ); // Step 3
}

// ── Session ended ────────────────────────────────────────────────────────────────────
// The refresh failed, so the board can no longer be kept live. It is replaced by this,
// full-screen and impossible to miss from across a room, rather than left frozen with the
// last values on it. The address still carries the deep link, so signing in again brings
// the same board straight back.

function SessionEnded({ onSignIn }: { onSignIn: () => void }) {
  const { t } = useTranslation();
  return (
    <div
      role="alertdialog"
      aria-modal="true"
      aria-labelledby="dash-session-ended-title"
      aria-describedby="dash-session-ended-body"
      style={{
        position: 'fixed',
        inset: 0,
        zIndex: 1000,
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        padding: 24,
        background: 'hsl(var(--background))',
        color: 'hsl(var(--foreground))',
      }}
    >
      <div style={{ display: 'flex', flexDirection: 'column', gap: 16, maxWidth: 520, textAlign: 'center' }}>
        <div id="dash-session-ended-title" style={{ fontSize: 28, fontWeight: 600 }}>
          {t('common:sessionEndedTitle')}
        </div>
        <div id="dash-session-ended-body" style={{ fontSize: 16, color: 'hsl(var(--muted-foreground))' }}>
          {t('common:sessionEndedBody')}
        </div>
        <div>
          <HeaderButton primary onClick={onSignIn}>
            {t('common:sessionEndedAction')}
          </HeaderButton>
        </div>
      </div>
    </div>
  );
}

// ── Step 1: Sign in ─────────────────────────────────────────────────────────
// email + password → login → identity token + memberships. Zero memberships is an
// error; one auto-selects; many show a picker. selectTenant yields the access token.

function SignIn({
  onAuthed,
  signOutFailed,
}: {
  onAuthed: (accessToken: string, refreshToken: string) => void;
  signOutFailed: boolean;
}) {
  const { t } = useTranslation();
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [identityToken, setIdentityToken] = useState<string | null>(null);
  const [memberships, setMemberships] = useState<Membership[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // 🔴 The two `rejectedKey`s differ because the two failures are different facts. At
  // this point the identity is already proven — the tenant is what did not work — so
  // reporting "invalid email or password" here would send the viewer to re-type
  // credentials the server has just accepted.
  const selectTenant = (idToken: string, tenant: string) => {
    setBusy(true);
    setError(null);
    gql('user-management', SELECT_TENANT, { identityToken: idToken, tenant }, { anonymous: true })
      .then((r) => onAuthed(r.selectTenant.accessToken, r.selectTenant.refreshToken))
      .catch((err: unknown) => {
        setError(t(signInErrorKey(err, 'signIn:enterTenantFailed')));
        setBusy(false);
      });
  };

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    gql('user-management', LOGIN, { email, password }, { anonymous: true })
      .then((r) => {
        const { identityToken: idToken, memberships: mships } = r.login;
        if (mships.length === 0) {
          setError(t('signIn:noTenants'));
          setBusy(false);
          return;
        }
        // Every tenant requires single sign-on, which a password cannot enter: say so on
        // the form, where the SSO button is, rather than offering choices that fail.
        if (mships.every((m) => m.ssoRequired)) {
          setError(t('signIn:ssoRequired'));
          setBusy(false);
          return;
        }
        if (mships.length === 1) {
          selectTenant(idToken, mships[0].tenant);
          return;
        }
        // More than one — hold the identity token and let the user pick a tenant.
        setIdentityToken(idToken);
        setMemberships(mships);
        setBusy(false);
      })
      .catch((err: unknown) => {
        // The backend deliberately does not distinguish a bad email from a bad
        // password, so neither does this.
        setError(t(signInErrorKey(err, 'signIn:invalidCredentials')));
        setBusy(false);
      });
  };

  // Single sign-on: the server picks the connection that speaks for the email's domain and
  // answers with the provider's address, which the browser is sent to as given. A null
  // answer means no connection covers the domain.
  const sso = () => {
    const address = email.trim();
    if (!address) {
      setError(t('signIn:ssoEmailRequired'));
      return;
    }
    setBusy(true);
    setError(null);
    startSso(address)
      .then((url) => {
        if (url === null) {
          setError(t('signIn:ssoNotConfigured'));
          setBusy(false);
          return;
        }
        window.location.assign(url);
      })
      .catch((err: unknown) => {
        setError(t(signInErrorKey(err, 'signIn:ssoUnavailable')));
        setBusy(false);
      });
  };

  // Tenant picker (only when the identity has more than one membership).
  if (identityToken) {
    return (
      <Centered>
        <Card>
          <div style={{ fontSize: 18, fontWeight: 600 }}>{t('signIn:chooseTenant')}</div>
          {error && <ErrorText>{error}</ErrorText>}
          <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
            {memberships.map((m) => (
              <HeaderButton
                key={m.tenant}
                onClick={() => selectTenant(identityToken, m.tenant)}
                // A tenant that requires single sign-on is shown, so its absence is not a
                // mystery, but cannot be chosen with this password sign-in.
                disabled={busy || m.ssoRequired}
              >
                {m.ssoRequired ? t('signIn:ssoRequiredTenant', { tenant: m.tenant }) : m.tenant}
              </HeaderButton>
            ))}
          </div>
          <LocaleSwitcher />
        </Card>
      </Centered>
    );
  }

  return (
    <Centered>
      <form onSubmit={submit} style={{ display: 'contents' }}>
        <Card data-shot="dash-signin">
          <div style={{ fontSize: 18, fontWeight: 600 }}>{t('signIn:title')}</div>
          <Field label={t('signIn:emailLabel')}>
            <TextInput
              type="email"
              value={email}
              onChange={setEmail}
              autoComplete="username"
              autoFocus
              data-shot="dash-email"
            />
          </Field>
          <Field label={t('signIn:passwordLabel')}>
            <TextInput
              type="password"
              value={password}
              onChange={setPassword}
              autoComplete="current-password"
              data-shot="dash-password"
            />
          </Field>
          {signOutFailed && <ErrorText>{t('signIn:signOutFailed')}</ErrorText>}
          {error && <ErrorText>{error}</ErrorText>}
          <HeaderButton type="submit" primary disabled={busy} data-shot="dash-signin-submit">
            {busy ? t('signIn:submitting') : t('signIn:submit')}
          </HeaderButton>
          <HeaderButton onClick={sso} disabled={busy}>
            {t('signIn:ssoButton')}
          </HeaderButton>
          <LocaleSwitcher />
        </Card>
      </form>
    </Centered>
  );
}

// ── Step 1, single sign-on return ───────────────────────────────────────────
// The identity provider's callback lands here with a one-time code (already taken out of
// the address bar by main.tsx). runSsoCompletion redeems it once, even under StrictMode's
// double mount. A failure shows its reason and a way back to the sign-in form; nothing
// here retries by itself. Every way out — signed in, or back to the form — is a full
// navigation to /dash/, never a client-side hop (sso.ts, leaveWithSession).

function SsoComplete() {
  const { t } = useTranslation();
  const [identityToken, setIdentityToken] = useState<string | null>(null);
  const [memberships, setMemberships] = useState<Membership[]>([]);
  const [busy, setBusy] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const selectTenant = (idToken: string, tenant: string) => {
    setBusy(true);
    setError(null);
    return gql('user-management', SELECT_TENANT, { identityToken: idToken, tenant }, { anonymous: true })
      .then((r) => {
        const session = { accessToken: r.selectTenant.accessToken, refreshToken: r.selectTenant.refreshToken };
        if (leaveWithSession(session)) return; // the browser is on its way to /dash/
        // The session could not cross to the next document. Staying signed in HERE is the
        // one thing this page must not do, so end it on the server and say the sign-in failed.
        void endServerSession(session.refreshToken);
        setError(t('signIn:ssoErrorRedeemFailed'));
        setBusy(false);
      })
      .catch((err: unknown) => {
        setError(t(signInErrorKey(err, 'signIn:enterTenantFailed')));
        setBusy(false);
      });
  };

  useEffect(() => {
    let live = true;
    void runSsoCompletion().then((out) => {
      if (!live) return;
      if (!out.ok) {
        setBusy(false);
        switch (out.reason) {
          case 'provider':
            setError(t(SSO_PROVIDER_ERROR_KEYS[out.providerError ?? ''] ?? 'signIn:ssoErrorRefused'));
            return;
          case 'missingCode':
            setError(t('signIn:ssoErrorMissingCode'));
            return;
          case 'missingVerifier':
            setError(t('signIn:ssoErrorMissingVerifier'));
            return;
          case 'redeemFailed':
            setError(t(signInErrorKey(out.cause, 'signIn:ssoErrorRedeemFailed')));
            return;
        }
      }
      if (out.memberships.length === 0) {
        setError(t('signIn:noTenants'));
        setBusy(false);
      } else if (out.memberships.length === 1) {
        void selectTenant(out.identityToken, out.memberships[0].tenant);
      } else {
        setIdentityToken(out.identityToken);
        setMemberships(out.memberships);
        setBusy(false);
      }
    });
    return () => {
      live = false;
    };
    // Page-lifetime state: only the mount matters; the second StrictMode mount shares the
    // first one's redemption.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return (
    <Centered>
      <Card>
        <div style={{ fontSize: 18, fontWeight: 600 }}>
          {identityToken ? t('signIn:chooseTenant') : t('signIn:title')}
        </div>
        {busy && !error && <div>{t('signIn:ssoCompleting')}</div>}
        {error && <ErrorText>{error}</ErrorText>}
        {identityToken && !error && (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
            {memberships.map((m) => (
              <HeaderButton
                key={m.tenant}
                onClick={() => void selectTenant(identityToken, m.tenant)}
                disabled={busy}
              >
                {m.tenant}
              </HeaderButton>
            ))}
          </div>
        )}
        {error && <HeaderButton onClick={leaveSsoReturn}>{t('signIn:ssoBack')}</HeaderButton>}
        <LocaleSwitcher />
      </Card>
    </Centered>
  );
}

// ── Step 2: Load ────────────────────────────────────────────────────────────
// Paste an exported definition + an optional binding manifest, parse both, and
// advance. Parse errors show inline; nothing throws to a white screen. The parsing
// itself is loadDashboard, in load.ts, so a test can drive it.

// The two manifest shapes, as JSON templates. Every token in them (`kind`,
// `deviceToken`, `anchor`, `targetToken`) is a field name the parser matches literally,
// so translating one would produce a manifest the parser refuses — they stay module
// consts, and the i18n lint reads them as the code samples they are.
//
// 🔴 SPLIT INTO AN ARRAY BECAUSE THE JOINER IS THE ONE PART THAT IS PROSE. This was a
// single string reading `…} }  or  { {…`, and that "or" is an English word sitting in
// the middle of what looks like code — the kind of literal an enumeration finds and a
// glance does not, because the line as a whole reads as a sample. The samples are data;
// the conjunction between them is copy, and it comes from the catalog below.
const MANIFEST_SAMPLES = [
  '{ "slotName": { "kind": "device", "deviceToken": "..." } }',
  '{ "slotName": { "kind": "anchor", "anchor": { "relationship": "...", "targetType": "area", "targetToken": "..." } } }',
];

// The manifest box's placeholder: an empty JSON object, i.e. "no overrides". A code
// sample like MANIFEST_SAMPLES above, never translated.
const EMPTY_OBJECT = '{ }';

export function Load({
  onRender,
  onSignOut,
  autoLoadToken = null,
  onAutoLoadSettled,
}: {
  onRender: (loaded: Loaded) => void;
  onSignOut: () => void;
  /** A deep link's dashboard token: loaded straight away, without showing the form. */
  autoLoadToken?: string | null;
  /** The deep link's load finished, either way, so "Change" does not reload it. */
  onAutoLoadSettled?: () => void;
}) {
  const { t } = useTranslation();
  const [dashboardToken, setDashboardToken] = useState(autoLoadToken ?? '');
  // Held from mount: a deep link shows the loading note, not the form, until its load ends.
  const [autoLoading, setAutoLoading] = useState(autoLoadToken !== null);
  const [definitionText, setDefinitionText] = useState('');
  const [manifestText, setManifestText] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [fetching, setFetching] = useState(false);

  const show = (result: Awaited<ReturnType<typeof loadPublishedDashboard>>) => {
    if ('error' in result) {
      setError(loadErrorMessage(result.error, t));
      return;
    }
    setError(null);
    onRender(result.loaded);
  };

  // The deep link: load the named dashboard once, on mount. A failure falls back to the form
  // with the token filled in and the reason shown, so the viewer can see what went wrong.
  useEffect(() => {
    if (autoLoadToken === null) return undefined;
    let live = true;
    void loadPublishedDashboard(autoLoadToken, '').then((result) => {
      if (!live) return;
      onAutoLoadSettled?.();
      setAutoLoading(false);
      show(result);
    });
    return () => {
      live = false;
    };
    // Mount only: the deep link is consumed once (the parent flips it off when it settles).
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // The paste door: offline, no server involved. The parse is synchronous; the wait is only for
  // the code of the board's lazily loaded widgets, so the first frame is the whole board.
  const render = async () => show(await withWidgetCode(loadDashboard(definitionText, manifestText)));

  // The default door: the dashboard's published snapshot, by token.
  const loadPublished = async (token: string = dashboardToken) => {
    setFetching(true);
    try {
      show(await loadPublishedDashboard(token, manifestText));
    } finally {
      setFetching(false);
    }
  };

  if (autoLoading) return <Centered data-shot="loading">{t('load:loading')}</Centered>;

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%' }}>
      <header
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 12,
          padding: '8px 16px',
          borderBottom: '1px solid hsl(var(--border))',
          flex: '0 0 auto',
        }}
      >
        <div style={{ flex: '1 1 auto', fontWeight: 600 }}>{t('load:title')}</div>
        <LocaleSwitcher />
        <HeaderButton onClick={onSignOut}>{t('common:signOut')}</HeaderButton>
      </header>

      <main
        data-shot="dash-load"
        style={{
          flex: '1 1 auto',
          minHeight: 0,
          overflow: 'auto',
          padding: 24,
          display: 'flex',
          flexDirection: 'column',
          gap: 16,
          maxWidth: 900,
          width: '100%',
          margin: '0 auto',
        }}
      >
        <DashboardList
          disabled={fetching}
          onOpen={(token) => {
            setDashboardToken(token);
            void loadPublished(token);
          }}
        />

        <Field label={t('load:tokenLabel')} hint={t('load:tokenHint')}>
          <TextInput
            value={dashboardToken}
            onChange={setDashboardToken}
            placeholder={t('load:tokenPlaceholder')}
            data-shot="dash-token"
          />
        </Field>
        <div>
          <HeaderButton
            onClick={() => void loadPublished()}
            primary
            disabled={dashboardToken.trim() === '' || fetching}
            data-shot="dash-load-published"
          >
            {fetching ? t('load:loading') : t('load:loadPublished')}
          </HeaderButton>
        </div>

        <div style={{ fontWeight: 600 }}>{t('load:pasteHeading')}</div>
        <Field label={t('load:definitionLabel')}>
          <TextArea
            value={definitionText}
            onChange={setDefinitionText}
            rows={14}
            placeholder={t('load:definitionPlaceholder')}
          />
        </Field>

        <Field
          label={t('load:manifestLabel')}
          hint={MANIFEST_SAMPLES.join(`  ${t('load:manifestSampleJoiner')}  `)}
        >
          <TextArea
            value={manifestText}
            onChange={setManifestText}
            rows={7}
            placeholder={EMPTY_OBJECT}
          />
        </Field>

        {error && <ErrorText>{error}</ErrorText>}

        <div>
          <HeaderButton onClick={render} disabled={definitionText.trim() === ''}>
            {t('load:render')}
          </HeaderButton>
        </div>
      </main>
    </div>
  );
}

// DashboardList offers the dashboards this viewer may open, so nobody has to know a token.
// A member limited to part of the tenant (a customer user) is served exactly the published
// dashboards shared with their customers; anyone else, every published dashboard. The token
// field below it stays, for a dashboard past the first page and for the deep-link habit.
function DashboardList({ onOpen, disabled }: { onOpen: (token: string) => void; disabled: boolean }) {
  const { t } = useTranslation();
  const [state, setState] = useState<
    { status: 'loading' } | { status: 'ready'; dashboards: DashboardListItem[]; more: boolean } | { status: 'error'; message: string }
  >({ status: 'loading' });

  useEffect(() => {
    let live = true;
    void listOpenableDashboards().then((result) => {
      if (!live) return;
      if ('error' in result) setState({ status: 'error', message: loadErrorMessage(result.error, t) });
      else setState({ status: 'ready', dashboards: result.dashboards, more: result.more });
    });
    return () => {
      live = false;
    };
    // Once per visit to this step: the list is what the viewer may open right now.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return (
    <section data-shot="dash-list" style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      <div style={{ fontWeight: 600 }}>{t('load:listHeading')}</div>
      {state.status === 'loading' ? (
        <div style={{ color: 'hsl(var(--muted-foreground))' }}>{t('load:loading')}</div>
      ) : state.status === 'error' ? (
        <ErrorText>{state.message}</ErrorText>
      ) : state.dashboards.length === 0 ? (
        <div style={{ color: 'hsl(var(--muted-foreground))' }}>{t('load:listEmpty')}</div>
      ) : (
        <>
          <div style={{ display: 'grid', gap: 8, gridTemplateColumns: 'repeat(auto-fill, minmax(220px, 1fr))' }}>
            {state.dashboards.map((d) => (
              <ListItemButton key={d.token} onClick={() => onOpen(d.token)} disabled={disabled}>
                <span style={{ fontWeight: 600 }}>{d.name || d.token}</span>
                {d.description && (
                  <span style={{ fontSize: 12, color: 'hsl(var(--muted-foreground))' }}>{d.description}</span>
                )}
              </ListItemButton>
            ))}
          </div>
          {state.more && <div style={{ fontSize: 12, color: 'hsl(var(--muted-foreground))' }}>{t('load:listMore')}</div>}
        </>
      )}
    </section>
  );
}

function ListItemButton({
  children,
  onClick,
  disabled,
}: {
  children: React.ReactNode;
  onClick: () => void;
  disabled: boolean;
}) {
  return (
    <button
      type="button"
      data-shot="dash-list-item"
      onClick={onClick}
      disabled={disabled}
      style={{
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'flex-start',
        gap: 4,
        textAlign: 'left',
        padding: '10px 12px',
        borderRadius: 8,
        border: '1px solid hsl(var(--border))',
        background: 'hsl(var(--card))',
        color: 'hsl(var(--foreground))',
        cursor: disabled ? 'default' : 'pointer',
        opacity: disabled ? 0.6 : 1,
      }}
    >
      {children}
    </button>
  );
}

// ── Step 3: View ────────────────────────────────────────────────────────────
// Render the parsed definition read-only, its slots resolved through the effective
// manifest. VIEW-ONLY — no edit mode, no save, no react-rnd.

// The custom-window strings for the shared time-range bar.
function absoluteLabels(t: TFunction): AbsoluteLabels {
  return {
    custom: t('view:absCustom'),
    from: t('view:absFrom'),
    to: t('view:absTo'),
    apply: t('view:absApply'),
    cancel: t('view:absCancel'),
    fixed: t('view:absFixed'),
    backToLive: t('view:absBackToLive'),
    copyLink: t('view:absCopyLink'),
    linkCopied: t('view:absLinkCopied'),
    loadFailed: t('view:absLoadFailed'),
    dismiss: t('view:absDismiss'),
    incomplete: t('view:absIncomplete'),
    unzoned: t('view:absUnzoned'),
    zone: (zone, offset) => t('view:absZone', { zone, offset }),
    problem: {
      unparseable: t('view:absProblemUnparseable'),
      order: t('view:absProblemOrder'),
      tooShort: t('view:absProblemTooShort'),
      tooLong: t('view:absProblemTooLong'),
      future: t('view:absProblemFuture'),
      tooEarly: t('view:absProblemTooEarly'),
    },
    ignored: (reason, raw) => t('view:absIgnored', { reason, raw }),
  };
}

// Exported for its behaviour test (view-bindings.test.tsx); App is the only render site.
export function View({
  loaded,
  authorities,
  onChange,
  onSignOut,
  kiosk = false,
  kioskChrome = true,
  density = null,
  range = null,
  paused = false,
  absolute,
  rejectedWindow,
  linkSearch,
  theme,
  onToggleTheme,
}: {
  loaded: Loaded;
  authorities: string[];
  onChange: () => void;
  onSignOut: () => void;
  /** Hide the chrome (header and its controls) and offer fullscreen instead. */
  kiosk?: boolean;
  /**
   * With kiosk: show its floating connection badge and fullscreen button. ?chrome=0 hides the
   * button, and the badge while the board is live; never a paused or disconnected state.
   */
  kioskChrome?: boolean;
  /**
   * The viewer's ?density= override, validated. It is handed to the renderer, where it wins over
   * the definition's own `canvas.density`; `data-density` on this root stays as the hook for
   * kiosk styling and for tests.
   */
  density?: DashDensity | null;
  /** The ?range= deep link, validated: a preset's span in seconds, or null for the board's default. */
  range?: number | null;
  /** The ?paused=1 deep link: open frozen at the moment of loading. */
  paused?: boolean;
  /** The ?from=&to= deep link, validated: a fixed window that wins over range and paused. */
  absolute?: AbsoluteRange;
  /** The ?from=&to= values that were unusable; the header says the link was ignored. */
  rejectedWindow?: AbsoluteLinkRejection;
  /**
   * The link this board is opened with, when it is not the page's own address (a playlist hands
   * each board the link filtered to the slots that board has). The address is then left alone: a
   * pick is kept in this board's state and never written back to the URL.
   */
  linkSearch?: string;
  theme?: DashTheme;
  onToggleTheme?: () => void;
}) {
  const { t } = useTranslation();
  const { definition, manifest, registry } = loaded;

  // base = definition defaults merged with the pasted manifest override; the cascade
  // (useResolvedBindings) resolves scoped slots from their parent + the selection overlay
  // and returns the settled manifest the renderer resolves every widget's slot through.
  // The resolver backs both the hub's anchor→device-token expansion and the cascade's
  // membership lookups.
  const resolver = useMemo(() => createDeviceResolver(), []);
  // The context/entity-selector candidate provider (ADR-039 selection amendment): backs a
  // context-selector so a viewer can re-point the dashboard's top-level context or pick a
  // member within it. Shares the resolver's device-management coupling via one factory.
  const lister = useMemo(() => createEntityLister(), []);
  const base = useMemo(() => effectiveBindings(definition, manifest), [definition, manifest]);
  // The view-driven selection overlay (an alarm-originator drill, a marker or selector pick),
  // kept in step with the address: `?bind.<slot>=<device>` opens the board with that device bound
  // (checked against the viewer's own scope), and a pick rewrites the address in place.
  const [search, setSearch] = useState(() => linkSearch ?? window.location.search);
  useEffect(() => {
    if (linkSearch !== undefined) return undefined;
    const onPop = () => setSearch(window.location.search);
    window.addEventListener('popstate', onPop);
    return () => window.removeEventListener('popstate', onPop);
  }, [linkSearch]);
  const address = useMemo(
    () => ({
      search,
      replaceSearch: (next: string) => {
        if (linkSearch !== undefined) {
          setSearch(next ? `?${next}` : '');
          return;
        }
        const { pathname, hash } = window.location;
        window.history.replaceState(window.history.state, '', `${pathname}${next ? `?${next}` : ''}${hash}`);
        setSearch(window.location.search);
      },
    }),
    [search, linkSearch],
  );
  const bound = useBoundDevices({ definition, resolver, address });
  const { selection, select } = bound;
  const bindings = useResolvedBindings(definition, base, selection, resolver);
  const candidates = useSlotCandidates(definition, bindings, resolver, lister);
  // ONE hub for this view's lifetime (per resolver + authorities), torn down on unmount.
  // It holds no bindings: a selection reaches the renderer as new `bindings`, and only the
  // widgets bound to the re-pointed slot re-subscribe.
  const hub = useDashboardHub(resolver, authorities);

  // The viewer's time range and pause. The link wins over the board's own default for the span;
  // it is read once, so a viewer's later pick is never undone by a re-render.
  const [timeRange, timeRangeControls] = useBoardTimeRange(definition.timeRange?.defaultSeconds, {
    spanSeconds: range ?? undefined,
    paused,
    absolute,
  });
  const timeLabels = {
    range: t('view:timeRange'),
    pause: t('view:pause'),
    resume: t('view:resume'),
    paused: t('view:paused'),
    pausedAt: (time: string) => t('view:pausedAt', { time }),
    actionsLive: t('view:actionsLive'),
    absolute: absoluteLabels(t),
  };
  const rangeNotice = rejectedWindow ? rejectedLinkNote(rejectedWindow, timeLabels) : undefined;

  const title = definition.title || t('view:untitled');
  const basemap = useTenantBasemapQuery();

  return (
    <div
      data-shot="dash-view"
      data-density={density ?? undefined}
      style={{ display: 'flex', flexDirection: 'column', height: '100%' }}
    >
      {kiosk ? (
        <>
          {/* A wall display has no header, but a frozen board must never look live: the
              connection state floats in the corner opposite the fullscreen button. */}
          {/* Kept clear of the fullscreen button opposite: a fixed window's pill is long enough to
              reach it on a narrow screen, and wraps instead. */}
          <div
            style={{
              position: 'fixed',
              top: 8,
              left: 8,
              zIndex: 10,
              display: 'flex',
              flexWrap: 'wrap',
              gap: 8,
              maxWidth: kioskChrome ? 'calc(100% - 136px)' : 'calc(100% - 16px)',
            }}
          >
            {/* chrome=0 hides it only while the board is live: a dropped board still says so. */}
            <ConnectionStatus hub={hub} onlyWhenDown={!kioskChrome} />
            {/* A wall board has no controls, but it must never be frozen without saying so. */}
            {timeRange.absolute ? (
              <AbsoluteIndicator absolute={timeRange.absolute} labels={timeLabels} />
            ) : (
              timeRange.paused && <PausedIndicator pausedAt={timeRange.pausedAt} labels={timeLabels} />
            )}
          </div>
          {kioskChrome && <FullscreenButton />}
        </>
      ) : (
        <header
          style={{
            display: 'flex',
            // Wraps on a narrow screen: unwrapped, every control was squeezed to its narrowest and
            // the time-range bar stacked one button per line (and a fixed window's dates one word
            // per line) down half a phone's height, with the row still wider than the screen.
            flexWrap: 'wrap',
            alignItems: 'center',
            gap: 12,
            padding: '8px 16px',
            borderBottom: '1px solid hsl(var(--border))',
            flex: '0 0 auto',
          }}
        >
          <div style={{ flex: '1 1 auto', fontWeight: 600 }}>{title}</div>
          <TimeRangeBar state={timeRange} controls={timeRangeControls} labels={timeLabels} notice={rangeNotice} />
          <ConnectionStatus hub={hub} />
          <LocaleSwitcher />
          {onToggleTheme && (
            <HeaderButton onClick={onToggleTheme} data-shot="dash-theme-toggle">
              {theme === 'dark' ? t('view:themeToLight') : t('view:themeToDark')}
            </HeaderButton>
          )}
          <HeaderButton onClick={onChange}>{t('view:change')}</HeaderButton>
          <HeaderButton onClick={onSignOut}>{t('common:signOut')}</HeaderButton>
        </header>
      )}

      <main style={{ flex: '1 1 auto', minHeight: 0 }}>
        {/* 🔴 The map runtime is BUNDLER WIRING, not configuration, and this app is the
            reference external embedder — so what it does here is what a third-party host
            must do. Without it a map widget renders a notice instead of a map, which is
            deliberate: the alternative is MapLibre deriving a worker URL that 404s and
            drawing nothing, on a surface where nobody would know to look. */}
        <TenantBasemapProvider basemap={basemap}>
          <MapRuntimeProvider runtime={MAP_RUNTIME}>
            {bound.phase === 'checking' ? (
              <p role="status" style={{ padding: 16, color: 'hsl(var(--muted-foreground))' }}>
                {t('view:linkChecking')}
              </p>
            ) : bound.phase === 'refused' && bound.problems ? (
              <BoundDevicesNotice
                title={t('view:linkBlockedTitle')}
                lines={describeBoundDevicesProblems(bound.problems, {
                  unknownSlot: (slot) => t('view:linkUnknownSlot', { slot }),
                  notADeviceSlot: (slot) => t('view:linkNotDeviceSlot', { slot }),
                  duplicateSlot: (slot) => t('view:linkDuplicateSlot', { slot }),
                  derivedSlot: (slot) => t('view:linkDerivedSlot', { slot }),
                  invalidToken: (slot, token) => t('view:linkInvalidToken', { slot, token }),
                  missing: (slot, token) => t('view:linkMissing', { slot, token }),
                  failed: (slot, token) => t('view:linkFailed', { slot, token }),
                })}
                dismissLabel={t('view:linkOpenWithout')}
                onDismiss={bound.dismiss}
                retryLabel={bound.problems.failed.length > 0 ? t('view:linkRetry') : undefined}
                onRetry={bound.retry}
              />
            ) : (
            <DashboardRenderer
              definition={definition}
              hub={hub}
              registry={registry}
              actions={hub}
              bindings={bindings}
              select={select}
              candidates={candidates}
              density={density ?? undefined}
              timeRange={timeRange}
            />
            )}
          </MapRuntimeProvider>
        </TenantBasemapProvider>
      </main>
    </div>
  );
}

// useTenantBasemapQuery fetches the tenant's effective basemap once the viewer is
// signed in, so a map widget on an embedded board draws on the same tiles it would in
// the console.
//
// 🔴 It degrades to null and never surfaces an error. A basemap is decoration on top
// of the data a viewer came for: failing the whole board because the tile source could
// not be read would be a strictly worse outcome than a plain panel. The widget already
// has a good answer for "no basemap" and says why.
function useTenantBasemapQuery(): Basemap | null {
  const [basemap, setBasemap] = useState<Basemap | null>(null);
  useEffect(() => {
    let cancelled = false;
    gql('user-management', TENANT_BASEMAP)
      .then((r) => {
        if (!cancelled) setBasemap(r.tenant.basemap);
      })
      .catch(() => {
        /* decoration, not data — see above */
      });
    return () => {
      cancelled = true;
    };
  }, []);
  return basemap;
}

/**
 * Kiosk chrome: the one control a kiosk board keeps. Hidden when the browser cannot do
 * fullscreen (or the page is not allowed to), since a button that does nothing is worse than
 * none. It floats over the board's corner at low opacity so it does not take a row from it.
 */
function FullscreenButton() {
  const { t } = useTranslation();
  const [on, setOn] = useState(() => document.fullscreenElement != null);
  useEffect(() => {
    const sync = () => setOn(document.fullscreenElement != null);
    document.addEventListener('fullscreenchange', sync);
    return () => document.removeEventListener('fullscreenchange', sync);
  }, []);
  if (!document.fullscreenEnabled) return null;
  const toggle = () => {
    const refused = (err: unknown) => console.warn('/dash: fullscreen was refused', err);
    // Promise.resolve: an older engine returns undefined rather than a promise, and one that
    // throws synchronously is caught the same way, so a refusal never escapes the handler.
    try {
      Promise.resolve(on ? document.exitFullscreen() : document.documentElement.requestFullscreen()).catch(
        refused,
      );
    } catch (err) {
      refused(err);
    }
  };
  return (
    <div data-shot="dash-fullscreen" style={{ position: 'fixed', top: 8, right: 8, zIndex: 10, opacity: 0.7 }}>
      <HeaderButton onClick={toggle}>
        {on ? t('view:exitFullscreen') : t('view:enterFullscreen')}
      </HeaderButton>
    </div>
  );
}

// ── Presentational helpers (inline styles + CSS vars — no Tailwind/shadcn) ────

/**
 * Language picker — chrome, not a form field. Mounted on all three steps, because a
 * viewer who cannot read the sign-in form cannot reach a switcher that only appears
 * after signing in; that ordering is the whole reason this control exists rather than
 * leaving the language to browser detection alone.
 *
 * Selecting a locale persists it as an explicit choice (setUserLocale) under the same
 * key the console writes, so a viewer who chose Spanish there arrives here in Spanish.
 * It reads SUPPORTED_LOCALES, so a new locale needs no change in this file.
 *
 * Deliberately NOT the console's SegmentedControl: this app ships no component kit —
 * that is part of what it demonstrates to an external embedder — so it is a group of
 * pressed-state buttons carrying their own accessible names. The endonym is the
 * accessible name and the code badge beside it is decorative, since a screen reader
 * reading "EN English" twice is noise.
 */
function LocaleSwitcher() {
  const { t, i18n } = useTranslation();
  // resolvedLanguage is the locale actually in effect after supportedLngs/fallback
  // resolution, never a raw unshipped browser value. Before it resolves there is no
  // language in effect, and '' matches no locale — the honest rendering of "not yet".
  const active = i18n.resolvedLanguage ?? '';

  return (
    <div
      role="group"
      aria-label={t('common:languagePicker')}
      style={{ display: 'flex', gap: 4, flex: '0 0 auto' }}
    >
      {SUPPORTED_LOCALES.map(({ code, label, badge }) => {
        const current = code === active;
        return (
          <button
            key={code}
            type="button"
            onClick={() => setUserLocale(code)}
            aria-pressed={current}
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: 6,
              fontSize: 12,
              padding: '4px 8px',
              borderRadius: 6,
              border: '1px solid hsl(var(--input))',
              cursor: 'pointer',
              color: current ? 'hsl(var(--primary-foreground))' : 'hsl(var(--foreground))',
              background: current ? 'hsl(var(--primary))' : 'hsl(var(--card))',
            }}
          >
            <span aria-hidden style={{ fontFamily: 'monospace', fontWeight: 600 }}>
              {badge}
            </span>
            <span>{label}</span>
          </button>
        );
      })}
    </div>
  );
}

// `data-shot`: the user-guide capture anchor, as in the console's CONVENTIONS.md.
function Centered({ children, 'data-shot': shot }: { children: React.ReactNode; 'data-shot'?: string }) {
  return (
    <div
      data-shot={shot}
      style={{
        height: '100%',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        padding: 24,
      }}
    >
      {children}
    </div>
  );
}

function Card({ children, 'data-shot': shot }: { children: React.ReactNode; 'data-shot'?: string }) {
  return (
    <div
      data-shot={shot}
      style={{
        display: 'flex',
        flexDirection: 'column',
        gap: 16,
        width: '100%',
        maxWidth: 360,
        padding: 24,
        borderRadius: 10,
        border: '1px solid hsl(var(--border))',
        background: 'hsl(var(--card))',
      }}
    >
      {children}
    </div>
  );
}

function Field({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <label style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      <span style={{ fontSize: 13, fontWeight: 600, color: 'hsl(var(--foreground))' }}>{label}</span>
      {children}
      {hint && (
        <span style={{ fontSize: 12, color: 'hsl(var(--muted-foreground))', fontFamily: 'monospace' }}>
          {hint}
        </span>
      )}
    </label>
  );
}

function TextInput({
  value,
  onChange,
  type = 'text',
  autoComplete,
  autoFocus,
  placeholder,
  'data-shot': shot,
}: {
  value: string;
  onChange: (v: string) => void;
  type?: string;
  autoComplete?: string;
  autoFocus?: boolean;
  placeholder?: string;
  'data-shot'?: string;
}) {
  return (
    <input
      data-shot={shot}
      type={type}
      value={value}
      onChange={(e) => onChange(e.target.value)}
      autoComplete={autoComplete}
      autoFocus={autoFocus}
      placeholder={placeholder}
      style={{
        fontSize: 14,
        padding: '8px 10px',
        borderRadius: 6,
        // --input, not --border. WCAG 1.4.11 asks 3:1 of the visual information
        // that identifies a control, and this field's border is the only thing
        // carrying that — there is no fill or label change to fall back on. The
        // three controls in this file use --input; the Card above uses --border,
        // which draws a REGION and is held to no such threshold. Both tokens are
        // defined in theme.css, which explains the split.
        border: '1px solid hsl(var(--input))',
        background: 'hsl(var(--background))',
        color: 'hsl(var(--foreground))',
      }}
    />
  );
}

function TextArea({
  value,
  onChange,
  rows,
  placeholder,
}: {
  value: string;
  onChange: (v: string) => void;
  rows: number;
  placeholder?: string;
}) {
  return (
    <textarea
      value={value}
      onChange={(e) => onChange(e.target.value)}
      rows={rows}
      placeholder={placeholder}
      spellCheck={false}
      style={{
        fontSize: 13,
        fontFamily: 'monospace',
        padding: '8px 10px',
        borderRadius: 6,
        border: '1px solid hsl(var(--input))',
        background: 'hsl(var(--background))',
        color: 'hsl(var(--foreground))',
        resize: 'vertical',
      }}
    />
  );
}

function ErrorText({ children }: { children: React.ReactNode }) {
  return <div style={{ color: 'hsl(var(--destructive))', fontSize: 13 }}>{children}</div>;
}

function HeaderButton({
  children,
  onClick,
  disabled,
  primary,
  type = 'button',
  'data-shot': shot,
}: {
  children: React.ReactNode;
  onClick?: () => void;
  disabled?: boolean;
  primary?: boolean;
  type?: 'button' | 'submit';
  'data-shot'?: string;
}) {
  return (
    <button
      data-shot={shot}
      type={type}
      onClick={onClick}
      disabled={disabled}
      style={{
        fontSize: 14,
        padding: '6px 14px',
        borderRadius: 6,
        border: '1px solid hsl(var(--input))',
        cursor: disabled ? 'default' : 'pointer',
        opacity: disabled ? 0.5 : 1,
        color: primary ? 'hsl(var(--primary-foreground))' : 'hsl(var(--foreground))',
        background: primary ? 'hsl(var(--primary))' : 'hsl(var(--card))',
        flex: '0 0 auto',
      }}
    >
      {children}
    </button>
  );
}
