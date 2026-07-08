// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The standalone dashboard viewer — the ADR-039 reference external embedder. It
// proves the embed story: bring your OWN auth (its own login, NOT the console's
// dc-auth session), paste an EXPORTED definition + an optional binding manifest,
// and render view-only. One definition + two manifests → two live dashboards on
// two different devices.
//
// Three steps: (1) sign in (login → selectTenant → a tenant access token held in
// React state); (2) load (paste + parse a definition and manifest); (3) view
// (render the definition read-only through a hub bound to the manifest). There is
// NO editing, no save, no token fetch.

import { decodeToken, gql, setAuthTokenGetter } from '@devicechain/client';
import {
  createDeviceResolver,
  createEntityLister,
  DashboardHub,
  effectiveBindings,
  migrateToSlots,
  parseBindingManifest,
  parseDashboardDefinition,
  type DashboardDefinition,
  type SelectionTarget,
  type SlotBinding,
} from '@devicechain/dashboards';
import { DashboardRenderer, useResolvedBindings, useSlotCandidates } from '@devicechain/widgets';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';

import { LOGIN, type Membership, SELECT_TENANT } from './queries';

// A loaded, parsed dashboard ready to render: the definition plus the effective
// binding manifest (defaults merged with the pasted override) it renders against.
interface Loaded {
  definition: DashboardDefinition;
  manifest: Record<string, SlotBinding>;
}

export default function App() {
  // The tenant access token, once signed in. Kept in state (to drive the UI) AND a
  // ref (so the token getter, registered once, always reads the latest value).
  const [token, setToken] = useState<string | null>(null);
  const tokenRef = useRef<string | null>(null);
  tokenRef.current = token;

  // The parsed dashboard to view, once loaded. Null → show the load form (Step 2).
  const [loaded, setLoaded] = useState<Loaded | null>(null);

  // Register the token getter ONCE at mount: it always returns the latest token
  // from the ref. There is no refresh logic — this is a reference viewer, so when
  // the token expires the user simply signs in again.
  useEffect(() => {
    setAuthTokenGetter(async () => tokenRef.current);
    return () => setAuthTokenGetter(null);
  }, []);

  const signOut = () => {
    setToken(null);
    setLoaded(null);
  };

  if (!token) return <SignIn onAuthed={setToken} />; // Step 1
  if (!loaded) return <Load onRender={setLoaded} onSignOut={signOut} />; // Step 2
  // The viewer's authorities gate action controls (alarm ack/clear); the server enforces
  // them regardless. Decoded from the access token this reference viewer holds.
  const authorities = decodeToken(token)?.authorities ?? [];
  return (
    <View loaded={loaded} authorities={authorities} onChange={() => setLoaded(null)} onSignOut={signOut} />
  ); // Step 3
}

// ── Step 1: Sign in ─────────────────────────────────────────────────────────
// email + password → login → identity token + memberships. Zero memberships is an
// error; one auto-selects; many show a picker. selectTenant yields the access token.

