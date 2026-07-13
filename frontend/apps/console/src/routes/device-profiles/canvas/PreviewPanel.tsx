// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The replay-preview panel (ADR-053 slice 9d — the headline): run the current canvas draft against
// replayed history and show the RAISE/RESOLVE edges it WOULD have produced over a window, without
// publishing anything. The server builds a throwaway in-memory DETECT core over an ephemeral replay
// consumer (zero perturbation of the live engine); this panel just picks a window, calls previewRule
// with the current graph, and renders the firing timeline + coverage stats + any degraded note.

import { useEffect, useRef, useState } from 'react';
import { Button } from '@/components/ui/button';
import { errMessage } from '@/routes/common';
import { previewRule, type PreviewResult } from '@/lib/api/event-processing';

// The window presets (hours back from now). A preview is capped server-side to 24h.
const WINDOWS: { label: string; hours: number }[] = [
  { label: 'Last 1h', hours: 1 },
  { label: 'Last 6h', hours: 6 },
  { label: 'Last 24h', hours: 24 },
];

type State =
  | { status: 'idle' }
  | { status: 'running' }
  | { status: 'done'; result: PreviewResult; hours: number }
  | { status: 'error'; message: string };

// notReadyReason, when non-null, both disables the Run button and is its tooltip — so a preview is
// gated on the parent's fresh, successful compile with an ACCURATE reason ("Waiting for the
// compiler…" during a debounced recompile vs. "Fix the compile errors…" on a real failure). graph is
// the current canvasDef JSON; structuralKey is the parent's compile-relevant fingerprint (excludes
// layout) — a change to it invalidates any shown/in-flight result so a stale run is never
// misattributed to the edited draft (Fable 9d-fe M1).
export function PreviewPanel({
  graph,
  profileToken,
  structuralKey,
  notReadyReason,
}: {
  graph: string;
  profileToken: string;
  structuralKey: string;
  notReadyReason: string | null;
}) {
  const [hours, setHours] = useState(24);
  const [state, setState] = useState<State>({ status: 'idle' });
  // Each run takes a token; a resolve applies only while its token is still current, so a run whose
  // graph changed (structuralKey effect below bumps the token) or that was superseded never lands.
  const runToken = useRef(0);

  // Invalidate a shown/in-flight result when the graph structurally changes — the result described a
  // prior draft, and showing it under the edited canvas would misrepresent "what would THIS do".
  useEffect(() => {
    runToken.current++;
    setState((s) => (s.status === 'done' || s.status === 'error' ? { status: 'idle' } : s));
  }, [structuralKey]);

  const run = async () => {
    const myToken = ++runToken.current;
    const ranHours = hours;
    setState({ status: 'running' });
    try {
      const end = new Date();
      const start = new Date(end.getTime() - ranHours * 3_600_000);
      const result = await previewRule({ graph, profileToken, start: start.toISOString(), end: end.toISOString() });
      if (runToken.current === myToken) setState({ status: 'done', result, hours: ranHours });
    } catch (err) {
      if (runToken.current === myToken) setState({ status: 'error', message: errMessage(err) });
    }
  };

  return (
    <div className="space-y-3 rounded-md border p-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <span className="text-sm font-semibold">Preview against history</span>
          <div className="flex overflow-hidden rounded-md border">
            {WINDOWS.map((w) => (
              <button
                key={w.hours}
                type="button"
                onClick={() => setHours(w.hours)}
                className={[
                  'px-2 py-1 text-xs transition-colors',
                  hours === w.hours ? 'bg-primary text-primary-foreground' : 'bg-transparent hover:bg-muted',
                ].join(' ')}
              >
                {w.label}
              </button>
            ))}
          </div>
        </div>
        <Button
          size="sm"
          variant="outline"
          onClick={run}
          loading={state.status === 'running'}
          disabled={notReadyReason !== null || state.status === 'running'}
          title={notReadyReason ?? undefined}
        >
          Run preview
        </Button>
      </div>

      <p className="text-xs text-muted-foreground">
        Replays this profile's published history and shows the raise/resolve edges this draft would have produced — nothing is published.
      </p>

      {state.status === 'error' && (
        <p className="rounded-md border border-destructive/50 bg-destructive/10 px-2 py-1.5 text-xs text-destructive">{state.message}</p>
      )}

      {state.status === 'done' && <PreviewOutcome result={state.result} hours={state.hours} />}
    </div>
  );
}

function PreviewOutcome({ result, hours }: { result: PreviewResult; hours: number }) {
  if (!result.ok) {
    // A resolver-level rejection (not a node compile error — the panel is gated on a good compile).
    // Surface the diagnostics directly rather than pointing at nodes that carry no error.
    return (
      <div className="space-y-1">
        {result.diagnostics.length === 0 ? (
          <p className="text-xs text-muted-foreground">The preview could not run.</p>
        ) : (
          result.diagnostics.map((d, i) => (
            <p key={i} className="rounded-md border border-destructive/50 bg-destructive/10 px-2 py-1.5 text-xs text-destructive">
              {d.message}
            </p>
          ))
        )}
      </div>
    );
  }
  const { stats, firings, degraded } = result;
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
        <span>
          <span className="font-medium text-foreground tabular-nums">{firings.length}</span> firing{firings.length === 1 ? '' : 's'} · last {hours}h
        </span>
        <span>
          <span className="tabular-nums">{stats.eventsScanned}</span> events scanned
        </span>
        <span>
          <span className="tabular-nums">{stats.wallMs}</span> ms
        </span>
        {stats.evalErrors > 0 && <span className="text-destructive">{stats.evalErrors} eval errors</span>}
      </div>

      {degraded && (
        <p className="rounded-md border border-amber-500/40 bg-amber-500/10 px-2 py-1.5 text-xs text-amber-700 dark:text-amber-400">{degraded}</p>
      )}

      {firings.length === 0 ? (
        <p className="text-xs text-muted-foreground">No firings in this window.</p>
      ) : (
        <ul className="max-h-56 space-y-1 overflow-y-auto">
          {firings.map((f, i) => (
            <li key={i} className="flex items-center gap-2 text-xs">
              <span
                className={[
                  'inline-block w-16 shrink-0 rounded px-1.5 py-0.5 text-center font-medium',
                  f.signal === 'raised' ? 'bg-destructive/15 text-destructive' : 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-400',
                ].join(' ')}
              >
                {f.signal === 'raised' ? 'RAISE' : 'RESOLVE'}
              </span>
              <span className="font-mono text-muted-foreground">{f.series}</span>
              <span className="ml-auto tabular-nums text-muted-foreground">{new Date(f.occurredAt).toLocaleString()}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