function SignIn({ onAuthed }: { onAuthed: (accessToken: string) => void }) {
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [identityToken, setIdentityToken] = useState<string | null>(null);
  const [memberships, setMemberships] = useState<Membership[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const selectTenant = (idToken: string, tenant: string) => {
    setBusy(true);
    setError(null);
    gql('user-management', SELECT_TENANT, { identityToken: idToken, tenant }, { anonymous: true })
      .then((r) => onAuthed(r.selectTenant.accessToken))
      .catch((err: unknown) => {
        setError(errorMessage(err));
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
          setError('This account has no tenants.');
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
        setError(errorMessage(err));
        setBusy(false);
      });
  };

  // Tenant picker (only when the identity has more than one membership).
  if (identityToken) {
    return (
      <Centered>
        <Card>
          <div style={{ fontSize: 18, fontWeight: 600 }}>Choose a tenant</div>
          {error && <ErrorText>{error}</ErrorText>}
          <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
            {memberships.map((m) => (
              <HeaderButton
                key={m.tenant}
                onClick={() => selectTenant(identityToken, m.tenant)}
                disabled={busy}
              >
                {m.tenant}
              </HeaderButton>
            ))}
          </div>
        </Card>
      </Centered>
    );
  }

  return (
    <Centered>
      <form onSubmit={submit} style={{ display: 'contents' }}>
        <Card>
          <div style={{ fontSize: 18, fontWeight: 600 }}>Sign in</div>
          <Field label="Email">
            <TextInput
              type="email"
              value={email}
              onChange={setEmail}
              autoComplete="username"
              autoFocus
            />
          </Field>
          <Field label="Password">
            <TextInput
              type="password"
              value={password}
              onChange={setPassword}
              autoComplete="current-password"
            />
          </Field>
          {error && <ErrorText>{error}</ErrorText>}
          <HeaderButton type="submit" primary disabled={busy}>
            {busy ? 'Signing in…' : 'Sign in'}
          </HeaderButton>
        </Card>
      </form>
    </Centered>
  );
}

// ── Step 2: Load ────────────────────────────────────────────────────────────
// Paste an exported definition + an optional binding manifest, parse both, and
// advance. Parse errors show inline; nothing throws to a white screen.

// Matches dashboard-management's server-side definition cap (1 MiB).
const MAX_PASTE_BYTES = 1 << 20;

const MANIFEST_HELP =
  '{ "slotName": { "kind": "device", "deviceToken": "..." } }  or  ' +
  '{ "slotName": { "kind": "anchor", "anchor": { "relationship": "...", "targetType": "area", "targetToken": "..." } } }';

function Load({
  onRender,
  onSignOut,
}: {
  onRender: (loaded: Loaded) => void;
  onSignOut: () => void;
}) {
  const [definitionText, setDefinitionText] = useState('');
  const [manifestText, setManifestText] = useState('');
  const [error, setError] = useState<string | null>(null);

  const render = () => {
    setError(null);

    // Bound the paste on the main thread (parse is synchronous) — matches the
    // server's definition size cap; a giant paste would only freeze this tab.
    if (definitionText.length > MAX_PASTE_BYTES) {
      setError('Definition is too large (over 1 MiB).');
      return;
    }

    // Parse the definition (required).
    let definition: DashboardDefinition;
    try {
      definition = migrateToSlots(parseDashboardDefinition(JSON.parse(definitionText)));
    } catch (err) {
      setError(`Definition: ${errorMessage(err)}`);
      return;
    }

    // Parse the manifest (optional — empty → no overrides).
    let manifest: Record<string, SlotBinding> = {};
    if (manifestText.trim() !== '') {
      let rawManifest: unknown;
      try {
        rawManifest = JSON.parse(manifestText);
      } catch (err) {
        setError(`Binding manifest: ${errorMessage(err)}`);
        return;
      }
      if (rawManifest === null || typeof rawManifest !== 'object' || Array.isArray(rawManifest)) {
        setError('Binding manifest must be a JSON object of slot → binding.');
        return;
      }
      manifest = parseBindingManifest(rawManifest);
      // Surface dropped entries (a typo'd shape) rather than silently binding the
      // wrong entity — or, for a stripped template, an unexplained blank widget.
      const dropped = Object.keys(rawManifest).length - Object.keys(manifest).length;
      if (dropped > 0) {
        setError(
          `Binding manifest: ${dropped} ${dropped === 1 ? 'entry was' : 'entries were'} ignored — ` +
            'check the shape (kind + deviceToken, or anchor with a targetToken).',
        );
        return;
      }
    }

    onRender({ definition, manifest });
  };

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
        <div style={{ flex: '1 1 auto', fontWeight: 600 }}>Load a dashboard</div>
        <HeaderButton onClick={onSignOut}>Sign out</HeaderButton>
      </header>

      <main
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
        <Field label="Definition (JSON)">
          <TextArea
            value={definitionText}
            onChange={setDefinitionText}
            rows={14}
            placeholder="Paste an exported dashboard definition…"
          />
        </Field>

        <Field label="Binding manifest (JSON, optional)" hint={MANIFEST_HELP}>
          <TextArea
            value={manifestText}
            onChange={setManifestText}
            rows={7}
            placeholder="{ }"
          />
        </Field>

        {error && <ErrorText>{error}</ErrorText>}

        <div>
          <HeaderButton onClick={render} primary disabled={definitionText.trim() === ''}>
            Render
          </HeaderButton>
        </div>
      </main>
    </div>
  );
}

// ── Step 3: View ────────────────────────────────────────────────────────────
// Render the parsed definition read-only through a hub bound to the effective
// manifest. VIEW-ONLY — no edit mode, no save, no react-rnd.

function View({
  loaded,
  authorities,
  onChange,
  onSignOut,
}: {
  loaded: Loaded;
  authorities: string[];
  onChange: () => void;
  onSignOut: () => void;
}) {
  const { definition, manifest } = loaded;

  // base = definition defaults merged with the pasted manifest override; the cascade
  // (useResolvedBindings) resolves scoped slots from their parent + the selection overlay
  // and returns the settled manifest the hub + renderer use. The resolver backs both the
  // hub's anchor→device-token expansion and the cascade's membership lookups.
  const resolver = useMemo(() => createDeviceResolver(), []);
  // The context/entity-selector candidate provider (ADR-039 selection amendment): backs a
  // context-selector so a viewer can re-point the dashboard's top-level context or pick a
  // member within it. Shares the resolver's device-management coupling via one factory.
  const lister = useMemo(() => createEntityLister(), []);
  const base = useMemo(() => effectiveBindings(definition, manifest), [definition, manifest]);
  // The view-driven selection overlay (an alarm-originator drill). Lives outside the hub
  // so the hub rebuild a rebind triggers never erases it.
  const [selection, setSelection] = useState<Record<string, SlotBinding>>({});
  const select = useCallback((t: SelectionTarget) => {
    setSelection((prev) => ({ ...prev, [t.slot]: t.binding }));
  }, []);
  const bindings = useResolvedBindings(definition, base, selection, resolver);
  const candidates = useSlotCandidates(definition, bindings, resolver, lister);
  const bindingsKey = useMemo(() => JSON.stringify(bindings), [bindings]);
  const authoritiesKey = authorities.join(',');
  // One hub for this view's lifetime, rebuilt when the resolved bindings change (the
  // shipped rebind path) and torn down on unmount.
  const hub = useMemo(
    () => new DashboardHub({ resolver, bindings, authorities }),
    // bindings read via bindingsKey (value identity), authorities via authoritiesKey.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [resolver, bindingsKey, authoritiesKey],
  );
  useEffect(() => () => hub.disposeAll(), [hub]);

  const title = definition.title || 'Dashboard';

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
        <div style={{ flex: '1 1 auto', fontWeight: 600 }}>{title}</div>
        <HeaderButton onClick={onChange}>Change</HeaderButton>
        <HeaderButton onClick={onSignOut}>Sign out</HeaderButton>
      </header>

      <main style={{ flex: '1 1 auto', minHeight: 0 }}>
        <DashboardRenderer
          definition={definition}
          hub={hub}
          actions={hub}
          bindings={bindings}
          select={select}
          candidates={candidates}
        />
      </main>
    </div>
  );
}

// ── Presentational helpers (inline styles + CSS vars — no Tailwind/shadcn) ────

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : 'Unexpected error';
}

function Centered({ children }: { children: React.ReactNode }) {
  return (
    <div
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

function Card({ children }: { children: React.ReactNode }) {
  return (
    <div
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
}: {
  value: string;
  onChange: (v: string) => void;
  type?: string;
  autoComplete?: string;
  autoFocus?: boolean;
}) {
  return (
    <input
      type={type}
      value={value}
      onChange={(e) => onChange(e.target.value)}
      autoComplete={autoComplete}
      autoFocus={autoFocus}
      style={{
        fontSize: 14,
        padding: '8px 10px',
        borderRadius: 6,
        border: '1px solid hsl(var(--border))',
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
        border: '1px solid hsl(var(--border))',
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
}: {
  children: React.ReactNode;
  onClick?: () => void;
  disabled?: boolean;
  primary?: boolean;
  type?: 'button' | 'submit';
}) {
  return (
    <button
      type={type}
      onClick={onClick}
      disabled={disabled}
      style={{
        fontSize: 14,
        padding: '6px 14px',
        borderRadius: 6,
        border: '1px solid hsl(var(--border))',
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
